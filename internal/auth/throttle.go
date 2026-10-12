package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

const (
	loginThrottleWindow         = 15 * time.Minute
	securityEventRetention      = 90 * 24 * time.Hour
	securityEventCoalesceWindow = time.Minute
	maximumStoredSecurityEvents = 2000
	ThrottleOperationLogin      = "login"
	ThrottleOperationSensitive  = "sensitive"
)

type LoginThrottle struct {
	service *Service
	now     func() time.Time
	active  chan struct{}
}

func NewLoginThrottle(service *Service) *LoginThrottle {
	return &LoginThrottle{service: service, now: time.Now, active: make(chan struct{}, 1)}
}

func (t *LoginThrottle) Begin() bool {
	select {
	case t.active <- struct{}{}:
		return true
	default:
		return false
	}
}

func (t *LoginThrottle) End() { <-t.active }

func (t *LoginThrottle) Check(ctx context.Context, client netip.Addr) (time.Duration, error) {
	return t.CheckOperation(ctx, ThrottleOperationLogin, client)
}

func (t *LoginThrottle) CheckOperation(ctx context.Context, operation string, client netip.Addr) (time.Duration, error) {
	if err := validateThrottleOperation(operation); err != nil {
		return 0, err
	}
	now := t.now().UTC()
	maximum := time.Duration(0)
	for _, key := range throttleKeys(operation, client) {
		var next sql.NullString
		err := t.service.db.QueryRowContext(ctx, `SELECT next_allowed_at FROM auth_throttle_state WHERE scope = ? AND key_hash = ?`, key.scope, key.hash[:]).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("read login throttle: %w", err)
		}
		if !next.Valid {
			continue
		}
		allowedAt, err := time.Parse(time.RFC3339Nano, next.String)
		if err != nil {
			return 0, errors.New("stored login throttle is invalid")
		}
		if remaining := allowedAt.Sub(now); remaining > maximum {
			maximum = remaining
		}
	}
	return maximum, nil
}

func (t *LoginThrottle) Failure(ctx context.Context, client netip.Addr) (time.Duration, error) {
	return t.FailureOperation(ctx, ThrottleOperationLogin, client)
}

func (t *LoginThrottle) FailureOperation(ctx context.Context, operation string, client netip.Addr) (time.Duration, error) {
	if err := validateThrottleOperation(operation); err != nil {
		return 0, err
	}
	now := t.now().UTC()
	tx, err := t.service.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	maximum := time.Duration(0)
	for _, key := range throttleKeys(operation, client) {
		attempts, started, err := loadThrottleState(ctx, tx, key)
		if err != nil {
			return 0, err
		}
		if started.IsZero() || now.Sub(started) >= loginThrottleWindow {
			attempts, started = 0, now
		}
		attempts++
		delay := progressiveDelay(attempts, key.threshold)
		if delay > maximum {
			maximum = delay
		}
		var next any
		if delay > 0 {
			next = now.Add(delay).Format(time.RFC3339Nano)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO auth_throttle_state (scope, key_hash, attempts, window_started_at, next_allowed_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(scope, key_hash) DO UPDATE SET attempts=excluded.attempts, window_started_at=excluded.window_started_at, next_allowed_at=excluded.next_allowed_at`, key.scope, key.hash[:], attempts, started.Format(time.RFC3339Nano), next)
		if err != nil {
			return 0, fmt.Errorf("update login throttle: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return maximum, nil
}

func (t *LoginThrottle) Success(ctx context.Context, client netip.Addr) error {
	return t.SuccessOperation(ctx, ThrottleOperationLogin, client)
}

func (t *LoginThrottle) SuccessOperation(ctx context.Context, operation string, client netip.Addr) error {
	if err := validateThrottleOperation(operation); err != nil {
		return err
	}
	for _, key := range throttleKeys(operation, client) {
		if _, err := t.service.db.ExecContext(ctx, `DELETE FROM auth_throttle_state WHERE scope = ? AND key_hash = ?`, key.scope, key.hash[:]); err != nil {
			return err
		}
	}
	return nil
}

type throttleKey struct {
	scope     string
	hash      [32]byte
	threshold int
}

func throttleKeys(operation string, client netip.Addr) []throttleKey {
	clientValue := "unknown"
	if client.IsValid() {
		clientValue = client.String()
	}
	clientHash := sha256.Sum256([]byte("credential-client\x00" + operation + "\x00" + clientValue))
	globalHash := sha256.Sum256([]byte("credential-global\x00" + operation))
	return []throttleKey{{scope: operation + "_client", hash: clientHash, threshold: 3}, {scope: operation + "_global", hash: globalHash, threshold: 10}}
}

func validateThrottleOperation(operation string) error {
	switch operation {
	case ThrottleOperationLogin, ThrottleOperationSensitive:
		return nil
	default:
		return errors.New("invalid credential throttle operation")
	}
}

func loadThrottleState(ctx context.Context, tx *sql.Tx, key throttleKey) (int, time.Time, error) {
	var attempts int
	var startedText string
	err := tx.QueryRowContext(ctx, `SELECT attempts, window_started_at FROM auth_throttle_state WHERE scope = ? AND key_hash = ?`, key.scope, key.hash[:]).Scan(&attempts, &startedText)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, nil
	}
	if err != nil {
		return 0, time.Time{}, err
	}
	started, err := time.Parse(time.RFC3339Nano, startedText)
	return attempts, started, err
}

func progressiveDelay(attempts, threshold int) time.Duration {
	if attempts <= threshold {
		return 0
	}
	shift := attempts - threshold - 1
	if shift > 5 {
		shift = 5
	}
	delay := time.Second * time.Duration(1<<shift)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func ClientReference(client netip.Addr) string {
	value := "unknown"
	if client.IsValid() {
		value = client.String()
	}
	digest := sha256.Sum256([]byte("security-event-client\x00" + value))
	return hex.EncodeToString(digest[:8])
}

func (s *Service) RecordSecurityEvent(ctx context.Context, eventType, outcome, details string) error {
	if len(eventType) > 64 || len(outcome) > 32 || len(details) > 256 {
		return errors.New("security event is too large")
	}
	now := time.Now().UTC()
	var previous string
	err := s.db.QueryRowContext(ctx, `SELECT occurred_at FROM auth_security_events WHERE event_type=? AND outcome=? AND details=? ORDER BY id DESC LIMIT 1`, eventType, outcome, details).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		occurredAt, parseErr := time.Parse(time.RFC3339Nano, previous)
		if parseErr != nil {
			return errors.New("stored security event timestamp is invalid")
		}
		if now.Sub(occurredAt) < securityEventCoalesceWindow {
			return nil
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_security_events (event_type, occurred_at, outcome, details) VALUES (?, ?, ?, ?)`, eventType, now.Format(time.RFC3339Nano), outcome, details); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_security_events WHERE occurred_at < ?`, now.Add(-securityEventRetention).Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_security_events WHERE id NOT IN (SELECT id FROM auth_security_events ORDER BY id DESC LIMIT ?)`, maximumStoredSecurityEvents); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.logger.Info("authentication security event", "eventType", eventType, "outcome", outcome, "details", details)
	return nil
}

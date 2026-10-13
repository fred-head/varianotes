package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
)

const (
	sessionPrincipalKey          = "principal"
	sessionCSRFKey               = "csrf_token"
	sessionMFAPendingKey         = "mfa_pending"
	sessionActivityWriteInterval = time.Minute
)

var errSessionInactive = errors.New("session is revoked or expired")

type SessionOptions struct {
	CookieSecure     bool
	IdleTimeout      time.Duration
	Lifetime         time.Duration
	RememberLifetime time.Duration
}

func DefaultSessionOptions(cookieSecure bool) SessionOptions {
	return SessionOptions{CookieSecure: cookieSecure, IdleTimeout: 7 * 24 * time.Hour, Lifetime: 12 * time.Hour, RememberLifetime: 30 * 24 * time.Hour}
}

type Sessions struct {
	manager *scs.SessionManager
	service *Service
	options SessionOptions
}

func NewSessions(service *Service, options SessionOptions) (*Sessions, error) {
	if service == nil || service.db == nil {
		return nil, errors.New("authentication service is required")
	}
	if options.IdleTimeout <= 0 || options.Lifetime <= 0 || options.RememberLifetime < options.Lifetime {
		return nil, errors.New("invalid session durations")
	}
	manager := scs.New()
	manager.Store = &sqliteSessionStore{db: service.db, codec: manager.Codec, defaultIdle: options.IdleTimeout}
	manager.Lifetime = options.RememberLifetime
	manager.IdleTimeout = 30 * 24 * time.Hour
	manager.Cookie.Name = "varianotes_session"
	if options.CookieSecure {
		manager.Cookie.Name = "__Secure-varianotes_session"
	}
	manager.Cookie.Domain = ""
	manager.Cookie.HttpOnly = true
	manager.Cookie.Path = "/api"
	manager.Cookie.Persist = false
	manager.Cookie.SameSite = http.SameSiteStrictMode
	manager.Cookie.Secure = options.CookieSecure
	return &Sessions{manager: manager, service: service, options: options}, nil
}

func (s *Sessions) LoadAndSave(next http.Handler) http.Handler { return s.manager.LoadAndSave(next) }

func (s *Sessions) Authenticated(ctx context.Context) bool {
	return s.manager.GetString(ctx, sessionPrincipalKey) == OwnerPrincipal
}

// MFAPending reports only whether this short-lived session has already passed
// the password step. It does not expose or weaken either authentication factor.
func (s *Sessions) MFAPending(ctx context.Context) bool {
	return s.manager.GetBool(ctx, sessionMFAPendingKey) && !s.Authenticated(ctx)
}

func (s *Sessions) Login(ctx context.Context, password string, remember bool, client string) error {
	s.service.credentialMu.Lock()
	defer s.service.credentialMu.Unlock()
	if err := s.service.verifyPassword(ctx, password); err != nil {
		return err
	}
	enabled, err := s.service.MFAEnabled(ctx)
	if err != nil {
		return err
	}
	if enabled {
		if err := s.manager.RenewToken(ctx); err != nil {
			return err
		}
		s.manager.SetDeadline(ctx, time.Now().Add(5*time.Minute))
		s.manager.RememberMe(ctx, false)
		s.manager.Put(ctx, sessionMFAPendingKey, true)
		s.manager.Put(ctx, "client", sanitizeClientDescription(client))
		s.manager.Put(ctx, "remember", remember)
		s.manager.Remove(ctx, sessionPrincipalKey)
		s.manager.Remove(ctx, sessionCSRFKey)
		return ErrMFARequired
	}
	return s.establish(ctx, remember, client)
}

func (s *Sessions) CompleteMFA(ctx context.Context, code string) error {
	s.service.credentialMu.Lock()
	defer s.service.credentialMu.Unlock()
	if err := s.service.ensureNoPendingEncryptionRecovery(ctx); err != nil {
		return err
	}
	if !s.manager.GetBool(ctx, sessionMFAPendingKey) {
		return ErrMFAInvalid
	}
	if err := s.service.VerifySecondFactor(ctx, code); err != nil {
		return ErrMFAInvalid
	}
	remember := s.manager.GetBool(ctx, "remember")
	client := s.manager.GetString(ctx, "client")
	s.manager.Remove(ctx, sessionMFAPendingKey)
	return s.establish(ctx, remember, client)
}

func (s *Sessions) EstablishAfterSetup(ctx context.Context, client string) error {
	return s.establish(ctx, false, client)
}

func (s *Sessions) establish(ctx context.Context, remember bool, client string) error {
	if err := s.manager.RenewToken(ctx); err != nil {
		return err
	}
	settings, err := s.service.SessionSettings(ctx)
	if err != nil {
		return err
	}
	lifetime := time.Duration(settings.LifetimeHours) * time.Hour
	if remember {
		lifetime = time.Duration(settings.RememberDays) * 24 * time.Hour
	}
	s.manager.SetDeadline(ctx, time.Now().Add(lifetime))
	s.manager.RememberMe(ctx, remember)
	s.manager.Put(ctx, sessionPrincipalKey, OwnerPrincipal)
	s.manager.Put(ctx, "client", sanitizeClientDescription(client))
	s.manager.Put(ctx, "idle_hours", settings.IdleHours)
	s.manager.Put(ctx, "remember", remember)
	s.manager.Remove(ctx, sessionMFAPendingKey)
	s.manager.Remove(ctx, sessionCSRFKey)
	_, err = s.CSRFToken(ctx)
	return err
}

func (s *Sessions) CurrentHash(ctx context.Context) []byte {
	token := s.manager.Token(ctx)
	if token == "" {
		return nil
	}
	hash := sessionHash(token)
	return append([]byte(nil), hash[:]...)
}

func (s *Sessions) ChangePassword(ctx context.Context, currentPassword, newPassword string) error {
	s.service.credentialMu.Lock()
	defer s.service.credentialMu.Unlock()
	currentHash := s.CurrentHash(ctx)
	if err := s.service.changePassword(ctx, currentPassword, newPassword, currentHash); err != nil {
		return err
	}
	remember := s.manager.GetBool(ctx, "remember")
	client := s.manager.GetString(ctx, "client")
	if err := s.establish(ctx, remember, client); err != nil {
		// The credential was already changed. Do not leave the old session alive
		// if rotating it unexpectedly fails.
		_ = s.manager.Destroy(ctx)
		return err
	}
	return nil
}

func (s *Sessions) CSRFToken(ctx context.Context) (string, error) {
	if existing := s.manager.GetString(ctx, sessionCSRFKey); existing != "" {
		return existing, nil
	}
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(value)
	s.manager.Put(ctx, sessionCSRFKey, token)
	return token, nil
}

func (s *Sessions) ExistingCSRFToken(ctx context.Context) string {
	return s.manager.GetString(ctx, sessionCSRFKey)
}

func (s *Sessions) ValidCSRFToken(ctx context.Context, supplied string) bool {
	expected := s.manager.GetString(ctx, sessionCSRFKey)
	if expected == "" || len(supplied) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) == 1
}

func (s *Sessions) Logout(ctx context.Context) error { return s.manager.Destroy(ctx) }

func (s *Sessions) RevokeCurrent(ctx context.Context) error { return s.manager.Destroy(ctx) }

func (s *Sessions) RotateCurrent(ctx context.Context) error {
	if !s.Authenticated(ctx) {
		return ErrAuthentication
	}
	return s.establish(ctx, s.manager.GetBool(ctx, "remember"), s.manager.GetString(ctx, "client"))
}

func (s *Sessions) RevokeAll(ctx context.Context) error {
	if _, err := s.service.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = ? WHERE revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return s.manager.Destroy(ctx)
}

func sanitizeClientDescription(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if len(value) > 200 {
		value = value[:200]
	}
	return value
}

type sqliteSessionStore struct {
	db          *sql.DB
	codec       scs.Codec
	defaultIdle time.Duration
}

func sessionHash(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func (s *sqliteSessionStore) Delete(token string) error {
	return s.DeleteCtx(context.Background(), token)
}
func (s *sqliteSessionStore) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}
func (s *sqliteSessionStore) Commit(token string, data []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, data, expiry)
}

func (s *sqliteSessionStore) DeleteCtx(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	hash := sessionHash(token)
	_, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = ? WHERE session_id_hash = ?`, time.Now().UTC().Format(time.RFC3339Nano), hash[:])
	return err
}

func (s *sqliteSessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	hash := sessionHash(token)
	var data []byte
	var lastActivityText, absoluteExpiryText string
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	err := s.db.QueryRowContext(ctx, `SELECT session_data, last_activity_at, absolute_expires_at FROM auth_sessions WHERE session_id_hash = ? AND revoked_at IS NULL AND idle_expires_at > ? AND absolute_expires_at > ?`, hash[:], now, now).Scan(&data, &lastActivityText, &absoluteExpiryText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	lastActivity, err := time.Parse(time.RFC3339Nano, lastActivityText)
	if err != nil {
		return nil, false, errors.New("stored session activity timestamp is invalid")
	}
	if nowTime.Sub(lastActivity) < sessionActivityWriteInterval {
		return data, true, nil
	}
	absoluteExpiry, err := time.Parse(time.RFC3339Nano, absoluteExpiryText)
	if err != nil {
		return nil, false, errors.New("stored absolute session expiry is invalid")
	}
	idle := s.defaultIdle
	if s.codec != nil {
		_, values, decodeErr := s.codec.Decode(data)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		if hours, ok := values["idle_hours"].(int); ok && hours >= 1 && hours <= 720 {
			idle = time.Duration(hours) * time.Hour
		}
	}
	if idle <= 0 {
		return data, true, nil
	}
	idleExpiry := nowTime.Add(idle)
	if idleExpiry.After(absoluteExpiry) {
		idleExpiry = absoluteExpiry
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET last_activity_at=?, idle_expires_at=? WHERE session_id_hash=? AND revoked_at IS NULL AND idle_expires_at>? AND absolute_expires_at>?`, now, idleExpiry.Format(time.RFC3339Nano), hash[:], now, now)
	if err != nil {
		return nil, false, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, false, nil
	}
	return data, true, nil
}

func (s *sqliteSessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expires := expiry.UTC().Format(time.RFC3339Nano)
	absoluteExpiry := expires
	client := ""
	if s.codec != nil {
		deadline, values, err := s.codec.Decode(data)
		if err != nil {
			return err
		}
		absoluteExpiry = deadline.UTC().Format(time.RFC3339Nano)
		if value, ok := values["client"].(string); ok {
			client = sanitizeClientDescription(value)
		}
		idle := s.defaultIdle
		if hours, ok := values["idle_hours"].(int); ok && hours >= 1 && hours <= 720 {
			idle = time.Duration(hours) * time.Hour
		}
		if idleExpiry := time.Now().UTC().Add(idle); idleExpiry.Before(deadline) {
			expires = idleExpiry.Format(time.RFC3339Nano)
		} else {
			expires = deadline.UTC().Format(time.RFC3339Nano)
		}
	}
	hash := sessionHash(token)
	// Expired and revoked rows no longer grant access. Prune them when a
	// session is written so the settings list and metadata stay bounded. Keep
	// the current token's row until the upsert checks it, preventing a stale
	// request from recreating an expired or revoked session.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions
		WHERE session_id_hash <> ? AND (
			revoked_at IS NOT NULL OR
			julianday(idle_expires_at) IS NULL OR julianday(absolute_expires_at) IS NULL OR
			julianday(idle_expires_at) <= julianday(?) OR julianday(absolute_expires_at) <= julianday(?)
		)`, hash[:], now, now); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions (session_id_hash, created_at, last_activity_at, idle_expires_at, absolute_expires_at, client_description, session_data)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id_hash) DO UPDATE SET last_activity_at=excluded.last_activity_at, idle_expires_at=excluded.idle_expires_at, absolute_expires_at=excluded.absolute_expires_at, client_description=excluded.client_description, session_data=excluded.session_data WHERE auth_sessions.revoked_at IS NULL AND auth_sessions.idle_expires_at>? AND auth_sessions.absolute_expires_at>?`, hash[:], now, now, expires, absoluteExpiry, client, data, now, now)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errSessionInactive
	}
	return nil
}

package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

var (
	ErrAuthenticationDisableBlocked = errors.New("authentication cannot be disabled while protected vault content exists")
	ErrPasswordChangePending        = errors.New("password change recovery is pending")
	ErrPasswordChangeRecovery       = errors.New("password change requires recovery")
)

type SessionSettings struct {
	IdleHours     int `json:"idleHours"`
	LifetimeHours int `json:"lifetimeHours"`
	RememberDays  int `json:"rememberDays"`
}

func (s *Service) SessionSettings(ctx context.Context) (SessionSettings, error) {
	var settings SessionSettings
	err := s.db.QueryRowContext(ctx, `SELECT session_idle_hours, session_lifetime_hours, remember_lifetime_days FROM auth_configuration WHERE id = 1`).Scan(&settings.IdleHours, &settings.LifetimeHours, &settings.RememberDays)
	if err != nil {
		return SessionSettings{}, fmt.Errorf("read session settings: %w", err)
	}
	return settings, nil
}

func (s *Service) UpdateSessionSettings(ctx context.Context, settings SessionSettings) error {
	if settings.IdleHours < 1 || settings.IdleHours > 720 || settings.LifetimeHours < 1 || settings.LifetimeHours > 24 || settings.RememberDays < 1 || settings.RememberDays > 90 {
		return errors.New("session durations are outside the allowed range")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE auth_configuration SET session_idle_hours=?, session_lifetime_hours=?, remember_lifetime_days=?, updated_at=? WHERE id=1`, settings.IdleHours, settings.LifetimeHours, settings.RememberDays, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type SessionRecord struct {
	ID                string     `json:"id"`
	CreatedAt         time.Time  `json:"createdAt"`
	LastActivityAt    time.Time  `json:"lastActivityAt"`
	IdleExpiresAt     time.Time  `json:"idleExpiresAt"`
	AbsoluteExpiresAt time.Time  `json:"absoluteExpiresAt"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
	ClientDescription string     `json:"clientDescription"`
	Current           bool       `json:"current"`
}

func (s *Service) Sessions(ctx context.Context, currentHash []byte) ([]SessionRecord, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT session_id_hash, created_at, last_activity_at, idle_expires_at, absolute_expires_at, revoked_at, client_description
		FROM auth_sessions
		WHERE revoked_at IS NULL AND julianday(idle_expires_at) > julianday(?) AND julianday(absolute_expires_at) > julianday(?)
		ORDER BY last_activity_at DESC LIMIT 100`, now, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]SessionRecord, 0)
	for rows.Next() {
		var hash []byte
		var created, activity, idle, absolute string
		var revoked sql.NullString
		var record SessionRecord
		if err := rows.Scan(&hash, &created, &activity, &idle, &absolute, &revoked, &record.ClientDescription); err != nil {
			return nil, err
		}
		var parseErr error
		if record.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, created); parseErr != nil {
			return nil, parseErr
		}
		if record.LastActivityAt, parseErr = time.Parse(time.RFC3339Nano, activity); parseErr != nil {
			return nil, parseErr
		}
		if record.IdleExpiresAt, parseErr = time.Parse(time.RFC3339Nano, idle); parseErr != nil {
			return nil, parseErr
		}
		if record.AbsoluteExpiresAt, parseErr = time.Parse(time.RFC3339Nano, absolute); parseErr != nil {
			return nil, parseErr
		}
		if revoked.Valid {
			value, err := time.Parse(time.RFC3339Nano, revoked.String)
			if err != nil {
				return nil, err
			}
			record.RevokedAt = &value
		}
		record.ID = base64.RawURLEncoding.EncodeToString(hash)
		record.Current = len(currentHash) == sha256.Size && equalBytes(hash, currentHash)
		result = append(result, record)
	}
	return result, rows.Err()
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func (s *Service) RevokeSession(ctx context.Context, id string, currentHash []byte) error {
	hash, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil || len(hash) != sha256.Size {
		return errors.New("invalid session identifier")
	}
	if equalBytes(hash, currentHash) {
		return errors.New("use logout to revoke the current session")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE session_id_hash=? AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), hash)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Service) RevokeOtherSessions(ctx context.Context, currentHash []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE session_id_hash<>? AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), currentHash)
	return err
}

func (s *Service) replacePassword(ctx context.Context, password string, keepSessionHash []byte, recovery, coordinated bool) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	credential, err := derivePasswordCredential(password, defaultPasswordParameters)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE auth_password_credentials SET algorithm_version=?, memory_kib=?, iterations=?, parallelism=?, salt=?, password_hash=?, updated_at=? WHERE owner_principal=?`, credential.AlgorithmVersion, credential.Parameters.MemoryKiB, credential.Parameters.Iterations, credential.Parameters.Parallelism, credential.Salt, credential.Hash, now.Format(time.RFC3339Nano), OwnerPrincipal)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrSetupUnavailable
	}
	if len(keepSessionHash) == sha256.Size {
		_, err = tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE session_id_hash<>? AND revoked_at IS NULL`, now.Format(time.RFC3339Nano), keepSessionHash)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE revoked_at IS NULL`, now.Format(time.RFC3339Nano))
	}
	if err != nil {
		return err
	}
	event := "password_changed"
	if recovery {
		event = "password_recovered"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_security_events(event_type, occurred_at, outcome, details) VALUES (?, ?, 'success', '')`, event, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if coordinated {
		result, err := tx.ExecContext(ctx, `UPDATE auth_password_change_journal SET phase='committed' WHERE id=1 AND phase='prepared'`)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrPasswordChangePending
		}
	}
	if recovery {
		if _, err := tx.ExecContext(ctx, `UPDATE auth_configuration SET encryption_password_separate=1, updated_at=? WHERE id=1`, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Service) RecoverPassword(ctx context.Context, password string) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	state, err := s.State(ctx)
	if err != nil {
		return err
	}
	if state.Mode != ModeLocal || !state.SetupCompleted {
		return ErrSetupUnavailable
	}
	if _, _, err := s.pendingPasswordChange(ctx); err != nil {
		return err
	}
	return s.replacePassword(ctx, password, nil, true, false)
}

func (s *Service) ChangePassword(ctx context.Context, currentPassword, newPassword string, keepSessionHash []byte) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	if err := s.verifyPassword(ctx, currentPassword); err != nil {
		return err
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	separate, err := s.EncryptionPasswordSeparate(ctx)
	if err != nil {
		return err
	}
	if separate {
		return s.replacePassword(ctx, newPassword, keepSessionHash, false, false)
	}
	coordinator := s.config.PasswordCoordinator
	if coordinator == nil {
		return s.replacePassword(ctx, newPassword, keepSessionHash, false, false)
	}
	plan, err := coordinator.PreparePasswordChange(ctx, currentPassword, newPassword)
	if err != nil {
		return err
	}
	if err := s.savePendingPasswordChange(ctx, "prepared", plan); err != nil {
		return fmt.Errorf("journal password change: %w", err)
	}
	if err := coordinator.ApplyPasswordChange(ctx, plan); err != nil {
		rollbackErr := coordinator.RollbackPasswordChange(ctx, plan)
		if rollbackErr == nil {
			rollbackErr = s.clearPendingPasswordChange(ctx)
		}
		if rollbackErr != nil {
			return errors.Join(ErrPasswordChangeRecovery, err, rollbackErr)
		}
		return err
	}
	if err := s.replacePassword(ctx, newPassword, keepSessionHash, false, true); err != nil {
		phase, pendingPlan, journalErr := s.pendingPasswordChange(ctx)
		if journalErr != nil {
			return errors.Join(ErrPasswordChangeRecovery, err, journalErr)
		}
		if phase == "committed" {
			return s.finishCommittedPasswordChange(ctx, coordinator, pendingPlan)
		}
		if phase != "prepared" {
			return errors.Join(ErrPasswordChangeRecovery, err)
		}
		rollbackErr := coordinator.RollbackPasswordChange(ctx, plan)
		if rollbackErr == nil {
			rollbackErr = s.clearPendingPasswordChange(ctx)
		}
		if rollbackErr != nil {
			return errors.Join(ErrPasswordChangeRecovery, err, rollbackErr, journalErr)
		}
		return err
	}
	return s.finishCommittedPasswordChange(ctx, coordinator, plan)
}

func (s *Service) finishCommittedPasswordChange(ctx context.Context, coordinator PasswordChangeCoordinator, plan []byte) error {
	if err := coordinator.ApplyPasswordChange(ctx, plan); err != nil {
		return errors.Join(ErrPasswordChangeRecovery, err)
	}
	if err := s.clearPendingPasswordChange(ctx); err != nil {
		return errors.Join(ErrPasswordChangeRecovery, err)
	}
	return nil
}

func (s *Service) savePendingPasswordChange(ctx context.Context, phase string, plan []byte) error {
	if phase != "prepared" || len(plan) == 0 || len(plan) > 32<<20 {
		return ErrPasswordChangePending
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_password_change_journal(id, phase, plan, created_at) VALUES (1, ?, ?, ?)`, phase, plan, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Service) pendingPasswordChange(ctx context.Context) (string, []byte, error) {
	var phase string
	var plan []byte
	err := s.db.QueryRowContext(ctx, `SELECT phase, plan FROM auth_password_change_journal WHERE id=1`).Scan(&phase, &plan)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if (phase != "prepared" && phase != "committed") || len(plan) == 0 || len(plan) > 32<<20 {
		return "", nil, ErrPasswordChangeRecovery
	}
	return phase, plan, nil
}

func (s *Service) clearPendingPasswordChange(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_password_change_journal WHERE id=1`)
	return err
}

func (s *Service) recoverPasswordChange(ctx context.Context) error {
	phase, plan, err := s.pendingPasswordChange(ctx)
	if err != nil || phase == "" {
		return err
	}
	coordinator := s.config.PasswordCoordinator
	if coordinator == nil {
		return ErrPasswordChangeRecovery
	}
	if phase == "prepared" {
		err = coordinator.RollbackPasswordChange(ctx, plan)
	} else {
		err = coordinator.ApplyPasswordChange(ctx, plan)
	}
	if err != nil {
		return errors.Join(ErrPasswordChangeRecovery, err)
	}
	if err := s.clearPendingPasswordChange(ctx); err != nil {
		return errors.Join(ErrPasswordChangeRecovery, err)
	}
	return nil
}

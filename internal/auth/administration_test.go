package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

type testPasswordChangeCoordinator struct {
	protected   bool
	prepareErr  error
	applyErr    error
	rollbackErr error
	prepared    int
	applyCalls  int
	rollbacks   int
}

func (c *testPasswordChangeCoordinator) PreparePasswordChange(_ context.Context, _, _ string) ([]byte, error) {
	c.prepared++
	if c.prepareErr != nil {
		return nil, c.prepareErr
	}
	return []byte("encrypted-envelope-plan"), nil
}

func (c *testPasswordChangeCoordinator) ApplyPasswordChange(_ context.Context, plan []byte) error {
	c.applyCalls++
	if string(plan) != "encrypted-envelope-plan" {
		return errors.New("unexpected password plan")
	}
	return c.applyErr
}

func (c *testPasswordChangeCoordinator) RollbackPasswordChange(_ context.Context, plan []byte) error {
	c.rollbacks++
	if string(plan) != "encrypted-envelope-plan" {
		return errors.New("unexpected rollback plan")
	}
	return c.rollbackErr
}

func (c *testPasswordChangeCoordinator) HasProtectedContent(context.Context) (bool, error) {
	return c.protected, nil
}

func configuredAuthService(t *testing.T) *Service {
	t.Helper()
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original secure password"); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestSessionSettingsAreBoundedAndPersistent(t *testing.T) {
	service := configuredAuthService(t)
	settings, err := service.SessionSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.IdleHours != 168 || settings.LifetimeHours != 12 || settings.RememberDays != 30 {
		t.Fatalf("unexpected defaults: %#v", settings)
	}
	want := SessionSettings{IdleHours: 48, LifetimeHours: 8, RememberDays: 45}
	if err := service.UpdateSessionSettings(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := service.SessionSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("settings=%#v want=%#v", got, want)
	}
	if err := service.UpdateSessionSettings(t.Context(), SessionSettings{IdleHours: 0, LifetimeHours: 8, RememberDays: 45}); err == nil {
		t.Fatal("accepted unsafe session duration")
	}
}

func TestPasswordChangeRevokesOtherSessionsAndRecoveryRevokesAll(t *testing.T) {
	service := configuredAuthService(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current := sha256.Sum256([]byte("current"))
	other := sha256.Sum256([]byte("other"))
	for _, hash := range [][32]byte{current, other} {
		if _, err := service.db.ExecContext(t.Context(), `INSERT INTO auth_sessions(session_id_hash,created_at,last_activity_at,idle_expires_at,absolute_expires_at,client_description,session_data) VALUES(?,?,?,?,?,'test',X'')`, hash[:], now, now, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ChangePassword(t.Context(), "original secure password", "replacement secure password", current[:]); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPassword(t.Context(), "original secure password"); err == nil {
		t.Fatal("old password remained valid")
	}
	if err := service.VerifyPassword(t.Context(), "replacement secure password"); err != nil {
		t.Fatal(err)
	}
	var currentRevoked, otherRevoked *string
	if err := service.db.QueryRowContext(t.Context(), `SELECT revoked_at FROM auth_sessions WHERE session_id_hash=?`, current[:]).Scan(&currentRevoked); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRowContext(t.Context(), `SELECT revoked_at FROM auth_sessions WHERE session_id_hash=?`, other[:]).Scan(&otherRevoked); err != nil {
		t.Fatal(err)
	}
	if currentRevoked != nil || otherRevoked == nil {
		t.Fatalf("wrong revocation state current=%v other=%v", currentRevoked, otherRevoked)
	}
	if err := service.RecoverPassword(t.Context(), "recovered secure password"); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPassword(t.Context(), "recovered secure password"); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRowContext(t.Context(), `SELECT revoked_at FROM auth_sessions WHERE session_id_hash=?`, current[:]).Scan(&currentRevoked); err != nil {
		t.Fatal(err)
	}
	if currentRevoked == nil {
		t.Fatal("recovery did not revoke current session")
	}
}

func TestPasswordChangeCoordinatesVaultEnvelopesAroundCredentialCommit(t *testing.T) {
	coordinator := &testPasswordChangeCoordinator{}
	path := filepath.Join(t.TempDir(), "auth.db")
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: path, PasswordCoordinator: coordinator}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original secure password"); err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(t.Context(), "original secure password", "replacement secure password", nil); err != nil {
		t.Fatal(err)
	}
	if coordinator.prepared != 1 || coordinator.applyCalls != 2 || coordinator.rollbacks != 0 {
		t.Fatalf("coordinator calls: prepared=%d apply=%d rollback=%d", coordinator.prepared, coordinator.applyCalls, coordinator.rollbacks)
	}
	if err := service.VerifyPassword(t.Context(), "replacement secure password"); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_password_change_journal`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("completed password change retained %d journal rows", pending)
	}
}

func TestPasswordChangeApplyFailureRollsBackAndKeepsOldCredential(t *testing.T) {
	coordinator := &testPasswordChangeCoordinator{applyErr: errors.New("vault unavailable")}
	service := configuredAuthService(t)
	service.config.PasswordCoordinator = coordinator
	if err := service.ChangePassword(t.Context(), "original secure password", "replacement secure password", nil); err == nil || !errors.Is(err, coordinator.applyErr) {
		t.Fatalf("password change error = %v", err)
	}
	if coordinator.prepared != 1 || coordinator.applyCalls != 1 || coordinator.rollbacks != 1 {
		t.Fatalf("coordinator calls: prepared=%d apply=%d rollback=%d", coordinator.prepared, coordinator.applyCalls, coordinator.rollbacks)
	}
	if err := service.VerifyPassword(t.Context(), "original secure password"); err != nil {
		t.Fatalf("old credential stopped working after rollback: %v", err)
	}
	var pending int
	if err := service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_password_change_journal`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("rolled-back password change retained %d journal rows", pending)
	}
}

func TestPasswordChangeJournalRecoversPreparedAndCommittedTransitions(t *testing.T) {
	for _, phase := range []string{"prepared", "committed"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.db")
			coordinator := &testPasswordChangeCoordinator{}
			service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: path, PasswordCoordinator: coordinator}, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.db.ExecContext(t.Context(), `INSERT INTO auth_password_change_journal(id,phase,plan,created_at) VALUES(1,?,?,?)`, phase, []byte("encrypted-envelope-plan"), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: path, PasswordCoordinator: coordinator}, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if phase == "prepared" && (coordinator.rollbacks != 1 || coordinator.applyCalls != 0) {
				t.Fatalf("prepared recovery calls: apply=%d rollback=%d", coordinator.applyCalls, coordinator.rollbacks)
			}
			if phase == "committed" && (coordinator.applyCalls != 1 || coordinator.rollbacks != 0) {
				t.Fatalf("committed recovery calls: apply=%d rollback=%d", coordinator.applyCalls, coordinator.rollbacks)
			}
			var pending int
			if err := restarted.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_password_change_journal`).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if pending != 0 {
				t.Fatalf("recovery retained %d journal rows", pending)
			}
		})
	}
}

func TestPasswordModeGuardAndResetBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original secure password"); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	coordinator := &testPasswordChangeCoordinator{protected: true}
	if blocked, err := Open(t.Context(), Config{Mode: ModeDisabled, ModeExplicit: true, MetadataPath: path, PasswordCoordinator: coordinator}, testLogger()); !errors.Is(err, ErrAuthenticationDisableBlocked) {
		if blocked != nil {
			_ = blocked.Close()
		}
		t.Fatalf("protected mode transition error = %v", err)
	}
	local := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	defer local.Close()
	if err := local.VerifyPassword(t.Context(), "original secure password"); err != nil {
		t.Fatalf("blocked mode transition removed authentication state: %v", err)
	}
	local.config.PasswordCoordinator = coordinator
	coordinator.protected = false
	coordinator.prepared = 0
	if err := local.RecoverPassword(t.Context(), "recovered secure password"); err != nil {
		t.Fatal(err)
	}
	if coordinator.prepared != 0 {
		t.Fatal("password reset without old password tried to rewrap vault keys")
	}
	separate, err := local.EncryptionPasswordSeparate(t.Context())
	if err != nil || !separate {
		t.Fatalf("password reset did not select a separate encryption password: separate=%v err=%v", separate, err)
	}
	if err := local.ChangePassword(t.Context(), "recovered secure password", "next secure password", nil); err != nil {
		t.Fatal(err)
	}
	if coordinator.prepared != 0 {
		t.Fatal("later login password change rewrapped separately protected vault keys")
	}
}

func TestVerifyPasswordFailsClosedWhilePasswordChangeIsPending(t *testing.T) {
	service := configuredAuthService(t)
	if _, err := service.db.ExecContext(t.Context(), `INSERT INTO auth_password_change_journal(id,phase,plan,created_at) VALUES(1,'prepared',X'01',?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPassword(t.Context(), "original secure password"); !errors.Is(err, ErrPasswordChangePending) {
		t.Fatalf("pending password change allowed authentication: %v", err)
	}
}

func TestSessionListingUsesOpaqueHashIdentifier(t *testing.T) {
	service := configuredAuthService(t)
	hash := sha256.Sum256([]byte("session"))
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	if _, err := service.db.ExecContext(t.Context(), `INSERT INTO auth_sessions(session_id_hash,created_at,last_activity_at,idle_expires_at,absolute_expires_at,client_description,session_data) VALUES(?,?,?,?,?,'Browser',X'')`, hash[:], now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	records, err := service.Sessions(t.Context(), hash[:])
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].Current || records[0].ID == "session" {
		t.Fatalf("unsafe session listing: %#v", records)
	}
}

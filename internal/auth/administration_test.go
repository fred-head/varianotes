package auth

import (
	"crypto/sha256"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

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

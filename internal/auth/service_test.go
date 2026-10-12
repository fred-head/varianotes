package auth

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInsecureSessionCookieSettingEmitsStartupWarning(t *testing.T) {
	var logs bytes.Buffer
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db"), CookieSecure: false}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if !strings.Contains(logs.String(), "session cookies are not marked Secure") {
		t.Fatalf("insecure cookie configuration was not warned: %s", logs.String())
	}
}

func TestServiceCreatesRestartSafeConfinedMetadata(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(directory, "auth.db")
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	state, err := service.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != ModeLocal || state.OwnerPrincipal != OwnerPrincipal || state.SetupCompleted || state.SchemaVersion != currentSchemaVersion {
		t.Fatalf("unexpected initial state %#v", state)
	}
	assertPermissions(t, directory, 0o700)
	assertPermissions(t, path, 0o600)

	var unrelatedTables int
	if err := service.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'auth_%' AND name != 'sqlite_sequence'
	`).Scan(&unrelatedTables); err != nil {
		t.Fatal(err)
	}
	if unrelatedTables != 0 {
		t.Fatalf("authentication database contains %d unrelated tables", unrelatedTables)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	defer restarted.Close()
	restartedState, err := restarted.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restartedState != state {
		t.Fatalf("state changed across restart: before %#v after %#v", state, restartedState)
	}
}

func TestModeTransitionInvalidatesAuthenticationArtifacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.db")
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := service.db.ExecContext(ctx, `UPDATE auth_configuration SET setup_completed = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(ctx, `
		INSERT INTO auth_sessions(session_id_hash, created_at, last_activity_at, idle_expires_at, absolute_expires_at)
		VALUES (?, ?, ?, ?, ?)
	`, []byte("hash-not-token"), now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(ctx, `
		INSERT INTO auth_password_credentials(
			owner_principal, algorithm, algorithm_version, memory_kib, iterations,
			parallelism, salt, password_hash, created_at, updated_at
		) VALUES (?, 'argon2id', 19, 65536, 3, 2, ?, ?, ?, ?)
	`, OwnerPrincipal, make([]byte, 16), make([]byte, 32), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(ctx, `INSERT INTO auth_mfa_configuration(id, enabled, secret_nonce, secret_ciphertext, created_at, updated_at) VALUES(1,1,?,?,?,?)`, make([]byte, 12), make([]byte, 32), now, now); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	disabled := openTestService(t, Config{Mode: ModeDisabled, ModeExplicit: true, MetadataPath: path})
	defer disabled.Close()
	state, err := disabled.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != ModeDisabled || !state.ModeExplicit || state.SetupCompleted {
		t.Fatalf("unsafe transition state %#v", state)
	}
	var sessions int
	if err := disabled.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("mode transition retained %d sessions", sessions)
	}
	var credentials int
	if err := disabled.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_password_credentials`).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if credentials != 0 {
		t.Fatalf("mode transition retained %d password credentials", credentials)
	}
	var mfa int
	if err := disabled.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_mfa_configuration`).Scan(&mfa); err != nil {
		t.Fatal(err)
	}
	if mfa != 0 {
		t.Fatalf("mode transition retained %d MFA configurations", mfa)
	}
}

func TestServiceFailsClosedForDamagedOrNewerMetadata(t *testing.T) {
	ctx := context.Background()
	t.Run("damaged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.db")
		if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		if service, err := Open(ctx, Config{Mode: ModeLocal, MetadataPath: path}, testLogger()); err == nil {
			service.Close()
			t.Fatal("expected damaged metadata to stop startup")
		}
	})

	t.Run("newer schema", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.db")
		service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
		if _, err := service.db.ExecContext(ctx, `INSERT INTO auth_schema_migrations(version, applied_at) VALUES (?, ?)`, currentSchemaVersion+1, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, err := Open(ctx, Config{Mode: ModeLocal, MetadataPath: path}, testLogger()); err == nil {
			reopened.Close()
			t.Fatal("expected newer schema to stop startup")
		}
	})
}

func TestServiceRejectsImplicitDisabledMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	service, err := Open(context.Background(), Config{Mode: ModeDisabled, MetadataPath: path}, testLogger())
	if err == nil {
		service.Close()
		t.Fatal("expected implicit disabled mode to be rejected")
	}
}

func TestVersionOneMetadataMigratesToPasswordCredentialSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.db")
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	if _, err := service.db.ExecContext(ctx, `DROP TABLE auth_password_credentials`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(ctx, `DELETE FROM auth_schema_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	migrated := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	defer migrated.Close()
	state, err := migrated.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != currentSchemaVersion || state.SetupCompleted {
		t.Fatalf("unsafe migrated state: %#v", state)
	}
	var tableCount int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'auth_password_credentials'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatal("password credential schema was not restored by migration")
	}
}

func TestMigrationIsAtomicAndDatabaseBackupIsPrivate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.db")
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: path})
	defer service.Close()

	err := service.applyMigration(ctx, migration{version: 99, statements: []string{
		`CREATE TABLE must_roll_back (id INTEGER PRIMARY KEY)`,
		`THIS IS NOT VALID SQL`,
	}})
	if err == nil {
		t.Fatal("expected migration failure")
	}
	var tableCount int
	if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'must_roll_back'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatal("failed migration was not rolled back")
	}

	backupPath, err := service.backupDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if backupPath == "" {
		t.Fatal("expected backup path")
	}
	assertPermissions(t, backupPath, 0o600)
}

func openTestService(t *testing.T, config Config) *Service {
	t.Helper()
	service, err := Open(context.Background(), config, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func assertPermissions(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("unexpected permissions for %s: got %o want %o", path, actual, expected)
	}
}

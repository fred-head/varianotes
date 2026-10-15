package app

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/auth"
)

func TestPasswordChangeJournalCannotBeRecoveredByConcurrentMFAResetCLI(t *testing.T) {
	registry, entries, _ := passwordChangeFixture(t, 2, false)
	paused, resume := make(chan struct{}), make(chan struct{})
	var pauseOnce, resumeOnce sync.Once
	coordinator := &vaultPasswordChangeCoordinator{registryPath: registry, afterEnvelopeWrite: func() {
		pauseOnce.Do(func() { close(paused); <-resume })
	}}
	config := auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(registry), "auth.db"), PasswordCoordinator: coordinator}
	service, err := auth.Open(t.Context(), config, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original vault password"); err != nil {
		t.Fatal(err)
	}
	cli := buildMFAResetCLI(t)
	changeDone := make(chan error, 1)
	go func() {
		changeDone <- service.ChangePassword(t.Context(), "original vault password", "replacement vault password", nil)
	}()
	t.Cleanup(func() { resumeOnce.Do(func() { close(resume) }); <-changeDone })
	select {
	case <-paused:
	case <-time.After(60 * time.Second):
		t.Fatal("password change did not reach the first durable envelope write")
	}
	assertPasswordJournalCount(t, config.MetadataPath, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, cli, "auth", "reset-mfa")
	command.Env = append(os.Environ(), "VARIANOTES_AUTH_MODE=local", "VARIANOTES_AUTH_METADATA="+config.MetadataPath, "VARIANOTES_NOTEBOOK_METADATA="+registry)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	cliDone := make(chan error, 1)
	go func() { cliDone <- command.Wait() }()
	cliReturned := false
	select {
	case err := <-cliDone:
		cliReturned = true
		t.Errorf("CLI returned during a live password change: %v; %s", err, output.String())
	case <-time.After(7 * time.Second):
		assertPasswordJournalCount(t, config.MetadataPath, 1)
	}
	resumeOnce.Do(func() { close(resume) })
	// Keep the result available for cleanup even when an assertion fails.
	changeErr := <-changeDone
	changeDone <- changeErr
	if changeErr != nil {
		t.Errorf("password change failed after CLI initialization: %v", changeErr)
	}
	if !cliReturned {
		if err := <-cliDone; err != nil {
			t.Errorf("CLI failed after password change completed: %v; %s", err, output.String())
		}
	}
	assertPasswordJournalCount(t, config.MetadataPath, 0)
	if err := service.VerifyPassword(t.Context(), "replacement vault password"); err != nil {
		t.Errorf("new login password rejected: %v", err)
	}
	if err := service.VerifyPassword(t.Context(), "original vault password"); err == nil {
		t.Error("old login password still accepted")
	}
	assertVaultPasswords(t, entries, registry, "replacement vault password")
}

func assertPasswordJournalCount(t *testing.T, filename string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM auth_password_change_journal").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("password journal count = %d, want %d", count, want)
	}
}

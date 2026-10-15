package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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

// This helper exits without defers, leaving real SQLite ownership and a durable
// envelope transaction for a different process to recover.
func TestAuthJournalOwnerProcessExit(t *testing.T) {
	registry := os.Getenv("VARIANOTES_TEST_CRASH_REGISTRY")
	if registry == "" {
		return
	}
	writes, stopAfter := 0, 1
	if os.Getenv("VARIANOTES_TEST_CRASH_PHASE") == "committed" {
		stopAfter = 3 // Two initial writes, then the first committed replay write.
	}
	coordinator := &vaultPasswordChangeCoordinator{registryPath: registry, afterEnvelopeWrite: func() {
		writes++
		if writes == stopAfter {
			os.Exit(91)
		}
	}}
	service, err := auth.Open(t.Context(), auth.Config{
		Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(registry), "auth.db"), PasswordCoordinator: coordinator,
	}, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.ChangePassword(t.Context(), "original vault password", "replacement vault password", nil); err != nil {
		t.Fatal(err)
	}
	t.Fatal("child did not stop at the selected durable boundary")
}

func TestPasswordJournalRecoveryAfterOwnerProcessExit(t *testing.T) {
	cli := buildMFAResetCLI(t)
	for _, scenario := range []string{"prepared", "committed", "unavailable vault"} {
		t.Run(scenario, func(t *testing.T) {
			registry, entries, _ := passwordChangeFixture(t, 2, false)
			config := auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(registry), "auth.db"), PasswordCoordinator: NewPasswordChangeCoordinator(registry)}
			service := setupJournalTestAuth(t, config)
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}
			phase := "prepared"
			if scenario == "committed" {
				phase = "committed"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAuthJournalOwnerProcessExit$")
			child.Env = append(os.Environ(), "VARIANOTES_TEST_CRASH_REGISTRY="+registry, "VARIANOTES_TEST_CRASH_PHASE="+phase)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 91 {
				t.Fatalf("child did not simulate process death: %v; %s", err, output)
			}
			beforePhase, beforePlan := readPasswordJournal(t, config.MetadataPath)
			if beforePhase != phase {
				t.Fatalf("interrupted phase = %q, want %q", beforePhase, phase)
			}
			if scenario == "unavailable vault" {
				// An unavailable registered vault must leave the full recovery
				// plan and the already updated envelope available for another try.
				envelopePath := filepath.Join(vaultEncryptionDirectory(registry, entries[0].ID), "key-envelope.json")
				beforeEnvelope, err := os.ReadFile(envelopePath)
				if err != nil {
					t.Fatal(err)
				}
				root := entries[1].LocalPath
				if err := os.Rename(root, root+".offline"); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Rename(root+".offline", root) }()
				if output, err := runJournalMFAResetCLI(ctx, cli, config.MetadataPath, registry); err == nil {
					t.Fatalf("CLI ignored unavailable recovery storage: %s", output)
				}
				afterPhase, afterPlan := readPasswordJournal(t, config.MetadataPath)
				afterEnvelope, err := os.ReadFile(envelopePath)
				if err != nil || beforePhase != afterPhase || !bytes.Equal(beforePlan, afterPlan) || !bytes.Equal(beforeEnvelope, afterEnvelope) {
					t.Fatalf("failed recovery changed journal/envelope: %v", err)
				}
				if err := os.Rename(root+".offline", root); err != nil {
					t.Fatal(err)
				}
			}
			if output, err := runJournalMFAResetCLI(ctx, cli, config.MetadataPath, registry); err != nil {
				t.Fatalf("CLI could not recover after owner process died: %v; %s", err, output)
			}
			assertPasswordJournalCount(t, config.MetadataPath, 0)
			recovered, err := auth.Open(t.Context(), config, passwordChangeLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			password, rejected := "original vault password", "replacement vault password"
			if phase == "committed" {
				password, rejected = rejected, password
			}
			if err := recovered.VerifyPassword(t.Context(), password); err != nil {
				t.Fatalf("recovered login rejected: %v", err)
			}
			if err := recovered.VerifyPassword(t.Context(), rejected); err == nil {
				t.Fatal("recovery retained the wrong login credential")
			}
			assertVaultPasswords(t, entries, registry, password)
		})
	}
}

func TestCanceledPasswordChangePreservesJournalForStartupRecovery(t *testing.T) {
	registry, entries, _ := passwordChangeFixture(t, 2, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	coordinator := &vaultPasswordChangeCoordinator{registryPath: registry, afterEnvelopeWrite: cancel}
	config := auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(registry), "auth.db"), PasswordCoordinator: coordinator}
	service := setupJournalTestAuth(t, config)
	defer service.Close()
	if err := service.ChangePassword(ctx, "original vault password", "replacement vault password", nil); !errors.Is(err, auth.ErrPasswordChangeRecovery) {
		t.Fatalf("canceled partial change did not report pending recovery: %v", err)
	}
	assertPasswordJournalCount(t, config.MetadataPath, 1)
	config.PasswordCoordinator = NewPasswordChangeCoordinator(registry)
	recovered, err := auth.Open(t.Context(), config, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	assertPasswordJournalCount(t, config.MetadataPath, 0)
	if err := recovered.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatal(err)
	}
	if err := recovered.VerifyPassword(t.Context(), "replacement vault password"); err == nil {
		t.Fatal("canceled change published the replacement credential")
	}
	assertVaultPasswords(t, entries, registry, "original vault password")
}

func setupJournalTestAuth(t *testing.T, config auth.Config) *auth.Service {
	t.Helper()
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
	return service
}

func runJournalMFAResetCLI(ctx context.Context, cli, authPath, registry string) ([]byte, error) {
	command := exec.CommandContext(ctx, cli, "auth", "reset-mfa")
	command.Env = append(os.Environ(), "VARIANOTES_AUTH_MODE=local", "VARIANOTES_AUTH_METADATA="+authPath, "VARIANOTES_NOTEBOOK_METADATA="+registry)
	return command.CombinedOutput()
}

func readPasswordJournal(t *testing.T, filename string) (string, []byte) {
	t.Helper()
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var phase string
	var plan []byte
	if err := db.QueryRowContext(t.Context(), "SELECT phase, plan FROM auth_password_change_journal WHERE id=1").Scan(&phase, &plan); err != nil {
		t.Fatal(err)
	}
	return phase, plan
}

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthJournalLockCancellationAndPathAliases(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "auth.db")
	if err := os.WriteFile(filename, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "auth-alias.db")
	if err := os.Symlink(filename, alias); err != nil {
		t.Fatal(err)
	}
	ownerContext, cancelOwner := context.WithCancel(t.Context())
	release, err := lockAuthJournal(ownerContext, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { release() }()
	cancelOwner()
	for _, path := range []string{filename, alias} {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		unexpectedRelease, err := lockAuthJournal(ctx, path)
		cancel()
		if err == nil {
			unexpectedRelease()
			t.Fatal("cancellation released the active owner's cross-instance lock")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock waiter did not honor cancellation: %v", err)
		}
	}
	release()
	// Release is used once by production callers. Avoid the deferred second call.
	release = func() {}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	releaseNext, err := lockAuthJournal(ctx, alias)
	if err != nil {
		t.Fatalf("canceled waiter stranded the next owner: %v", err)
	}
	releaseNext()
}

func TestAuthJournalInitializationCancellationPreservesPendingPlan(t *testing.T) {
	service := configuredAuthService(t)
	if err := service.savePendingPasswordChange(t.Context(), "prepared", []byte("encrypted-envelope-plan")); err != nil {
		t.Fatal(err)
	}
	release, err := lockAuthJournal(t.Context(), service.config.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if other, err := Open(ctx, service.Config(), testLogger()); !errors.Is(err, context.DeadlineExceeded) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("blocked initialization did not cancel: %v", err)
	}
	assertPendingAuthPlan(t, service)
}

func TestAuthJournalLockFailurePreservesPendingPlan(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "corrupt database"} {
		t.Run(kind, func(t *testing.T) {
			service := configuredAuthService(t)
			if err := service.savePendingPasswordChange(t.Context(), "prepared", []byte("encrypted-envelope-plan")); err != nil {
				t.Fatal(err)
			}
			filename := service.config.MetadataPath + ".journal-lock"
			if err := os.Remove(filename); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(service.config.MetadataPath, filename); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filename, 0o700); err != nil {
					t.Fatal(err)
				}
			case "corrupt database":
				if err := os.WriteFile(filename, []byte("not a SQLite database"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if other, err := Open(t.Context(), service.Config(), testLogger()); err == nil {
				_ = other.Close()
				t.Fatal("initialization ignored an unusable coordination file")
			}
			assertPendingAuthPlan(t, service)
		})
	}
}

func TestAuthJournalFailedRollbackPreservesPlanUntilRecovery(t *testing.T) {
	service := configuredAuthService(t)
	coordinator := &testPasswordChangeCoordinator{applyErr: errors.New("write failed"), rollbackErr: errors.New("rollback failed")}
	service.config.PasswordCoordinator = coordinator
	if err := service.ChangePassword(t.Context(), "original secure password", "replacement secure password", nil); err == nil {
		t.Fatal("failed change unexpectedly succeeded")
	}
	phase, _, err := service.pendingPasswordChange(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if phase == "" {
		t.Fatal("change did not reach its failing envelope rollback")
	}
	assertPendingAuthPlan(t, service)
	if err := service.RecoverPassword(t.Context(), "another secure password"); !errors.Is(err, ErrPasswordChangePending) {
		t.Fatalf("operator reset ignored pending envelope recovery: %v", err)
	}
	if _, _, err := service.BeginPasswordRecovery(t.Context(), "unused", "another secure password"); !errors.Is(err, ErrPasswordChangePending) {
		t.Fatalf("code-based reset ignored pending envelope recovery: %v", err)
	}
	if other, err := Open(t.Context(), service.Config(), testLogger()); err == nil {
		_ = other.Close()
		t.Fatal("startup discarded a plan after failed rollback")
	}
	assertPendingAuthPlan(t, service)
	coordinator.applyErr, coordinator.rollbackErr = nil, nil
	other, err := Open(t.Context(), service.Config(), testLogger())
	if err != nil {
		t.Fatalf("recovery did not retry after storage became available: %v", err)
	}
	defer other.Close()
	if phase, _, err := other.pendingPasswordChange(t.Context()); err != nil || phase != "" {
		t.Fatalf("successful recovery did not complete the journal: %s %v", phase, err)
	}
}

func assertPendingAuthPlan(t *testing.T, service *Service) {
	t.Helper()
	phase, plan, err := service.pendingPasswordChange(t.Context())
	if err != nil || phase != "prepared" || string(plan) != "encrypted-envelope-plan" {
		t.Fatalf("pending plan changed: phase=%s plan=%q err=%v", phase, plan, err)
	}
}

func TestAuthJournalCredentialEntrypointsWaitForOtherOwner(t *testing.T) {
	service := configuredAuthService(t)
	release, err := lockAuthJournal(t.Context(), service.config.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code := "VNRC1-" + strings.Repeat("12345678-", 7) + "12345678"
	calls := map[string]func(context.Context) error{
		"password change": func(ctx context.Context) error {
			return service.ChangePassword(ctx, "original secure password", "replacement secure password", nil)
		},
		"operator reset": func(ctx context.Context) error {
			return service.RecoverPassword(ctx, "replacement secure password")
		},
		"prepare code": func(ctx context.Context) error {
			_, _, err := service.BeginEncryptionRecoveryCode(ctx, "original secure password", "", make([]byte, 32))
			return err
		},
		"confirm code": func(ctx context.Context) error {
			return service.ConfirmEncryptionRecoveryCode(ctx, "challenge", code, true, make([]byte, 32))
		},
		"prepare password recovery": func(ctx context.Context) error {
			_, _, err := service.BeginPasswordRecovery(ctx, code, "replacement secure password")
			return err
		},
		"confirm password recovery": func(ctx context.Context) error {
			return service.ConfirmPasswordRecovery(ctx, "challenge", "replacement secure password", code, true)
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if err := call(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("credential entrypoint bypassed active ownership: %v", err)
			}
		})
	}
	if phase, _, err := service.pendingPasswordChange(t.Context()); err != nil || phase != "" {
		t.Fatalf("canceled waiter created a password journal: %s %v", phase, err)
	}
	if phase, _, err := service.pendingEncryptionRecovery(t.Context()); err != nil || phase != "" {
		t.Fatalf("canceled waiter created a recovery journal: %s %v", phase, err)
	}
}

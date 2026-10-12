package auth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestMFACryptographicDomainsUseVariaNotesIdentity(t *testing.T) {
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")})
	defer service.Close()

	const secret = "fresh-install-authenticator-secret"
	nonce, ciphertext, err := service.encryptMFASecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(service.encryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte("varianotes-totp-v1"))
	if err != nil || string(plaintext) != secret {
		t.Fatalf("VariaNotes MFA context did not decrypt its secret: %v", err)
	}
	if _, err := gcm.Open(nil, nonce, ciphertext, []byte("other-product-totp-v1")); err == nil {
		t.Fatal("MFA ciphertext was accepted under a different encryption context")
	}

	code := "ABCD-EFGH"
	wantRecoveryHash := sha256.Sum256([]byte("varianotes-mfa-recovery-v1\x00" + normalizeRecoveryCode(code)))
	if got := recoveryHash(code); got != wantRecoveryHash {
		t.Fatal("recovery-code hash does not use the VariaNotes domain")
	}
}

func TestMFAEnrollmentEncryptionRecoveryAndDisable(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: filepath.Join(directory, "auth.db")})
	defer service.Close()
	setupPassword(t, service, "a sufficiently long password")

	enrollment, err := service.BeginMFAEnrollment(ctx, "a sufficiently long password", "", testMFASessionHash())
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.Secret == "" || len(enrollment.RecoveryCodes) != mfaRecoveryCodeCount || len(enrollment.QRCode) < 100 {
		t.Fatalf("incomplete enrollment: %#v", enrollment)
	}
	if enabled, _ := service.MFAEnabled(ctx); enabled {
		t.Fatal("MFA enabled before confirmation")
	}
	database, err := os.ReadFile(service.config.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(database, []byte(enrollment.Secret)) {
		t.Fatal("plaintext TOTP secret is present in SQLite")
	}
	keyPath := filepath.Join(directory, "auth.key")
	assertPermissions(t, keyPath, 0o600)

	code, err := totp.GenerateCode(enrollment.Secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmMFAEnrollment(ctx, code, false, testMFASessionHash()); err == nil {
		t.Fatal("MFA enabled without recovery-code confirmation")
	}
	if err := service.ConfirmMFAEnrollment(ctx, code, true, testMFASessionHash()); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := service.MFAEnabled(ctx); !enabled {
		t.Fatal("MFA was not enabled")
	}
	if _, enabled, err := service.decryptMFASecret(ctx); err != nil || !enabled {
		t.Fatalf("encrypted secret unavailable: %v", err)
	}

	if err := service.VerifySecondFactor(ctx, code); !errors.Is(err, ErrMFAInvalid) {
		t.Fatal("TOTP used for enrollment confirmation was replayable")
	}
	if err := service.VerifySecondFactor(ctx, enrollment.RecoveryCodes[0]); err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
	if err := service.VerifySecondFactor(ctx, enrollment.RecoveryCodes[0]); !errors.Is(err, ErrMFAInvalid) {
		t.Fatal("recovery code was reusable")
	}

	newCodes, err := service.RegenerateRecoveryCodes(ctx, "a sufficiently long password", enrollment.RecoveryCodes[1])
	if err != nil || len(newCodes) != mfaRecoveryCodeCount {
		t.Fatalf("regenerate recovery codes: %v", err)
	}
	if err := service.VerifySecondFactor(ctx, enrollment.RecoveryCodes[2]); !errors.Is(err, ErrMFAInvalid) {
		t.Fatal("old recovery code survived regeneration")
	}
	if err := service.DisableMFA(ctx, "a sufficiently long password", newCodes[0]); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := service.MFAEnabled(ctx); enabled {
		t.Fatal("MFA remained enabled")
	}
}

func TestReplacingMFAKeepsExistingFactorUntilConfirmation(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")})
	defer service.Close()
	setupPassword(t, service, "a sufficiently long password")
	first, _ := service.BeginMFAEnrollment(ctx, "a sufficiently long password", "", testMFASessionHash())
	firstCode, _ := totp.GenerateCode(first.Secret, time.Now().UTC())
	if err := service.ConfirmMFAEnrollment(ctx, firstCode, true, testMFASessionHash()); err != nil {
		t.Fatal(err)
	}

	replacement, err := service.BeginMFAEnrollment(ctx, "a sufficiently long password", first.RecoveryCodes[0], testMFASessionHash())
	if err != nil {
		t.Fatal(err)
	}
	if enabled, _ := service.MFAEnabled(ctx); !enabled {
		t.Fatal("replacement disabled existing MFA before confirmation")
	}
	if err := service.VerifySecondFactor(ctx, first.RecoveryCodes[1]); err != nil {
		t.Fatal("existing recovery codes were invalidated before confirmation")
	}
	newCode, _ := totp.GenerateCode(replacement.Secret, time.Now().UTC())
	if err := service.ConfirmMFAEnrollment(ctx, newCode, true, testMFASessionHash()); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifySecondFactor(ctx, first.RecoveryCodes[2]); !errors.Is(err, ErrMFAInvalid) {
		t.Fatal("old recovery code survived completed replacement")
	}
}

func TestMFAEnrollmentExpiresIsSessionBoundAndCanBeCancelled(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")})
	defer service.Close()
	setupPassword(t, service, "a sufficiently long password")
	sessionA := testMFASessionHash()
	sessionB := sha256.Sum256([]byte("another-browser-session"))

	enrollment, err := service.BeginMFAEnrollment(ctx, "a sufficiently long password", "", sessionA)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enrollment.Secret, time.Now().UTC())
	if err := service.ConfirmMFAEnrollment(ctx, code, true, sessionB[:]); !errors.Is(err, ErrMFAUnavailable) {
		t.Fatalf("different session confirmed pending MFA enrollment: %v", err)
	}
	if _, err := service.db.ExecContext(ctx, `UPDATE auth_mfa_configuration SET pending_expires_at=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmMFAEnrollment(ctx, code, true, sessionA); !errors.Is(err, ErrMFAUnavailable) {
		t.Fatalf("expired MFA enrollment was accepted: %v", err)
	}

	if _, err := service.BeginMFAEnrollment(ctx, "a sufficiently long password", "", sessionA); err != nil {
		t.Fatal(err)
	}
	if err := service.CancelMFAEnrollment(ctx, sessionB[:]); !errors.Is(err, ErrMFAUnavailable) {
		t.Fatalf("another session cancelled enrollment: %v", err)
	}
	var pending int
	if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_mfa_configuration WHERE pending_secret_nonce IS NOT NULL`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("another session cancelled enrollment: count=%d err=%v", pending, err)
	}
	if err := service.CancelMFAEnrollment(ctx, sessionA); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_recovery_artifacts WHERE kind='mfa_recovery_pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending recovery codes survived cancellation: count=%d err=%v", pending, err)
	}
}

func TestMissingMFAEncryptionKeyFailsClosed(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	config := Config{Mode: ModeLocal, MetadataPath: filepath.Join(directory, "auth.db")}
	service := openTestService(t, config)
	setupPassword(t, service, "a sufficiently long password")
	enrollment, _ := service.BeginMFAEnrollment(ctx, "a sufficiently long password", "", testMFASessionHash())
	code, _ := totp.GenerateCode(enrollment.Secret, time.Now().UTC())
	if err := service.ConfirmMFAEnrollment(ctx, code, true, testMFASessionHash()); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "auth.key")); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(ctx, config, testLogger()); err == nil {
		reopened.Close()
		t.Fatal("missing MFA encryption key did not stop startup")
	}
	config.AllowMFAKeyRecovery = true
	recovery, err := Open(ctx, config, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.ResetMFA(ctx); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Close(); err != nil {
		t.Fatal(err)
	}
	config.AllowMFAKeyRecovery = false
	reopened, err := Open(ctx, config, testLogger())
	if err != nil {
		t.Fatalf("explicit MFA reset did not restore startup: %v", err)
	}
	reopened.Close()
}

func TestExplicitMFARecoveryQuarantinesCorruptEncryptionKey(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "auth.key")
	if err := os.WriteFile(keyPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{Mode: ModeLocal, MetadataPath: filepath.Join(directory, "auth.db"), EncryptionKeyPath: keyPath, AllowMFAKeyRecovery: true}
	service, err := Open(t.Context(), config, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	key, err := os.ReadFile(keyPath)
	if err != nil || len(key) != 32 {
		t.Fatalf("replacement encryption key was not generated: bytes=%d err=%v", len(key), err)
	}
	quarantined, err := filepath.Glob(keyPath + ".invalid-*")
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("corrupt encryption key was not quarantined: paths=%v err=%v", quarantined, err)
	}
}

func TestTOTPClockWindowAndAtomicRecoveryCodeConsumption(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")})
	defer service.Close()
	setupPassword(t, service, "a sufficiently long password")
	enrollment, err := service.BeginMFAEnrollment(ctx, "a sufficiently long password", "", testMFASessionHash())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	confirmation, _ := totp.GenerateCode(enrollment.Secret, now)
	if err := service.ConfirmMFAEnrollment(ctx, confirmation, true, testMFASessionHash()); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifySecondFactor(ctx, confirmation); !errors.Is(err, ErrMFAInvalid) {
		t.Fatal("TOTP used for enrollment confirmation was replayable")
	}
	previous, _ := totp.GenerateCode(enrollment.Secret, now.Add(-30*time.Second))
	if _, valid := matchingTOTPStep(enrollment.Secret, previous, now); !valid {
		t.Fatal("documented previous TOTP clock window was rejected")
	}
	tooOld, _ := totp.GenerateCode(enrollment.Secret, now.Add(-60*time.Second))
	if err := service.VerifySecondFactor(ctx, tooOld); !errors.Is(err, ErrMFAInvalid) {
		t.Fatal("TOTP outside the documented clock window was accepted")
	}

	var successes int
	var mutex sync.Mutex
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if service.VerifySecondFactor(ctx, enrollment.RecoveryCodes[0]) == nil {
				mutex.Lock()
				successes++
				mutex.Unlock()
			}
		}()
	}
	group.Wait()
	if successes != 1 {
		t.Fatalf("one-time recovery code succeeded %d times; want exactly once", successes)
	}
}

func TestMFASecretsAreNotLoggedAndResetDoesNotTouchNotes(t *testing.T) {
	directory := t.TempDir()
	notePath := filepath.Join(directory, "notebook", "Keep me.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o700); err != nil {
		t.Fatal(err)
	}
	const note = "# Canonical note\n\nAuthentication recovery must not alter this.\n"
	if err := os.WriteFile(notePath, []byte(note), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(directory, "auth.db")}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	setupPassword(t, service, "a sufficiently long password")
	enrollment, err := service.BeginMFAEnrollment(t.Context(), "a sufficiently long password", "", testMFASessionHash())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enrollment.Secret, time.Now().UTC())
	if err := service.ConfirmMFAEnrollment(t.Context(), code, true, testMFASessionHash()); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetMFA(t.Context()); err != nil {
		t.Fatal(err)
	}
	logged := logs.String()
	for _, secret := range append([]string{enrollment.Secret, code}, enrollment.RecoveryCodes...) {
		if strings.Contains(logged, secret) {
			t.Fatal("MFA secret or submitted code was written to logs")
		}
	}
	content, err := os.ReadFile(notePath)
	if err != nil || string(content) != note {
		t.Fatalf("MFA recovery modified canonical note data: %v", err)
	}
}

func setupPassword(t *testing.T, service *Service, password string) {
	t.Helper()
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, password); err != nil {
		t.Fatal(err)
	}
}

func testMFASessionHash() []byte {
	digest := sha256.Sum256([]byte("varianotes-test-mfa-session"))
	return digest[:]
}

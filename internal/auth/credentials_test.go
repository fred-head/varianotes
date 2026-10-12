package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestBootstrapSetupIsOperatorAuthorizedOneTimeAndRestartSafe(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: testDatabasePath(t)})
	token, err := service.CreateBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(token.Value) < 40 || time.Until(token.ExpiresAt) <= 0 {
		t.Fatalf("invalid bootstrap token metadata: %#v", token)
	}
	var stored []byte
	if err := service.db.QueryRowContext(ctx, `SELECT secret_hash FROM auth_recovery_artifacts WHERE id = ?`, bootstrapArtifactID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == token.Value || strings.Contains(string(stored), token.Value) {
		t.Fatal("plaintext bootstrap token was persisted")
	}
	wantDigest := sha256.Sum256([]byte(token.Value))
	if string(stored) != string(wantDigest[:]) {
		t.Fatal("bootstrap token was not stored as the expected one-way digest")
	}

	password := "  sehr lange 🔐 Passphrase  "
	if err := service.CompleteSetup(ctx, token.Value, password); err != nil {
		t.Fatal(err)
	}
	state, err := service.State(ctx)
	if err != nil || !state.SetupCompleted {
		t.Fatalf("owner setup was not completed: %#v, %v", state, err)
	}
	if err := service.VerifyPassword(ctx, password); err != nil {
		t.Fatalf("stored Unicode passphrase did not verify: %v", err)
	}
	if err := service.VerifyPassword(ctx, strings.TrimSpace(password)); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("password whitespace was not preserved: %v", err)
	}
	if err := service.CompleteSetup(ctx, token.Value, password); !errors.Is(err, ErrInvalidBootstrap) && !errors.Is(err, ErrSetupUnavailable) {
		t.Fatalf("bootstrap token was reusable: %v", err)
	}
	if _, err := service.CreateBootstrapToken(ctx); !errors.Is(err, ErrSetupUnavailable) {
		t.Fatalf("created token after completed setup: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := openTestService(t, Config{Mode: ModeLocal, MetadataPath: service.config.MetadataPath})
	defer restarted.Close()
	if err := restarted.VerifyPassword(ctx, password); err != nil {
		t.Fatalf("password did not survive restart: %v", err)
	}
}

func TestBootstrapRotationExpiryAndWrongTokenAreRejected(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: testDatabasePath(t)})
	defer service.Close()
	first, err := service.CreateBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{first.Value, "wrong-token", ""} {
		if err := service.CompleteSetup(ctx, invalid, "valid passphrase 🔐"); !errors.Is(err, ErrInvalidBootstrap) {
			t.Fatalf("accepted invalid token %q: %v", invalid, err)
		}
	}
	if _, err := service.db.ExecContext(ctx, `UPDATE auth_recovery_artifacts SET expires_at = ? WHERE id = ?`, formatTime(time.Now().Add(-time.Second)), bootstrapArtifactID); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(ctx, second.Value, "valid passphrase 🔐"); !errors.Is(err, ErrInvalidBootstrap) {
		t.Fatalf("accepted expired token: %v", err)
	}
}

func TestPasswordValidationRejectsWeakOrExcessiveInputsBeforeHashing(t *testing.T) {
	tests := []struct {
		password string
		want     error
	}{
		{"short", ErrPasswordTooShort},
		{strings.Repeat("x", maximumPasswordBytes+1), ErrPasswordTooLarge},
		{string([]byte{0xff, 0xfe, 0xfd}), ErrInvalidPassword},
		{strings.Repeat(" ", minimumPasswordRunes), ErrPasswordTooWeak},
		{"passwordpassword", ErrPasswordTooWeak},
	}
	for _, test := range tests {
		if err := ValidatePassword(test.password); !errors.Is(err, test.want) {
			t.Fatalf("ValidatePassword() = %v, want %v", err, test.want)
		}
	}
}

func TestPasswordUsesVersionedArgon2idParametersAndUpgrades(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: testDatabasePath(t)})
	defer service.Close()
	token, err := service.CreateBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	password := "violet meadow lantern orbit"
	if err := service.CompleteSetup(ctx, token.Value, password); err != nil {
		t.Fatal(err)
	}
	credential, err := service.loadPasswordCredential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if credential.AlgorithmVersion != argon2.Version || credential.Parameters != defaultPasswordParameters || len(credential.Salt) != passwordSaltBytes {
		t.Fatalf("unexpected Argon2id credential metadata: %#v", credential)
	}
	if string(credential.Hash) == password || string(credential.Salt) == password {
		t.Fatal("password was stored in credential material")
	}

	weaker := PasswordParameters{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	oldCredential, err := derivePasswordCredential(password, weaker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(ctx, `
		UPDATE auth_password_credentials SET memory_kib = ?, iterations = ?, parallelism = ?, salt = ?, password_hash = ?
		WHERE owner_principal = ?
	`, weaker.MemoryKiB, weaker.Iterations, weaker.Parallelism, oldCredential.Salt, oldCredential.Hash, OwnerPrincipal); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPassword(ctx, password); err != nil {
		t.Fatal(err)
	}
	upgraded, err := service.loadPasswordCredential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Parameters != defaultPasswordParameters {
		t.Fatalf("credential parameters were not upgraded: %#v", upgraded.Parameters)
	}
	if string(upgraded.Salt) == string(oldCredential.Salt) {
		t.Fatal("parameter upgrade reused the password salt")
	}
}

func TestParameterUpgradeNeverLowersAnExistingCost(t *testing.T) {
	existing := PasswordParameters{MemoryKiB: 128 * 1024, Iterations: 2, Parallelism: 4, SaltLength: 32, KeyLength: 64}
	target := strongerPasswordParameters(existing, defaultPasswordParameters)
	if target.MemoryKiB != existing.MemoryKiB || target.Iterations != defaultPasswordParameters.Iterations ||
		target.Parallelism != existing.Parallelism || target.SaltLength != existing.SaltLength || target.KeyLength != existing.KeyLength {
		t.Fatalf("parameter upgrade lowered an existing cost: %#v", target)
	}
}

func TestOversizedStoredCredentialMaterialIsRejectedBeforeConversion(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: testDatabasePath(t)})
	defer service.Close()
	token, err := service.CreateBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(ctx, token.Value, "credential length test password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(ctx, `UPDATE auth_password_credentials SET salt = zeroblob(65)`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.loadPasswordCredential(ctx); err == nil || err.Error() != "stored password parameters are invalid" {
		t.Fatalf("oversized credential material was accepted: %v", err)
	}
}

func TestOnlyOneConcurrentSetupCanComplete(t *testing.T) {
	ctx := context.Background()
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: testDatabasePath(t)})
	defer service.Close()
	token, err := service.CreateBootstrapToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsSeen <- service.CompleteSetup(ctx, token.Value, "parallel setup password")
		}()
	}
	wait.Wait()
	close(errorsSeen)
	successes := 0
	failures := 0
	for err := range errorsSeen {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInvalidBootstrap) || errors.Is(err, ErrSetupUnavailable) {
			failures++
		} else {
			t.Fatalf("unexpected concurrent setup result: %v", err)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent setup results: %d successes, %d expected failures", successes, failures)
	}
}

func TestMissingCredentialPerformsDummyVerificationAndReturnsUniformError(t *testing.T) {
	service := openTestService(t, Config{Mode: ModeLocal, MetadataPath: testDatabasePath(t)})
	defer service.Close()
	for _, password := range []string{"", "wrong", strings.Repeat("x", maximumPasswordBytes+1)} {
		if err := service.VerifyPassword(context.Background(), password); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("non-uniform authentication error for %q: %v", password, err)
		}
	}
}

func BenchmarkArgon2idDefaultParameters(b *testing.B) {
	for range b.N {
		_, err := derivePasswordCredential("benchmark password only", defaultPasswordParameters)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func testDatabasePath(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/auth.db"
}

var _ queryRower = (*sql.Tx)(nil)

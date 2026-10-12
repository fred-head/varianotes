package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	BootstrapTokenLifetime = 15 * time.Minute
	bootstrapArtifactID    = "initial-owner-setup"
	bootstrapArtifactKind  = "bootstrap-setup"
	bootstrapTokenBytes    = 32
	passwordSaltBytes      = 16
	minimumPasswordRunes   = 15
	maximumPasswordBytes   = 1024
)

var (
	ErrSetupUnavailable = errors.New("owner setup is unavailable")
	ErrInvalidBootstrap = errors.New("invalid or expired setup authorization")
	ErrPasswordTooShort = errors.New("password must contain at least 15 characters")
	ErrPasswordTooLarge = errors.New("password must not exceed 1024 bytes")
	ErrInvalidPassword  = errors.New("password must be valid UTF-8")
	ErrPasswordTooWeak  = errors.New("password is too common or easily guessed")
	ErrAuthentication   = errors.New("authentication failed")
)

var commonPasswords = map[string]struct{}{
	"123456789012345": {}, "1234567890123456": {}, "12345678901234567890": {},
	"adminadminadmin": {}, "administrator": {}, "changemechangeme": {},
	"correcthorsebatterystaple": {}, "iloveyouiloveyou": {}, "letmeinletmeinletmein": {},
	"passwordpassword": {}, "password123456": {}, "password123456789": {},
	"qwertyqwertyqwerty": {}, "varianotesvarianotes": {}, "welcomewelcome": {},
}

type PasswordParameters struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// The default parameters use the second Argon2id profile recommended by RFC
// 9106, with parallelism reduced for typical small self-hosted instances. The
// memory cost remains 64 MiB and exceeds OWASP's current minimum profile.
var defaultPasswordParameters = PasswordParameters{
	MemoryKiB:   64 * 1024,
	Iterations:  3,
	Parallelism: 2,
	SaltLength:  passwordSaltBytes,
	KeyLength:   32,
}

type BootstrapToken struct {
	Value     string
	ExpiresAt time.Time
}

type passwordCredential struct {
	AlgorithmVersion int
	Parameters       PasswordParameters
	Salt             []byte
	Hash             []byte
}

func (s *Service) CreateBootstrapToken(ctx context.Context) (BootstrapToken, error) {
	state, err := s.State(ctx)
	if err != nil {
		return BootstrapToken{}, err
	}
	if state.Mode != ModeLocal || state.SetupCompleted {
		return BootstrapToken{}, ErrSetupUnavailable
	}

	secret := make([]byte, bootstrapTokenBytes)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return BootstrapToken{}, fmt.Errorf("generate setup authorization: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(secret)
	digest := sha256.Sum256([]byte(value))
	now := time.Now().UTC()
	expiresAt := now.Add(BootstrapTokenLifetime)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapToken{}, fmt.Errorf("begin setup authorization: %w", err)
	}
	defer tx.Rollback()
	var setupCompleted int
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT mode, setup_completed FROM auth_configuration WHERE id = 1`).Scan(&mode, &setupCompleted); err != nil {
		return BootstrapToken{}, fmt.Errorf("read setup state: %w", err)
	}
	if Mode(mode) != ModeLocal || setupCompleted != 0 {
		return BootstrapToken{}, ErrSetupUnavailable
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_artifacts WHERE kind = ?`, bootstrapArtifactKind); err != nil {
		return BootstrapToken{}, fmt.Errorf("replace setup authorization: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_recovery_artifacts(id, kind, secret_hash, created_at, expires_at, consumed_at)
		VALUES (?, ?, ?, ?, ?, NULL)
	`, bootstrapArtifactID, bootstrapArtifactKind, digest[:], formatTime(now), formatTime(expiresAt)); err != nil {
		return BootstrapToken{}, fmt.Errorf("store setup authorization: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_security_events(event_type, occurred_at, outcome, details)
		VALUES ('bootstrap_created', ?, 'success', '')
	`, formatTime(now)); err != nil {
		return BootstrapToken{}, fmt.Errorf("record setup authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BootstrapToken{}, fmt.Errorf("commit setup authorization: %w", err)
	}
	return BootstrapToken{Value: value, ExpiresAt: expiresAt}, nil
}

func (s *Service) CompleteSetup(ctx context.Context, bootstrapToken, password string) error {
	state, err := s.State(ctx)
	if err != nil {
		return err
	}
	if state.Mode != ModeLocal || state.SetupCompleted {
		return ErrSetupUnavailable
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}
	if err := s.validateBootstrapToken(ctx, bootstrapToken); err != nil {
		return err
	}
	credential, err := derivePasswordCredential(password, defaultPasswordParameters)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin owner setup: %w", err)
	}
	defer tx.Rollback()
	if err := validateBootstrapTokenIn(ctx, tx, bootstrapToken, now); err != nil {
		return err
	}
	var mode string
	var setupCompleted int
	if err := tx.QueryRowContext(ctx, `SELECT mode, setup_completed FROM auth_configuration WHERE id = 1`).Scan(&mode, &setupCompleted); err != nil {
		return fmt.Errorf("read owner setup state: %w", err)
	}
	if Mode(mode) != ModeLocal || setupCompleted != 0 {
		return ErrSetupUnavailable
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO auth_password_credentials(
			owner_principal, algorithm, algorithm_version, memory_kib, iterations,
			parallelism, salt, password_hash, created_at, updated_at
		) VALUES (?, 'argon2id', ?, ?, ?, ?, ?, ?, ?, ?)
	`, OwnerPrincipal, credential.AlgorithmVersion, credential.Parameters.MemoryKiB,
		credential.Parameters.Iterations, credential.Parameters.Parallelism,
		credential.Salt, credential.Hash, formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("store owner credential: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm owner credential: %w", err)
	}
	if affected != 1 {
		return ErrSetupUnavailable
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE auth_configuration SET setup_completed = 1, updated_at = ?
		WHERE id = 1 AND mode = 'local' AND setup_completed = 0
	`, formatTime(now))
	if err != nil {
		return fmt.Errorf("complete owner setup: %w", err)
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm owner setup: %w", err)
	}
	if affected != 1 {
		return ErrSetupUnavailable
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE auth_recovery_artifacts SET consumed_at = ?
		WHERE id = ? AND kind = ? AND consumed_at IS NULL
	`, formatTime(now), bootstrapArtifactID, bootstrapArtifactKind)
	if err != nil {
		return fmt.Errorf("consume setup authorization: %w", err)
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("confirm setup authorization consumption: %w", err)
	}
	if affected != 1 {
		return ErrInvalidBootstrap
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_security_events(event_type, occurred_at, outcome, details)
		VALUES ('owner_setup_completed', ?, 'success', '')
	`, formatTime(now)); err != nil {
		return fmt.Errorf("record owner setup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit owner setup: %w", err)
	}
	return nil
}

func (s *Service) VerifyPassword(ctx context.Context, password string) error {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	return s.verifyPassword(ctx, password)
}

func (s *Service) verifyPassword(ctx context.Context, password string) error {
	phase, _, journalErr := s.pendingPasswordChange(ctx)
	if journalErr != nil {
		return journalErr
	}
	if phase != "" {
		return ErrPasswordChangePending
	}
	if len(password) > maximumPasswordBytes || !utf8.ValidString(password) {
		return ErrAuthentication
	}
	credential, err := s.loadPasswordCredential(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		credential = dummyPasswordCredential()
	}
	if !validStoredParameters(credential) {
		return errors.New("stored password parameters are invalid")
	}
	actual := argon2.IDKey([]byte(password), credential.Salt, credential.Parameters.Iterations,
		credential.Parameters.MemoryKiB, credential.Parameters.Parallelism, credential.Parameters.KeyLength)
	matched := subtle.ConstantTimeCompare(actual, credential.Hash) == 1
	if err != nil || !matched {
		return ErrAuthentication
	}
	if credentialNeedsUpgrade(credential) {
		if err := s.upgradePasswordCredential(ctx, password, credential); err != nil {
			s.logger.Warn("password parameter upgrade failed", "error", err)
		}
	}
	return nil
}

func ValidatePassword(password string) error {
	if len(password) > maximumPasswordBytes {
		return ErrPasswordTooLarge
	}
	if !utf8.ValidString(password) {
		return ErrInvalidPassword
	}
	if utf8.RuneCountInString(password) < minimumPasswordRunes {
		return ErrPasswordTooShort
	}
	if passwordIsEasilyGuessed(password) {
		return ErrPasswordTooWeak
	}
	return nil
}

func passwordIsEasilyGuessed(password string) bool {
	normalized := strings.Map(func(value rune) rune {
		if unicode.IsSpace(value) {
			return -1
		}
		return unicode.ToLower(value)
	}, password)
	if _, common := commonPasswords[normalized]; common {
		return true
	}
	var first rune
	allSame := true
	for index, value := range []rune(password) {
		if index == 0 {
			first = value
			continue
		}
		if value != first {
			allSame = false
			break
		}
	}
	return allSame
}

func (s *Service) validateBootstrapToken(ctx context.Context, token string) error {
	return validateBootstrapTokenIn(ctx, s.db, token, time.Now().UTC())
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateBootstrapTokenIn(ctx context.Context, query queryRower, token string, now time.Time) error {
	if len(token) == 0 || len(token) > 256 {
		return ErrInvalidBootstrap
	}
	var storedHash []byte
	var expiresAtText string
	var consumedAt sql.NullString
	err := query.QueryRowContext(ctx, `
		SELECT secret_hash, expires_at, consumed_at FROM auth_recovery_artifacts
		WHERE id = ? AND kind = ?
	`, bootstrapArtifactID, bootstrapArtifactKind).Scan(&storedHash, &expiresAtText, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidBootstrap
	}
	if err != nil {
		return fmt.Errorf("read setup authorization: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
	if err != nil {
		return errors.New("stored setup authorization is invalid")
	}
	digest := sha256.Sum256([]byte(token))
	valid := subtle.ConstantTimeCompare(digest[:], storedHash) == 1
	if !valid || consumedAt.Valid || !now.Before(expiresAt) {
		return ErrInvalidBootstrap
	}
	return nil
}

func derivePasswordCredential(password string, parameters PasswordParameters) (passwordCredential, error) {
	if !validParameters(parameters) {
		return passwordCredential{}, errors.New("invalid password hashing parameters")
	}
	salt := make([]byte, parameters.SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return passwordCredential{}, fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, parameters.Iterations, parameters.MemoryKiB, parameters.Parallelism, parameters.KeyLength)
	return passwordCredential{AlgorithmVersion: argon2.Version, Parameters: parameters, Salt: salt, Hash: hash}, nil
}

func (s *Service) loadPasswordCredential(ctx context.Context) (passwordCredential, error) {
	var credential passwordCredential
	var memoryKiB, iterations, parallelism int64
	err := s.db.QueryRowContext(ctx, `
		SELECT algorithm_version, memory_kib, iterations, parallelism, salt, password_hash
		FROM auth_password_credentials WHERE owner_principal = ? AND algorithm = 'argon2id'
	`, OwnerPrincipal).Scan(&credential.AlgorithmVersion, &memoryKiB, &iterations, &parallelism, &credential.Salt, &credential.Hash)
	if err != nil {
		return passwordCredential{}, err
	}
	if memoryKiB < 0 || iterations < 0 || parallelism < 0 || memoryKiB > int64(^uint32(0)) || iterations > int64(^uint32(0)) || parallelism > int64(^uint8(0)) {
		return passwordCredential{}, errors.New("stored password parameters are invalid")
	}
	saltLength, err := boundedCredentialLength(credential.Salt, 16, 64)
	if err != nil {
		return passwordCredential{}, err
	}
	keyLength, err := boundedCredentialLength(credential.Hash, 32, 64)
	if err != nil {
		return passwordCredential{}, err
	}
	credential.Parameters = PasswordParameters{
		MemoryKiB: uint32(memoryKiB), Iterations: uint32(iterations), Parallelism: uint8(parallelism),
		SaltLength: saltLength, KeyLength: keyLength,
	}
	return credential, nil
}

func boundedCredentialLength(value []byte, minimum, maximum int) (uint32, error) {
	if len(value) < minimum || len(value) > maximum {
		return 0, errors.New("stored password parameters are invalid")
	}
	// #nosec G115 -- the explicit maximum above is 64, well inside uint32.
	return uint32(len(value)), nil
}

func (s *Service) upgradePasswordCredential(ctx context.Context, password string, previous passwordCredential) error {
	target := strongerPasswordParameters(previous.Parameters, defaultPasswordParameters)
	credential, err := derivePasswordCredential(password, target)
	if err != nil {
		return err
	}
	now := formatTime(time.Now().UTC())
	_, err = s.db.ExecContext(ctx, `
		UPDATE auth_password_credentials SET algorithm_version = ?, memory_kib = ?, iterations = ?,
			parallelism = ?, salt = ?, password_hash = ?, updated_at = ?
		WHERE owner_principal = ? AND password_hash = ?
	`, credential.AlgorithmVersion, credential.Parameters.MemoryKiB, credential.Parameters.Iterations,
		credential.Parameters.Parallelism, credential.Salt, credential.Hash, now, OwnerPrincipal, previous.Hash)
	return err
}

func strongerPasswordParameters(left, right PasswordParameters) PasswordParameters {
	return PasswordParameters{
		MemoryKiB:   max(left.MemoryKiB, right.MemoryKiB),
		Iterations:  max(left.Iterations, right.Iterations),
		Parallelism: max(left.Parallelism, right.Parallelism),
		SaltLength:  max(left.SaltLength, right.SaltLength),
		KeyLength:   max(left.KeyLength, right.KeyLength),
	}
}

func credentialNeedsUpgrade(credential passwordCredential) bool {
	return credential.AlgorithmVersion != argon2.Version || credential.Parameters.MemoryKiB < defaultPasswordParameters.MemoryKiB ||
		credential.Parameters.Iterations < defaultPasswordParameters.Iterations || credential.Parameters.Parallelism < defaultPasswordParameters.Parallelism ||
		credential.Parameters.SaltLength < defaultPasswordParameters.SaltLength || credential.Parameters.KeyLength < defaultPasswordParameters.KeyLength
}

func validStoredParameters(credential passwordCredential) bool {
	return credential.AlgorithmVersion == argon2.Version && validParameters(credential.Parameters) &&
		len(credential.Salt) == int(credential.Parameters.SaltLength) && len(credential.Hash) == int(credential.Parameters.KeyLength)
}

func validParameters(parameters PasswordParameters) bool {
	return parameters.MemoryKiB >= 19*1024 && parameters.MemoryKiB <= 256*1024 &&
		parameters.Iterations >= 1 && parameters.Iterations <= 10 &&
		parameters.Parallelism >= 1 && parameters.Parallelism <= 8 &&
		parameters.SaltLength >= 16 && parameters.SaltLength <= 64 &&
		parameters.KeyLength >= 32 && parameters.KeyLength <= 64
}

func dummyPasswordCredential() passwordCredential {
	salt := sha256.Sum256([]byte("VariaNotes fixed dummy credential salt"))
	hash := make([]byte, defaultPasswordParameters.KeyLength)
	return passwordCredential{
		AlgorithmVersion: argon2.Version,
		Parameters:       defaultPasswordParameters,
		Salt:             append([]byte(nil), salt[:defaultPasswordParameters.SaltLength]...),
		Hash:             hash,
	}
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

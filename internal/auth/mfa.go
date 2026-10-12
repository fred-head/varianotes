package auth

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	mfaIssuer             = "VariaNotes"
	mfaRecoveryCodeCount  = 10
	mfaEnrollmentLifetime = 15 * time.Minute
)

var (
	ErrMFARequired    = errors.New("multi-factor authentication required")
	ErrMFAInvalid     = errors.New("authentication failed")
	ErrMFAUnavailable = errors.New("multi-factor authentication is unavailable")
)

type MFAEnrollment struct {
	Secret        string   `json:"secret"`
	QRCode        string   `json:"qrCode"`
	RecoveryCodes []string `json:"recoveryCodes"`
}

func loadOrCreateEncryptionKey(config Config) ([]byte, error) {
	keyPath := config.EncryptionKeyPath
	if keyPath == "" {
		keyPath = filepath.Join(filepath.Dir(config.MetadataPath), "auth.key")
	}
	// #nosec G304 -- keyPath is derived from the absolute operator auth configuration, not request input.
	value, err := os.ReadFile(keyPath)
	if err == nil {
		if len(value) != 32 {
			if !config.AllowMFAKeyRecovery {
				return nil, errors.New("authentication encryption key must contain exactly 32 bytes")
			}
			quarantinePath := fmt.Sprintf("%s.invalid-%s", keyPath, time.Now().UTC().Format("20060102T150405.000000000Z"))
			if err := os.Rename(keyPath, quarantinePath); err != nil {
				return nil, fmt.Errorf("quarantine invalid authentication encryption key: %w", err)
			}
			err = os.ErrNotExist
		} else {
			if err := os.Chmod(keyPath, 0o600); err != nil {
				return nil, fmt.Errorf("secure authentication encryption key: %w", err)
			}
			return value, nil
		}
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read authentication encryption key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("create authentication key directory: %w", err)
	}
	value = make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return nil, fmt.Errorf("generate authentication encryption key: %w", err)
	}
	// #nosec G304 -- keyPath is confined to the operator-configured auth directory and created exclusively with mode 0600.
	file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create authentication encryption key: %w", err)
	}
	if _, err := file.Write(value); err != nil {
		// #nosec G104 -- preserve the actionable write error; Close is best-effort on this failure path.
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		// #nosec G104 -- preserve the actionable sync error; Close is best-effort on this failure path.
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return value, nil
}

func (s *Service) MFAEnabled(ctx context.Context) (bool, error) {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM auth_mfa_configuration WHERE id=1`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return enabled == 1, err
}

func (s *Service) encryptMFASecret(secret string) ([]byte, []byte, error) {
	block, err := aes.NewCipher(s.encryptionKey)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, []byte(secret), []byte("varianotes-totp-v1")), nil
}

func (s *Service) decryptMFASecret(ctx context.Context) (string, bool, error) {
	var enabled int
	var nonce, ciphertext []byte
	err := s.db.QueryRowContext(ctx, `SELECT enabled, secret_nonce, secret_ciphertext FROM auth_mfa_configuration WHERE id=1`).Scan(&enabled, &nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if enabled != 1 {
		return "", false, nil
	}
	plain, err := s.decryptSecret(nonce, ciphertext)
	if err != nil {
		return "", false, err
	}
	return plain, true, nil
}

func (s *Service) decryptSecret(nonce, ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(s.encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte("varianotes-totp-v1"))
	if err != nil {
		return "", errors.New("decrypt MFA secret: authentication encryption key is invalid")
	}
	return string(plain), nil
}

func (s *Service) BeginMFAEnrollment(ctx context.Context, currentPassword, currentFactor string, sessionHash []byte) (MFAEnrollment, error) {
	if len(sessionHash) != sha256.Size {
		return MFAEnrollment{}, ErrMFAInvalid
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	if err := s.VerifyPassword(ctx, currentPassword); err != nil {
		return MFAEnrollment{}, ErrMFAInvalid
	}
	enabled, err := s.MFAEnabled(ctx)
	if err != nil {
		return MFAEnrollment{}, err
	}
	if enabled {
		if err := s.VerifySecondFactor(ctx, currentFactor); err != nil {
			return MFAEnrollment{}, ErrMFAInvalid
		}
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: mfaIssuer, AccountName: OwnerPrincipal, Period: 30, SecretSize: 20, Secret: nil, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1, Rand: rand.Reader})
	if err != nil {
		return MFAEnrollment{}, err
	}
	nonce, ciphertext, err := s.encryptMFASecret(key.Secret())
	if err != nil {
		return MFAEnrollment{}, err
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return MFAEnrollment{}, err
	}
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	expiresAt := nowTime.Add(mfaEnrollmentLifetime).Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MFAEnrollment{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_mfa_configuration(id, enabled, pending_secret_nonce, pending_secret_ciphertext, pending_expires_at, pending_session_hash, created_at, updated_at) VALUES(1,0,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET pending_secret_nonce=excluded.pending_secret_nonce, pending_secret_ciphertext=excluded.pending_secret_ciphertext, pending_expires_at=excluded.pending_expires_at, pending_session_hash=excluded.pending_session_hash, updated_at=excluded.updated_at`, nonce, ciphertext, expiresAt, append([]byte(nil), sessionHash...), now, now); err != nil {
		return MFAEnrollment{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_artifacts WHERE kind='mfa_recovery_pending'`); err != nil {
		return MFAEnrollment{}, err
	}
	for index, hash := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_recovery_artifacts(id, kind, secret_hash, created_at) VALUES(?, 'mfa_recovery_pending', ?, ?)`, fmt.Sprintf("mfa-%02d-%x", index, hash[:8]), hash, now); err != nil {
			return MFAEnrollment{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MFAEnrollment{}, err
	}
	image, err := key.Image(256, 256)
	if err != nil {
		return MFAEnrollment{}, err
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image); err != nil {
		return MFAEnrollment{}, err
	}
	return MFAEnrollment{Secret: key.Secret(), QRCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData.Bytes()), RecoveryCodes: codes}, nil
}

func (s *Service) ConfirmMFAEnrollment(ctx context.Context, code string, recoveryCodesStored bool, sessionHash []byte) error {
	if !recoveryCodesStored {
		return errors.New("recovery-code storage must be confirmed")
	}
	if len(sessionHash) != sha256.Size {
		return ErrMFAUnavailable
	}
	var nonce, ciphertext, storedSessionHash []byte
	var expiresAtText string
	if err := s.db.QueryRowContext(ctx, `SELECT pending_secret_nonce, pending_secret_ciphertext, pending_expires_at, pending_session_hash FROM auth_mfa_configuration WHERE id=1`).Scan(&nonce, &ciphertext, &expiresAtText, &storedSessionHash); err != nil {
		return ErrMFAUnavailable
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtText)
	if err != nil || !time.Now().UTC().Before(expiresAt) || subtle.ConstantTimeCompare(storedSessionHash, sessionHash) != 1 {
		return ErrMFAUnavailable
	}
	secret, err := s.decryptSecret(nonce, ciphertext)
	if err != nil {
		return ErrMFAUnavailable
	}
	step, valid := matchingTOTPStep(secret, code, time.Now().UTC())
	if !valid {
		return ErrMFAInvalid
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE auth_mfa_configuration SET enabled=1, secret_nonce=pending_secret_nonce, secret_ciphertext=pending_secret_ciphertext, pending_secret_nonce=NULL, pending_secret_ciphertext=NULL, pending_expires_at=NULL, pending_session_hash=NULL, last_totp_step=?, updated_at=? WHERE id=1 AND pending_secret_nonce IS NOT NULL AND pending_expires_at>? AND pending_session_hash=?`, step, now, now, sessionHash)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrMFAUnavailable
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_artifacts WHERE kind='mfa_recovery'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE auth_recovery_artifacts SET kind='mfa_recovery' WHERE kind='mfa_recovery_pending'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) CancelMFAEnrollment(ctx context.Context, sessionHash []byte) error {
	if len(sessionHash) != sha256.Size {
		return ErrMFAUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE auth_mfa_configuration SET pending_secret_nonce=NULL, pending_secret_ciphertext=NULL, pending_expires_at=NULL, pending_session_hash=NULL, updated_at=? WHERE id=1 AND pending_session_hash=?`, time.Now().UTC().Format(time.RFC3339Nano), sessionHash)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrMFAUnavailable
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_artifacts WHERE kind='mfa_recovery_pending'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) consumeTOTP(ctx context.Context, secret, code string) bool {
	step, valid := matchingTOTPStep(secret, code, time.Now().UTC())
	if !valid {
		return false
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auth_mfa_configuration SET last_totp_step=? WHERE id=1 AND enabled=1 AND last_totp_step<?`, step, step)
	if err != nil {
		return false
	}
	count, _ := result.RowsAffected()
	return count == 1
}

func matchingTOTPStep(secret, code string, now time.Time) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	current := now.UTC().Unix() / 30
	for _, step := range []int64{current, current - 1, current + 1} {
		generated, err := totp.GenerateCodeCustom(secret, time.Unix(step*30, 0), totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && subtle.ConstantTimeCompare([]byte(generated), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

func normalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
func recoveryHash(code string) [32]byte {
	return sha256.Sum256([]byte("varianotes-mfa-recovery-v1\x00" + normalizeRecoveryCode(code)))
}

func (s *Service) VerifySecondFactor(ctx context.Context, code string) error {
	secret, enabled, err := s.decryptMFASecret(ctx)
	if err != nil || !enabled {
		return ErrMFAInvalid
	}
	if s.consumeTOTP(ctx, secret, strings.TrimSpace(code)) {
		return nil
	}
	hash := recoveryHash(code)
	rows, err := s.db.QueryContext(ctx, `SELECT id, secret_hash FROM auth_recovery_artifacts WHERE kind='mfa_recovery' AND consumed_at IS NULL`)
	if err != nil {
		return ErrMFAInvalid
	}
	defer rows.Close()
	matched := ""
	for rows.Next() {
		var id string
		var stored []byte
		if rows.Scan(&id, &stored) == nil && len(stored) == len(hash) && subtle.ConstantTimeCompare(stored, hash[:]) == 1 {
			matched = id
		}
	}
	if matched == "" {
		return ErrMFAInvalid
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auth_recovery_artifacts SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), matched)
	if err != nil {
		return ErrMFAInvalid
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrMFAInvalid
	}
	return nil
}

func (s *Service) DisableMFA(ctx context.Context, currentPassword, code string) error {
	if err := s.VerifyPassword(ctx, currentPassword); err != nil {
		return ErrMFAInvalid
	}
	if err := s.VerifySecondFactor(ctx, code); err != nil {
		return ErrMFAInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_mfa_configuration`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_artifacts WHERE kind LIKE 'mfa_recovery%'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) RegenerateRecoveryCodes(ctx context.Context, currentPassword, code string) ([]string, error) {
	if err := s.VerifyPassword(ctx, currentPassword); err != nil {
		return nil, ErrMFAInvalid
	}
	if err := s.VerifySecondFactor(ctx, code); err != nil {
		return nil, ErrMFAInvalid
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_artifacts WHERE kind='mfa_recovery'`); err != nil {
		return nil, err
	}
	for index, hash := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_recovery_artifacts(id, kind, secret_hash, created_at) VALUES(?, 'mfa_recovery', ?, ?)`, fmt.Sprintf("mfa-%02d-%x", index, hash[:8]), hash, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return codes, nil
}

func (s *Service) ResetMFA(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_mfa_configuration`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_recovery_artifacts WHERE kind LIKE 'mfa_recovery%'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func generateRecoveryCodes() ([]string, [][]byte, error) {
	codes := make([]string, 0, mfaRecoveryCodeCount)
	hashes := make([][]byte, 0, mfaRecoveryCodeCount)
	for range mfaRecoveryCodeCount {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		plain := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		code := strings.Join([]string{plain[0:5], plain[5:10], plain[10:15], plain[15:20], plain[20:]}, "-")
		hash := recoveryHash(code)
		codes = append(codes, code)
		hashes = append(hashes, append([]byte(nil), hash[:]...))
	}
	return codes, hashes, nil
}

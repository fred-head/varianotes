package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	recoveryChallengeLifetime    = 10 * time.Minute
	maxPendingRecoveryChallenges = 2
	maxRecoveryPlanBytes         = 32 << 20
)

var (
	ErrInvalidRecoveryCode      = errors.New("invalid recovery code")
	ErrRecoveryCodeUnavailable  = errors.New("recovery code is unavailable")
	ErrRecoveryNotConfirmed     = errors.New("recovery code confirmation is required")
	ErrRecoveryChallengeExpired = errors.New("recovery challenge is invalid or expired")
	ErrRecoveryPending          = errors.New("encryption recovery is pending")
	ErrRecoveryRequired         = errors.New("encryption recovery requires restart recovery")
)

type pendingRecoveryChallenge struct {
	operation   string
	plan        []byte
	sessionHash []byte
	expiresAt   time.Time
}

type recoveryJournalPayload struct {
	Operation  string              `json:"operation"`
	Plan       []byte              `json:"plan"`
	Credential *passwordCredential `json:"credential,omitempty"`
}

// BeginEncryptionRecoveryCode creates a new one-time recovery code and keeps
// its envelope plan in memory until the owner confirms the displayed value.
func (s *Service) BeginEncryptionRecoveryCode(ctx context.Context, currentPassword, separateEncryptionPassword string, sessionHash []byte) (string, string, error) {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	releaseJournal, err := lockAuthJournal(ctx, s.config.MetadataPath)
	if err != nil {
		return "", "", err
	}
	defer releaseJournal()
	if len(sessionHash) != sha256.Size {
		return "", "", ErrAuthentication
	}
	state, err := s.State(ctx)
	if err != nil {
		return "", "", err
	}
	if state.Mode != ModeLocal || !state.SetupCompleted {
		return "", "", ErrSetupUnavailable
	}
	if s.config.RecoveryCoordinator == nil {
		return "", "", ErrRecoveryCodeUnavailable
	}
	if err := s.ensureNoPendingEncryptionRecovery(ctx); err != nil {
		return "", "", err
	}
	if err := s.verifyPassword(ctx, currentPassword); err != nil {
		return "", "", err
	}
	encryptionPassword := currentPassword
	if state.EncryptionPasswordSeparate {
		encryptionPassword = separateEncryptionPassword
		if encryptionPassword == "" {
			return "", "", ErrAuthentication
		}
	}
	code, err := newRecoveryCode()
	if err != nil {
		return "", "", err
	}
	plan, err := s.config.RecoveryCoordinator.PrepareRecoveryCode(ctx, encryptionPassword, code)
	if err != nil {
		return "", "", err
	}
	challenge, err := s.storeRecoveryChallenge("code", plan, sessionHash)
	if err != nil {
		return "", "", err
	}
	return challenge, code, nil
}

// ConfirmEncryptionRecoveryCode activates a displayed recovery code only
// after the owner submits the same code and confirms that it was saved.
func (s *Service) ConfirmEncryptionRecoveryCode(ctx context.Context, challenge, code string, confirmed bool, sessionHash []byte) error {
	if !confirmed {
		return ErrRecoveryNotConfirmed
	}
	canonical, err := normalizeVaultRecoveryCode(code)
	if err != nil {
		return ErrInvalidRecoveryCode
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	releaseJournal, err := lockAuthJournal(ctx, s.config.MetadataPath)
	if err != nil {
		return err
	}
	defer releaseJournal()
	state, err := s.State(ctx)
	if err != nil {
		return err
	}
	if state.Mode != ModeLocal || !state.SetupCompleted {
		return ErrSetupUnavailable
	}
	pending, err := s.loadRecoveryChallenge(challenge, "code", sessionHash)
	if err != nil {
		return err
	}
	if err := s.config.RecoveryCoordinator.VerifyRecoveryPlan(ctx, pending.plan, "", canonical); err != nil {
		return ErrInvalidRecoveryCode
	}
	if err := s.executeEncryptionRecovery(ctx, "code", pending.plan, nil); err != nil {
		return err
	}
	s.removeRecoveryChallenge(challenge)
	return nil
}

// BeginPasswordRecovery validates the existing recovery code and prepares a
// replacement password and recovery-code envelope plan. Neither the current
// envelopes nor the login credential change until the new code is confirmed.
func (s *Service) BeginPasswordRecovery(ctx context.Context, recoveryCode, newPassword string) (string, string, error) {
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	releaseJournal, err := lockAuthJournal(ctx, s.config.MetadataPath)
	if err != nil {
		return "", "", err
	}
	defer releaseJournal()
	if phase, _, err := s.pendingPasswordChange(ctx); err != nil {
		return "", "", err
	} else if phase != "" {
		return "", "", ErrPasswordChangePending
	}
	state, err := s.State(ctx)
	if err != nil {
		return "", "", err
	}
	if state.Mode != ModeLocal || !state.SetupCompleted {
		return "", "", ErrSetupUnavailable
	}
	if s.config.RecoveryCoordinator == nil {
		return "", "", ErrRecoveryCodeUnavailable
	}
	if err := ValidatePassword(newPassword); err != nil {
		return "", "", err
	}
	if err := s.ensureNoPendingEncryptionRecovery(ctx); err != nil {
		return "", "", err
	}
	oldCode, err := normalizeVaultRecoveryCode(recoveryCode)
	if err != nil {
		return "", "", ErrInvalidRecoveryCode
	}
	newCode, err := newRecoveryCode()
	if err != nil {
		return "", "", err
	}
	plan, err := s.config.RecoveryCoordinator.PreparePasswordRecovery(ctx, oldCode, newPassword, newCode)
	if err != nil {
		return "", "", err
	}
	challenge, err := s.storeRecoveryChallenge("password", plan, nil)
	if err != nil {
		return "", "", err
	}
	return challenge, newCode, nil
}

// ConfirmPasswordRecovery commits a recovery only after the replacement
// password and one-time code both verify against every prepared vault.
func (s *Service) ConfirmPasswordRecovery(ctx context.Context, challenge, newPassword, newCode string, confirmed bool) error {
	if !confirmed {
		return ErrRecoveryNotConfirmed
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	canonical, err := normalizeVaultRecoveryCode(newCode)
	if err != nil {
		return ErrInvalidRecoveryCode
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	releaseJournal, err := lockAuthJournal(ctx, s.config.MetadataPath)
	if err != nil {
		return err
	}
	defer releaseJournal()
	state, err := s.State(ctx)
	if err != nil {
		return err
	}
	if state.Mode != ModeLocal || !state.SetupCompleted {
		return ErrSetupUnavailable
	}
	pending, err := s.loadRecoveryChallenge(challenge, "password", nil)
	if err != nil {
		return err
	}
	if err := s.config.RecoveryCoordinator.VerifyRecoveryPlan(ctx, pending.plan, newPassword, canonical); err != nil {
		return ErrInvalidRecoveryCode
	}
	credential, err := derivePasswordCredential(newPassword, defaultPasswordParameters)
	if err != nil {
		return err
	}
	if err := s.executeEncryptionRecovery(ctx, "password", pending.plan, &credential); err != nil {
		return err
	}
	s.removeRecoveryChallenge(challenge)
	return nil
}

func newRecoveryCode() (string, error) {
	secret := make([]byte, 32)
	defer clear(secret)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return "", fmt.Errorf("generate encryption recovery code: %w", err)
	}
	encoded := strings.ToUpper(hex.EncodeToString(secret))
	groups := make([]string, 0, 8)
	for offset := 0; offset < len(encoded); offset += 8 {
		groups = append(groups, encoded[offset:offset+8])
	}
	return "VNRC1-" + strings.Join(groups, "-"), nil
}

func normalizeVaultRecoveryCode(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "VNRC1-")
	value = strings.ReplaceAll(value, "-", "")
	if len(value) != 64 {
		return "", ErrInvalidRecoveryCode
	}
	secret, err := hex.DecodeString(value)
	if err != nil || len(secret) != 32 {
		clear(secret)
		return "", ErrInvalidRecoveryCode
	}
	defer clear(secret)
	groups := make([]string, 0, 8)
	for offset := 0; offset < len(value); offset += 8 {
		groups = append(groups, value[offset:offset+8])
	}
	return "VNRC1-" + strings.Join(groups, "-"), nil
}

func (s *Service) storeRecoveryChallenge(operation string, plan, sessionHash []byte) (string, error) {
	if len(plan) == 0 || len(plan) > maxRecoveryPlanBytes {
		return "", ErrRecoveryCodeUnavailable
	}
	secret := make([]byte, 24)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return "", fmt.Errorf("generate recovery challenge: %w", err)
	}
	challenge := base64.RawURLEncoding.EncodeToString(secret)
	s.pendingRecoveryMu.Lock()
	defer s.pendingRecoveryMu.Unlock()
	now := time.Now()
	for key, pending := range s.pendingRecovery {
		if !now.Before(pending.expiresAt) {
			delete(s.pendingRecovery, key)
		}
	}
	if len(s.pendingRecovery) >= maxPendingRecoveryChallenges {
		return "", ErrRecoveryPending
	}
	if s.pendingRecovery == nil {
		s.pendingRecovery = make(map[string]pendingRecoveryChallenge)
	}
	s.pendingRecovery[challenge] = pendingRecoveryChallenge{
		operation: operation, plan: append([]byte(nil), plan...),
		sessionHash: append([]byte(nil), sessionHash...), expiresAt: now.Add(recoveryChallengeLifetime),
	}
	return challenge, nil
}

func (s *Service) loadRecoveryChallenge(challenge, operation string, sessionHash []byte) (pendingRecoveryChallenge, error) {
	s.pendingRecoveryMu.Lock()
	defer s.pendingRecoveryMu.Unlock()
	pending, exists := s.pendingRecovery[challenge]
	if !exists {
		return pendingRecoveryChallenge{}, ErrRecoveryChallengeExpired
	}
	if !time.Now().Before(pending.expiresAt) {
		delete(s.pendingRecovery, challenge)
		return pendingRecoveryChallenge{}, ErrRecoveryChallengeExpired
	}
	if pending.operation != operation || !bytes.Equal(pending.sessionHash, sessionHash) {
		return pendingRecoveryChallenge{}, ErrRecoveryChallengeExpired
	}
	return pendingRecoveryChallenge{operation: pending.operation, plan: append([]byte(nil), pending.plan...), sessionHash: append([]byte(nil), pending.sessionHash...), expiresAt: pending.expiresAt}, nil
}

func (s *Service) removeRecoveryChallenge(challenge string) {
	s.pendingRecoveryMu.Lock()
	delete(s.pendingRecovery, challenge)
	s.pendingRecoveryMu.Unlock()
}

func (s *Service) executeEncryptionRecovery(ctx context.Context, operation string, plan []byte, credential *passwordCredential) error {
	if s.config.RecoveryCoordinator == nil {
		return ErrRecoveryCodeUnavailable
	}
	if !recoveryPlanMatchesOperation(plan, operation) {
		return ErrRecoveryRequired
	}
	if err := s.ensureNoPendingEncryptionRecovery(ctx); err != nil {
		return err
	}
	if phase, _, err := s.pendingPasswordChange(ctx); err != nil {
		return err
	} else if phase != "" {
		return ErrPasswordChangePending
	}
	payload := recoveryJournalPayload{Operation: operation, Plan: append([]byte(nil), plan...), Credential: credential}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maxRecoveryPlanBytes {
		return ErrRecoveryRequired
	}
	if err := s.savePendingEncryptionRecovery(ctx, encoded); err != nil {
		return err
	}
	if err := s.config.RecoveryCoordinator.ApplyRecoveryPlan(ctx, plan); err != nil {
		rollbackErr := s.config.RecoveryCoordinator.RollbackRecoveryPlan(ctx, plan)
		if rollbackErr == nil {
			rollbackErr = s.clearPendingEncryptionRecovery(ctx)
		}
		if rollbackErr != nil {
			return errors.Join(ErrRecoveryRequired, err, rollbackErr)
		}
		return err
	}
	if err := s.commitEncryptionRecovery(ctx, operation, credential); err != nil {
		phase, pending, journalErr := s.pendingEncryptionRecovery(ctx)
		if journalErr != nil {
			return errors.Join(ErrRecoveryRequired, err, journalErr)
		}
		if phase == "committed" {
			return s.finishCommittedEncryptionRecovery(ctx, pending)
		}
		rollbackErr := s.config.RecoveryCoordinator.RollbackRecoveryPlan(ctx, plan)
		if rollbackErr == nil {
			rollbackErr = s.clearPendingEncryptionRecovery(ctx)
		}
		if rollbackErr != nil {
			return errors.Join(ErrRecoveryRequired, err, rollbackErr)
		}
		return err
	}
	return s.finishCommittedEncryptionRecovery(ctx, encoded)
}

func (s *Service) commitEncryptionRecovery(ctx context.Context, operation string, credential *passwordCredential) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin encryption recovery commit: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if credential != nil {
		result, err := tx.ExecContext(ctx, `UPDATE auth_password_credentials SET algorithm_version=?, memory_kib=?, iterations=?, parallelism=?, salt=?, password_hash=?, updated_at=? WHERE owner_principal=?`,
			credential.AlgorithmVersion, credential.Parameters.MemoryKiB, credential.Parameters.Iterations, credential.Parameters.Parallelism,
			credential.Salt, credential.Hash, now.Format(time.RFC3339Nano), OwnerPrincipal)
		if err != nil {
			return fmt.Errorf("replace recovered owner password: %w", err)
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrSetupUnavailable
		}
		if _, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=? WHERE revoked_at IS NULL`, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("revoke sessions after password recovery: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE auth_configuration SET encryption_password_separate=0, updated_at=? WHERE id=1`, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("bind recovered vault password: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE auth_encryption_recovery_journal SET phase='committed' WHERE id=1 AND phase='prepared'`)
	if err != nil {
		return fmt.Errorf("commit encryption recovery journal: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrRecoveryPending
	}
	event := "encryption_recovery_code_changed"
	if operation == "password" {
		event = "password_recovered_with_code"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_security_events(event_type, occurred_at, outcome, details) VALUES (?, ?, 'success', '')`, event, now.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record encryption recovery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit encryption recovery: %w", err)
	}
	return nil
}

func (s *Service) finishCommittedEncryptionRecovery(ctx context.Context, encoded []byte) error {
	payload, err := decodeRecoveryJournalPayload(encoded)
	if err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if s.config.RecoveryCoordinator == nil {
		return ErrRecoveryRequired
	}
	if err := s.config.RecoveryCoordinator.ApplyRecoveryPlan(ctx, payload.Plan); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if err := s.clearPendingEncryptionRecovery(ctx); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}

func (s *Service) savePendingEncryptionRecovery(ctx context.Context, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxRecoveryPlanBytes {
		return ErrRecoveryRequired
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_encryption_recovery_journal(id, phase, payload, created_at) VALUES (1, 'prepared', ?, ?)`, payload, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return errors.Join(ErrRecoveryPending, err)
	}
	return nil
}

func (s *Service) pendingEncryptionRecovery(ctx context.Context) (string, []byte, error) {
	var phase string
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT phase, payload FROM auth_encryption_recovery_journal WHERE id=1`).Scan(&phase, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	if (phase != "prepared" && phase != "committed") || len(payload) == 0 || len(payload) > maxRecoveryPlanBytes {
		return "", nil, ErrRecoveryRequired
	}
	if _, err := decodeRecoveryJournalPayload(payload); err != nil {
		return "", nil, ErrRecoveryRequired
	}
	return phase, payload, nil
}

func (s *Service) ensureNoPendingEncryptionRecovery(ctx context.Context) error {
	phase, _, err := s.pendingEncryptionRecovery(ctx)
	if err != nil {
		return err
	}
	if phase != "" {
		return ErrRecoveryPending
	}
	return nil
}

func decodeRecoveryJournalPayload(encoded []byte) (recoveryJournalPayload, error) {
	if len(encoded) == 0 || len(encoded) > maxRecoveryPlanBytes {
		return recoveryJournalPayload{}, ErrRecoveryRequired
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var payload recoveryJournalPayload
	if err := decoder.Decode(&payload); err != nil {
		return recoveryJournalPayload{}, errors.Join(ErrRecoveryRequired, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return recoveryJournalPayload{}, ErrRecoveryRequired
	}
	if (payload.Operation != "code" && payload.Operation != "password") || len(payload.Plan) == 0 || len(payload.Plan) > maxRecoveryPlanBytes || (payload.Operation == "password") != (payload.Credential != nil) {
		return recoveryJournalPayload{}, ErrRecoveryRequired
	}
	if !recoveryPlanMatchesOperation(payload.Plan, payload.Operation) {
		return recoveryJournalPayload{}, ErrRecoveryRequired
	}
	if payload.Credential != nil && !validStoredParameters(*payload.Credential) {
		return recoveryJournalPayload{}, ErrRecoveryRequired
	}
	return payload, nil
}

func recoveryPlanMatchesOperation(plan []byte, operation string) bool {
	var header struct {
		Operation string `json:"operation"`
	}
	return json.Unmarshal(plan, &header) == nil && header.Operation == operation
}

func (s *Service) clearPendingEncryptionRecovery(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_encryption_recovery_journal WHERE id=1`)
	return err
}

func (s *Service) recoverEncryptionRecovery(ctx context.Context) error {
	phase, encoded, err := s.pendingEncryptionRecovery(ctx)
	if err != nil || phase == "" {
		return err
	}
	payload, err := decodeRecoveryJournalPayload(encoded)
	if err != nil || s.config.RecoveryCoordinator == nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if phase == "prepared" {
		err = s.config.RecoveryCoordinator.RollbackRecoveryPlan(ctx, payload.Plan)
	} else {
		err = s.config.RecoveryCoordinator.ApplyRecoveryPlan(ctx, payload.Plan)
	}
	if err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	if err := s.clearPendingEncryptionRecovery(ctx); err != nil {
		return errors.Join(ErrRecoveryRequired, err)
	}
	return nil
}

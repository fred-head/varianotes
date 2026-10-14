package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
)

const recoveryPlanVersion = 1

type vaultRecoveryCoordinator struct {
	registryPath string
}

type vaultRecoveryPlan struct {
	Version   int                   `json:"version"`
	Operation string                `json:"operation"`
	Updates   []vaultRecoveryUpdate `json:"updates"`
}

type vaultRecoveryUpdate struct {
	NotebookID          string `json:"notebook_id"`
	OldPasswordEnvelope []byte `json:"old_password_envelope,omitempty"`
	NewPasswordEnvelope []byte `json:"new_password_envelope,omitempty"`
	OldRecoveryEnvelope []byte `json:"old_recovery_envelope,omitempty"`
	NewRecoveryEnvelope []byte `json:"new_recovery_envelope"`
	OldRecoveryPresent  bool   `json:"old_recovery_present"`
	CatalogSHA256       string `json:"catalog_sha256"`
}

// NewRecoveryCodeCoordinator connects one-time recovery codes to registered
// vault-key envelope metadata. The recovery code itself is never included in
// a returned or persisted plan.
func NewRecoveryCodeCoordinator(registryPath string) auth.RecoveryCodeCoordinator {
	return &vaultRecoveryCoordinator{registryPath: strings.TrimSpace(registryPath)}
}

func (c *vaultRecoveryCoordinator) PrepareRecoveryCode(ctx context.Context, password, recoveryCode string) ([]byte, error) {
	plan := vaultRecoveryPlan{Version: recoveryPlanVersion, Operation: "code", Updates: make([]vaultRecoveryUpdate, 0)}
	entries, err := c.registeredNotebooks()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateRegisteredVault(entry); err != nil {
			return nil, fmt.Errorf("registered vault %q is unavailable: %w", entry.ID, err)
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil {
			return nil, fmt.Errorf("inspect registered vault %q: %w", entry.ID, err)
		}
		if !state.protected {
			continue
		}
		key, _, oldRecovery, oldRecoveryPresent, catalogDigest, err := c.inspectVault(ctx, entry, state, password, "")
		if err != nil {
			return nil, err
		}
		newRecovery, err := encryption.EncryptVaultKey(recoveryCode, key)
		clear(key)
		if err != nil {
			return nil, err
		}
		newRecoveryBytes, err := json.Marshal(newRecovery)
		if err != nil {
			return nil, err
		}
		plan.Updates = append(plan.Updates, vaultRecoveryUpdate{
			NotebookID:          entry.ID,
			OldRecoveryEnvelope: oldRecovery, OldRecoveryPresent: oldRecoveryPresent,
			NewRecoveryEnvelope: newRecoveryBytes, CatalogSHA256: catalogDigest,
		})
	}
	if len(plan.Updates) == 0 {
		return nil, errors.New("no protected vault key envelopes are available for recovery")
	}
	sort.Slice(plan.Updates, func(i, j int) bool { return plan.Updates[i].NotebookID < plan.Updates[j].NotebookID })
	return json.Marshal(plan)
}

func (c *vaultRecoveryCoordinator) PreparePasswordRecovery(ctx context.Context, recoveryCode, newPassword, newRecoveryCode string) ([]byte, error) {
	plan := vaultRecoveryPlan{Version: recoveryPlanVersion, Operation: "password", Updates: make([]vaultRecoveryUpdate, 0)}
	entries, err := c.registeredNotebooks()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateRegisteredVault(entry); err != nil {
			return nil, fmt.Errorf("registered vault %q is unavailable: %w", entry.ID, err)
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil {
			return nil, fmt.Errorf("inspect registered vault %q: %w", entry.ID, err)
		}
		if !state.protected {
			continue
		}
		key, oldPassword, oldRecovery, oldRecoveryPresent, catalogDigest, err := c.inspectVault(ctx, entry, state, "", recoveryCode)
		if err != nil {
			return nil, err
		}
		newPasswordEnvelope, err := encryption.EncryptVaultKey(newPassword, key)
		if err != nil {
			clear(key)
			return nil, err
		}
		newRecoveryEnvelope, err := encryption.EncryptVaultKey(newRecoveryCode, key)
		clear(key)
		if err != nil {
			return nil, err
		}
		newPasswordBytes, err := json.Marshal(newPasswordEnvelope)
		if err != nil {
			return nil, err
		}
		newRecoveryBytes, err := json.Marshal(newRecoveryEnvelope)
		if err != nil {
			return nil, err
		}
		plan.Updates = append(plan.Updates, vaultRecoveryUpdate{
			NotebookID: entry.ID, OldPasswordEnvelope: oldPassword, NewPasswordEnvelope: newPasswordBytes,
			OldRecoveryEnvelope: oldRecovery, OldRecoveryPresent: oldRecoveryPresent,
			NewRecoveryEnvelope: newRecoveryBytes, CatalogSHA256: catalogDigest,
		})
	}
	if len(plan.Updates) == 0 {
		return nil, auth.ErrInvalidRecoveryCode
	}
	sort.Slice(plan.Updates, func(i, j int) bool { return plan.Updates[i].NotebookID < plan.Updates[j].NotebookID })
	return json.Marshal(plan)
}

func (c *vaultRecoveryCoordinator) VerifyRecoveryPlan(ctx context.Context, encoded []byte, password, recoveryCode string) error {
	plan, err := decodeVaultRecoveryPlan(encoded)
	if err != nil {
		return err
	}
	if plan.Operation == "password" && password == "" {
		return errors.New("password recovery plan requires a replacement password")
	}
	for _, update := range plan.Updates {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := c.findRegisteredNotebook(update.NotebookID)
		if err != nil {
			return err
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil || !state.hasCatalog {
			return errors.Join(errors.New("registered vault recovery metadata is unavailable"), err)
		}
		catalogStore, err := encryption.NewCatalogStore(state.catalogPath)
		if err != nil {
			return err
		}
		catalogBytes, err := readRegularFile(state.catalogPath, 16<<20)
		if err != nil {
			return err
		}
		catalogDigest := sha256.Sum256(catalogBytes)
		if hex.EncodeToString(catalogDigest[:]) != update.CatalogSHA256 {
			return errors.New("catalog changed during recovery confirmation")
		}
		recoveryEnvelope, err := decodeVaultKeyEnvelope(update.NewRecoveryEnvelope)
		if err != nil {
			return err
		}
		key, err := encryption.DecryptVaultKey(recoveryCode, recoveryEnvelope)
		if err != nil {
			return auth.ErrInvalidRecoveryCode
		}
		if password != "" {
			if len(update.NewPasswordEnvelope) == 0 {
				clear(key)
				return errors.New("password recovery plan is missing a replacement password envelope")
			}
			passwordEnvelope, decodeErr := decodeVaultKeyEnvelope(update.NewPasswordEnvelope)
			if decodeErr != nil {
				clear(key)
				return decodeErr
			}
			passwordKey, decryptErr := encryption.DecryptVaultKey(password, passwordEnvelope)
			if decryptErr != nil || subtle.ConstantTimeCompare(passwordKey, key) != 1 {
				clear(key)
				clear(passwordKey)
				return auth.ErrInvalidRecoveryCode
			}
			clear(passwordKey)
		}
		_, err = catalogStore.Load(key, registeredCatalogID(entry.ID))
		clear(key)
		if err != nil {
			return fmt.Errorf("verify registered vault %q recovery catalog: %w", entry.ID, err)
		}
	}
	return nil
}

func (c *vaultRecoveryCoordinator) ApplyRecoveryPlan(ctx context.Context, encoded []byte) error {
	return c.applyRecoveryPlan(ctx, encoded, true)
}

func (c *vaultRecoveryCoordinator) RollbackRecoveryPlan(ctx context.Context, encoded []byte) error {
	return c.applyRecoveryPlan(ctx, encoded, false)
}

func (c *vaultRecoveryCoordinator) inspectVault(ctx context.Context, entry notebookRecord, state vaultProtectionState, password, recoveryCode string) ([]byte, []byte, []byte, bool, string, error) {
	if !state.hasCatalog || !state.hasEnvelope {
		return nil, nil, nil, false, "", fmt.Errorf("registered vault %q has incomplete encryption metadata", entry.ID)
	}
	catalogStore, err := encryption.NewCatalogStore(state.catalogPath)
	if err != nil {
		return nil, nil, nil, false, "", err
	}
	passwordEnvelopeBytes, passwordEnvelope, err := readRecoveryEnvelope(state.envelopePath)
	if err != nil {
		return nil, nil, nil, false, "", fmt.Errorf("load registered vault %q password envelope: %w", entry.ID, err)
	}
	var key []byte
	if password != "" {
		key, err = encryption.DecryptVaultKey(password, passwordEnvelope)
		if err != nil {
			if errors.Is(err, encryption.ErrAuthentication) || errors.Is(err, encryption.ErrInvalidPassword) {
				return nil, nil, nil, false, "", auth.ErrAuthentication
			}
			return nil, nil, nil, false, "", err
		}
	} else {
		if !state.hasRecoveryEnvelope {
			return nil, nil, nil, false, "", auth.ErrInvalidRecoveryCode
		}
		_, oldRecoveryEnvelope, readErr := readRecoveryEnvelope(state.recoveryEnvelopePath)
		if readErr != nil {
			return nil, nil, nil, false, "", readErr
		}
		key, err = encryption.DecryptVaultKey(recoveryCode, oldRecoveryEnvelope)
		if err != nil {
			return nil, nil, nil, false, "", auth.ErrInvalidRecoveryCode
		}
	}
	_, err = catalogStore.Load(key, registeredCatalogID(entry.ID))
	if err != nil {
		clear(key)
		return nil, nil, nil, false, "", fmt.Errorf("verify registered vault %q catalog: %w", entry.ID, err)
	}
	var oldRecovery []byte
	oldRecoveryPresent := false
	if state.hasRecoveryEnvelope {
		oldRecovery, _, err = readRecoveryEnvelope(state.recoveryEnvelopePath)
		if err != nil {
			clear(key)
			return nil, nil, nil, false, "", err
		}
		oldRecoveryPresent = true
	}
	catalogBytes, err := readRegularFile(state.catalogPath, 16<<20)
	if err != nil {
		clear(key)
		return nil, nil, nil, false, "", err
	}
	digest := sha256.Sum256(catalogBytes)
	return key, passwordEnvelopeBytes, oldRecovery, oldRecoveryPresent, hex.EncodeToString(digest[:]), nil
}

func (c *vaultRecoveryCoordinator) applyRecoveryPlan(ctx context.Context, encoded []byte, forward bool) error {
	plan, err := decodeVaultRecoveryPlan(encoded)
	if err != nil {
		return err
	}
	entries, err := c.registeredNotebooks()
	if err != nil {
		return err
	}
	byID := make(map[string]notebookRecord, len(entries))
	planned := make(map[string]struct{}, len(plan.Updates))
	for _, update := range plan.Updates {
		planned[update.NotebookID] = struct{}{}
	}
	for _, entry := range entries {
		byID[entry.ID] = entry
		if err := validateRegisteredVault(entry); err != nil {
			return fmt.Errorf("registered vault %q is unavailable during encryption recovery: %w", entry.ID, err)
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil {
			return fmt.Errorf("inspect registered vault %q during encryption recovery: %w", entry.ID, err)
		}
		if state.protected {
			if _, exists := planned[entry.ID]; !exists {
				return fmt.Errorf("registered vault %q became protected during encryption recovery", entry.ID)
			}
		}
	}
	for _, update := range plan.Updates {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, exists := byID[update.NotebookID]
		if !exists {
			return fmt.Errorf("registered vault %q disappeared during encryption recovery", update.NotebookID)
		}
		if err := validateRegisteredVault(entry); err != nil {
			return fmt.Errorf("registered vault %q is unavailable during encryption recovery: %w", entry.ID, err)
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil || !state.hasCatalog || !state.hasEnvelope {
			return errors.Join(fmt.Errorf("registered vault %q encryption metadata is unavailable", entry.ID), err)
		}
		catalogBytes, err := readRegularFile(state.catalogPath, 16<<20)
		if err != nil {
			return fmt.Errorf("read registered vault %q catalog: %w", entry.ID, err)
		}
		digest := sha256.Sum256(catalogBytes)
		if hex.EncodeToString(digest[:]) != update.CatalogSHA256 {
			return fmt.Errorf("registered vault %q catalog changed during encryption recovery", entry.ID)
		}
		if plan.Operation == "password" {
			if err := applyRecoveryEnvelope(state.envelopePath, update.OldPasswordEnvelope, update.NewPasswordEnvelope, true, true, forward); err != nil {
				return fmt.Errorf("replace registered vault %q password envelope: %w", entry.ID, err)
			}
		}
		if err := applyRecoveryEnvelope(state.recoveryEnvelopePath, update.OldRecoveryEnvelope, update.NewRecoveryEnvelope, update.OldRecoveryPresent, true, forward); err != nil {
			return fmt.Errorf("replace registered vault %q recovery envelope: %w", entry.ID, err)
		}
	}
	return nil
}

func applyRecoveryEnvelope(filename string, oldBytes, newBytes []byte, oldPresent, newPresent, forward bool) error {
	store, err := encryption.NewVaultKeyEnvelopeStore(filename)
	if err != nil {
		return err
	}
	currentBytes, currentPresent, err := readOptionalRecoveryEnvelope(filename)
	if err != nil {
		return err
	}
	targetBytes, sourceBytes := newBytes, oldBytes
	targetPresent, sourcePresent := newPresent, oldPresent
	if !forward {
		targetBytes, sourceBytes = oldBytes, newBytes
		targetPresent, sourcePresent = oldPresent, newPresent
	}
	if currentPresent == targetPresent && bytes.Equal(currentBytes, targetBytes) {
		if !targetPresent {
			// A previous removal may have been interrupted before its directory
			// sync. Sync even when the target is already absent before clearing
			// the durable recovery journal.
			return store.Delete()
		}
		return nil
	}
	if currentPresent != sourcePresent || !bytes.Equal(currentBytes, sourceBytes) {
		return errors.New("key envelope changed during recovery")
	}
	if !targetPresent {
		return store.Delete()
	}
	envelope, err := decodeVaultKeyEnvelope(targetBytes)
	if err != nil {
		return err
	}
	return store.Save(envelope)
}

func readOptionalRecoveryEnvelope(filename string) ([]byte, bool, error) {
	exists, err := regularFileExists(filename)
	if err != nil || !exists {
		return nil, false, err
	}
	encoded, _, err := readRecoveryEnvelope(filename)
	return encoded, err == nil, err
}

func readRecoveryEnvelope(filename string) ([]byte, encryption.VaultKeyEnvelope, error) {
	encoded, err := readRegularFile(filename, 128<<10)
	if err != nil {
		return nil, encryption.VaultKeyEnvelope{}, err
	}
	envelope, err := decodeVaultKeyEnvelope(encoded)
	if err != nil {
		return nil, encryption.VaultKeyEnvelope{}, err
	}
	canonical, err := json.Marshal(envelope)
	return canonical, envelope, err
}

func decodeVaultRecoveryPlan(encoded []byte) (vaultRecoveryPlan, error) {
	if len(encoded) == 0 || len(encoded) > 32<<20 {
		return vaultRecoveryPlan{}, errors.New("invalid encryption recovery plan")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var plan vaultRecoveryPlan
	if err := decoder.Decode(&plan); err != nil {
		return vaultRecoveryPlan{}, fmt.Errorf("decode encryption recovery plan: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || plan.Version != recoveryPlanVersion || (plan.Operation != "code" && plan.Operation != "password") || len(plan.Updates) == 0 {
		return vaultRecoveryPlan{}, errors.New("invalid encryption recovery plan")
	}
	seen := make(map[string]struct{}, len(plan.Updates))
	for _, update := range plan.Updates {
		if update.NotebookID == "" || len(update.CatalogSHA256) != sha256.Size*2 || len(update.NewRecoveryEnvelope) == 0 || (update.OldRecoveryPresent != (len(update.OldRecoveryEnvelope) != 0)) {
			return vaultRecoveryPlan{}, errors.New("invalid encryption recovery plan")
		}
		if _, err := hex.DecodeString(update.CatalogSHA256); err != nil {
			return vaultRecoveryPlan{}, errors.New("invalid encryption recovery catalog digest")
		}
		if _, err := decodeVaultKeyEnvelope(update.NewRecoveryEnvelope); err != nil {
			return vaultRecoveryPlan{}, errors.New("invalid recovery code envelope")
		}
		if plan.Operation == "password" && (len(update.OldPasswordEnvelope) == 0 || len(update.NewPasswordEnvelope) == 0) {
			return vaultRecoveryPlan{}, errors.New("invalid password recovery plan")
		}
		if plan.Operation == "password" {
			if !update.OldRecoveryPresent {
				return vaultRecoveryPlan{}, errors.New("password recovery plan has no old recovery envelope")
			}
			if _, err := decodeVaultKeyEnvelope(update.OldPasswordEnvelope); err != nil {
				return vaultRecoveryPlan{}, errors.New("invalid old password envelope in recovery plan")
			}
			if _, err := decodeVaultKeyEnvelope(update.NewPasswordEnvelope); err != nil {
				return vaultRecoveryPlan{}, errors.New("invalid new password envelope in recovery plan")
			}
		} else if len(update.OldPasswordEnvelope) != 0 || len(update.NewPasswordEnvelope) != 0 {
			return vaultRecoveryPlan{}, errors.New("code setup plan unexpectedly changes a password envelope")
		}
		if update.OldRecoveryPresent {
			if _, err := decodeVaultKeyEnvelope(update.OldRecoveryEnvelope); err != nil {
				return vaultRecoveryPlan{}, errors.New("invalid old recovery envelope in recovery plan")
			}
		}
		if _, exists := seen[update.NotebookID]; exists {
			return vaultRecoveryPlan{}, errors.New("duplicate vault in encryption recovery plan")
		}
		seen[update.NotebookID] = struct{}{}
	}
	return plan, nil
}

func (c *vaultRecoveryCoordinator) registeredNotebooks() ([]notebookRecord, error) {
	return (&vaultPasswordChangeCoordinator{registryPath: c.registryPath}).registeredNotebooks()
}

func (c *vaultRecoveryCoordinator) findRegisteredNotebook(id string) (notebookRecord, error) {
	entries, err := c.registeredNotebooks()
	if err != nil {
		return notebookRecord{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			if err := validateRegisteredVault(entry); err != nil {
				return notebookRecord{}, err
			}
			return entry, nil
		}
	}
	return notebookRecord{}, os.ErrNotExist
}

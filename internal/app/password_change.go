package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
)

const passwordChangePlanVersion = 1

type vaultPasswordChangeCoordinator struct {
	registryPath string
}

type passwordChangePlan struct {
	Version int                      `json:"version"`
	Updates []passwordEnvelopeUpdate `json:"updates"`
}

type passwordEnvelopeUpdate struct {
	NotebookID    string `json:"notebook_id"`
	OldEnvelope   []byte `json:"old_envelope"`
	NewEnvelope   []byte `json:"new_envelope"`
	CatalogSHA256 string `json:"catalog_sha256"`
}

// NewPasswordChangeCoordinator connects auth credential changes to the
// registered vault key-envelope metadata. The registry path is operator
// configuration; notebook paths and IDs are loaded from its current contents.
func NewPasswordChangeCoordinator(registryPath string) auth.PasswordChangeCoordinator {
	return &vaultPasswordChangeCoordinator{registryPath: strings.TrimSpace(registryPath)}
}

func (c *vaultPasswordChangeCoordinator) PreparePasswordChange(ctx context.Context, currentPassword, newPassword string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := c.registeredNotebooks()
	if err != nil {
		return nil, err
	}
	plan := passwordChangePlan{Version: passwordChangePlanVersion, Updates: make([]passwordEnvelopeUpdate, 0)}
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
		if !state.hasCatalog || !state.hasEnvelope {
			return nil, fmt.Errorf("registered vault %q has incomplete encryption metadata", entry.ID)
		}
		catalogStore, err := encryption.NewCatalogStore(state.catalogPath)
		if err != nil {
			return nil, err
		}
		envelopeStore, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
		if err != nil {
			return nil, err
		}
		oldEnvelope, err := envelopeStore.Load()
		if err != nil {
			return nil, fmt.Errorf("load vault %q key envelope: %w", entry.ID, err)
		}
		vaultKey, err := encryption.DecryptVaultKey(currentPassword, oldEnvelope)
		if err != nil {
			return nil, fmt.Errorf("unlock registered vault %q: %w", entry.ID, err)
		}
		_, idErr := catalogStore.Load(vaultKey, registeredCatalogID(entry.ID))
		newEnvelope, wrapErr := encryption.EncryptVaultKey(newPassword, vaultKey)
		clear(vaultKey)
		if idErr != nil {
			return nil, fmt.Errorf("verify registered vault %q catalog: %w", entry.ID, idErr)
		}
		if wrapErr != nil {
			return nil, wrapErr
		}
		oldBytes, err := json.Marshal(oldEnvelope)
		if err != nil {
			return nil, err
		}
		newBytes, err := json.Marshal(newEnvelope)
		if err != nil {
			return nil, err
		}
		catalogBytes, err := readRegularFile(state.catalogPath, 16<<20)
		if err != nil {
			return nil, fmt.Errorf("fingerprint registered vault %q catalog: %w", entry.ID, err)
		}
		digest := sha256.Sum256(catalogBytes)
		plan.Updates = append(plan.Updates, passwordEnvelopeUpdate{
			NotebookID: entry.ID, OldEnvelope: oldBytes, NewEnvelope: newBytes,
			CatalogSHA256: hex.EncodeToString(digest[:]),
		})
	}
	sort.Slice(plan.Updates, func(i, j int) bool { return plan.Updates[i].NotebookID < plan.Updates[j].NotebookID })
	return json.Marshal(plan)
}

func (c *vaultPasswordChangeCoordinator) ApplyPasswordChange(ctx context.Context, encoded []byte) error {
	return c.apply(ctx, encoded, true)
}

func (c *vaultPasswordChangeCoordinator) RollbackPasswordChange(ctx context.Context, encoded []byte) error {
	return c.apply(ctx, encoded, false)
}

func (c *vaultPasswordChangeCoordinator) HasProtectedContent(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	entries, err := c.registeredNotebooks()
	if err != nil {
		return false, err
	}
	protected := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := validateRegisteredVault(entry); err != nil {
			return false, fmt.Errorf("registered vault %q is unavailable: %w", entry.ID, err)
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil {
			return false, fmt.Errorf("inspect registered vault %q: %w", entry.ID, err)
		}
		if state.protected {
			protected = true
		}
	}
	return protected, nil
}

func (c *vaultPasswordChangeCoordinator) apply(ctx context.Context, encoded []byte, forward bool) error {
	plan, err := decodePasswordChangePlan(encoded)
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
			return fmt.Errorf("registered vault %q is unavailable during password change: %w", entry.ID, err)
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil {
			return fmt.Errorf("inspect registered vault %q during password change: %w", entry.ID, err)
		}
		if state.protected {
			if _, exists := planned[entry.ID]; !exists {
				return fmt.Errorf("registered vault %q became protected during password change", entry.ID)
			}
		}
	}
	for _, update := range plan.Updates {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, exists := byID[update.NotebookID]
		if !exists {
			return fmt.Errorf("registered vault %q disappeared during password change", update.NotebookID)
		}
		if err := validateRegisteredVault(entry); err != nil {
			return fmt.Errorf("registered vault %q is unavailable during password change: %w", entry.ID, err)
		}
		state, err := inspectVaultProtection(entry, c.registryPath)
		if err != nil || !state.hasCatalog || !state.hasEnvelope {
			return errors.Join(fmt.Errorf("registered vault %q encryption metadata is unavailable", entry.ID), err)
		}
		// Content saves keep the data key and can legitimately change catalog
		// bytes after preparation. Replay/rollback depends on exact old/new
		// envelopes, never the mutable content snapshot. The prepare step already
		// authenticated the catalog and key against this registration.
		if err := checkRegisteredCatalogIdentity(state.catalogPath, entry.ID); err != nil {
			return err
		}
		store, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
		if err != nil {
			return err
		}
		current, err := store.Load()
		if err != nil {
			return fmt.Errorf("load registered vault %q key envelope: %w", entry.ID, err)
		}
		currentBytes, err := json.Marshal(current)
		if err != nil {
			return err
		}
		oldEnvelope, err := decodeVaultKeyEnvelope(update.OldEnvelope)
		if err != nil {
			return err
		}
		newEnvelope, err := decodeVaultKeyEnvelope(update.NewEnvelope)
		if err != nil {
			return err
		}
		oldBytes, _ := json.Marshal(oldEnvelope)
		newBytes, _ := json.Marshal(newEnvelope)
		targetBytes, sourceBytes := newBytes, oldBytes
		if !forward {
			targetBytes, sourceBytes = oldBytes, newBytes
		}
		switch {
		case bytes.Equal(currentBytes, targetBytes):
			// A visible prior rename may still lack its directory barrier.
			// Replay identical authenticated bytes through the durable writer.
		case bytes.Equal(currentBytes, sourceBytes):
		default:
			return fmt.Errorf("registered vault %q key envelope changed during password change", entry.ID)
		}
		if forward {
			err = store.Save(newEnvelope)
		} else {
			err = store.Save(oldEnvelope)
		}
		if err != nil {
			return fmt.Errorf("replace registered vault %q key envelope: %w", entry.ID, err)
		}
	}
	return nil
}

func (c *vaultPasswordChangeCoordinator) registeredNotebooks() ([]notebookRecord, error) {
	if c.registryPath == "" {
		return nil, nil
	}
	notebookRegistryMu.Lock()
	registry, err := loadNotebookRegistry(c.registryPath)
	notebookRegistryMu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load registered vaults: %w", err)
	}
	seen := make(map[string]struct{}, len(registry.Entries))
	entries := make([]notebookRecord, 0, len(registry.Entries))
	for _, entry := range registry.Entries {
		if entry.ID == "" || strings.ContainsAny(entry.ID, `/\\`) || entry.ID == "." || entry.ID == ".." {
			return nil, errors.New("registered vault has an invalid identity")
		}
		if _, exists := seen[entry.ID]; exists {
			return nil, errors.New("registered vault identities are duplicated")
		}
		seen[entry.ID] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, nil
}

type vaultProtectionState struct {
	protected            bool
	hasCatalog           bool
	hasEnvelope          bool
	hasRecoveryEnvelope  bool
	catalogPath          string
	envelopePath         string
	recoveryEnvelopePath string
}

func inspectVaultProtection(entry notebookRecord, registryPath string) (vaultProtectionState, error) {
	metadataDir := vaultEncryptionDirectory(registryPath, entry.ID)
	state := vaultProtectionState{
		catalogPath:          filepath.Join(metadataDir, "catalog.json"),
		envelopePath:         filepath.Join(metadataDir, "key-envelope.json"),
		recoveryEnvelopePath: filepath.Join(metadataDir, "recovery-key-envelope.json"),
	}
	metadataRoot := filepath.Dir(metadataDir)
	rootInfo, rootErr := os.Lstat(metadataRoot)
	if errors.Is(rootErr, os.ErrNotExist) {
		ageContent, scanErr := vaultContainsAgeCiphertext(entry.LocalPath)
		state.protected = ageContent
		return state, scanErr
	}
	if rootErr != nil {
		return state, rootErr
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return state, errors.New("encryption metadata root is not a real directory")
	}
	directoryInfo, directoryErr := os.Lstat(metadataDir)
	if errors.Is(directoryErr, os.ErrNotExist) {
		ageContent, scanErr := vaultContainsAgeCiphertext(entry.LocalPath)
		state.protected = ageContent
		return state, scanErr
	}
	if directoryErr != nil {
		return state, directoryErr
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return state, errors.New("vault encryption metadata is not a real directory")
	}
	var err error
	state.hasCatalog, err = regularFileExists(state.catalogPath)
	if err != nil {
		return state, err
	}
	state.hasEnvelope, err = regularFileExists(state.envelopePath)
	if err != nil {
		return state, err
	}
	state.hasRecoveryEnvelope, err = regularFileExists(state.recoveryEnvelopePath)
	if err != nil {
		return state, err
	}
	if state.hasCatalog || state.hasEnvelope || state.hasRecoveryEnvelope {
		state.protected = true
		return state, nil
	}
	ageContent, err := vaultContainsAgeCiphertext(entry.LocalPath)
	if err != nil {
		return state, err
	}
	state.protected = ageContent
	return state, nil
}

func vaultEncryptionDirectory(registryPath, notebookID string) string {
	digest := sha256.Sum256([]byte(notebookID))
	return filepath.Join(filepath.Dir(registryPath), "encryption", hex.EncodeToString(digest[:]))
}

// registeredCatalogID binds authenticated vault metadata to its independently
// configured registration. Never derive the expected identity from the catalog
// being checked, or enroll an existing catalog automatically. Unbound pre-release
// fixtures remain decryptable externally but must fail closed in the app.
func registeredCatalogID(notebookID string) string {
	digest := sha256.Sum256([]byte("varianotes/registered-vault-catalog/v1\x00" + notebookID))
	return hex.EncodeToString(digest[:16])
}

func checkRegisteredCatalogIdentity(filename, notebookID string) error {
	store, err := encryption.NewCatalogStore(filename)
	if err != nil {
		return err
	}
	id, err := store.ReadVaultID()
	if err != nil {
		return err
	}
	if id != registeredCatalogID(notebookID) {
		return encryption.ErrCatalogAuthentication
	}
	return nil
}

func validateRegisteredVault(entry notebookRecord) error {
	if strings.TrimSpace(entry.LocalPath) == "" || !filepath.IsAbs(entry.LocalPath) {
		return errors.New("registered vault path is not absolute")
	}
	info, err := os.Lstat(filepath.Clean(entry.LocalPath))
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("registered vault path is not a real directory")
	}
	return nil
}

func vaultContainsAgeCiphertext(root string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link prevents safe encryption-state inspection: %s", name)
		}
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".age") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, fs.SkipAll) {
		return true, nil
	}
	return found, err
}

func regularFileExists(filename string) (bool, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("encryption metadata is not a regular file: %s", filename)
	}
	return true, nil
}

func readRegularFile(filename string, maximum int64) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(filename))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(filename)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, errors.New("encryption metadata file is unsafe or too large")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, errors.New("encryption metadata changed while opening")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	currentInfo, statErr := root.Lstat(name)
	if statErr != nil || !currentInfo.Mode().IsRegular() || !os.SameFile(info, currentInfo) || int64(len(content)) > maximum {
		return nil, errors.New("encryption metadata changed while reading")
	}
	return content, nil
}

func decodePasswordChangePlan(encoded []byte) (passwordChangePlan, error) {
	if len(encoded) == 0 || len(encoded) > 32<<20 {
		return passwordChangePlan{}, errors.New("invalid password change journal plan")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var plan passwordChangePlan
	if err := decoder.Decode(&plan); err != nil {
		return passwordChangePlan{}, fmt.Errorf("decode password change journal plan: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || plan.Version != passwordChangePlanVersion {
		return passwordChangePlan{}, errors.New("invalid password change journal plan")
	}
	seen := make(map[string]struct{}, len(plan.Updates))
	for _, update := range plan.Updates {
		if update.NotebookID == "" || update.CatalogSHA256 == "" || len(update.OldEnvelope) == 0 || len(update.NewEnvelope) == 0 {
			return passwordChangePlan{}, errors.New("invalid password change journal plan")
		}
		if _, exists := seen[update.NotebookID]; exists {
			return passwordChangePlan{}, errors.New("duplicate vault in password change journal plan")
		}
		seen[update.NotebookID] = struct{}{}
	}
	return plan, nil
}

func decodeVaultKeyEnvelope(encoded []byte) (encryption.VaultKeyEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var envelope encryption.VaultKeyEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return encryption.VaultKeyEnvelope{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return encryption.VaultKeyEnvelope{}, errors.New("invalid password change envelope")
	}
	return envelope, nil
}

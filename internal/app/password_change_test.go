package app

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
	_ "modernc.org/sqlite"
)

func TestPasswordChangeRewrapsEveryRegisteredVaultAndPreservesContent(t *testing.T) {
	metadataPath, entries, content := passwordChangeFixture(t, 2, true)
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	planBytes, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "new vault password")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := decodePasswordChangePlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Updates) != 2 {
		t.Fatalf("prepared %d vault updates, want 2", len(plan.Updates))
	}
	if err := coordinator.ApplyPasswordChange(t.Context(), planBytes); err != nil {
		t.Fatal(err)
	}
	oldEnvelopes := make(map[string][]byte, len(plan.Updates))
	for _, update := range plan.Updates {
		oldEnvelopes[update.NotebookID] = update.OldEnvelope
	}
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		store, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		key, err := encryption.DecryptVaultKey("new vault password", envelope)
		if err != nil {
			t.Fatalf("new password did not unlock %s: %v", entry.ID, err)
		}
		defer clear(key)
		oldEnvelopeBytes, _ := json.Marshal(mustDecodeEnvelope(t, oldEnvelopes[entry.ID]))
		newEnvelopeBytes, _ := json.Marshal(envelope)
		if bytes.Equal(oldEnvelopeBytes, newEnvelopeBytes) {
			t.Fatalf("password change did not replace the envelope for %s", entry.ID)
		}
		got, err := os.ReadFile(content[entry.ID])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte("ciphertext bytes stay untouched")) {
			t.Fatalf("password change modified content in %s", entry.ID)
		}
		catalogStore, err := encryption.NewCatalogStore(state.catalogPath)
		if err != nil {
			t.Fatal(err)
		}
		catalogID, err := catalogStore.ReadVaultID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalogStore.Load(key, catalogID); err != nil {
			t.Fatalf("catalog no longer verifies for %s: %v", entry.ID, err)
		}
	}
	if err := coordinator.RollbackPasswordChange(t.Context(), planBytes); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		store, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
		if err != nil {
			t.Fatal(err)
		}
		current, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		currentBytes, _ := json.Marshal(current)
		if !bytes.Equal(currentBytes, oldEnvelopes[entry.ID]) {
			t.Fatalf("rollback did not restore the envelope for %s", entry.ID)
		}
	}
}

func TestPasswordChangePreflightRejectsUnavailableRegisteredVault(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 1, false)
	missing := notebookRecord{ID: "missing", Name: "Missing", LocalPath: filepath.Join(t.TempDir(), "not-mounted")}
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	registry.Entries = append(registry.Entries, missing)
	if err := writeNotebookRegistry(metadataPath, registry); err != nil {
		t.Fatal(err)
	}
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	if _, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "new vault password"); err == nil {
		t.Fatal("password change accepted an unavailable registered vault")
	}
	assertVaultPasswords(t, entries, metadataPath, "original vault password")
}

func TestDisabledAuthenticationIsBlockedByCiphertextInTrash(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 1, true)
	service, err := auth.Open(t.Context(), auth.Config{
		Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(metadataPath), "auth.db"),
		PasswordCoordinator: NewPasswordChangeCoordinator(metadataPath),
	}, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original auth password"); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = auth.Open(t.Context(), auth.Config{
		Mode: auth.ModeDisabled, ModeExplicit: true, MetadataPath: filepath.Join(filepath.Dir(metadataPath), "auth.db"),
		PasswordCoordinator: NewPasswordChangeCoordinator(metadataPath),
	}, passwordChangeLogger())
	if !errors.Is(err, auth.ErrAuthenticationDisableBlocked) {
		t.Fatalf("disabled authentication startup error = %v", err)
	}
	local, err := auth.Open(t.Context(), auth.Config{
		Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(metadataPath), "auth.db"),
		PasswordCoordinator: NewPasswordChangeCoordinator(metadataPath),
	}, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if err := local.VerifyPassword(t.Context(), "original auth password"); err != nil {
		t.Fatalf("blocked mode transition changed auth credentials: %v", err)
	}
	state, err := inspectVaultProtection(entries[0], metadataPath)
	if err != nil || !state.protected {
		t.Fatalf("Trash ciphertext was not considered protected: state=%#v err=%v", state, err)
	}
}

func TestPasswordChangeRecoversAcrossVaultAndAuthDatabaseInterruption(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 2, true)
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	authPath := filepath.Join(filepath.Dir(metadataPath), "auth.db")
	config := auth.Config{Mode: auth.ModeLocal, MetadataPath: authPath, PasswordCoordinator: coordinator}
	service, err := auth.Open(t.Context(), config, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original vault password"); err != nil {
		t.Fatal(err)
	}
	planBytes, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "new auth password")
	if err != nil {
		t.Fatal(err)
	}
	if err := insertPasswordChangeJournal(t, authPath, "prepared", planBytes); err != nil {
		t.Fatal(err)
	}
	plan, err := decodePasswordChangePlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	firstState, err := inspectVaultProtection(entries[0], metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := encryption.NewVaultKeyEnvelopeStore(firstState.envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Save(mustDecodeEnvelope(t, plan.Updates[0].NewEnvelope)); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := auth.Open(t.Context(), config, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatalf("prepared interruption changed auth password: %v", err)
	}
	if err := recovered.ChangePassword(t.Context(), "original vault password", "new auth password", nil); err != nil {
		t.Fatal(err)
	}
	if err := recovered.VerifyPassword(t.Context(), "new auth password"); err != nil {
		t.Fatalf("new auth password does not verify: %v", err)
	}
	assertVaultPasswords(t, entries, metadataPath, "new auth password")
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedPasswordChangeRecoversForward(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 1, true)
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	authPath := filepath.Join(filepath.Dir(metadataPath), "auth.db")
	config := auth.Config{Mode: auth.ModeLocal, MetadataPath: authPath, PasswordCoordinator: coordinator}
	service, err := auth.Open(t.Context(), config, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original auth password"); err != nil {
		t.Fatal(err)
	}
	plan, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "replacement vault password")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverPassword(t.Context(), "replacement auth password"); err != nil {
		t.Fatal(err)
	}
	if err := insertPasswordChangeJournal(t, authPath, "committed", plan); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := auth.Open(t.Context(), config, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err := recovered.VerifyPassword(t.Context(), "replacement auth password"); err != nil {
		t.Fatal(err)
	}
	assertVaultPasswords(t, entries, metadataPath, "replacement vault password")
}

func passwordChangeFixture(t *testing.T, count int, includeTrash bool) (string, []notebookRecord, map[string]string) {
	t.Helper()
	metadataPath := filepath.Join(t.TempDir(), "app", "notebooks.json")
	entries := make([]notebookRecord, 0, count)
	contentPaths := make(map[string]string, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("vault-%d", index)
		root := filepath.Join(t.TempDir(), id)
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		entry := notebookRecord{ID: id, Name: id, LocalPath: root}
		entries = append(entries, entry)
		catalog, err := encryption.NewCatalog()
		if err != nil {
			t.Fatal(err)
		}
		key, err := encryption.NewVaultKey()
		if err != nil {
			t.Fatal(err)
		}
		catalogPath := filepath.Join(vaultEncryptionDirectory(metadataPath, id), "catalog.json")
		catalogStore, err := encryption.NewCatalogStore(catalogPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := catalogStore.Save(catalog, key); err != nil {
			t.Fatal(err)
		}
		envelope, err := encryption.EncryptVaultKey("original vault password", key)
		if err != nil {
			t.Fatal(err)
		}
		envelopeStore, err := encryption.NewVaultKeyEnvelopeStore(filepath.Join(vaultEncryptionDirectory(metadataPath, id), "key-envelope.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := envelopeStore.Save(envelope); err != nil {
			t.Fatal(err)
		}
		if includeTrash {
			path := filepath.Join(root, ".trash", "item-1", "note.md.age")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("ciphertext bytes stay untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			contentPaths[id] = path
		}
		clear(key)
	}
	if err := writeNotebookRegistry(metadataPath, notebookRegistry{ActiveID: entries[0].ID, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	return metadataPath, entries, contentPaths
}

func assertVaultPasswords(t *testing.T, entries []notebookRecord, metadataPath, password string) {
	t.Helper()
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		store, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		key, err := encryption.DecryptVaultKey(password, envelope)
		if err != nil {
			t.Fatalf("password %q did not unlock %s: %v", password, entry.ID, err)
		}
		clear(key)
	}
}

func mustDecodeEnvelope(t *testing.T, encoded []byte) encryption.VaultKeyEnvelope {
	t.Helper()
	var envelope encryption.VaultKeyEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestPasswordResetDoesNotChangeVaultKeyEnvelope(t *testing.T) {
	metadataPath, entries, content := passwordChangeFixture(t, 1, true)
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	service, err := auth.Open(t.Context(), auth.Config{
		Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(metadataPath), "auth.db"),
		PasswordCoordinator: coordinator,
	}, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "original auth password"); err != nil {
		t.Fatal(err)
	}
	state, err := inspectVaultProtection(entries[0], metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverPassword(t.Context(), "recovered auth password"); err != nil {
		t.Fatal(err)
	}
	after, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, _ := json.Marshal(before)
	afterBytes, _ := json.Marshal(after)
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("password reset changed the vault key envelope")
	}
	if err := service.VerifyPassword(t.Context(), "recovered auth password"); err != nil {
		t.Fatal(err)
	}
	separate, err := service.EncryptionPasswordSeparate(t.Context())
	if err != nil || !separate {
		t.Fatalf("password recovery did not separate encryption password: separate=%v err=%v", separate, err)
	}
	if err := service.ChangePassword(t.Context(), "recovered auth password", "later auth password", nil); err != nil {
		t.Fatalf("later login password change depended on the vault password: %v", err)
	}
	if _, err := encryption.DecryptVaultKey("original vault password", after); err != nil {
		t.Fatalf("password reset lost the original encryption password: %v", err)
	}
	unchanged, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	unchangedBytes, _ := json.Marshal(unchanged)
	if !bytes.Equal(beforeBytes, unchangedBytes) {
		t.Fatal("later login password change modified the separate vault envelope")
	}
	contentBytes, err := os.ReadFile(content[entries[0].ID])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contentBytes, []byte("ciphertext bytes stay untouched")) {
		t.Fatal("password reset changed vault ciphertext")
	}
	_ = service.Close()
}

func TestPasswordChangePlanContainsNoPlaintextPasswords(t *testing.T) {
	metadataPath, _, _ := passwordChangeFixture(t, 1, true)
	coordinator := NewPasswordChangeCoordinator(metadataPath)
	plan, err := coordinator.PreparePasswordChange(t.Context(), "original vault password", "new local login password")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plan, []byte("original vault password")) || bytes.Contains(plan, []byte("new local login password")) {
		t.Fatal("password change journal contains plaintext credentials")
	}
}

func passwordChangeLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func insertPasswordChangeJournal(t *testing.T, filename, phase string, plan []byte) error {
	t.Helper()
	database, err := sql.Open("sqlite", filename)
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.ExecContext(t.Context(), `INSERT INTO auth_password_change_journal(id,phase,plan,created_at) VALUES(1,?,?,?)`, phase, plan, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

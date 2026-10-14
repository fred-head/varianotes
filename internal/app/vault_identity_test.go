package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/fred-head/varianotes/internal/files"
)

func TestRegisteredVaultRejectsCompleteForeignPackageAfterRestart(t *testing.T) {
	registry, entries, _ := passwordChangeFixture(t, 2, false)
	keys := make([][]byte, len(entries))
	for i, entry := range entries {
		state, err := inspectVaultProtection(entry, registry)
		if err != nil {
			t.Fatal(err)
		}
		envelopes, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := envelopes.Load()
		if err != nil {
			t.Fatal(err)
		}
		keys[i], err = encryption.DecryptVaultKey("original vault password", envelope)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(keys[i])
		store, err := encryption.NewCatalogStore(state.catalogPath)
		if err != nil {
			t.Fatal(err)
		}
		id, err := store.ReadVaultID()
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := store.Load(keys[i], id)
		if err != nil {
			t.Fatal(err)
		}
		objectID, err := encryption.NewCatalogObjectID()
		if err != nil {
			t.Fatal(err)
		}
		ciphertext, contentKey, err := encryption.EncryptContent(keys[i], objectID, []byte(entry.ID+" secret"))
		if err != nil {
			t.Fatal(err)
		}
		catalog.Objects = append(catalog.Objects, encryption.CatalogObject{ObjectID: objectID, Kind: encryption.CatalogObjectNote, LogicalPath: "Note.md", PhysicalPath: "Note.md.age", Protected: true, CiphertextHash: encryption.CiphertextSHA256(ciphertext), ContentKeyEnvelope: &contentKey})
		if err := store.Save(catalog, keys[i]); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(entry.LocalPath, "Note.md.age"), ciphertext, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"catalog.json", "key-envelope.json"} {
		foreign, err := os.ReadFile(filepath.Join(vaultEncryptionDirectory(registry, entries[1].ID), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vaultEncryptionDirectory(registry, entries[0].ID), name), foreign, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	foreignContent, err := os.ReadFile(filepath.Join(entries[1].LocalPath, "Note.md.age"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entries[0].LocalPath, "Note.md.age"), foreignContent, 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := files.NewRepository(entries[0].LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	// Construct fresh access as after a process restart, using B's legitimate
	// key (both envelopes use the same owner password).
	access := newProtectedNoteAccess(repository, registry, entries[0])
	if _, err := access.catalogForKey(keys[1]); !errors.Is(err, encryption.ErrCatalogAuthentication) {
		t.Fatalf("foreign vault accepted under A's registration: %v", err)
	}
	if _, err := NewPasswordChangeCoordinator(registry).PreparePasswordChange(t.Context(), "original vault password", "replacement password"); !errors.Is(err, encryption.ErrCatalogAuthentication) {
		t.Fatalf("password change accepted foreign identity: %v", err)
	}
	if _, err := NewRecoveryCodeCoordinator(registry).PrepareRecoveryCode(t.Context(), "original vault password", "replacement recovery code"); !errors.Is(err, encryption.ErrCatalogAuthentication) {
		t.Fatalf("recovery setup accepted foreign identity: %v", err)
	}
	remaining, err := os.ReadFile(filepath.Join(entries[0].LocalPath, "Note.md.age"))
	if err != nil || string(remaining) != string(foreignContent) {
		t.Fatalf("identity rejection changed content: %v", err)
	}
}

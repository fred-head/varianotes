package files

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestMovePreparedRestartPreservesSourceAndOrdinaryBacklink(t *testing.T) {
	for _, choice := range []string{MoveProtectionKeep, MoveProtectionDecrypt} {
		t.Run(choice, func(t *testing.T) {
			root, metadata := t.TempDir(), t.TempDir()
			writeConversionFile(t, root, "Index.md", "[secret](private/Note.md)")
			if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
				t.Fatal(err)
			}
			repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
			catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
			if err := store.Save(catalog, key); err != nil {
				t.Fatal(err)
			}
			addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "secret move sentinel")
			catalog, err := store.Load(key, catalog.VaultID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, "private", "Note.md.age"))
			if err != nil {
				t.Fatal(err)
			}
			repository.transactionPrepared = func() error {
				current, err := os.ReadFile(filepath.Join(root, "private", "Note.md.age"))
				if err != nil || !bytes.Equal(before, current) {
					t.Fatalf("source changed before commit: %v", err)
				}
				assertFileContains(t, filepath.Join(root, "Index.md"), "private/Note.md")
				if _, err := os.Stat(filepath.Join(root, "moved", "Note.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("plaintext before commit: %v", err)
				}
				return encryption.ErrTransactionNeedsRecovery
			}
			preview, err := repository.PreviewMoveLinksWithCatalog("private", "moved", catalog, key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.MoveWithCatalog("private", "moved", preview.Token, choice, catalog, key, store, journal); !errors.Is(err, encryption.ErrTransactionNeedsRecovery) {
				t.Fatalf("crash boundary: %v", err)
			}
			restarted, err := encryption.NewTransactionJournal(root, store)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Recover(key); err != nil {
				t.Fatal(err)
			}
			current, err := os.ReadFile(filepath.Join(root, "private", "Note.md.age"))
			if err != nil || !bytes.Equal(before, current) {
				t.Fatalf("restart lost source: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "moved")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("prepared destination survived: %v", err)
			}
			assertFileContains(t, filepath.Join(root, "Index.md"), "private/Note.md")
		})
	}
}

func TestDecryptRestorePreparedRestartKeepsOnlyTrashCiphertext(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	writeConversionFile(t, root, "Note.md", "restore plaintext sentinel")
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	conversion, err := repository.PrepareExistingConversion(t.Context(), "Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversion.Commit(); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := repository.MoveToTrashWithCatalog("Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	repository.transactionPrepared = func() error {
		if _, err := os.Stat(filepath.Join(root, "Note.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore published plaintext before commit: %v", err)
		}
		return encryption.ErrTransactionNeedsRecovery
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, MoveProtectionDecrypt, catalog, key, store, journal); !errors.Is(err, encryption.ErrTransactionNeedsRecovery) {
		t.Fatalf("crash boundary: %v", err)
	}
	restarted, err := encryption.NewTransactionJournal(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Note.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared restart left plaintext: %v", err)
	}
	repository = mustRepository(t, root)
	items, err := repository.TrashItemsWithCatalog(catalog, key, journal)
	if err != nil || len(items) != 1 || !items[0].Protected {
		t.Fatalf("restart lost protected Trash: %#v %v", items, err)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, MoveProtectionDecrypt, catalog, key, store, restarted); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(root, "Note.md"), "restore plaintext sentinel")
}

package files

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestExistingConversionStagesFolderAttachmentsAndAssociatedTrash(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Shared.assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConversionFile(t, root, "folder/Doc.md", "![shared](../Shared.assets/shared.png)\n")
	writeConversionFile(t, root, "Shared.assets/shared.png", string(testPNG))
	writeConversionFile(t, root, "folder/Deleted.md", "![old](Deleted.assets/old.png)\n")
	writeConversionFile(t, root, "folder/Deleted.assets/old.png", string(testPNG))
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	trashed, err := repository.MoveToTrash("folder/Deleted.md")
	if err != nil {
		t.Fatal(err)
	}

	operation, err := repository.PrepareExistingConversion(context.Background(), "folder", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("prepare folder conversion: %v", err)
	}
	if operation.Summary.Notes != 2 || operation.Summary.Attachments != 2 || operation.Summary.TrashItems != 1 {
		t.Fatalf("conversion summary = %#v", operation.Summary)
	}
	for _, relative := range []string{"folder/Doc.md", "Shared.assets/shared.png", ".trash/" + trashed.ID + "/content/item", ".trash/" + trashed.ID + "/content/item.assets/old.png"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Errorf("plaintext original %q disappeared before commit: %v", relative, err)
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative+".age"))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("live ciphertext %q appeared before commit: %v", relative+".age", err)
		}
	}
	if err := operation.Abort(); err != nil {
		t.Fatalf("abort conversion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "folder", "Doc.md")); err != nil {
		t.Fatalf("abort did not preserve the note: %v", err)
	}
	if _, err := store.Load(key, catalog.VaultID); err != nil {
		t.Fatalf("abort changed the catalog: %v", err)
	}
	assertNoConversionStages(t, root, metadata)
}

func TestExistingConversionCommitsAllNotesAssetsAndTrashForward(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Shared.assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConversionFile(t, root, "folder/Doc.md", "![shared](../Shared.assets/shared.png)\n")
	writeConversionFile(t, root, "Shared.assets/shared.png", string(testPNG))
	writeConversionFile(t, root, "folder/Deleted.md", "![old](Deleted.assets/old.png)\n")
	writeConversionFile(t, root, "folder/Deleted.assets/old.png", string(testPNG))
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	trashed, err := repository.MoveToTrash("folder/Deleted.md")
	if err != nil {
		t.Fatal(err)
	}

	operation, err := repository.PrepareExistingConversion(context.Background(), "folder", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("prepare folder conversion: %v", err)
	}
	if err := operation.Commit(); err != nil {
		t.Fatalf("commit folder conversion: %v", err)
	}
	for _, relative := range []string{
		"folder/Doc.md", "Shared.assets/shared.png",
		".trash/" + trashed.ID + "/content/item",
		".trash/" + trashed.ID + "/content/item.assets/old.png",
	} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("plaintext original %q remains after commit: %v", relative, err)
		}
		ciphertext, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative+".age")))
		if err != nil {
			t.Errorf("committed ciphertext %q is missing: %v", relative+".age", err)
		} else if bytes.Contains(ciphertext, []byte("![shared]")) || bytes.Contains(ciphertext, testPNG) {
			t.Errorf("ciphertext %q contains plaintext", relative+".age")
		}
	}
	updated, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFolderRule(updated, "folder") || len(updated.Objects) != 4 {
		t.Fatalf("catalog did not commit the folder and all four files: %#v", updated)
	}
	note, protected, err := repository.ReadMarkdownWithCatalog("folder/Doc.md", updated, key)
	if err != nil || !protected || note.Content != "![shared](../Shared.assets/shared.png)\n" {
		t.Fatalf("read converted note: protected=%v err=%v note=%#v", protected, err, note)
	}
	asset, protected, err := repository.ReadAssetWithCatalog("folder/Doc.md", "../Shared.assets/shared.png", updated, key)
	if err != nil || !protected || !bytes.Equal(asset.Content, testPNG) {
		t.Fatalf("read converted shared attachment: protected=%v err=%v", protected, err)
	}
	clear(asset.Content)
	items, err := repository.TrashItems()
	if err != nil || len(items) != 1 || items[0].ID != trashed.ID {
		t.Fatalf("list converted Trash item: %#v err=%v", items, err)
	}
	if _, err := repository.RestoreTrashItem(trashed.ID); !errors.Is(err, ErrTrashEncrypted) {
		t.Fatalf("restore encrypted Trash item = %v", err)
	}
	if _, err := repository.DeleteTrashItem(trashed.ID); !errors.Is(err, ErrTrashEncrypted) {
		t.Fatalf("delete encrypted Trash item = %v", err)
	}
	assertNoConversionStages(t, root, metadata)
}

func TestExistingConversionProtectsEmptyFoldersAndTrashOnlySelections(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConversionFile(t, root, "Old.md", "old in Trash")
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	trashed, err := repository.MoveToTrash("Old.md")
	if err != nil {
		t.Fatal(err)
	}

	empty, err := repository.PrepareExistingConversion(context.Background(), "Empty", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.Commit(); err != nil {
		t.Fatalf("commit empty-folder protection rule: %v", err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil || !hasFolderRule(catalog, "Empty") {
		t.Fatalf("empty folder rule was not committed: %#v err=%v", catalog, err)
	}

	old, err := repository.PrepareExistingConversion(context.Background(), "Old.md", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("prepare conversion for note only in Trash: %v", err)
	}
	if old.Summary.Notes != 1 || old.Summary.TrashItems != 1 {
		t.Fatalf("Trash-only conversion summary = %#v", old.Summary)
	}
	if err := old.Commit(); err != nil {
		t.Fatalf("commit Trash-only conversion: %v", err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(root, ".trash", trashed.ID, "content", "item.age")
	if _, err := os.Stat(physical); err != nil {
		t.Fatalf("Trash-only ciphertext is missing: %v", err)
	}
	if _, found := catalogNote(catalog, ".trash/"+trashed.ID+"/content/item"); !found {
		t.Fatal("Trash-only note is absent from the authenticated catalog")
	}
}

func TestExistingConversionIncludesAndRetainsDeletedFolderCiphertext(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	writeConversionFile(t, root, "Retired/Note.md", "from deleted folder")
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	trashed, err := repository.MoveToTrash("Retired")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := repository.PrepareExistingConversion(context.Background(), "Retired", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("prepare deleted-folder conversion: %v", err)
	}
	if operation.Summary.Notes != 1 || operation.Summary.TrashItems != 1 {
		t.Fatalf("deleted-folder conversion summary = %#v", operation.Summary)
	}
	if err := operation.Commit(); err != nil {
		t.Fatalf("commit deleted-folder conversion: %v", err)
	}
	physical := filepath.Join(root, ".trash", trashed.ID, "content", "item", "Note.md.age")
	if _, err := os.Stat(physical); err != nil {
		t.Fatalf("deleted-folder ciphertext is missing: %v", err)
	}
	if _, err := repository.DeleteTrashItem(trashed.ID); !errors.Is(err, ErrTrashEncrypted) {
		t.Fatalf("delete encrypted folder Trash item = %v", err)
	}
}

func TestExistingConversionPreflightRejectsUnsafeEntriesAndCollisions(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, string)
		want  error
	}{
		{name: "symlink", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "Folder"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("outside", filepath.Join(root, "Folder", "Note.md")); err != nil {
				t.Fatal(err)
			}
		}, want: ErrConversionUnsafe},
		{name: "unsupported file", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "Folder"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeConversionFile(t, root, "Folder/Note.md", "note")
			writeConversionFile(t, root, "Folder/data.txt", "unsupported")
		}, want: ErrConversionUnsafe},
		{name: "ciphertext collision", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "Folder"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeConversionFile(t, root, "Folder/Note.md", "note")
			writeConversionFile(t, root, "Folder/Note.md.age", "untracked")
		}, want: ErrConversionCollision},
		{name: "hard link", setup: func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "Folder"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeConversionFile(t, root, "Folder/Note.md", "note")
			if err := os.Link(filepath.Join(root, "Folder", "Note.md"), filepath.Join(root, "outside.md")); err != nil {
				t.Skipf("hard links are unavailable: %v", err)
			}
		}, want: ErrConversionUnsafe},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			metadata := t.TempDir()
			test.setup(t, root)
			repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
			if _, err := repository.PrepareExistingConversion(context.Background(), "Folder", catalog, key, store, journal); !errors.Is(err, test.want) {
				t.Fatalf("conversion error = %v, want %v", err, test.want)
			}
			if _, err := os.Lstat(filepath.Join(root, "Folder", "Note.md")); err != nil && test.name != "symlink" {
				t.Fatalf("preflight removed the original note: %v", err)
			}
			if _, err := store.Load(key, catalog.VaultID); err != nil {
				t.Fatalf("preflight changed the catalog: %v", err)
			}
			assertNoConversionStages(t, root, metadata)
		})
	}
}

func TestExistingConversionCancellationBeforePreparePreservesOriginals(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	writeConversionFile(t, root, "Note.md", "keep this plaintext until commit")
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.PrepareExistingConversion(ctx, "Note.md", catalog, key, store, journal); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled conversion error = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "Note.md")); err != nil || string(content) != "keep this plaintext until commit" {
		t.Fatalf("canceled conversion changed the original: %q err=%v", content, err)
	}
	assertNoConversionStages(t, root, metadata)
}

func TestExistingConversionCancellationAfterStagingAbortsBeforeCommit(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	writeConversionFile(t, root, "Note.md", "keep until commit")
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	ctx, cancel := context.WithCancel(context.Background())
	operation, err := repository.PrepareExistingConversion(ctx, "Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("prepare conversion: %v", err)
	}
	cancel()
	if err := operation.Commit(); !errors.Is(err, context.Canceled) {
		t.Fatalf("commit canceled conversion = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "Note.md")); err != nil || string(content) != "keep until commit" {
		t.Fatalf("canceled staged conversion changed the source: %q err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled staged conversion installed ciphertext: %v", err)
	}
	if _, err := store.Load(key, catalog.VaultID); err != nil {
		t.Fatalf("canceled staged conversion changed the catalog: %v", err)
	}
	assertNoConversionStages(t, root, metadata)
}

func newConversionFixture(t *testing.T, root, metadata string) (*Repository, *encryption.CatalogStore, *encryption.TransactionJournal, encryption.Catalog, []byte) {
	t.Helper()
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x79}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	return repository, store, journal, catalog, key
}

func writeConversionFile(t *testing.T, root, relative, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNoConversionStages(t *testing.T, roots ...string) {
	t.Helper()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if strings.HasPrefix(entry.Name(), ".vnotes-txn-") || strings.HasSuffix(entry.Name(), ".transaction.json") {
				t.Errorf("transaction stage remains: %q", filename)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

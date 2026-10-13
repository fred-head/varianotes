package files

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestProtectedMarkdownCreateSaveConflictAndTree(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x49}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)

	create, protected, err := repository.PrepareMarkdownCreateWithCatalog("private/Note.md", catalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected create: protected=%v err=%v", protected, err)
	}
	if _, err := os.Stat(filepath.Join(root, "private", "Note.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext note appeared before commit: %v", err)
	}
	if err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte("# Note")) {
			t.Errorf("plaintext appeared in temporary file %q", filename)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := create.Commit(nil); err != nil {
		t.Fatalf("commit protected create: %v", err)
	}
	physicalPath := filepath.Join(root, "private", "Note.md.age")
	ciphertext, err := os.ReadFile(physicalPath)
	if err != nil {
		t.Fatalf("protected ciphertext is missing: %v", err)
	}
	if bytes.Contains(ciphertext, []byte("# Note")) {
		t.Fatal("protected note title was stored in plaintext")
	}
	loadedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, isProtected, err := repository.ReadMarkdownWithCatalog("private/Note.md", loadedCatalog, key)
	if err != nil || !isProtected || loaded.Content != "# Note\n" || loaded.Version == "" {
		t.Fatalf("read created note: %#v protected=%v err=%v", loaded, isProtected, err)
	}

	stale, _, err := repository.PrepareMarkdownWriteWithCatalog("private/Note.md", "stale draft", strings.Repeat("0", 64), loadedCatalog, key, store, journal)
	if !errors.Is(err, ErrConflict) || stale != nil {
		t.Fatalf("stale write result=%v err=%v", stale, err)
	}
	serverBefore, err := os.ReadFile(physicalPath)
	if err != nil {
		t.Fatal(err)
	}
	serverNote, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", loadedCatalog, key)
	if err != nil || serverNote.Content != "# Note\n" {
		t.Fatalf("stale write changed server note: %#v err=%v", serverNote, err)
	}

	write, protected, err := repository.PrepareMarkdownWriteWithCatalog("private/Note.md", "# Updated\n", loaded.Version, loadedCatalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected save: protected=%v err=%v", protected, err)
	}
	if err := write.Commit(nil); err != nil {
		t.Fatalf("commit protected save: %v", err)
	}
	updatedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	updated, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", updatedCatalog, key)
	if err != nil || updated.Content != "# Updated\n" || updated.Version == loaded.Version {
		t.Fatalf("read saved note: %#v err=%v", updated, err)
	}
	serverAfter, err := os.ReadFile(physicalPath)
	if err != nil || bytes.Equal(serverBefore, serverAfter) || bytes.Contains(serverAfter, []byte("# Updated")) {
		t.Fatalf("save did not replace ciphertext safely: err=%v", err)
	}
	if _, _, err := repository.PrepareMarkdownCreateWithCatalog("private/Other.md", catalog, key, store, journal); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale catalog create error = %v, want conflict", err)
	}
	latestCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil || len(latestCatalog.Objects) != 1 || latestCatalog.Objects[0].LogicalPath != "private/Note.md" {
		t.Fatalf("stale catalog update lost an existing object: %#v err=%v", latestCatalog, err)
	}

	tree, err := repository.TreeWithCatalog(updatedCatalog)
	if err != nil || len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].Path != "private/Note.md" {
		t.Fatalf("protected note missing from logical tree: %#v err=%v", tree, err)
	}
}

func TestProtectedMarkdownCorruptionFailsClosedAndPreservesBytes(t *testing.T) {
	root := t.TempDir()
	metadata := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x54}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	create, _, err := repository.PrepareMarkdownCreateWithCatalog("private/Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := create.Commit(nil); err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(root, "private", "Note.md.age")
	if err := os.WriteFile(physicalPath, []byte("corrupted ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	storedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", storedCatalog, key); !errors.Is(err, ErrProtectedContentCorrupt) {
		t.Fatalf("corrupt ciphertext read error = %v", err)
	}
	before, err := os.ReadFile(physicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.PrepareMarkdownWriteWithCatalog("private/Note.md", "overwrite", "any", storedCatalog, key, store, journal); !errors.Is(err, ErrProtectedContentCorrupt) {
		t.Fatalf("corrupt ciphertext write error = %v", err)
	}
	after, err := os.ReadFile(physicalPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("corrupt content was changed: err=%v", err)
	}
}

func protectedFixture(t *testing.T, root, metadata string, catalog encryption.Catalog, key []byte) (*encryption.CatalogStore, *encryption.TransactionJournal) {
	t.Helper()
	store, err := encryption.NewCatalogStore(filepath.Join(metadata, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(catalog, key); err != nil {
		t.Fatal(err)
	}
	journal, err := encryption.NewTransactionJournal(root, store)
	if err != nil {
		t.Fatal(err)
	}
	return store, journal
}

package files

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestProtectedSaveAbortRecoversOldContentAfterRestart(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x73}, encryption.VaultKeySize)
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
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	originalCiphertext, err := os.ReadFile(filepath.Join(root, "private", "Note.md.age"))
	if err != nil {
		t.Fatal(err)
	}
	originalCatalog, err := os.ReadFile(filepath.Join(metadata, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	operation, protected, err := repository.PrepareMarkdownWriteWithCatalog(
		"private/Note.md", "# Uncommitted P17 save marker\n", current.Version, catalog, key, store, journal,
	)
	if err != nil || !protected {
		t.Fatalf("prepare protected save: protected=%t err=%v", protected, err)
	}
	operation.Abort()
	assertP17MarkerNotPersisted(t, "Uncommitted P17 save marker", root, metadata)

	restarted, err := encryption.NewTransactionJournal(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(key); err != nil {
		t.Fatalf("recover aborted save after restart: %v", err)
	}
	loaded, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	readBack, protected, err := repository.ReadMarkdownWithCatalog("private/Note.md", loaded, key)
	if err != nil || !protected || readBack.Content != current.Content {
		t.Fatalf("aborted save changed prior note: %#v protected=%t err=%v", readBack, protected, err)
	}
	finalCiphertext, err := os.ReadFile(filepath.Join(root, "private", "Note.md.age"))
	if err != nil || !bytes.Equal(finalCiphertext, originalCiphertext) {
		t.Fatalf("aborted save changed prior ciphertext: err=%v", err)
	}
	finalCatalog, err := os.ReadFile(filepath.Join(metadata, "catalog.json"))
	if err != nil || !bytes.Equal(finalCatalog, originalCatalog) {
		t.Fatalf("aborted save changed prior catalog: err=%v", err)
	}
	assertNoConversionStages(t, root, metadata)
}

func TestProtectedArchiveExportAbortDoesNotMutateVault(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x74}, encryption.VaultKeySize)
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
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	physicalPath := filepath.Join(root, "private", "Note.md.age")
	before, err := os.ReadFile(physicalPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.InspectArchiveWithCatalog(catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	writer := &p17FailingWriter{remaining: 128}
	err = repository.WriteArchiveWithCatalog(writer, catalog, key, snapshot.Fingerprint, nil)
	if !errors.Is(err, io.ErrClosedPipe) || writer.written == 0 {
		t.Fatalf("aborted archive export error=%v, bytes started=%d", err, writer.written)
	}
	after, err := os.ReadFile(physicalPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("aborted export changed vault ciphertext: err=%v", err)
	}
	assertP17MarkerNotPersisted(t, "# Note", root, metadata)
	assertNoConversionStages(t, root, metadata)
}

func TestProtectedTrashRejectsCorruptCiphertextWithoutMovingIt(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x75}, encryption.VaultKeySize)
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
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	ciphertextPath := filepath.Join(root, "private", "Note.md.age")
	corruptCiphertext := []byte("damaged protected note")
	if err := os.WriteFile(ciphertextPath, corruptCiphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	catalogBefore, err := os.ReadFile(filepath.Join(metadata, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveToTrashWithCatalog("private/Note.md", catalog, key, store, journal); !errors.Is(err, ErrProtectedContentCorrupt) {
		t.Fatalf("Trash move with corrupt ciphertext = %v, want corruption error", err)
	}
	retained, err := os.ReadFile(ciphertextPath)
	if err != nil || !bytes.Equal(retained, corruptCiphertext) {
		t.Fatalf("failed Trash move changed the corrupt source: %q err=%v", retained, err)
	}
	catalogAfter, err := os.ReadFile(filepath.Join(metadata, "catalog.json"))
	if err != nil || !bytes.Equal(catalogAfter, catalogBefore) {
		t.Fatalf("failed Trash move changed the catalog: err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".trash")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Trash move left a Trash path: %v", err)
	}
	assertNoConversionStages(t, root, metadata)
}

type p17FailingWriter struct {
	remaining int
	written   int
}

func (w *p17FailingWriter) Write(data []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	if len(data) > w.remaining {
		count := w.remaining
		w.written += count
		w.remaining = 0
		return count, io.ErrClosedPipe
	}
	w.remaining -= len(data)
	w.written += len(data)
	return len(data), nil
}

func assertP17MarkerNotPersisted(t *testing.T, marker string, roots ...string) {
	t.Helper()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			contents, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			if bytes.Contains(contents, []byte(marker)) {
				t.Errorf("plaintext marker remained in %s", filename)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

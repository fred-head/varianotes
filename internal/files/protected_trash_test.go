package files

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestProtectedTrashKeepsCiphertextAndRestoresWithProtection(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x61}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "unique-trash-sentinel-31p13\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}

	item, err := repository.MoveToTrashWithCatalog("private/Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("move protected note to Trash: %v", err)
	}
	trashPath := filepath.Join(root, ".trash", item.ID, "content", "item.age")
	ciphertext, err := os.ReadFile(trashPath)
	if err != nil || bytes.Contains(ciphertext, []byte("unique-trash-sentinel-31p13")) {
		t.Fatalf("Trash did not retain ciphertext: err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "private", "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source ciphertext remains after Trash: %v", err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := repository.TrashItemsWithCatalog(catalog, key, journal)
	if err != nil || len(items) != 1 || !items[0].Protected || items[0].ProtectionChoiceRequired {
		t.Fatalf("protected Trash listing = %#v, err=%v", items, err)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, "", catalog, key, store, journal); err != nil {
		t.Fatalf("restore into protected folder: %v", err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	markdown, protected, err := repository.ReadMarkdownWithCatalog("private/Note.md", catalog, key)
	if err != nil || !protected || markdown.Content != "unique-trash-sentinel-31p13\n" {
		t.Fatalf("restored protected note = %#v, protected=%v, err=%v", markdown, protected, err)
	}
}

func TestProtectedTrashRestoreRequiresChoiceAndCanDecrypt(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x62}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	mustWrite(t, filepath.Join(root, "notes", "Note.md"), "decrypt-me-31p13\n")
	operation, err := repository.PrepareExistingConversion(
		t.Context(), "notes/Note.md", catalog, key, store, journal,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.Commit(); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := repository.MoveToTrashWithCatalog("notes/Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, "", catalog, key, store, journal); !errors.Is(err, ErrMoveProtectionChoiceRequired) {
		t.Fatalf("restore without protection choice = %v", err)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, MoveProtectionDecrypt, catalog, key, store, journal); err != nil {
		t.Fatalf("decrypt restore: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "notes", "Note.md"))
	if err != nil || string(content) != "decrypt-me-31p13\n" {
		t.Fatalf("decrypted restore = %q, err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "notes", "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext remains after decrypt restore: %v", err)
	}
	updated, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := catalogNote(updated, "notes/Note.md"); found {
		t.Fatal("decrypted note remains in the encryption catalog")
	}
}

func TestProtectedTrashRestoreCollisionPreservesTrashAndDestination(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x69}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "original-protected-copy-31p13\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := repository.MoveToTrashWithCatalog("private/Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private", "Note.md"), []byte("new-destination-copy-31p13\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, MoveProtectionKeep, updated, key, store, journal); !errors.Is(err, ErrRestoreCollision) {
		t.Fatalf("restore over an existing destination returned %v", err)
	}
	destination, err := os.ReadFile(filepath.Join(root, "private", "Note.md"))
	if err != nil || string(destination) != "new-destination-copy-31p13\n" {
		t.Fatalf("existing destination was changed: %q err=%v", destination, err)
	}
	trashCiphertext := filepath.Join(root, ".trash", item.ID, "content", "item.age")
	if ciphertext, err := os.ReadFile(trashCiphertext); err != nil || bytes.Contains(ciphertext, []byte("original-protected-copy-31p13")) {
		t.Fatalf("protected Trash copy was lost or exposed: err=%v", err)
	}
}

func TestProtectedFolderTrashAndRestorePreservesRecursivePolicy(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "private", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x63}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/nested/Note.md", "nested-protected-31p13\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := repository.MoveToTrashWithCatalog("private", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("move protected folder to Trash: %v", err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := repository.TrashItemsWithCatalog(catalog, key, journal)
	if err != nil || len(listed) != 1 || !listed[0].ProtectionChoiceRequired {
		t.Fatalf("folder Trash listing = %#v, err=%v", listed, err)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, "", catalog, key, store, journal); !errors.Is(err, ErrMoveProtectionChoiceRequired) {
		t.Fatalf("restore without keep/decrypt choice = %v", err)
	}
	if entries, readErr := os.ReadDir(filepath.Join(root, "private")); readErr == nil {
		t.Fatalf("empty source folder remains before restore: %v", entries)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, MoveProtectionKeep, catalog, key, store, journal); err != nil {
		t.Fatalf("restore protected folder: %v", err)
	}
	updated, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if protected, err := updated.IsPathProtected("private/new/Note.md"); err != nil || !protected {
		t.Fatalf("recursive protection was not restored: protected=%v err=%v", protected, err)
	}
	markdown, protected, err := repository.ReadMarkdownWithCatalog("private/nested/Note.md", updated, key)
	if err != nil || !protected || markdown.Content != "nested-protected-31p13\n" {
		t.Fatalf("restored folder note = %#v, protected=%v, err=%v", markdown, protected, err)
	}
}

func TestProtectedFolderTrashPreservesSharedAttachmentAtOriginalPath(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "private", "Note.assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x67}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "![shared](Note.assets/shared.png)\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	assetOp, _, err := repository.PrepareAssetUploadWithCatalog("private/Note.md", testPNG, catalog, key, store, journal)
	if err != nil || assetOp.Commit(nil) != nil {
		t.Fatalf("create shared protected asset: operation=%v err=%v", assetOp, err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	var assetPath string
	for _, object := range catalog.Objects {
		if object.Kind == encryption.CatalogObjectAsset {
			assetPath = object.LogicalPath
		}
	}
	if assetPath == "" {
		t.Fatal("shared attachment is absent from the catalog")
	}
	name := filepath.Base(assetPath)
	mustWrite(t, filepath.Join(root, "Reference.md"), "![shared](private/Note.assets/"+name+")\n")
	item, err := repository.MoveToTrashWithCatalog("private", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("move folder with shared attachment to Trash: %v", err)
	}
	sharedCiphertext := filepath.Join(root, filepath.FromSlash(assetPath+".age"))
	if _, err := os.Stat(sharedCiphertext); err != nil {
		t.Fatalf("shared attachment left its logical path: %v", err)
	}
	updated, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreTrashItemWithCatalog(item.ID, MoveProtectionKeep, updated, key, store, journal); err != nil {
		t.Fatalf("restore folder around retained shared attachment: %v", err)
	}
	updated, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, protected, err := repository.ReadAssetWithCatalog("Reference.md", "private/Note.assets/"+name, updated, key)
	if err != nil || !protected || !bytes.Equal(loaded.Content, testPNG) {
		t.Fatalf("shared attachment reference after restore: protected=%v err=%v", protected, err)
	}
	clear(loaded.Content)
}

func TestProtectedTrashRetainsSharedAttachmentAndBlocksPermanentDeleteOfReferencedTrashAsset(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x64}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "![image](Note.assets/image.png)\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	assetOp, protected, err := repository.PrepareAssetUploadWithCatalog("private/Note.md", testPNG, catalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected asset: protected=%v err=%v", protected, err)
	}
	if err := assetOp.Commit(nil); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	var assetPath string
	for _, object := range catalog.Objects {
		if object.Kind == encryption.CatalogObjectAsset {
			assetPath = object.LogicalPath
		}
	}
	if assetPath == "" {
		t.Fatal("uploaded attachment is missing from the catalog")
	}
	name := filepath.Base(assetPath)
	mustWrite(t, filepath.Join(root, "Reference.md"), "![shared](private/Note.assets/"+name+")\n")
	item, err := repository.MoveToTrashWithCatalog("private/Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatalf("move note with shared attachment to Trash: %v", err)
	}
	assetCiphertext := filepath.Join(root, filepath.FromSlash(assetPath+".age"))
	if _, err := os.Stat(assetCiphertext); err != nil {
		t.Fatalf("shared attachment did not remain at its logical path: %v", err)
	}
	updated, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, isProtected, err := repository.ReadAssetWithCatalog("Reference.md", "private/Note.assets/"+name, updated, key)
	if err != nil || !isProtected || !bytes.Equal(loaded.Content, testPNG) {
		t.Fatalf("external shared reference stopped working: protected=%v err=%v", isProtected, err)
	}
	clear(loaded.Content)
	if _, err := repository.DeleteTrashItemWithCatalog(item.ID, updated, key, store, journal); err != nil {
		t.Fatalf("delete note while retaining referenced attachment: %v", err)
	}
	if _, err := os.Stat(assetCiphertext); err != nil {
		t.Fatalf("permanent note delete removed a shared attachment: %v", err)
	}
}

func TestProtectedTrashPermanentDeleteBlocksReferencedTrashAttachment(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "private", "Note.assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "private", "Note.md"), "![shared](Note.assets/image.png)\n")
	mustWrite(t, filepath.Join(root, "private", "Note.assets", "image.png"), string(testPNG))
	repository := mustRepository(t, root)
	item, err := repository.MoveToTrash("private/Note.md")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "Reference.md"), "![shared](private/Note.assets/image.png)\n")
	key := bytes.Repeat([]byte{0x65}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	conversion, err := repository.PrepareExistingConversion(t.Context(), "private/Note.md", catalog, key, store, journal)
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
	if _, err := repository.DeleteTrashItemWithCatalog(item.ID, catalog, key, store, journal); !errors.Is(err, ErrTrashReferencedAsset) {
		t.Fatalf("permanent delete with a shared attachment = %v", err)
	}
	items, err := repository.TrashItemsWithCatalog(catalog, key, journal)
	if err != nil || len(items) != 1 {
		t.Fatalf("referenced protected Trash was lost: %#v err=%v", items, err)
	}
}

func TestProtectedTrashCommitRecoversForwardAndFinalizesStagedMetadata(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x66}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "recover-forward-31p13\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	trashRoot := filepath.Join(root, trashDirectoryName)
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pendingRoot := filepath.Join(trashRoot, ".pending-"+id)
	finalContent := filepath.Join(trashRoot, id, trashContentName)
	if err := os.MkdirAll(pendingRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(finalContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := repository.writeTrashMetadata(trashDirectoryName+"/.pending-"+id, trashMetadata{
		OriginalPath: "private/Note.md", Type: "file", DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	oldCiphertext := filepath.Join(root, "private", "Note.md.age")
	ciphertext, err := os.ReadFile(oldCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	logicalTarget := filepath.ToSlash(filepath.Join(trashDirectoryName, id, trashContentName, "item"))
	newCiphertext := filepath.Join(root, filepath.FromSlash(logicalTarget+".age"))
	if err := os.WriteFile(newCiphertext, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	for index := range catalog.Objects {
		if catalog.Objects[index].LogicalPath == "private/Note.md" {
			catalog.Objects[index].LogicalPath = logicalTarget
			catalog.Objects[index].PhysicalPath = logicalTarget + ".age"
		}
	}
	if err := store.Save(catalog, key); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldCiphertext); err != nil {
		t.Fatal(err)
	}
	items, err := repository.TrashItemsWithCatalog(catalog, key, journal)
	if err != nil || len(items) != 1 || !items[0].Protected {
		t.Fatalf("reconciled Trash item = %#v err=%v", items, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".trash", id, trashMetadataName)); err != nil {
		t.Fatalf("recovery did not publish Trash metadata: %v", err)
	}
	if _, err := os.Stat(newCiphertext); err != nil {
		t.Fatalf("recovery did not retain Trash ciphertext: %v", err)
	}

	// A prepared but uncommitted operation has no destination catalog objects;
	// its pending metadata and empty target directories are discarded on restart.
	staleID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stalePending := filepath.Join(trashRoot, ".pending-"+staleID)
	staleContent := filepath.Join(trashRoot, staleID, trashContentName)
	if err := os.MkdirAll(stalePending, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staleContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := repository.writeTrashMetadata(trashDirectoryName+"/.pending-"+staleID, trashMetadata{
		OriginalPath: "private/Uncommitted.md", Type: "file", DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TrashItemsWithCatalog(catalog, key, journal); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stalePending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncommitted pending Trash record was not discarded: %v", err)
	}

	// A crash can happen after the two staging directories are created but
	// before the metadata file is written. No payload is moved before metadata.
	missingMetadataID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	missingMetadataPending := filepath.Join(trashRoot, ".pending-"+missingMetadataID)
	missingMetadataFinal := filepath.Join(trashRoot, missingMetadataID)
	if err := os.MkdirAll(missingMetadataPending, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(missingMetadataFinal, trashContentName, "item", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TrashItemsWithCatalog(catalog, key, journal); err != nil {
		t.Fatalf("reconcile metadata-less pending Trash: %v", err)
	}
	for _, stalePath := range []string{missingMetadataPending, missingMetadataFinal} {
		if _, err := os.Lstat(stalePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("metadata-less staging path was not discarded: %s: %v", stalePath, err)
		}
	}
}

func TestProtectedTrashReconcilesCompletedAndAmbiguousRoots(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x68}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}

	completedID := "cccccccccccccccccccccccccccccccc"
	completedRoot := filepath.Join(root, ".trash", completedID)
	if err := os.MkdirAll(filepath.Join(completedRoot, "content", "item", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if items, err := repository.TrashItemsWithCatalog(catalog, key, journal); err != nil || len(items) != 0 {
		t.Fatalf("listing after a completed Trash operation = %#v err=%v", items, err)
	}
	if _, err := os.Stat(completedRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed empty Trash root was not cleaned: %v", err)
	}

	ambiguousID := "dddddddddddddddddddddddddddddddd"
	ambiguousRoot := filepath.Join(root, ".trash", ambiguousID)
	ambiguousFile := filepath.Join(ambiguousRoot, "content", "item", "retained.bin")
	if err := os.MkdirAll(filepath.Dir(ambiguousFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ambiguousFile, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.TrashItemsWithCatalog(catalog, key, journal); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("listing with ambiguous metadata-less Trash returned %v", err)
	}
	if contents, err := os.ReadFile(ambiguousFile); err != nil || string(contents) != "preserve me" {
		t.Fatalf("ambiguous Trash data was not preserved: %q err=%v", contents, err)
	}
}

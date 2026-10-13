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

func TestProtectedFolderMoveKeepsPolicyAndUpdatesProtectedLinks(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "Index.md"), "[A](private/A.md)\n")
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x7a}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/A.md", "[B](B.md)\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/B.md", "# B\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}

	preview, err := repository.PreviewMoveLinksWithCatalog("private", "archive/private", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rewrites) != 1 || preview.Rewrites[0].NotePath != "Index.md" {
		t.Fatalf("unexpected protected folder preview: %#v", preview.Rewrites)
	}
	if _, err := repository.MoveWithCatalog("private", "archive/private", preview.Token, MoveProtectionKeep, catalog, key, store, journal); err != nil {
		t.Fatalf("move protected folder: %v", err)
	}
	updatedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	for _, notePath := range []string{"archive/private/A.md", "archive/private/B.md"} {
		markdown, protected, readErr := repository.ReadMarkdownWithCatalog(notePath, updatedCatalog, key)
		if readErr != nil || !protected || markdown.Content == "" {
			t.Fatalf("moved protected note %q: protected=%v markdown=%#v err=%v", notePath, protected, markdown, readErr)
		}
		if _, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(notePath))); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("plaintext moved note %q exists: %v", notePath, statErr)
		}
	}
	if protected, err := updatedCatalog.IsPathProtected("archive/private/New.md"); err != nil || !protected {
		t.Fatalf("recursive protection did not follow the folder: protected=%v err=%v", protected, err)
	}
	if protected, err := updatedCatalog.IsPathProtected("private/New.md"); err != nil || protected {
		t.Fatalf("old folder policy remained after move: protected=%v err=%v", protected, err)
	}
	assertFileContains(t, filepath.Join(root, "Index.md"), "[A](archive/private/A.md)")
	ciphertext, err := os.ReadFile(filepath.Join(root, "archive", "private", "A.md.age"))
	if err != nil || bytes.Contains(ciphertext, []byte("[B](B.md)")) {
		t.Fatalf("moved protected note was not ciphertext: err=%v", err)
	}
}

func TestProtectedMoveRequiresChoiceAndDecryptsOnlyOutsideProtectedFolders(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	for _, directory := range []string{"private", "public", "also-private"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x35}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}, {Path: "also-private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "# Plaintext sentinel\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := repository.PreviewMoveLinksWithCatalog("private/Note.md", "public/Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveWithCatalog("private/Note.md", "public/Note.md", preview.Token, "", catalog, key, store, journal); !errors.Is(err, ErrMoveProtectionChoiceRequired) {
		t.Fatalf("missing protection choice error = %v", err)
	}
	protectedTargetPreview, err := repository.PreviewMoveLinksWithCatalog("private/Note.md", "also-private/Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveWithCatalog("private/Note.md", "also-private/Note.md", protectedTargetPreview.Token, MoveProtectionDecrypt, catalog, key, store, journal); !errors.Is(err, ErrMoveDecryptIntoProtected) {
		t.Fatalf("decrypt into a protected folder error = %v", err)
	}
	if _, err := repository.MoveWithCatalog("private/Note.md", "public/Note.md", preview.Token, MoveProtectionDecrypt, catalog, key, store, journal); err != nil {
		t.Fatalf("decrypt move: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "public", "Note.md")); err != nil || string(content) != "# Plaintext sentinel\n" {
		t.Fatalf("decrypted destination content = %q, err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "public", "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext remained after decrypt move: %v", err)
	}
	updatedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := catalogNote(updatedCatalog, "public/Note.md"); found {
		t.Fatal("decrypted note remained in the encryption catalog")
	}
	if _, err := repository.ReadMarkdown("public/Note.md"); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedMoveRejectsStalePreviewWithoutMovingSource(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "Index.md"), "[Note](private/Note.md)\n")
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x22}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "# Before\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := repository.PreviewMoveLinksWithCatalog("private", "archive/private", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	note, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	write, _, err := repository.PrepareMarkdownWriteWithCatalog("private/Note.md", "# After\n", note.Version, catalog, key, store, journal)
	if err != nil || write.Commit(nil) != nil {
		t.Fatalf("change note after preview: operation=%v err=%v", write, err)
	}
	latest, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveWithCatalog("private", "archive/private", preview.Token, MoveProtectionKeep, latest, key, store, journal); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale protected preview error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "private", "Note.md.age")); err != nil {
		t.Fatalf("stale preview moved the source: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "archive", "private")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale preview created destination: %v", err)
	}
}

func TestMoveIntoProtectedFolderEncryptsExistingPlaintextAutomatically(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "Plain.md"), "# Keep me protected\n")
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x68}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	preview, err := repository.PreviewMoveLinksWithCatalog("Plain.md", "private/Plain.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveWithCatalog("Plain.md", "private/Plain.md", preview.Token, "", catalog, key, store, journal); err != nil {
		t.Fatalf("move into protected folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "Plain.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext source remained after protected move: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "private", "Plain.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext destination exists after protected move: %v", err)
	}
	updatedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	object, found := catalogNote(updatedCatalog, "private/Plain.md")
	if !found || !object.Protected {
		t.Fatalf("moved note was not protected in the catalog: %#v found=%v", object, found)
	}
	markdown, protected, err := repository.ReadMarkdownWithCatalog("private/Plain.md", updatedCatalog, key)
	if err != nil || !protected || markdown.Content != "# Keep me protected\n" {
		t.Fatalf("read moved protected note: markdown=%#v protected=%v err=%v", markdown, protected, err)
	}
}

func TestProtectedMoveRewritesSeveralEncryptedInboundLinksTogether(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x11}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/A.md", "[Target](Target.md#part)\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/B.md", "[Target](Target.md)\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Target.md", "# Target\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := repository.PreviewMoveLinksWithCatalog("private/Target.md", "archive/Target.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rewrites) != 2 {
		t.Fatalf("preview did not include both encrypted backlinks: %#v", preview.Rewrites)
	}
	if _, err := repository.MoveWithCatalog("private/Target.md", "archive/Target.md", preview.Token, MoveProtectionDecrypt, catalog, key, store, journal); err != nil {
		t.Fatalf("move with encrypted backlink updates: %v", err)
	}
	updatedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	for _, notePath := range []string{"private/A.md", "private/B.md"} {
		markdown, _, readErr := repository.ReadMarkdownWithCatalog(notePath, updatedCatalog, key)
		if readErr != nil || !strings.Contains(markdown.Content, "../archive/Target.md") {
			t.Fatalf("encrypted backlink in %q was not updated atomically: markdown=%q err=%v", notePath, markdown.Content, readErr)
		}
	}
	if content, err := os.ReadFile(filepath.Join(root, "archive", "Target.md")); err != nil || string(content) != "# Target\n" {
		t.Fatalf("decrypted target content = %q, err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "archive", "Target.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext remained after decrypt move: %v", err)
	}
	if _, found := catalogNote(updatedCatalog, "archive/Target.md"); found {
		t.Fatal("decrypted target remained in the encryption catalog")
	}
}

func TestProtectedMoveRollsBackFilesWhenEncryptedLinkTransactionCannotPrepare(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	for _, directory := range []string{"private", "refs", "archive"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(root, "Index.md"), "[Target](private/Target.md)\n")
	otherVault := t.TempDir()
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x31}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}, {Path: "refs"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Target.md", "# Target\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	addProtectedTestNote(t, repository, store, journal, catalog, key, "refs/Reference.md", "[Target](../private/Target.md)\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := repository.PreviewMoveLinksWithCatalog("private", "archive/private", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	wrongJournal, err := encryption.NewTransactionJournal(otherVault, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveWithCatalog("private", "archive/private", preview.Token, MoveProtectionKeep, catalog, key, store, wrongJournal); err == nil {
		t.Fatal("move succeeded without a valid encrypted-link transaction root")
	}
	if _, err := os.Stat(filepath.Join(root, "private", "Target.md.age")); err != nil {
		t.Fatalf("failed move did not restore protected source: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "archive", "private")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed move left destination behind: %v", err)
	}
	assertFileContains(t, filepath.Join(root, "Index.md"), "[Target](private/Target.md)")
	updatedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	reference, _, err := repository.ReadMarkdownWithCatalog("refs/Reference.md", updatedCatalog, key)
	if err != nil || reference.Content != "[Target](../private/Target.md)\n" {
		t.Fatalf("failed move partially rewrote encrypted backlink: content=%q err=%v", reference.Content, err)
	}
}

func TestDecryptMoveKeepsSharedAssetEncryptedAndRewritesProtectedReferences(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	for _, directory := range []string{"private", "refs", "public"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x71}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}, {Path: "refs"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "# Note\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	upload, protected, err := repository.PrepareAssetUploadWithCatalog("private/Note.md", testPNG, catalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected shared asset: protected=%v err=%v", protected, err)
	}
	if err := upload.Commit(nil); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	note, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	write, _, err := repository.PrepareMarkdownWriteWithCatalog("private/Note.md", "![shared]("+upload.Asset.RelativePath+")\n", note.Version, catalog, key, store, journal)
	if err != nil || write.Commit(nil) != nil {
		t.Fatalf("reference uploaded protected asset: operation=%v err=%v", write, err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	addProtectedTestNote(t, repository, store, journal, catalog, key, "refs/Reference.md", "![shared](../private/"+upload.Asset.RelativePath+")\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := repository.PreviewMoveLinksWithCatalog("private/Note.md", "public/Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rewrites) != 1 || preview.Rewrites[0].NotePath != "refs/Reference.md" {
		t.Fatalf("shared asset link was not included in move preview: %#v", preview.Rewrites)
	}
	if _, err := repository.MoveWithCatalog("private/Note.md", "public/Note.md", preview.Token, MoveProtectionDecrypt, catalog, key, store, journal); err != nil {
		t.Fatalf("decrypt note move with shared asset: %v", err)
	}
	updatedCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	reference, _, err := repository.ReadMarkdownWithCatalog("refs/Reference.md", updatedCatalog, key)
	expectedReference := "![shared](../public/" + upload.Asset.RelativePath + ")\n"
	if err != nil || reference.Content != expectedReference {
		t.Fatalf("protected shared-asset reference = %q, err=%v", reference.Content, err)
	}
	asset, protected, err := repository.ReadAssetWithCatalog("public/Note.md", upload.Asset.RelativePath, updatedCatalog, key)
	if err != nil || !protected || !bytes.Equal(asset.Content, testPNG) {
		t.Fatalf("shared asset did not remain protected after decrypting one note: protected=%v err=%v catalog=%#v", protected, err, updatedCatalog.Objects)
	}
	clear(asset.Content)
	assetName := strings.TrimPrefix(upload.Asset.RelativePath, "Note.assets/")
	if _, err := os.Lstat(filepath.Join(root, "public", "Note.assets", assetName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared asset acquired a plaintext copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "public", "Note.assets", assetName+".age")); err != nil {
		t.Fatalf("shared asset ciphertext did not follow the moved note: %v", err)
	}
}

func addProtectedTestNote(t *testing.T, repository *Repository, store *encryption.CatalogStore, journal *encryption.TransactionJournal, catalog encryption.Catalog, key []byte, notePath, content string) {
	t.Helper()
	create, _, err := repository.PrepareMarkdownCreateWithCatalog(notePath, catalog, key, store, journal)
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
	created, _, err := repository.ReadMarkdownWithCatalog(notePath, catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	write, _, err := repository.PrepareMarkdownWriteWithCatalog(notePath, content, created.Version, catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Commit(nil); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedMovePreviewDoesNotIncludePlaintextInToken(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "archive"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x53}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Secret.md", "unique-secret-text-4e7\n")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := repository.PreviewMoveLinksWithCatalog("private", "archive/private", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview.Token, "unique-secret-text-4e7") {
		t.Fatal("move token exposed plaintext")
	}
}

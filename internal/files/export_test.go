package files

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestWriteArchiveIncludesVaultFilesAndSkipsPrivateOrUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write := func(relative, contents string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Welcome.md", "# ordinary Markdown")
	write("Welcome.assets/image.png", "ordinary asset bytes")
	write(".git/config", "legacy remote credentials")
	write(".trash/deleted.md", "deleted note")
	write("node_modules/private.md", "dependency data")
	write("Nested/Page.md", "nested note")

	outsideSecret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(outsideSecret, []byte("outside secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSecret, filepath.Join(root, "linked-secret.md")); err != nil {
		t.Fatal(err)
	}

	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateArchive(); err != nil {
		t.Fatalf("validate archive: %v", err)
	}
	var archive bytes.Buffer
	if err := repository.WriteArchive(&archive); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}

	files := make(map[string]string, len(reader.File))
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			t.Fatalf("open %q: %v", entry.Name, err)
		}
		contents, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read %q: %v", entry.Name, errors.Join(readErr, closeErr))
		}
		files[entry.Name] = string(contents)
	}

	want := map[string]string{
		"Welcome.md":               "# ordinary Markdown",
		"Welcome.assets/image.png": "ordinary asset bytes",
		"Nested/Page.md":           "nested note",
	}
	if len(files) != len(want) {
		t.Fatalf("archive contains unexpected entries: %#v", files)
	}
	for path, contents := range want {
		if files[path] != contents {
			t.Errorf("archive entry %q = %q, want %q", path, files[path], contents)
		}
	}
}

func TestProtectedArchiveUsesLogicalPathsAndRejectsChangedOrCorruptSnapshot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "vault")
	metadata := filepath.Join(base, "metadata")
	if err := os.MkdirAll(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Public.md"), []byte("public bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x58}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	create, protected, err := repository.PrepareMarkdownCreateWithCatalog("private/Note.md", catalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected note: protected=%t err=%v", protected, err)
	}
	if err := create.Commit(nil); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	asset, protected, err := repository.PrepareAssetUploadWithCatalog("private/Note.md", testPNG, catalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected attachment: protected=%t err=%v", protected, err)
	}
	if err := asset.Commit(nil); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := repository.InspectArchiveWithCatalog(catalog, key)
	if err != nil || !snapshot.ContainsProtected || snapshot.Fingerprint == "" {
		t.Fatalf("protected export snapshot=%#v err=%v", snapshot, err)
	}
	var archive bytes.Buffer
	if err := repository.WriteArchiveWithCatalog(&archive, catalog, key, snapshot.Fingerprint, nil); err != nil {
		t.Fatalf("write protected archive: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatalf("read protected archive: %v", err)
	}
	contents := make(map[string][]byte)
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			t.Fatalf("open archive item %q: %v", entry.Name, err)
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			t.Fatalf("read archive item %q: %v", entry.Name, err)
		}
		contents[entry.Name] = data
	}
	note, hasNote := contents["private/Note.md"]
	if !hasNote || string(note) != "# Note\n" {
		t.Fatalf("protected note export = %q, want the logical note contents", note)
	}
	assetArchivePath := filepath.ToSlash(filepath.Join("private", asset.Asset.RelativePath))
	if !bytes.Equal(contents[assetArchivePath], testPNG) {
		t.Fatalf("protected attachment %q was not exported at its logical path", assetArchivePath)
	}
	if string(contents["Public.md"]) != "public bytes" || len(contents) != 3 {
		t.Fatalf("archive paths or public content differ: %#v", contents)
	}
	for archivedPath := range contents {
		if strings.HasSuffix(archivedPath, ".age") || strings.Contains(archivedPath, ".trash/") {
			t.Fatalf("archive exposed a physical or Trash path: %q", archivedPath)
		}
	}

	if err := os.WriteFile(filepath.Join(root, "Public.md"), []byte("changed bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	var changedOutput bytes.Buffer
	if err := repository.WriteArchiveWithCatalog(&changedOutput, catalog, key, snapshot.Fingerprint, nil); !errors.Is(err, ErrExportSnapshotChanged) {
		t.Fatalf("changed snapshot error=%v", err)
	}
	if changedOutput.Len() != 0 {
		t.Fatal("changed snapshot wrote archive bytes before rejecting the export")
	}

	protectedObject := catalog.Objects[0]
	ciphertextPath := filepath.Join(root, filepath.FromSlash(protectedObject.PhysicalPath))
	ciphertext, err := os.ReadFile(ciphertextPath)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xff
	if err := os.WriteFile(ciphertextPath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	var corruptOutput bytes.Buffer
	if err := repository.WriteArchiveWithCatalog(&corruptOutput, catalog, key, "", nil); !errors.Is(err, ErrProtectedContentCorrupt) {
		t.Fatalf("corrupt protected export error=%v", err)
	}
	if corruptOutput.Len() != 0 {
		t.Fatal("corrupt protected content was detected after archive streaming started")
	}
}

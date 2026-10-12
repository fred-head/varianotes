package files

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
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

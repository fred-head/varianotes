package files

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

func TestSaveAndReadAsset(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Network", "BGP.md"), "# BGP")
	repository := mustRepository(t, root)

	saved, err := repository.SaveAsset("Network/BGP.md", bytes.NewReader(testPNG))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.RelativePath, "BGP.assets/") || !strings.HasSuffix(saved.RelativePath, ".png") {
		t.Fatalf("unexpected relative asset path: %q", saved.RelativePath)
	}
	loaded, err := repository.ReadAsset("Network/BGP.md", saved.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContentType != "image/png" || !bytes.Equal(loaded.Content, testPNG) {
		t.Fatalf("unexpected loaded asset: %#v", loaded)
	}
}

func TestSaveAssetRejectsUnsupportedAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	repository := mustRepository(t, root)

	if _, err := repository.SaveAsset("Note.md", strings.NewReader("not an image")); !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("expected unsupported media rejection, got %v", err)
	}
	tooLarge := io.LimitReader(zeroReader{}, maxAssetSize+1)
	if _, err := repository.SaveAsset("Note.md", tooLarge); !errors.Is(err, ErrAssetTooLarge) {
		t.Fatalf("expected oversized asset rejection, got %v", err)
	}
}

func TestReadAssetRejectsWrongOwnershipAndTraversal(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	mustWrite(t, filepath.Join(root, "Other.assets", "image.png"), string(testPNG))
	repository := mustRepository(t, root)

	for _, unsafe := range []string{"Other.assets/image.png", "../image.png", "/image.png", `Note.assets\image.png`} {
		if _, err := repository.ReadAsset("Note.md", unsafe); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("expected %q to be rejected, got %v", unsafe, err)
		}
	}
}

func TestSaveAssetRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "Note.assets")); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	if _, err := repository.SaveAsset("Note.md", bytes.NewReader(testPNG)); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected symlink asset directory rejection, got %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

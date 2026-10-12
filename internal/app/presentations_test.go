package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImagePresentationsPersistAndFollowMoves(t *testing.T) {
	directory := t.TempDir()
	store := newImagePresentationStore(filepath.Join(directory, "notebooks.json"))
	if err := store.set("private", "Network/BGP.md", "BGP.assets/topology.png", presentationMedium, ""); err != nil {
		t.Fatal(err)
	}
	restarted := newImagePresentationStore(filepath.Join(directory, "notebooks.json"))
	values, err := restarted.list("private", "Network/BGP.md")
	if err != nil || values["BGP.assets/topology.png"] != presentationMedium {
		t.Fatalf("persisted presentation = %#v, %v", values, err)
	}
	if err := restarted.move("private", "Network/BGP.md", "Archive/Routing.md"); err != nil {
		t.Fatal(err)
	}
	values, err = restarted.list("private", "Archive/Routing.md")
	if err != nil || values["Routing.assets/topology.png"] != presentationMedium {
		t.Fatalf("renamed presentation = %#v, %v", values, err)
	}
	if err := restarted.move("private", "Archive", "Reference"); err != nil {
		t.Fatal(err)
	}
	values, err = restarted.list("private", "Reference/Routing.md")
	if err != nil || values["Routing.assets/topology.png"] != presentationMedium {
		t.Fatalf("folder-moved presentation = %#v, %v", values, err)
	}
	if err := restarted.deletePath("private", "Reference"); err != nil {
		t.Fatal(err)
	}
	values, err = restarted.list("private", "Reference/Routing.md")
	if err != nil || len(values) != 0 {
		t.Fatalf("deleted presentation = %#v, %v", values, err)
	}
}

func TestImagePresentationsRejectInvalidSizes(t *testing.T) {
	store := newImagePresentationStore("")
	if err := store.set("private", "Note.md", "Note.assets/image.png", "123px", ""); err != errInvalidPresentationSize {
		t.Fatalf("invalid size error = %v", err)
	}
}

func TestImagePresentationsRejectOversizedMetadata(t *testing.T) {
	directory := t.TempDir()
	metadataPath := filepath.Join(directory, "image-presentations.json")
	if err := os.WriteFile(metadataPath, []byte(strings.Repeat("x", maximumPresentationMetadataSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newImagePresentationStore(filepath.Join(directory, "notebooks.json"))
	if _, err := store.list("private", "Note.md"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized metadata error = %v", err)
	}
}

func TestImagePresentationsRejectMetadataSymlinks(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"records":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "image-presentations.json")); err != nil {
		t.Fatal(err)
	}
	store := newImagePresentationStore(filepath.Join(directory, "notebooks.json"))
	if _, err := store.list("private", "Note.md"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink metadata error = %v", err)
	}
}

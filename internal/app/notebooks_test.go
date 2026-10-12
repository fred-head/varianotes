package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveManagedNotebookDirectoryRejectsTraversalID(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "notebooks")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	removeManagedNotebookDirectory(base, "..", root)

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("traversal ID removed data outside the managed root: %v", err)
	}
}

func TestRemoveManagedNotebookDirectoryRemovesExactManagedChild(t *testing.T) {
	base := filepath.Join(t.TempDir(), "notebooks")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	notebook := filepath.Join(base, id)
	if err := os.Mkdir(notebook, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(notebook, "note.md")
	if err := os.WriteFile(marker, []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}

	removeManagedNotebookDirectory(base, id, notebook)

	if _, err := os.Stat(notebook); !os.IsNotExist(err) {
		t.Fatalf("managed notebook still exists: %v", err)
	}
}

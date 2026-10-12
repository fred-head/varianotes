package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryTreeAndRead(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Welcome.md"), "# Welcome")
	mustWrite(t, filepath.Join(root, "Notes", "BGP.md"), "# BGP")
	mustWrite(t, filepath.Join(root, "Notes", "ignore.txt"), "ignored")
	mustWrite(t, filepath.Join(root, ".git", "config"), "ignored")
	mustWrite(t, filepath.Join(root, "node_modules", "dependency", "README.md"), "ignored")
	mustWrite(t, filepath.Join(root, "Welcome.assets", "README.md"), "ignored")

	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repository.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 || tree[0].Name != "Notes" || tree[1].Name != "Welcome.md" {
		t.Fatalf("unexpected tree: %#v", tree)
	}
	markdown, err := repository.ReadMarkdown("Notes/BGP.md")
	if err != nil {
		t.Fatal(err)
	}
	if markdown.Content != "# BGP" || markdown.Version == "" {
		t.Fatalf("unexpected Markdown: %#v", markdown)
	}
}

func TestRepositorySearchesNamesFoldersAndMarkdownContent(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Network", "BGP.md"), "# Routing\nConfigure the neighbor carefully.\nAnother neighbor line.")
	mustWrite(t, filepath.Join(root, "Network", "ignore.txt"), "neighbor")
	mustWrite(t, filepath.Join(root, "BGP.assets", "hidden.md"), "neighbor")
	mustWrite(t, filepath.Join(root, ".git", "hidden.md"), "neighbor")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}

	content, err := repository.Search("neighbor")
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 || content[0].Path != "Network/BGP.md" || content[0].Type != "content" || content[0].Line != 2 {
		t.Fatalf("unexpected content results: %#v", content)
	}

	names, err := repository.Search("network")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0].Type != "directory" {
		t.Fatalf("unexpected name results: %#v", names)
	}
}

func TestWriteMarkdownAtomicallyAndDetectsConflict(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "note.md")
	mustWrite(t, filePath, "before")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.ReadMarkdown("note.md")
	if err != nil {
		t.Fatal(err)
	}
	written, err := repository.WriteMarkdown("note.md", "after", loaded.Version)
	if err != nil {
		t.Fatal(err)
	}
	if written.Content != "after" || written.Version == loaded.Version {
		t.Fatalf("unexpected write result: %#v", written)
	}
	if _, err := repository.WriteMarkdown("note.md", "stale", loaded.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "after" {
		t.Fatalf("conflicting write changed file: %q", content)
	}
}

func TestReadMarkdownRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "note.md"), "safe")
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, unsafe := range []string{"../note.md", "/tmp/note.md", "./note.md", `folder\note.md`, "line\nbreak.md", "tab\tname.md", "note.txt"} {
		_, err := repository.ReadMarkdown(unsafe)
		if !errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrNotMarkdown) {
			t.Errorf("expected %q to be rejected, got %v", unsafe, err)
		}
	}
}

func TestReadMarkdownRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.md")
	mustWrite(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "secret.md")); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repository.ReadMarkdown("secret.md"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected symlink escape rejection, got %v", err)
	}
}

func mustWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

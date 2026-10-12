package files

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConfinedRegularFileRejectsSymlinkEscape(t *testing.T) {
	repositoryRoot := t.TempDir()
	externalRoot := t.TempDir()
	external := filepath.Join(externalRoot, "secret.md")
	if err := os.WriteFile(external, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(repositoryRoot, "escaped.md")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	root, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, _, err := openConfinedRegularFile(root, "escaped.md", nil, maxMarkdownSize)
	if file != nil {
		_ = file.Close()
	}
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root-scoped open did not reject the symlink escape: %v", err)
	}
}

func TestConfinedRegularFileCannotEscapeDuringSymlinkReplacement(t *testing.T) {
	repositoryRoot := t.TempDir()
	externalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repositoryRoot, "safe.md"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(externalRoot, "secret.md")
	if err := os.WriteFile(external, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repositoryRoot, "candidate.md")
	if err := os.WriteFile(link, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Stat(link)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var replacements sync.WaitGroup
	replacements.Add(1)
	go func() {
		defer replacements.Done()
		for range 1000 {
			_ = os.Remove(link)
			_ = os.Symlink(external, link)
			_ = os.Remove(link)
			_ = os.Symlink("safe.md", link)
		}
	}()
	for range 1000 {
		file, _, openErr := openConfinedRegularFile(root, "candidate.md", expected, maxMarkdownSize)
		if openErr != nil {
			continue
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read confined file: %v, close: %v", readErr, closeErr)
		}
		if string(content) != "safe" {
			t.Fatalf("root-scoped open escaped during replacement: %q", content)
		}
	}
	replacements.Wait()
}

func TestConfinedRegularFileEnforcesTypeAndSize(t *testing.T) {
	repositoryRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repositoryRoot, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "large.md"), []byte("too large"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, _, err := openConfinedRegularFile(root, "folder", nil, maxMarkdownSize); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("directory was accepted as a regular file: %v", err)
	}
	if _, _, err := openConfinedRegularFile(root, "large.md", nil, 1); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized file was accepted: %v", err)
	}
}

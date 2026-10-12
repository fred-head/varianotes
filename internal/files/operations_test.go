package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateNoteAndFolder(t *testing.T) {
	root := t.TempDir()
	repository := mustRepository(t, root)

	if err := repository.Create("Network", "directory"); err != nil {
		t.Fatal(err)
	}
	if err := repository.Create("Network/BGP.md", "file"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "Network", "BGP.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# BGP\n" {
		t.Fatalf("unexpected initial note: %q", content)
	}
	if err := repository.Create("Network/BGP.md", "file"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected existing entry rejection, got %v", err)
	}
}

func TestMoveNoteWithAssetsAndRewriteLink(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Old.md"), "# Note\n\n![](Old.assets/image.png)\n")
	mustWrite(t, filepath.Join(root, "Old.assets", "image.png"), "image")
	repository := mustRepository(t, root)

	if err := repository.Move("Old.md", "Renamed.md"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "Renamed.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "# Note\n\n![](Renamed.assets/image.png)\n" {
		t.Fatalf("asset link was not rewritten: %q", content)
	}
	if _, err := os.Stat(filepath.Join(root, "Renamed.assets", "image.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Old.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old note still exists: %v", err)
	}
}

func TestRenameNoteRewritesCommonMarkAssetLinksWithSpaces(t *testing.T) {
	root := t.TempDir()
	content := "# Note\n\n![one](<Old Note.assets/one.png>)\n\n![two](Old%20Note.assets/two.png)\n"
	mustWrite(t, filepath.Join(root, "Old Note.md"), content)
	mustWrite(t, filepath.Join(root, "Old Note.assets", "one.png"), "one")
	mustWrite(t, filepath.Join(root, "Old Note.assets", "two.png"), "two")
	repository := mustRepository(t, root)

	if err := repository.Move("Old Note.md", "New Note.md"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(filepath.Join(root, "New Note.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Note\n\n![one](<New Note.assets/one.png>)\n\n![two](New%20Note.assets/two.png)\n"
	if string(updated) != want {
		t.Fatalf("asset links with spaces were not rewritten:\n%s", updated)
	}
}

func TestMoveFolderAndRejectMoveIntoItself(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Source", "Note.md"), "note")
	repository := mustRepository(t, root)

	if err := repository.Move("Source", "Source/Nested"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected descendant move rejection, got %v", err)
	}
	if err := repository.Move("Source", "Archive"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Archive", "Note.md")); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteNoteDeletesOwnedAssets(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	mustWrite(t, filepath.Join(root, "Note.assets", "image.png"), "image")
	repository := mustRepository(t, root)

	if err := repository.Delete("Note.md"); err != nil {
		t.Fatal(err)
	}
	for _, deleted := range []string{"Note.md", "Note.assets"} {
		if _, err := os.Stat(filepath.Join(root, deleted)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected %s to be deleted, got %v", deleted, err)
		}
	}
}

func TestDeleteFolderRecursively(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Folder", "Nested", "Note.md"), "note")
	repository := mustRepository(t, root)

	if err := repository.Delete("Folder"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Folder")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected folder to be deleted, got %v", err)
	}
}

func TestDeleteNoteRejectsSymlinkAssetsWithoutDeletingNote(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "Note.assets")); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)

	if err := repository.Delete("Note.md"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected unsafe asset rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Note.md")); err != nil {
		t.Fatalf("note was deleted despite unsafe assets: %v", err)
	}
}

func TestOperationsRejectUnsafePaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Safe.md"), "safe")
	repository := mustRepository(t, root)

	for _, unsafe := range []string{"", ".", "..", "../Outside.md", "/tmp/Outside.md", `Folder\Note.md`, "line\nbreak.md", "tab\tname.md", ".git/config.md", "node_modules/readme.md"} {
		if err := repository.Create(unsafe, "file"); !errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrNotMarkdown) {
			t.Errorf("expected create path %q to be rejected, got %v", unsafe, err)
		}
	}
	if err := repository.Delete("."); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected repository root deletion rejection, got %v", err)
	}
}

func TestOperationsRejectSymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	if err := repository.Create("escape/Note.md", "file"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected symlink parent rejection, got %v", err)
	}
}

func mustRepository(t *testing.T, root string) *Repository {
	t.Helper()
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

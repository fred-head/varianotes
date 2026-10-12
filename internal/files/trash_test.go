package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTrashAndRestoreNoteWithAssets(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Folder", "Note.md"), "# Secret\n\n![](Note.assets/image.png)")
	mustWrite(t, filepath.Join(root, "Folder", "Note.assets", "image.png"), "image")
	repository := mustRepository(t, root)

	item, err := repository.MoveToTrash("Folder/Note.md")
	if err != nil {
		t.Fatal(err)
	}
	if item.OriginalPath != "Folder/Note.md" || item.Type != "file" || item.ID == "" || item.Size == 0 {
		t.Fatalf("unexpected trash item: %#v", item)
	}
	if _, err := os.Stat(filepath.Join(root, ".trash", item.ID, "content", "item")); err != nil {
		t.Fatalf("trash content was not stored under a server-generated path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".trash", item.ID, "content", "Folder", "Note.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("user-provided path leaked into the trash storage layout: %v", err)
	}
	for _, removed := range []string{"Folder/Note.md", "Folder/Note.assets"} {
		if _, err := os.Stat(filepath.Join(root, removed)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s remained in the normal notebook: %v", removed, err)
		}
	}
	tree, err := repository.Tree()
	if err != nil || len(tree) != 1 || tree[0].Name != "Folder" || len(tree[0].Children) != 0 {
		t.Fatalf("trash leaked into the normal tree: %#v, %v", tree, err)
	}
	results, err := repository.Search("Secret")
	if err != nil || len(results) != 0 {
		t.Fatalf("trash leaked into search: %#v, %v", results, err)
	}
	candidates, err := repository.UnreferencedAssets()
	if err != nil || len(candidates) != 0 {
		t.Fatalf("trash leaked into asset cleanup: %#v, %v", candidates, err)
	}

	items, err := repository.TrashItems()
	if err != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("trash listing failed: %#v, %v", items, err)
	}
	if _, err := repository.RestoreTrashItem(item.ID); err != nil {
		t.Fatal(err)
	}
	for _, restored := range []string{"Folder/Note.md", "Folder/Note.assets/image.png"} {
		if _, err := os.Stat(filepath.Join(root, restored)); err != nil {
			t.Fatalf("%s was not restored: %v", restored, err)
		}
	}
	if items, err := repository.TrashItems(); err != nil || len(items) != 0 {
		t.Fatalf("restored item remained in trash: %#v, %v", items, err)
	}
}

func TestTrashRestoresNestedFolderAsOrdinaryFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Projects", "Nested", "Note.md"), "note")
	mustWrite(t, filepath.Join(root, "Projects", "Nested", "Note.assets", "image.png"), "image")
	repository := mustRepository(t, root)
	item, err := repository.MoveToTrash("Projects")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RestoreTrashItem(item.ID); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "Projects", "Nested", "Note.md")); err != nil || string(content) != "note" {
		t.Fatalf("folder content was not restored: %q, %v", content, err)
	}
}

func TestTrashRestoreCollisionPreservesBothCopiesAndPermanentDeleteIsExplicit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "trashed copy")
	repository := mustRepository(t, root)
	item, err := repository.MoveToTrash("Note.md")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "Note.md"), "new copy")
	if _, err := repository.RestoreTrashItem(item.ID); !errors.Is(err, ErrRestoreCollision) {
		t.Fatalf("restore collision was not reported: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "Note.md")); err != nil || string(content) != "new copy" {
		t.Fatalf("existing target was overwritten: %q, %v", content, err)
	}
	if items, err := repository.TrashItems(); err != nil || len(items) != 1 {
		t.Fatalf("colliding trash item was lost: %#v, %v", items, err)
	}
	if _, err := repository.DeleteTrashItem(item.ID); err != nil {
		t.Fatal(err)
	}
	if items, err := repository.TrashItems(); err != nil || len(items) != 0 {
		t.Fatalf("permanently deleted item remains: %#v, %v", items, err)
	}
}

func TestTrashRejectsUnsafeIDsRootsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "safe")
	repository := mustRepository(t, root)
	for _, id := range []string{"", "../escape", "not-a-trash-id", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := repository.RestoreTrashItem(id); !errors.Is(err, ErrInvalidTrashID) {
			t.Errorf("unsafe restore ID %q was accepted: %v", id, err)
		}
		if _, err := repository.DeleteTrashItem(id); !errors.Is(err, ErrInvalidTrashID) {
			t.Errorf("unsafe delete ID %q was accepted: %v", id, err)
		}
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".trash")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MoveToTrash("Note.md"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("symlink trash root was accepted: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "Note.md")); err != nil || string(content) != "safe" {
		t.Fatalf("note changed after unsafe trash rejection: %q, %v", content, err)
	}
}

func TestTrashRejectsSymlinksInsideFoldersWithoutMovingTheFolder(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Folder", "Note.md"), "safe")
	outside := filepath.Join(t.TempDir(), "outside.md")
	mustWrite(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(root, "Folder", "linked.md")); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	if _, err := repository.MoveToTrash("Folder"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("folder containing a symlink was accepted: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "Folder", "Note.md")); err != nil || string(content) != "safe" {
		t.Fatalf("folder changed after unsafe trash rejection: %q, %v", content, err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "outside" {
		t.Fatalf("symlink target changed: %q, %v", content, err)
	}
}

package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnreferencedAssetsHandlesPortableMarkdownPaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Test Note.md"), "![](<Test Note.assets/with space.png>)\n![Shared](Test%20Note.assets/shared.jpg)")
	mustWrite(t, filepath.Join(root, "Other.md"), "[shared]: <Test Note.assets/shared.jpg>")
	mustWrite(t, filepath.Join(root, "Nested", "BGP.md"), "![Topology](BGP.assets/topology.webp)")
	for _, name := range []string{"with space.png", "shared.jpg", "orphan.gif"} {
		mustWrite(t, filepath.Join(root, "Test Note.assets", name), "image")
	}
	mustWrite(t, filepath.Join(root, "Nested", "BGP.assets", "topology.webp"), "image")
	mustWrite(t, filepath.Join(root, "Nested", "BGP.assets", "unused.jpeg"), "image")
	mustWrite(t, filepath.Join(root, "arbitrary.assets", "ignored.png"), "image")

	repository := mustRepository(t, root)
	assets, err := repository.UnreferencedAssets()
	if err != nil {
		t.Fatal(err)
	}
	paths := assetPaths(assets)
	if len(paths) != 2 || !paths["Test Note.assets/orphan.gif"] || !paths["Nested/BGP.assets/unused.jpeg"] {
		t.Fatalf("unexpected unreferenced assets: %#v", assets)
	}
}

func TestUnreferencedAssetsSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	external := t.TempDir()
	mustWrite(t, filepath.Join(external, "outside.png"), "image")
	if err := os.Symlink(external, filepath.Join(root, "Note.assets")); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	assets, err := repository.UnreferencedAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 0 {
		t.Fatalf("symlink assets must not be candidates: %#v", assets)
	}
}

func TestDeleteUnreferencedAssetsKeepsUnselectedAndCleansEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	mustWrite(t, filepath.Join(root, "Note.assets", "first.png"), "image")
	mustWrite(t, filepath.Join(root, "Note.assets", "second.webp"), "image")
	repository := mustRepository(t, root)

	result, err := repository.DeleteUnreferencedAssets([]string{"Note.assets/first.png"})
	if err != nil || len(result.Deleted) != 1 || len(result.Failures) != 0 {
		t.Fatalf("unexpected first cleanup: %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Note.assets", "second.webp")); err != nil {
		t.Fatalf("non-selected asset was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Note.assets")); err != nil {
		t.Fatalf("non-empty directory was removed: %v", err)
	}

	result, err = repository.DeleteUnreferencedAssets([]string{"Note.assets/second.webp"})
	if err != nil || len(result.Deleted) != 1 {
		t.Fatalf("unexpected second cleanup: %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Note.assets")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty assets directory still exists: %v", err)
	}
}

func TestDeleteUnreferencedAssetsRevalidatesReferences(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	mustWrite(t, filepath.Join(root, "Note.assets", "later.png"), "image")
	repository := mustRepository(t, root)
	assets, err := repository.UnreferencedAssets()
	if err != nil || len(assets) != 1 {
		t.Fatalf("expected initial candidate: %#v, %v", assets, err)
	}
	mustWrite(t, filepath.Join(root, "Note.md"), "![](Note.assets/later.png)")

	result, err := repository.DeleteUnreferencedAssets([]string{"Note.assets/later.png"})
	if err != nil || len(result.Deleted) != 0 || len(result.Failures) != 1 {
		t.Fatalf("stale cleanup was not rejected: %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Note.assets", "later.png")); err != nil {
		t.Fatalf("newly referenced asset was removed: %v", err)
	}
}

func TestDeleteUnreferencedAssetsRejectsManipulatedPaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Note.md"), "note")
	repository := mustRepository(t, root)
	for _, unsafe := range []string{"../outside.assets/image.png", "/tmp/Note.assets/image.png", `Note.assets\image.png`, "random/image.png"} {
		if _, err := repository.DeleteUnreferencedAssets([]string{unsafe}); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("expected %q to be rejected, got %v", unsafe, err)
		}
	}
}

func assetPaths(assets []UnreferencedAsset) map[string]bool {
	result := make(map[string]bool, len(assets))
	for _, asset := range assets {
		result[asset.Path] = true
	}
	return result
}

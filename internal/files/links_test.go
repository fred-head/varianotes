package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoteLinksDetectsPortableAndBrokenTargetsConservatively(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Folder", "Current.md"), strings.Join([]string{
		"[Existing](<Other Note.md#section>)",
		"[Broken](Missing%20Note.md)",
		"[External](https://example.com/Other.md)",
		"[Anchor](#section)",
		"![](Current.assets/image.png)",
		"`[Code](Other%20Note.md)`",
		"```",
		"[Fenced](Other%20Note.md)",
		"```",
	}, "\n"))
	mustWrite(t, filepath.Join(root, "Folder", "Other Note.md"), "target")
	repository := mustRepository(t, root)

	links, err := repository.NoteLinks("Folder/Current.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 4 {
		t.Fatalf("unexpected detected links: %#v", links)
	}
	if !links[0].Internal || !links[0].Exists || links[0].TargetPath != "Folder/Other Note.md" {
		t.Fatalf("existing internal link was not resolved: %#v", links[0])
	}
	if !links[1].Internal || links[1].Exists || links[1].TargetPath != "Folder/Missing Note.md" {
		t.Fatalf("broken internal link was not reported: %#v", links[1])
	}
	if links[2].Internal || links[3].Internal {
		t.Fatalf("external URL or anchor was treated as an internal note: %#v", links)
	}
}

func TestMoveWithLinkUpdatesRewritesInboundAndOutboundLinks(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Folder", "A.md"), "[B](B.md)\n[Root](../Root.md)\n![](A.assets/image.png)\n")
	mustWrite(t, filepath.Join(root, "Folder", "A.assets", "image.png"), "image")
	mustWrite(t, filepath.Join(root, "Folder", "B.md"), "[A](A.md)\n")
	mustWrite(t, filepath.Join(root, "Root.md"), "[A](Folder/A.md#top)\n")
	if err := os.Mkdir(filepath.Join(root, "Archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)

	preview, err := repository.PreviewMoveLinks("Folder/A.md", "Archive/Renamed.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rewrites) != 3 || preview.Token == "" {
		t.Fatalf("unexpected rewrite preview: %#v", preview)
	}
	if _, err := repository.MoveWithLinkUpdates(preview.Source, preview.Target, preview.Token); err != nil {
		t.Fatal(err)
	}

	assertFileContains(t, filepath.Join(root, "Folder", "B.md"), "[A](../Archive/Renamed.md)")
	assertFileContains(t, filepath.Join(root, "Root.md"), "[A](Archive/Renamed.md#top)")
	assertFileContains(t, filepath.Join(root, "Archive", "Renamed.md"), "[B](../Folder/B.md)")
	assertFileContains(t, filepath.Join(root, "Archive", "Renamed.md"), "![](Renamed.assets/image.png)")
	if _, err := os.Stat(filepath.Join(root, "Archive", "Renamed.assets", "image.png")); err != nil {
		t.Fatalf("owned assets did not move with renamed note: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Folder", "A.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old note remains after move: %v", err)
	}
}

func TestMoveWithLinkUpdatesRejectsStalePreviewWithoutMoving(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "A.md"), "[B](B.md)\n")
	mustWrite(t, filepath.Join(root, "B.md"), "target")
	repository := mustRepository(t, root)
	preview, err := repository.PreviewMoveLinks("B.md", "Moved.md")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "A.md"), "changed\n[B](B.md)\n")
	if _, err := repository.MoveWithLinkUpdates("B.md", "Moved.md", preview.Token); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale preview was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "B.md")); err != nil {
		t.Fatalf("source moved despite stale preview: %v", err)
	}
}

func TestFolderMoveKeepsInternalRelativeLinksAndUpdatesInboundLinks(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Projects", "A.md"), "[B](B.md)\n")
	mustWrite(t, filepath.Join(root, "Projects", "B.md"), "[A](A.md)\n")
	mustWrite(t, filepath.Join(root, "Index.md"), "[A](Projects/A.md)\n")
	if err := os.Mkdir(filepath.Join(root, "Archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	preview, err := repository.PreviewMoveLinks("Projects", "Archive/Projects")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rewrites) != 1 || preview.Rewrites[0].NotePath != "Index.md" {
		t.Fatalf("folder preview rewrote stable internal links: %#v", preview.Rewrites)
	}
	if _, err := repository.MoveWithLinkUpdates(preview.Source, preview.Target, preview.Token); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(root, "Index.md"), "[A](Archive/Projects/A.md)")
	assertFileContains(t, filepath.Join(root, "Archive", "Projects", "A.md"), "[B](B.md)")
}

func TestMoveWithLinkUpdatesPreservesEncodedAngleLinkDetails(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Folder", "Index.md"), "[Target](<Target%20Note.md?view=compact#details>)\n")
	mustWrite(t, filepath.Join(root, "Folder", "Target Note.md"), "target\n")
	if err := os.Mkdir(filepath.Join(root, "Archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)

	preview, err := repository.PreviewMoveLinks("Folder/Target Note.md", "Archive/Renamed Note.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Rewrites) != 1 || preview.Rewrites[0].Before != "<Target%20Note.md?view=compact#details>" || preview.Rewrites[0].After != "<../Archive/Renamed%20Note.md?view=compact#details>" {
		t.Fatalf("encoded angle link was not previewed faithfully: %#v", preview.Rewrites)
	}
	if _, err := repository.MoveWithLinkUpdates(preview.Source, preview.Target, preview.Token); err != nil {
		t.Fatal(err)
	}
	assertFileContains(t, filepath.Join(root, "Folder", "Index.md"), "[Target](<../Archive/Renamed%20Note.md?view=compact#details>)")
}

func assertFileContains(t *testing.T, name, expected string) {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), expected) {
		t.Fatalf("%s does not contain %q:\n%s", name, expected, content)
	}
}

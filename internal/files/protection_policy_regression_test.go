package files

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestProtectedSaveRejectsPlaintextSharedAttachment(t *testing.T) {
	root := t.TempDir()
	writeConversionFile(t, root, "Note.md", "original")
	repository, store, journal, catalog, key := newConversionFixture(t, root, t.TempDir())
	conversion, err := repository.PrepareExistingConversion(context.Background(), "Note.md", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversion.Commit(); err != nil {
		t.Fatal(err)
	}
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	writeConversionFile(t, root, "Shared.assets/plain.png", string(testPNG))
	note, _, err := repository.ReadMarkdownWithCatalog("Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "Note.md.age"))
	if err != nil {
		t.Fatal(err)
	}
	operation, _, err := repository.PrepareMarkdownWriteWithCatalog("Note.md", "![shared](Shared.assets/plain.png)", note.Version, catalog, key, store, journal)
	if operation != nil {
		operation.Abort()
	}
	if !errors.Is(err, ErrProtectedObjectMissing) {
		t.Fatalf("plaintext reference was accepted: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, "Note.md.age"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected save changed note: %v", err)
	}
	current, err := store.Load(key, catalog.VaultID)
	if err != nil || len(current.Objects) != len(catalog.Objects) {
		t.Fatalf("rejected save changed catalog: %v", err)
	}
}

func TestConversionRejectsUnsupportedLocalReferences(t *testing.T) {
	for _, attachment := range []string{"Shared.assets/private.pdf", "outside.png", "Shared.assets/private%2Epdf"} {
		t.Run(attachment, func(t *testing.T) {
			root := t.TempDir()
			writeConversionFile(t, root, "Note.md", "[attachment]("+attachment+")")
			physical := attachment
			if attachment == "Shared.assets/private%2Epdf" {
				physical = "Shared.assets/private.pdf"
			}
			writeConversionFile(t, root, physical, string(testPNG))
			repository, store, journal, catalog, key := newConversionFixture(t, root, t.TempDir())
			operation, err := repository.PrepareExistingConversion(context.Background(), "Note.md", catalog, key, store, journal)
			if operation != nil {
				operation.Abort()
			}
			if !errors.Is(err, ErrConversionUnsafe) {
				t.Fatalf("local attachment was silently skipped: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "Note.md")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed preflight staged note: %v", err)
			}
		})
	}
}

func TestExportRejectsUntrackedProtectedDescendantsBeforeFirstByte(t *testing.T) {
	for _, relative := range []string{"private/Secret.md", "private/Secret.assets/image.png", "private/document.pdf"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			writeConversionFile(t, root, relative, "secret")
			repository, _, _, catalog, key := newConversionFixture(t, root, t.TempDir())
			catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
			var output bytes.Buffer
			err := repository.WriteArchiveWithCatalog(&output, catalog, key, "", nil)
			if !errors.Is(err, ErrProtectedObjectMissing) || output.Len() != 0 {
				t.Fatalf("policy bypass: err=%v bytes=%d", err, output.Len())
			}
		})
	}
}

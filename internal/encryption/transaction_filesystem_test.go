package encryption

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaintextPublicationAndDirectoriesRecoverAtEveryBoundary(t *testing.T) {
	for _, phase := range []string{"transaction.directory.create", "transaction.content_stage.file_sync", "transaction.commit.before", "transaction.commit.after", "transaction.content.delete", "transaction.plaintext.publish.before", "transaction.plaintext.publish.after_write", "transaction.catalog.rename", "transaction.directory.remove", "transaction.cleanup.catalog_sync", "transaction.cleanup.journal_sync"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newTransactionFixture(t, 1)
			old := fixture.oldCatalog.Objects[0]
			oldBytes := fixture.oldBytes[old.PhysicalPath]
			plaintext, err := DecryptContent(fixture.key, old.ObjectID, oldBytes, *old.ContentKeyEnvelope)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(plaintext)
			next := fixture.oldCatalog
			next.Objects = []CatalogObject{}
			next.FolderRules = []FolderProtectionRule{}
			changes := []CiphertextChange{
				{Path: "restored/note.md", ObjectID: old.ObjectID, Ciphertext: oldBytes, ContentKeyEnvelope: *old.ContentKeyEnvelope, PublishPlaintext: true},
				{Path: old.PhysicalPath, Delete: true, ExpectedSHA256: old.CiphertextHash},
			}
			injectTransactionFailure(fixture.journal, phase)
			tx, err := fixture.journal.PrepareFilesystem(changes, next, fixture.key, []string{"restored"}, []string{"private"})
			committed := false
			if err == nil {
				if _, err := os.Stat(filepath.Join(fixture.vaultRoot, "restored", "note.md")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("precommit plaintext: %v", err)
				}
				if err := filepath.WalkDir(fixture.vaultRoot, func(name string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						return nil
					}
					data, err := os.ReadFile(name)
					if bytes.Contains(data, plaintext) {
						t.Fatalf("plaintext staging: %s", name)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				err = tx.Commit()
				committed = errors.Is(err, ErrTransactionNeedsRecovery)
			}
			if err == nil {
				t.Fatal("fault boundary was not reached")
			}
			restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.RecoverForVault(fixture.key, fixture.oldCatalog.VaultID); err != nil {
				t.Fatal(err)
			}
			if committed {
				data, err := os.ReadFile(filepath.Join(fixture.vaultRoot, "restored", "note.md"))
				if err != nil || !bytes.Equal(data, plaintext) {
					t.Fatalf("forward recovery: %v", err)
				}
				if _, err := os.Stat(filepath.Join(fixture.vaultRoot, "private")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("source directory survived: %v", err)
				}
			} else {
				assertTransactionFiles(t, fixture, fixture.oldBytes)
				assertTransactionCatalog(t, fixture, fixture.oldCatalog)
				if _, err := os.Stat(filepath.Join(fixture.vaultRoot, "restored")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("prepared directory survived: %v", err)
				}
			}
			if err := restarted.RecoverForVault(fixture.key, fixture.oldCatalog.VaultID); err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(fixture.vaultRoot, func(name string, entry os.DirEntry, err error) error {
				if err == nil && strings.HasPrefix(entry.Name(), ".vnotes-txn-") {
					t.Errorf("stage remains: %s", name)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlaintextPublicationRejectsRecursiveProtectionBeforeStaging(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	object := fixture.oldCatalog.Objects[0]
	change := CiphertextChange{Path: "private/leak.md", ObjectID: object.ObjectID, Ciphertext: fixture.oldBytes[object.PhysicalPath], ContentKeyEnvelope: *object.ContentKeyEnvelope, PublishPlaintext: true}
	if _, err := fixture.journal.Prepare([]CiphertextChange{change}, fixture.oldCatalog, fixture.key); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("protected plaintext publication accepted: %v", err)
	}
	assertNoTransactionStages(t, fixture)
}

func TestCommittedPlaintextPublicationRetainsExternalCollision(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	object := fixture.oldCatalog.Objects[0]
	change := CiphertextChange{Path: "Collision.md", ObjectID: object.ObjectID, Ciphertext: fixture.oldBytes[object.PhysicalPath], ContentKeyEnvelope: *object.ContentKeyEnvelope, PublishPlaintext: true}
	tx, err := fixture.journal.Prepare([]CiphertextChange{change}, fixture.oldCatalog, fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	injectTransactionFailure(fixture.journal, "transaction.commit.after")
	if err := tx.Commit(); !errors.Is(err, ErrTransactionNeedsRecovery) {
		t.Fatal(err)
	}
	filename := filepath.Join(fixture.vaultRoot, "Collision.md")
	if err := os.WriteFile(filename, []byte("external current file"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(fixture.key); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("external file overwritten: %v", err)
	}
	data, err := os.ReadFile(filename)
	if err != nil || string(data) != "external current file" {
		t.Fatalf("external bytes changed: %v", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
}

package encryption

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransactionPrepareStagesCiphertextWithoutChangingLiveState(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)

	stageCount := 0
	err = filepath.WalkDir(fixture.vaultRoot, func(filename string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".vnotes-txn-") {
			return nil
		}
		stageCount++
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("new plaintext")) || !bytes.Equal(data, fixture.changes[0].Ciphertext) {
			return fmt.Errorf("staging file %q did not contain only the expected ciphertext", filename)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stageCount != 1 {
		t.Fatalf("content stage count = %d, want one", stageCount)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	assertTransactionFiles(t, fixture, newBytesByPath(fixture.changes))
	assertTransactionCatalog(t, fixture, fixture.nextCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestTransactionRestartDiscardsPreparedState(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	runTransactionProcessExit(t, fixture, "prepared")
	restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(fixture.key); err != nil {
		t.Fatalf("discard prepared transaction after restart: %v", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestRegisteredTransactionRecoveryRejectsForeignIdentityBeforeMutation(t *testing.T) {
	for _, state := range []string{"prepared", "committed"} {
		t.Run(state, func(t *testing.T) {
			fixture := newTransactionFixture(t, 1)
			tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
			if err != nil {
				t.Fatal(err)
			}
			if state == "committed" {
				fixture.journal.fault = func(phase string) error {
					if phase == "transaction.commit.after" {
						return errors.New("simulated crash")
					}
					return nil
				}
				if err := tx.Commit(); !errors.Is(err, ErrTransactionNeedsRecovery) {
					t.Fatalf("crash boundary: %v", err)
				}
			}
			journalPath := filepath.Join(fixture.catalogStore.directory, fixture.journal.name)
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
			if err != nil {
				t.Fatal(err)
			}
			foreignID, err := NewCatalogObjectID()
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.RecoverForVault(fixture.key, foreignID); !errors.Is(err, ErrCatalogAuthentication) {
				t.Fatalf("foreign journal accepted: %v", err)
			}
			after, err := os.ReadFile(journalPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("foreign recovery changed journal: %v", err)
			}
			assertTransactionFiles(t, fixture, fixture.oldBytes)
			assertTransactionCatalog(t, fixture, fixture.oldCatalog)
			if state == "prepared" {
				if err := restarted.RecoverForVault(fixture.key, fixture.oldCatalog.VaultID); !errors.Is(err, ErrTransactionPending) {
					t.Fatalf("live owner not preserved: %v", err)
				}
				if err := tx.Abort(); err != nil {
					t.Fatal(err)
				}
			}
			if err := restarted.RecoverForVault(fixture.key, fixture.oldCatalog.VaultID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransactionRecoveryCleansOrphanJournalTemporary(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	orphan := filepath.Join(fixture.catalogStore.directory, ".vnotes-journal-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.tmp")
	if err := os.WriteFile(orphan, []byte("partial authenticated journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.Recover(fixture.key); err != nil {
		t.Fatalf("recover orphan journal temporary: %v", err)
	}
	if _, err := os.Lstat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan journal temporary remains: %v", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
}

func TestTransactionDeleteRemovesContentAndCatalogEntryTogether(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	fixture.nextCatalog.Objects = []CatalogObject{}
	fixture.changes = []CiphertextChange{{Path: "private/note-0.md.age", Delete: true}}
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatalf("prepare delete transaction: %v", err)
	}
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit delete transaction: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.vaultRoot, "private", "note-0.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted ciphertext still exists: %v", err)
	}
	assertTransactionCatalog(t, fixture, fixture.nextCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestConversionTransactionKeepsOriginalUntilCommitAndChecksSource(t *testing.T) {
	fixture := newPlaintextConversionFixture(t, []byte("selected existing note"))
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatalf("prepare conversion transaction: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.vaultRoot, "Note.md")); err != nil || string(content) != "selected existing note" {
		t.Fatalf("plaintext original changed before commit: %q err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.vaultRoot, "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext target appeared before commit: %v", err)
	}
	journalBytes, err := os.ReadFile(filepath.Join(fixture.catalogStore.directory, fixture.journal.name))
	if err != nil {
		t.Fatal(err)
	}
	plaintextDigest := CiphertextSHA256([]byte("selected existing note"))
	if bytes.Contains(journalBytes, []byte(plaintextDigest)) {
		t.Fatal("prepared journal persisted the plaintext source digest")
	}
	if err := os.WriteFile(filepath.Join(fixture.vaultRoot, "Note.md"), []byte("concurrent edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("commit after source changed = %v, want conflict", err)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.vaultRoot, "Note.md")); err != nil || string(content) != "concurrent edit" {
		t.Fatalf("concurrent source edit was lost: %q err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.vaultRoot, "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext target was installed after a source conflict: %v", err)
	}
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestConversionTransactionRejectsHardlinksAtPrepareAndCommit(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		fixture := newPlaintextConversionFixture(t, []byte("selected existing note"))
		if err := os.Link(filepath.Join(fixture.vaultRoot, "Note.md"), filepath.Join(filepath.Dir(fixture.vaultRoot), "outside.md")); err != nil {
			t.Skipf("hard links are unavailable: %v", err)
		}
		if _, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key); !errors.Is(err, ErrInvalidTransaction) {
			t.Fatalf("prepare with hard-linked source = %v, want invalid transaction", err)
		}
		assertPlaintextConversionNotCommitted(t, fixture)
	})

	t.Run("after prepare", func(t *testing.T) {
		fixture := newPlaintextConversionFixture(t, []byte("selected existing note"))
		tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(fixture.vaultRoot, "Note.md"), filepath.Join(filepath.Dir(fixture.vaultRoot), "outside.md")); err != nil {
			t.Skipf("hard links are unavailable: %v", err)
		}
		if err := tx.Commit(); !errors.Is(err, ErrInvalidTransaction) {
			t.Fatalf("commit after adding a hard link = %v, want invalid transaction", err)
		}
		assertPlaintextConversionNotCommitted(t, fixture)
		if _, err := os.Stat(filepath.Join(filepath.Dir(fixture.vaultRoot), "outside.md")); err != nil {
			t.Fatalf("hard-linked alias was removed: %v", err)
		}
	})
}

func TestConversionTransactionCancellationAndInjectedFailuresPreserveSource(t *testing.T) {
	phases := []string{
		"transaction.journal.prepared.write", "transaction.journal.prepared.file_sync",
		"transaction.journal.prepared.rename", "transaction.journal.prepared.directory_sync",
		"transaction.content_stage.write", "transaction.content_stage.file_sync",
		"transaction.content_stage.directory_sync", "transaction.catalog_stage.write",
		"transaction.catalog_stage.file_sync", "transaction.catalog_stage.directory_sync",
		"transaction.commit.before",
	}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			fixture := newPlaintextConversionFixture(t, []byte("keep until commit"))
			fired := false
			fixture.journal.fault = func(actual string) error {
				if !fired && actual == phase {
					fired = true
					return errors.New("injected disk failure")
				}
				return nil
			}
			tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
			if err == nil {
				if phase != "transaction.commit.before" {
					t.Fatalf("Prepare did not hit injected phase %q", phase)
				}
				err = tx.Commit()
			}
			if err == nil || !fired {
				t.Fatalf("injected phase %q returned %v, fired=%v", phase, err, fired)
			}
			if recoverErr := fixture.journal.Recover(fixture.key); recoverErr != nil {
				t.Fatalf("recover after injected phase %q: %v", phase, recoverErr)
			}
			assertPlaintextConversionNotCommitted(t, fixture)
		})
	}

	t.Run("cancellation during staging", func(t *testing.T) {
		fixture := newPlaintextConversionFixture(t, []byte("keep until commit"))
		ctx, cancel := context.WithCancel(context.Background())
		fixture.journal.fault = func(actual string) error {
			if actual == "transaction.content_stage.file_sync" {
				cancel()
			}
			return nil
		}
		if _, err := fixture.journal.PrepareContext(ctx, fixture.changes, fixture.nextCatalog, fixture.key); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled stage error = %v", err)
		}
		assertPlaintextConversionNotCommitted(t, fixture)
	})
}

func TestConversionTransactionRecoversForwardAfterCommitPoint(t *testing.T) {
	fixture := newPlaintextConversionFixture(t, []byte("recover after commit"))
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	injectTransactionFailure(fixture.journal, "transaction.commit.after")
	if err := tx.Commit(); !errors.Is(err, ErrTransactionNeedsRecovery) {
		t.Fatalf("commit failure after durable point = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.vaultRoot, "Note.md")); err != nil || string(content) != "recover after commit" {
		t.Fatalf("source changed before recovery: %q err=%v", content, err)
	}
	restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(fixture.key); err != nil {
		t.Fatalf("recover committed conversion: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.vaultRoot, "Note.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext source survived committed recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.vaultRoot, "Note.md.age")); err != nil {
		t.Fatalf("ciphertext target missing after recovery: %v", err)
	}
	assertTransactionCatalog(t, fixture, fixture.nextCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestConversionTransactionRestartBeforeCommitPreservesSource(t *testing.T) {
	fixture := newPlaintextConversionFixture(t, []byte("restart before commit"))
	runTransactionProcessExit(t, fixture, "prepared")
	restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(fixture.key); err != nil {
		t.Fatalf("discard uncommitted conversion after restart: %v", err)
	}
	assertPlaintextConversionNotCommitted(t, fixture)
}

func TestTransactionCanCommitFolderRuleWithoutContentChanges(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	fixture.nextCatalog = fixture.oldCatalog
	fixture.nextCatalog.FolderRules = append(fixture.nextCatalog.FolderRules, FolderProtectionRule{Path: "empty"})
	if err := os.Mkdir(filepath.Join(fixture.vaultRoot, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.journal.Prepare(nil, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatalf("prepare catalog-only transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit catalog-only transaction: %v", err)
	}
	assertTransactionCatalog(t, fixture, fixture.nextCatalog)
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	assertNoTransactionStages(t, fixture)
}

func newPlaintextConversionFixture(t *testing.T, plaintext []byte) transactionFixture {
	t.Helper()
	base := t.TempDir()
	vaultRoot := filepath.Join(base, "vault")
	if err := os.Mkdir(vaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x73}, VaultKeySize)
	oldCatalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	objectID, err := NewCatalogObjectID()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, envelope, err := EncryptContent(key, objectID, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	object := CatalogObject{
		ObjectID: objectID, Kind: CatalogObjectNote, LogicalPath: "Note.md", PhysicalPath: "Note.md.age",
		CiphertextHash: CiphertextSHA256(ciphertext), ContentKeyEnvelope: &envelope, Protected: true,
	}
	nextCatalog := Catalog{Version: CatalogFormatVersion, VaultID: oldCatalog.VaultID, Objects: []CatalogObject{object}, FolderRules: []FolderProtectionRule{}}
	catalogStore, err := NewCatalogStore(filepath.Join(base, "metadata", "vault", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalogStore.Save(oldCatalog, key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "Note.md"), plaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := NewTransactionJournal(vaultRoot, catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	changes := []CiphertextChange{
		{Path: "Note.md.age", ObjectID: objectID, Ciphertext: ciphertext, ContentKeyEnvelope: envelope},
		{Path: "Note.md", Delete: true, ExpectedSHA256: CiphertextSHA256(plaintext), RejectHardlinks: true},
	}
	return transactionFixture{
		vaultRoot: vaultRoot, catalogStore: catalogStore, journal: journal, key: key,
		changes: changes, oldBytes: map[string][]byte{"Note.md": append([]byte(nil), plaintext...)},
		oldCatalog: oldCatalog, nextCatalog: nextCatalog,
	}
}

func assertPlaintextConversionNotCommitted(t *testing.T, fixture transactionFixture) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(fixture.vaultRoot, "Note.md"))
	if err != nil || !bytes.Equal(content, fixture.oldBytes["Note.md"]) {
		t.Fatalf("plaintext source was not preserved: %q err=%v", content, err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.vaultRoot, "Note.md.age")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext target was installed before commit: %v", err)
	}
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestTransactionInjectedPrepareFailuresPreserveOriginals(t *testing.T) {
	phases := []string{
		"transaction.journal.prepared.write",
		"transaction.journal.prepared.file_sync",
		"transaction.journal.prepared.rename",
		"transaction.journal.prepared.directory_sync",
		"transaction.content_stage.write",
		"transaction.content_stage.file_sync",
		"transaction.content_stage.directory_sync",
		"transaction.catalog_stage.write",
		"transaction.catalog_stage.file_sync",
		"transaction.catalog_stage.directory_sync",
	}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			fixture := newTransactionFixture(t, 1)
			injectTransactionFailure(fixture.journal, phase)
			if _, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key); err == nil {
				t.Fatalf("Prepare unexpectedly succeeded at %s", phase)
			}
			restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Recover(fixture.key); err != nil {
				t.Fatalf("recover failed prepare: %v", err)
			}
			assertTransactionFiles(t, fixture, fixture.oldBytes)
			assertTransactionCatalog(t, fixture, fixture.oldCatalog)
			assertNoTransactionStages(t, fixture)
		})
	}
}

func TestTransactionCommitFailuresRecoverForward(t *testing.T) {
	phases := []string{
		"transaction.journal.committed.directory_sync",
		"transaction.commit.after",
		"transaction.content.rename",
		"transaction.content.directory_sync",
		"transaction.catalog.rename",
		"transaction.catalog.directory_sync",
	}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			fixture := newTransactionFixture(t, 1)
			tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
			if err != nil {
				t.Fatalf("prepare transaction: %v", err)
			}
			injectTransactionFailure(fixture.journal, phase)
			if err := tx.Commit(); !errors.Is(err, ErrTransactionNeedsRecovery) {
				t.Fatalf("Commit error = %v, want recovery required", err)
			}
			restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Recover(fixture.key); err != nil {
				t.Fatalf("recover committed transaction: %v", err)
			}
			assertTransactionFiles(t, fixture, newBytesByPath(fixture.changes))
			assertTransactionCatalog(t, fixture, fixture.nextCatalog)
			assertNoTransactionStages(t, fixture)
		})
	}
}

func TestTransactionCommitMarkerFailuresBeforeCommitPreserveOriginals(t *testing.T) {
	phases := []string{
		"transaction.commit.before",
		"transaction.journal.committed.write",
		"transaction.journal.committed.file_sync",
		"transaction.journal.committed.rename",
	}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			fixture := newTransactionFixture(t, 1)
			tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
			if err != nil {
				t.Fatalf("prepare transaction: %v", err)
			}
			injectTransactionFailure(fixture.journal, phase)
			if err := tx.Commit(); err == nil || errors.Is(err, ErrTransactionNeedsRecovery) {
				t.Fatalf("Commit error = %v, want pre-commit failure", err)
			}
			assertTransactionFiles(t, fixture, fixture.oldBytes)
			assertTransactionCatalog(t, fixture, fixture.oldCatalog)
			assertNoTransactionStages(t, fixture)
		})
	}
}

func TestTransactionCommitConflictLeavesExternalChangeUntouched(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	external := []byte("external replacement")
	if err := os.WriteFile(filepath.Join(fixture.vaultRoot, filepath.FromSlash(fixture.changes[0].Path)), external, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("Commit error = %v, want transaction conflict", err)
	}
	actual, err := os.ReadFile(filepath.Join(fixture.vaultRoot, filepath.FromSlash(fixture.changes[0].Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, external) {
		t.Fatalf("external replacement was overwritten: %q", actual)
	}
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestTransactionCommitConflictPreservesExternalCatalogChange(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	externalCatalog := fixture.oldCatalog
	externalCatalog.FolderRules = []FolderProtectionRule{{Path: "private"}, {Path: "external"}}
	if err := fixture.catalogStore.Save(externalCatalog, fixture.key); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); !errors.Is(err, ErrTransactionConflict) {
		t.Fatalf("Commit error = %v, want transaction conflict", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	assertTransactionCatalog(t, fixture, externalCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestTransactionPrepareRejectsCiphertextThatDisagreesWithCatalog(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	filename := filepath.Join(fixture.vaultRoot, filepath.FromSlash(fixture.changes[0].Path))
	if err := os.WriteFile(filename, []byte("corrupted ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key); !errors.Is(err, ErrCatalogCiphertextCheck) {
		t.Fatalf("Prepare error = %v, want catalog ciphertext mismatch", err)
	}
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	assertNoTransactionStages(t, fixture)
}

func TestTransactionRejectsPlaintextUnsafePathsAndBadJournalAuthentication(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	plaintext := fixture.changes[0]
	plaintext.Ciphertext = []byte("plaintext must never enter a staging file")
	if _, err := fixture.journal.Prepare([]CiphertextChange{plaintext}, fixture.nextCatalog, fixture.key); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("plaintext prepare error = %v", err)
	}
	unsafe := fixture.changes[0]
	unsafe.Path = "../outside.age"
	if _, err := fixture.journal.Prepare([]CiphertextChange{unsafe}, fixture.nextCatalog, fixture.key); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("traversal prepare error = %v", err)
	}
	if _, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key); err != nil {
		t.Fatalf("prepare transaction: %v", err)
	}
	journalPath := filepath.Join(fixture.catalogStore.directory, fixture.journal.name)
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	if err := os.WriteFile(journalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.Recover(fixture.key); !errors.Is(err, ErrTransactionJournalCorrupt) {
		t.Fatalf("tampered journal error = %v", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
}

func TestTransactionPrepareRejectsTamperedNewCiphertext(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	change := fixture.changes[0]
	change.Ciphertext = append([]byte(nil), change.Ciphertext...)
	change.Ciphertext[len(change.Ciphertext)-1] ^= 1
	fixture.nextCatalog.Objects[0].CiphertextHash = CiphertextSHA256(change.Ciphertext)
	if _, err := fixture.journal.Prepare([]CiphertextChange{change}, fixture.nextCatalog, fixture.key); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("Prepare error = %v, want invalid authenticated ciphertext", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	assertNoTransactionStages(t, fixture)
}

type transactionFixture struct {
	vaultRoot    string
	catalogStore *CatalogStore
	journal      *TransactionJournal
	key          []byte
	changes      []CiphertextChange
	oldBytes     map[string][]byte
	oldCatalog   Catalog
	nextCatalog  Catalog
}

func newTransactionFixture(t *testing.T, count int) transactionFixture {
	t.Helper()
	if count < 1 {
		t.Fatal("transaction fixture needs at least one file")
	}
	base := t.TempDir()
	vaultRoot := filepath.Join(base, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x61}, VaultKeySize)
	catalogStore, err := NewCatalogStore(filepath.Join(base, "metadata", "vault", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldCatalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	nextCatalog := Catalog{Version: CatalogFormatVersion, VaultID: oldCatalog.VaultID, Objects: []CatalogObject{}, FolderRules: []FolderProtectionRule{{Path: "private"}}}
	oldCatalog.FolderRules = []FolderProtectionRule{{Path: "private"}}
	changes := make([]CiphertextChange, 0, count)
	oldBytes := make(map[string][]byte, count)
	for index := range count {
		objectID := fmt.Sprintf("%032x", index+1)
		logicalPath := fmt.Sprintf("private/note-%d.md", index)
		physicalPath := logicalPath + ".age"
		oldCiphertext, oldEnvelope, err := EncryptContent(key, objectID, []byte(fmt.Sprintf("old plaintext %d", index)))
		if err != nil {
			t.Fatal(err)
		}
		newCiphertext, newEnvelope, err := EncryptContent(key, objectID, []byte(fmt.Sprintf("new plaintext %d", index)))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vaultRoot, filepath.FromSlash(physicalPath)), oldCiphertext, 0o600); err != nil {
			t.Fatal(err)
		}
		oldBytes[physicalPath] = oldCiphertext
		oldCatalog.Objects = append(oldCatalog.Objects, CatalogObject{
			ObjectID: objectID, Kind: CatalogObjectNote, LogicalPath: logicalPath,
			PhysicalPath: physicalPath, CiphertextHash: CiphertextSHA256(oldCiphertext), ContentKeyEnvelope: &oldEnvelope, Protected: true,
		})
		nextCatalog.Objects = append(nextCatalog.Objects, CatalogObject{
			ObjectID: objectID, Kind: CatalogObjectNote, LogicalPath: logicalPath,
			PhysicalPath: physicalPath, CiphertextHash: CiphertextSHA256(newCiphertext), ContentKeyEnvelope: &newEnvelope, Protected: true,
		})
		changes = append(changes, CiphertextChange{
			Path: physicalPath, ObjectID: objectID, Ciphertext: newCiphertext, ContentKeyEnvelope: newEnvelope,
		})
	}
	if err := catalogStore.Save(oldCatalog, key); err != nil {
		t.Fatalf("save initial catalog: %v", err)
	}
	journal, err := NewTransactionJournal(vaultRoot, catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	return transactionFixture{
		vaultRoot: vaultRoot, catalogStore: catalogStore, journal: journal, key: key,
		changes: changes, oldBytes: oldBytes, oldCatalog: oldCatalog, nextCatalog: nextCatalog,
	}
}

func injectTransactionFailure(journal *TransactionJournal, phase string) {
	failed := false
	journal.fault = func(actual string) error {
		if !failed && actual == phase {
			failed = true
			return fmt.Errorf("injected transaction failure at %s", phase)
		}
		return nil
	}
}

func assertTransactionFiles(t *testing.T, fixture transactionFixture, expected map[string][]byte) {
	t.Helper()
	for filename, want := range expected {
		actual, err := os.ReadFile(filepath.Join(fixture.vaultRoot, filepath.FromSlash(filename)))
		if err != nil {
			t.Fatalf("read transaction file %q: %v", filename, err)
		}
		if !bytes.Equal(actual, want) {
			t.Errorf("transaction file %q differs", filename)
		}
	}
}

func assertTransactionCatalog(t *testing.T, fixture transactionFixture, want Catalog) {
	t.Helper()
	loaded, err := fixture.catalogStore.Load(fixture.key, want.VaultID)
	if err != nil {
		t.Fatalf("load transaction catalog: %v", err)
	}
	wantBytes, err := MarshalCatalog(want, fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	loadedBytes, err := MarshalCatalog(loaded, fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loadedBytes, wantBytes) {
		t.Errorf("transaction catalog differs: got %#v, want %#v", loaded, want)
	}
}

func assertNoTransactionStages(t *testing.T, fixture transactionFixture) {
	t.Helper()
	for _, root := range []string{fixture.vaultRoot, fixture.catalogStore.directory} {
		err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".vnotes-txn-") || strings.HasPrefix(name, ".vnotes-journal-") || name == fixture.journal.name {
				t.Errorf("transaction staging artifact remains: %s", filename)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func newBytesByPath(changes []CiphertextChange) map[string][]byte {
	result := make(map[string][]byte, len(changes))
	for _, change := range changes {
		if !change.Delete {
			result[change.Path] = change.Ciphertext
		}
	}
	return result
}

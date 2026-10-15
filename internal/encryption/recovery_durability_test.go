package encryption

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// This is an actual abrupt subprocess exit, not a returned error or a new
// object in the same process. It models interruption before a directory fsync;
// it does not claim to simulate a physical power cut or a filesystem journal.
func TestRecoveryProcessBoundary(t *testing.T) {
	if phase := os.Getenv("VNOTES_RECOVERY_EXIT_PHASE"); phase != "" {
		store, err := NewCatalogStore(os.Getenv("VNOTES_RECOVERY_CATALOG"))
		if err != nil {
			t.Fatal(err)
		}
		journal, err := NewTransactionJournal(os.Getenv("VNOTES_RECOVERY_VAULT"), store)
		if err != nil {
			t.Fatal(err)
		}
		key := bytes.Repeat([]byte{0x73}, VaultKeySize)
		journal.fault = func(at string) error {
			if at == phase {
				os.Exit(86)
			}
			return nil
		}
		if os.Getenv("VNOTES_RECOVERY_DECIDE") == "1" {
			record, err := journal.loadRecord(key)
			if err != nil {
				t.Fatal(err)
			}
			record.State = "committed"
			if err := journal.saveRecord(record, key); err != nil {
				t.Fatal(err)
			}
		} else if err := journal.Recover(key); err != nil {
			t.Fatal(err)
		}
		return
	}
	fixture := newPlaintextConversionFixture(t, []byte("process-boundary secret"))
	if _, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key); err != nil {
		t.Fatal(err)
	}
	run := func(decide bool, phase string) {
		t.Helper()
		command := exec.Command(os.Args[0], "-test.run=^TestRecoveryProcessBoundary$")
		command.Env = append(os.Environ(),
			"VNOTES_RECOVERY_EXIT_PHASE="+phase,
			"VNOTES_RECOVERY_VAULT="+fixture.vaultRoot,
			"VNOTES_RECOVERY_CATALOG="+filepath.Join(fixture.catalogStore.directory, fixture.catalogStore.name))
		if decide {
			command.Env = append(command.Env, "VNOTES_RECOVERY_DECIDE=1")
		}
		output, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 86 {
			t.Fatalf("missing abrupt exit at %s: %v %s", phase, err, output)
		}
	}
	run(true, "transaction.journal.committed.directory_sync")
	// A second process must encounter the commit barrier before deleting data.
	run(false, "transaction.journal.committed.directory_sync")
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	// Die again after ciphertext rename but before its parent directory fsync.
	run(false, "transaction.content.directory_sync")
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(fixture.key); err != nil {
		t.Fatal(err)
	}
	assertTransactionCatalog(t, fixture, fixture.nextCatalog)
	if _, err := os.Stat(filepath.Join(fixture.vaultRoot, "Note.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remains: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.vaultRoot, "Note.md.age"))
	if err != nil || !bytes.Equal(stored, fixture.changes[0].Ciphertext) {
		t.Fatalf("ciphertext lost: %v", err)
	}
}

func TestRecoveryRequiresDurableCommitAndInstalledContent(t *testing.T) {
	for _, phase := range []string{"transaction.journal.committed.directory_sync", "transaction.content.directory_sync"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newPlaintextConversionFixture(t, []byte("source must survive failed recovery"))
			tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
			if err != nil {
				t.Fatal(err)
			}
			injectTransactionFailure(fixture.journal, phase)
			if err := tx.Commit(); !errors.Is(err, ErrTransactionNeedsRecovery) {
				t.Fatalf("commit: %v", err)
			}
			restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
			if err != nil {
				t.Fatal(err)
			}
			injectTransactionFailure(restarted, phase)
			if err := restarted.Recover(fixture.key); !errors.Is(err, ErrTransactionNeedsRecovery) {
				t.Errorf("recovery skipped failed barrier: %v", err)
			}
			assertTransactionFiles(t, fixture, fixture.oldBytes)
			restarted.fault = nil
			if err := restarted.Recover(fixture.key); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryRequiresDurableInstalledCatalog(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	injectTransactionFailure(fixture.journal, "transaction.catalog.directory_sync")
	if err := tx.Commit(); !errors.Is(err, ErrTransactionNeedsRecovery) {
		t.Fatal(err)
	}
	restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	injectTransactionFailure(restarted, "transaction.catalog.directory_sync")
	if err := restarted.Recover(fixture.key); !errors.Is(err, ErrTransactionNeedsRecovery) {
		t.Fatalf("recovery skipped catalog barrier: %v", err)
	}
	if _, err := restarted.loadRecord(fixture.key); err != nil {
		t.Fatalf("journal cleared before durable catalog: %v", err)
	}
	restarted.fault = nil
	if err := restarted.Recover(fixture.key); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryResyncsAlreadyRemovedDirectory(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	root, err := os.OpenRoot(fixture.vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Model a visible rmdir before parent fsync, including a removed parent.
	injected := errors.New("ancestor sync failed")
	fixture.journal.fault = func(phase string) error {
		if phase == "transaction.directory.sync" {
			return injected
		}
		return nil
	}
	if err := fixture.journal.removeTransactionDirectories(root, []string{"gone", "gone/nested"}); !errors.Is(err, injected) {
		t.Fatalf("absent directories skipped their surviving ancestor barrier: %v", err)
	}
	fixture.journal.fault = nil
	if err := fixture.journal.removeTransactionDirectories(root, []string{"gone", "gone/nested"}); err != nil {
		t.Fatal(err)
	}
}

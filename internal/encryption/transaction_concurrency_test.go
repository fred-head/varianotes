package encryption

import (
	"bytes"
	"encoding/gob"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// B1: an independent export journal reads prepared, then cleans that stale
// record after the writer installs ciphertext with a new content-key envelope.
func TestRecoveryCannotDiscardActiveSave(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	preparedRead, installed := make(chan struct{}), make(chan struct{})
	clean, finish := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(clean); close(finish) })
	export, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore, TransactionJournalOptions{
		Fault: func(phase string) error {
			if phase == "transaction.recovery.prepared" {
				close(preparedRead)
				<-clean
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.journal.fault = func(phase string) error {
		if phase == "transaction.content.directory_sync" {
			close(installed)
			<-finish
		}
		return nil
	}
	recovery := make(chan error, 1)
	go func() { recovery <- export.RecoverForVault(fixture.key, fixture.oldCatalog.VaultID) }()
	var commitErr error
	select {
	case <-preparedRead:
		// This is the unsafe interleaving on the original implementation.
		commit := make(chan error, 1)
		go func() { commit <- tx.Commit() }()
		select {
		case <-installed:
		case <-time.After(5 * time.Second):
			t.Fatal("writer never installed ciphertext")
		}
		clean <- struct{}{}
		if err := <-recovery; err != nil {
			t.Errorf("stale recovery: %v", err)
		}
		finish <- struct{}{}
		commitErr = <-commit
	case err := <-recovery:
		// With transaction ownership, recovery must reject live prepared work.
		if !errors.Is(err, ErrTransactionPending) {
			t.Fatalf("active recovery returned %v", err)
		}
		fixture.journal.fault = nil
		commitErr = tx.Commit()
	case <-time.After(5 * time.Second):
		t.Fatal("recovery neither identified live work nor reached the pause")
	}
	if commitErr != nil {
		t.Errorf("B1: active save lost its stages: %v", commitErr)
	}
	restarted, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverForVault(fixture.key, fixture.oldCatalog.VaultID); err != nil {
		t.Errorf("restart recovery: %v", err)
	}
	catalog, err := fixture.catalogStore.Load(fixture.key, fixture.oldCatalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.vaultRoot, filepath.FromSlash(fixture.changes[0].Path)))
	if err != nil || !bytes.Equal(stored, fixture.changes[0].Ciphertext) {
		t.Fatalf("saved ciphertext lost: %v", err)
	}
	object := catalog.Objects[0]
	if err := VerifyCatalogCiphertext(object, stored); err != nil {
		t.Fatalf("B1: installed ciphertext has no matching catalog/envelope: %v", err)
	}
	plaintext, err := DecryptContent(fixture.key, object.ObjectID, stored, *object.ContentKeyEnvelope)
	if err != nil {
		t.Fatalf("saved content cannot be decrypted: %v", err)
	}
	clear(plaintext)
	assertTransactionCatalog(t, fixture, fixture.nextCatalog)
	assertNoTransactionStages(t, fixture)
}

type transactionProcessPlan struct {
	Root, CatalogPath, Phase string
	Key                      []byte
	Changes                  []CiphertextChange
	Catalog                  Catalog
}

// An actual exited writer is essential: reopening a journal in the same
// process is another concurrent user, not a restart that abandons its owner.
func runTransactionProcessExit(t *testing.T, fixture transactionFixture, phase string) {
	t.Helper()
	plan := transactionProcessPlan{
		Root: fixture.vaultRoot, CatalogPath: filepath.Join(fixture.catalogStore.directory, fixture.catalogStore.name),
		Phase: phase, Key: fixture.key, Changes: fixture.changes, Catalog: fixture.nextCatalog,
	}
	var encoded bytes.Buffer
	if err := gob.NewEncoder(&encoded).Encode(plan); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "test-plan.gob")
	if err := os.WriteFile(filename, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestTransactionWriterProcessExit$")
	command.Env = append(os.Environ(), "VNOTES_WRITER_EXIT_PLAN="+filename)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("missing writer exit at %s: %v %s", phase, err, output)
	}
}

func TestTransactionWriterProcessExit(t *testing.T) {
	filename := os.Getenv("VNOTES_WRITER_EXIT_PLAN")
	if filename == "" {
		return
	}
	encoded, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var plan transactionProcessPlan
	if err := gob.NewDecoder(bytes.NewReader(encoded)).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	store, err := NewCatalogStore(plan.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := NewTransactionJournal(plan.Root, store, TransactionJournalOptions{
		Fault: func(phase string) error {
			if phase == plan.Phase {
				os.Exit(86)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := journal.Prepare(plan.Changes, plan.Catalog, plan.Key)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Phase == "prepared" {
		os.Exit(86)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("writer completed without hitting the exit boundary")
}

func TestSaveProcessRestartPreservesCatalogAndEnvelope(t *testing.T) {
	for _, phase := range []string{"prepared", "transaction.content.directory_sync", "transaction.catalog.rename"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newTransactionFixture(t, 1)
			runTransactionProcessExit(t, fixture, phase)
			if err := fixture.journal.RecoverForVault(fixture.key, fixture.oldCatalog.VaultID); err != nil {
				t.Fatal(err)
			}
			if phase == "prepared" {
				assertTransactionFiles(t, fixture, fixture.oldBytes)
				assertTransactionCatalog(t, fixture, fixture.oldCatalog)
			} else {
				assertTransactionFiles(t, fixture, newBytesByPath(fixture.changes))
				assertTransactionCatalog(t, fixture, fixture.nextCatalog)
			}
			assertNoTransactionStages(t, fixture)
		})
	}
}

func TestAbortCleanupFailureReleasesActiveOwnerForRecovery(t *testing.T) {
	fixture := newTransactionFixture(t, 1)
	tx, err := fixture.journal.Prepare(fixture.changes, fixture.nextCatalog, fixture.key)
	if err != nil {
		t.Fatal(err)
	}
	injectTransactionFailure(fixture.journal, "transaction.cleanup.content_sync")
	if err := tx.Abort(); err == nil {
		t.Fatal("abort did not encounter the injected cleanup failure")
	}
	reopened, err := NewTransactionJournal(fixture.vaultRoot, fixture.catalogStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Recover(fixture.key); err != nil {
		t.Fatalf("failed Abort left a live owner: %v", err)
	}
	assertTransactionFiles(t, fixture, fixture.oldBytes)
	assertTransactionCatalog(t, fixture, fixture.oldCatalog)
	assertNoTransactionStages(t, fixture)
}

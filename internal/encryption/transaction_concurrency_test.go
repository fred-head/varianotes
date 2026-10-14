package encryption

import (
	"bytes"
	"errors"
	"os"
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

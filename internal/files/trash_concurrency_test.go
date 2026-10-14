package files

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
)

// B2: B keeps the pre-operation catalog. A either stops after source deletion
// before catalog rename, or commits and stops before publishing metadata.
func TestTrashReconciliationWithStaleCatalogPreservesRestoreMapping(t *testing.T) {
	for _, boundary := range []string{"catalog-rename", "metadata-rename"} {
		t.Run(boundary, func(t *testing.T) {
			repository, store, _, stale, key, _ := trashDurabilityFixture(t)
			original := "private/nested/Note.md"
			before, err := os.ReadFile(filepath.Join(repository.root, filepath.FromSlash(original+".age")))
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("interrupted Trash publication")
			// The conversion is already protected and makes no catalog change.
			// Arm the fault only once the Trash transaction is prepared.
			armed := false
			faultJournal, err := encryption.NewTransactionJournal(repository.root, store, encryption.TransactionJournalOptions{
				Fault: func(phase string) error {
					if armed && boundary == "catalog-rename" && phase == "transaction.catalog.rename" {
						return injected
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			repository.transactionPrepared = func() error { armed = true; return nil }
			repository.content.fault = func(phase, relative string) error {
				if armed && boundary == "metadata-rename" && phase == "file.rename" && strings.HasSuffix(relative, "/metadata.json") {
					return injected
				}
				return nil
			}
			snapshotReady, reconcile := make(chan struct{}), make(chan struct{})
			listed := make(chan error, 1)
			go func() {
				oldSnapshot := stale
				close(snapshotReady)
				<-reconcile
				_, err := repository.TrashItemsWithCatalog(oldSnapshot, key, faultJournal)
				listed <- err
			}()
			<-snapshotReady
			t.Cleanup(func() { close(reconcile) })
			if _, err := repository.MoveToTrashWithCatalog("private", stale, key, store, faultJournal); !errors.Is(err, injected) {
				t.Fatalf("fault boundary not reached: %v", err)
			}
			if _, err := os.Stat(filepath.Join(repository.root, filepath.FromSlash(original+".age"))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("source was not deleted before interruption: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(repository.root, ".trash"))
			if err != nil {
				t.Fatal(err)
			}
			var pending string
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".pending-") {
					pending = filepath.Join(repository.root, ".trash", entry.Name(), trashMetadataName)
				}
			}
			if pending == "" {
				t.Fatal("pending restore mapping missing at fault boundary")
			}
			mapping, err := os.ReadFile(pending)
			if err != nil {
				t.Fatal(err)
			}
			// B now runs with the snapshot taken before A. A repeated recovery
			// failure must keep the mapping, even though the live catalog is old.
			reconcile <- struct{}{}
			select {
			case err := <-listed:
				if !errors.Is(err, injected) {
					t.Errorf("reconciliation skipped the interrupted recovery/finalization barrier: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stale listing stalled")
			}
			retained, err := os.ReadFile(pending)
			if err != nil || !bytes.Equal(retained, mapping) {
				t.Errorf("B2: stale reconciliation deleted OriginalPath: %v", err)
			}
			repository.content.fault = nil
			restarted := mustRepository(t, repository.root)
			journal, err := encryption.NewTransactionJournal(repository.root, store)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.RecoverForVault(key, stale.VaultID); err != nil {
				t.Fatal(err)
			}
			latest, err := store.Load(key, stale.VaultID)
			if err != nil {
				t.Fatal(err)
			}
			items, err := restarted.TrashItemsWithCatalog(latest, key, journal)
			if err != nil || len(items) != 1 || items[0].OriginalPath != "private" {
				t.Fatalf("B2: restart lost usable restore mapping: %#v %v", items, err)
			}
			if _, err := restarted.RestoreTrashItemWithCatalog(items[0].ID, MoveProtectionKeep, latest, key, store, journal); err != nil {
				t.Fatal(err)
			}
			latest, err = store.Load(key, stale.VaultID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(repository.root, filepath.FromSlash(original+".age")))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("restore changed ciphertext bytes or original path: %v", err)
			}
			note, protected, err := restarted.ReadMarkdownWithCatalog(original, latest, key)
			if err != nil || !protected || note.Content != "trash-durability-secret" {
				t.Fatalf("restore lost exact content: %#v %v", note, err)
			}
		})
	}
}

func TestTrashReconciliationRetainsAmbiguousPendingPayload(t *testing.T) {
	repository, _, journal, catalog, key, _ := trashDurabilityFixture(t)
	id := "abababababababababababababababab"
	pending := ".trash/.pending-" + id
	target := ".trash/" + id + "/content/item/retained.age"
	if err := os.MkdirAll(filepath.Join(repository.root, filepath.FromSlash(pending)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repository.root, filepath.FromSlash(target))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := repository.writeTrashMetadata(pending, trashMetadata{
		OriginalPath: "private", Type: "directory", DeletedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	data := []byte("unknown payload must retain its mapping")
	if err := os.WriteFile(filepath.Join(repository.root, filepath.FromSlash(target)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	mapping := filepath.Join(repository.root, filepath.FromSlash(pending), trashMetadataName)
	before, err := os.ReadFile(mapping)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TrashItemsWithCatalog(catalog, key, journal); !errors.Is(err, encryption.ErrTransactionNeedsRecovery) {
		t.Fatalf("ambiguous pending payload was not retained: %v", err)
	}
	after, err := os.ReadFile(mapping)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("ambiguous OriginalPath lost: %v", err)
	}
	after, err = os.ReadFile(filepath.Join(repository.root, filepath.FromSlash(target)))
	if err != nil || !bytes.Equal(data, after) {
		t.Fatalf("ambiguous payload lost: %v", err)
	}
}

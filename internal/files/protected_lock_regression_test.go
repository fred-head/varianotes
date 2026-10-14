package files

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestPreparedTrashMutationCannotCommitAfterSuccessfulLock(t *testing.T) {
	for _, action := range []string{"trash", "restore", "delete"} {
		t.Run(action, func(t *testing.T) {
			root, metadata := t.TempDir(), t.TempDir()
			writeConversionFile(t, root, "Note.md", "protected mutation sentinel")
			repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
			conversion, err := repository.PrepareExistingConversion(t.Context(), "Note.md", catalog, key, store, journal)
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
			var item TrashItem
			physical := "Note.md.age"
			if action != "trash" {
				item, err = repository.MoveToTrashWithCatalog("Note.md", catalog, key, store, journal)
				if err != nil {
					t.Fatal(err)
				}
				catalog, err = store.Load(key, catalog.VaultID)
				if err != nil {
					t.Fatal(err)
				}
				for _, object := range catalog.Objects {
					if object.Kind == encryption.CatalogObjectNote {
						physical = object.PhysicalPath
					}
				}
			}
			before, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(physical)))
			if err != nil {
				t.Fatal(err)
			}
			manager := encryption.NewKeyManager()
			defer manager.Close()
			session := sha256.Sum256([]byte("mutating session"))
			if err := manager.Unlock(catalog.VaultID, session[:], key); err != nil {
				t.Fatal(err)
			}
			lease, err := manager.BeginUse(catalog.VaultID, session[:])
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			prepared, resume := make(chan struct{}), make(chan struct{})
			calls := 0
			guarded := journal.WithCommitGuard(func(commit func() error) error {
				calls++
				if calls == 2 {
					close(prepared)
					<-resume
				}
				return lease.GuardCommit(commit)
			})
			done := make(chan error, 1)
			go func() {
				done <- lease.UseKey(func(key []byte) error {
					var err error
					switch action {
					case "trash":
						_, err = repository.MoveToTrashWithCatalog("Note.md", catalog, key, store, guarded)
					case "restore":
						_, err = repository.RestoreTrashItemWithCatalog(item.ID, MoveProtectionDecrypt, catalog, key, store, guarded)
					case "delete":
						_, err = repository.DeleteTrashItemWithCatalog(item.ID, catalog, key, store, guarded)
					}
					return err
				})
			}()
			select {
			case <-prepared:
			case err := <-done:
				t.Fatalf("did not reach final prepared boundary: %v", err)
			case <-time.After(3 * time.Second):
				close(resume)
				t.Fatal("prepare stalled")
			}
			if err := manager.LockVault(catalog.VaultID, encryption.LockReasonManual); err != nil {
				close(resume)
				t.Fatal(err)
			}
			close(resume)
			select {
			case err := <-done:
				if !errors.Is(err, encryption.ErrVaultLocked) {
					t.Fatalf("mutation committed after successful lock: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("mutation stalled after lock")
			}
			after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(physical)))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("late mutation changed original ciphertext: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "Note.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("late restore published plaintext: %v", err)
			}
			assertNoConversionStages(t, root, metadata)
		})
	}
}

func TestSaveMoveAndManualLockHaveNoLockOrderCycle(t *testing.T) {
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository, store, journal, catalog, key := newConversionFixture(t, root, metadata)
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	if err := store.Save(catalog, key); err != nil {
		t.Fatal(err)
	}
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "old")
	catalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	manager := encryption.NewKeyManager()
	defer manager.Close()
	session := sha256.Sum256([]byte("save and move session"))
	if err := manager.Unlock(catalog.VaultID, session[:], key); err != nil {
		t.Fatal(err)
	}
	save, err := manager.BeginUse(catalog.VaultID, session[:])
	if err != nil {
		t.Fatal(err)
	}
	move, err := manager.BeginUse(catalog.VaultID, session[:])
	if err != nil {
		t.Fatal(err)
	}
	defer move.Close()
	note, _, err := repository.ReadMarkdownWithCatalog("private/Note.md", catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	operation, _, err := repository.PrepareMarkdownWriteWithCatalog("private/Note.md", "new", note.Version, catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Abort()
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- move.UseKey(func(key []byte) error {
			close(started)
			_, err := repository.MoveWithCatalog("private/Note.md", "Moved.md", "", MoveProtectionKeep, catalog, key, store, journal.WithCommitGuard(move.GuardCommit))
			return err
		})
	}()
	<-started
	locked := make(chan error, 1)
	go func() { locked <- manager.LockVault(catalog.VaultID, encryption.LockReasonManual) }()
	select {
	case err := <-locked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		operation.Abort()
		t.Fatal("lock blocked behind prepared save/move")
	}
	if err := save.Commit(func() error { return operation.Commit(nil) }); !errors.Is(err, encryption.ErrVaultLocked) {
		t.Fatalf("late save accepted: %v", err)
	}
	operation.Abort()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("late move accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("save/move lock cycle")
	}
}

func TestOrdinaryTrashPathsInEncryptedVaultRejectRevokedLease(t *testing.T) {
	for _, action := range []string{"trash", "restore", "delete"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			writeConversionFile(t, root, "Note.md", "ordinary current content")
			repository, store, journal, catalog, key := newConversionFixture(t, root, t.TempDir())
			physical := "Note.md"
			var item TrashItem
			if action != "trash" {
				var err error
				item, err = repository.MoveToTrash("Note.md")
				if err != nil {
					t.Fatal(err)
				}
				resolved, err := repository.loadTrashItem(item.ID)
				if err != nil {
					t.Fatal(err)
				}
				physical = resolved.contentPath
			}
			before, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(physical)))
			if err != nil {
				t.Fatal(err)
			}
			manager := encryption.NewKeyManager()
			defer manager.Close()
			session := sha256.Sum256([]byte("ordinary mutation"))
			if err := manager.Unlock(catalog.VaultID, session[:], key); err != nil {
				t.Fatal(err)
			}
			lease, err := manager.BeginUse(catalog.VaultID, session[:])
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			err = lease.UseKey(func(key []byte) error {
				if err := manager.LockVault(catalog.VaultID, encryption.LockReasonManual); err != nil {
					return err
				}
				guarded := journal.WithCommitGuard(lease.GuardCommit)
				var err error
				switch action {
				case "trash":
					_, err = repository.MoveToTrashWithCatalog("Note.md", catalog, key, store, guarded)
				case "restore":
					_, err = repository.RestoreTrashItemWithCatalog(item.ID, "", catalog, key, store, guarded)
				case "delete":
					_, err = repository.DeleteTrashItemWithCatalog(item.ID, catalog, key, store, guarded)
				}
				return err
			})
			if !errors.Is(err, encryption.ErrVaultLocked) {
				t.Fatalf("ordinary mutation bypassed lock: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(physical)))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("revoked operation changed file: %v", err)
			}
		})
	}
}

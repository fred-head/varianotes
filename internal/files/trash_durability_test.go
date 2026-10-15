package files

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func trashDurabilityFixture(t *testing.T) (*Repository, *encryption.CatalogStore, *encryption.TransactionJournal, encryption.Catalog, []byte, string) {
	t.Helper()
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "private", "nested", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x64}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/nested/Note.md", "trash-durability-secret")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	return repository, store, journal, catalog, key, filepath.Join(metadata, "catalog.json")
}

func TestProtectedTrashDurabilityBeforeSourceDelete(t *testing.T) {
	repository, store, journal, catalog, key, catalogPath := trashDurabilityFixture(t)
	synced := map[string]bool{}
	repository.content.fault = func(phase, relative string) error {
		if phase == "directory.sync" {
			synced[relative] = true
		}
		return nil
	}
	guard := journal.WithCommitGuard(func(commit func() error) error {
		encoded, err := os.ReadFile(filepath.Join(filepath.Dir(catalogPath), "."+filepath.Base(catalogPath)+".transaction.json"))
		if err != nil {
			return err
		}
		var record struct {
			CreateDirectories []string `json:"create_directories"`
		}
		if err := json.Unmarshal(encoded, &record); err != nil {
			return err
		}
		// The initial conversion can be an empty transaction; only Trash has
		// pending metadata and publishes objects below .trash.
		entries, _ := os.ReadDir(filepath.Join(repository.root, ".trash"))
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".pending-") {
				continue
			}
			if !synced[path.Join(".trash", entry.Name())] || !synced[".trash"] {
				return errors.New("R2: restore metadata/parent not durable before commit")
			}
			if len(record.CreateDirectories) < 5 {
				return errors.New("R2: destination chain not journaled")
			}
		}
		return commit()
	})
	if _, err := repository.MoveToTrashWithCatalog("private", catalog, key, store, guard); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedTrashProcessRestart(t *testing.T) {
	if boundary := os.Getenv("VNOTES_TRASH_EXIT"); boundary != "" {
		repository := mustRepository(t, os.Getenv("VNOTES_TRASH_ROOT"))
		store, err := encryption.NewCatalogStore(os.Getenv("VNOTES_TRASH_CATALOG"))
		if err != nil {
			t.Fatal(err)
		}
		key := bytes.Repeat([]byte{0x64}, encryption.VaultKeySize)
		vaultID, err := store.ReadVaultID()
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := store.Load(key, vaultID)
		if err != nil {
			t.Fatal(err)
		}
		journal, err := encryption.NewTransactionJournal(repository.root, store)
		if err != nil {
			t.Fatal(err)
		}
		repository.content.fault = func(phase, relative string) error {
			if boundary == "pending-created" && phase == "directory.created" && strings.Contains(relative, ".pending-") {
				os.Exit(86)
			}
			if boundary == "metadata-written" && phase == "file.created" && strings.Contains(relative, ".pending-") {
				os.Exit(86)
			}
			if boundary == "metadata-renamed" && phase == "file.renamed" && strings.HasSuffix(relative, "/metadata.json") {
				os.Exit(86)
			}
			return nil
		}
		repository.transactionPrepared = func() error {
			if boundary == "directories-prepared" {
				os.Exit(86)
			}
			return nil
		}
		guard := journal.WithCommitGuard(func(commit func() error) error {
			if err := commit(); err != nil {
				return err
			}
			if boundary == "content-committed" {
				latest, err := store.Load(key, catalog.VaultID)
				if err != nil {
					return err
				}
				for _, object := range latest.Objects {
					if strings.HasPrefix(object.LogicalPath, ".trash/") {
						os.Exit(86)
					}
				}
			}
			return nil
		})
		if _, err := repository.MoveToTrashWithCatalog("private", catalog, key, store, guard); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, boundary := range []string{"pending-created", "metadata-written", "directories-prepared", "content-committed", "metadata-renamed"} {
		t.Run(boundary, func(t *testing.T) {
			repository, store, _, catalog, key, catalogPath := trashDurabilityFixture(t)
			command := exec.Command(os.Args[0], "-test.run=^TestProtectedTrashProcessRestart$")
			command.Env = append(os.Environ(), "VNOTES_TRASH_EXIT="+boundary, "VNOTES_TRASH_ROOT="+repository.root, "VNOTES_TRASH_CATALOG="+catalogPath)
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 86 {
				t.Fatalf("abrupt exit missing: %v %s", err, output)
			}
			restarted := mustRepository(t, repository.root)
			journal, err := encryption.NewTransactionJournal(repository.root, store)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.RecoverForVault(key, catalog.VaultID); err != nil {
				t.Fatal(err)
			}
			catalog, err = store.Load(key, catalog.VaultID)
			if err != nil {
				t.Fatal(err)
			}
			items, err := restarted.TrashItemsWithCatalog(catalog, key)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "pending-created" || boundary == "metadata-written" || boundary == "directories-prepared" {
				if len(items) != 0 {
					t.Fatal("uncommitted trash published")
				}
			} else {
				if len(items) != 1 {
					t.Fatalf("restore metadata lost: %#v", items)
				}
				if _, err := restarted.RestoreTrashItemWithCatalog(items[0].ID, MoveProtectionKeep, catalog, key, store, journal); err != nil {
					t.Fatal(err)
				}
				catalog, err = store.Load(key, catalog.VaultID)
				if err != nil {
					t.Fatal(err)
				}
			}
			markdown, protected, err := restarted.ReadMarkdownWithCatalog("private/nested/Note.md", catalog, key)
			if err != nil || !protected || markdown.Content != "trash-durability-secret" {
				t.Fatalf("data/protection lost: %#v %v", markdown, err)
			}
		})
	}
}

func TestProtectedTrashBarrierFailuresPreserveRestoreMapping(t *testing.T) {
	for _, at := range []string{"pending", "final-target", "final-source"} {
		t.Run(at, func(t *testing.T) {
			repository, store, journal, catalog, key, _ := trashDurabilityFixture(t)
			renamed := false
			injected := errors.New("trash sync failed")
			repository.content.fault = func(phase, relative string) error {
				if phase == "file.renamed" {
					renamed = true
				}
				if phase == "directory.sync" {
					pending := strings.Contains(relative, ".pending-")
					if at == "pending" && pending && !renamed || at == "final-target" && renamed && !pending || at == "final-source" && renamed && pending {
						return injected
					}
				}
				return nil
			}
			if _, err := repository.MoveToTrashWithCatalog("private", catalog, key, store, journal); !errors.Is(err, injected) {
				t.Fatalf("barrier not enforced: %v", err)
			}
			catalog, err := store.Load(key, catalog.VaultID)
			if err != nil {
				t.Fatal(err)
			}
			if at != "pending" {
				// Repeated failure must retain the pending recovery directory.
				if _, err := repository.TrashItemsWithCatalog(catalog, key); !errors.Is(err, injected) {
					t.Fatalf("retry skipped failed barrier: %v", err)
				}
			}
			repository.content.fault = nil
			items, err := repository.TrashItemsWithCatalog(catalog, key)
			if err != nil {
				t.Fatal(err)
			}
			if at == "pending" {
				if len(items) != 0 {
					t.Fatal("failed prepare published Trash")
				}
			} else {
				if len(items) != 1 {
					t.Fatalf("restore mapping lost: %#v", items)
				}
				if _, err := repository.RestoreTrashItemWithCatalog(items[0].ID, MoveProtectionKeep, catalog, key, store, journal); err != nil {
					t.Fatal(err)
				}
				catalog, err = store.Load(key, catalog.VaultID)
				if err != nil {
					t.Fatal(err)
				}
			}
			markdown, protected, err := repository.ReadMarkdownWithCatalog("private/nested/Note.md", catalog, key)
			if err != nil || !protected || markdown.Content != "trash-durability-secret" {
				t.Fatalf("source/restore lost: %#v %v", markdown, err)
			}
		})
	}
}

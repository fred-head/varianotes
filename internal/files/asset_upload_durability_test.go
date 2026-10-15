package files

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

const (
	assetRestartRootEnv     = "VNOTES_ASSET_RESTART_ROOT"
	assetRestartMetadataEnv = "VNOTES_ASSET_RESTART_METADATA"
)

func protectedAssetUploadFixture(t *testing.T) (*Repository, *encryption.CatalogStore, *encryption.TransactionJournal, encryption.Catalog, []byte, string, string) {
	t.Helper()
	root, metadata := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, root)
	key := bytes.Repeat([]byte{0x6b}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	addProtectedTestNote(t, repository, store, journal, catalog, key, "private/Note.md", "protected upload durability")
	catalog, err = store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	return repository, store, journal, catalog, key, root, metadata
}

func prepareProtectedAssetUpload(t *testing.T, repository *Repository, store *encryption.CatalogStore, journal *encryption.TransactionJournal, catalog encryption.Catalog, key []byte) *PreparedAssetOperation {
	t.Helper()
	operation, protected, err := repository.PrepareAssetUploadWithCatalog("private/Note.md", testPNG, catalog, key, store, journal)
	if err != nil || !protected {
		if operation != nil {
			operation.Abort()
		}
		t.Fatalf("prepare protected asset upload: protected=%v err=%v", protected, err)
	}
	return operation
}

func TestProtectedAssetUploadSyncsNewAssetsParentBeforePrepareReturns(t *testing.T) {
	repository, store, journal, catalog, key, root, _ := protectedAssetUploadFixture(t)
	assetsDirectory := filepath.Join(root, "private", "Note.assets")
	if _, err := os.Lstat(assetsDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture already has an asset directory: %v", err)
	}
	parentSyncs := 0
	repository.content.fault = func(phase, relative string) error {
		if phase == "directory.sync" && relative == "private" {
			parentSyncs++
		}
		return nil
	}

	operation := prepareProtectedAssetUpload(t, repository, store, journal, catalog, key)
	if parentSyncs != 1 {
		operation.Abort()
		t.Fatalf("parent directory syncs before upload preparation = %d, want 1", parentSyncs)
	}
	if err := operation.Commit(nil); err != nil {
		t.Fatalf("commit protected upload after parent sync: %v", err)
	}
}

func TestProtectedAssetUploadParentSyncFailureBlocksCommitAndRetryResyncs(t *testing.T) {
	repository, store, journal, catalog, key, root, _ := protectedAssetUploadFixture(t)
	injected := errors.New("injected asset parent directory sync failure")
	parentSyncs := 0
	repository.content.fault = func(phase, relative string) error {
		if phase == "directory.sync" && relative == "private" {
			parentSyncs++
			if parentSyncs == 1 {
				return injected
			}
		}
		return nil
	}

	failed, protected, err := repository.PrepareAssetUploadWithCatalog("private/Note.md", testPNG, catalog, key, store, journal)
	if failed != nil {
		failed.Abort()
	}
	if !errors.Is(err, injected) || !protected {
		t.Fatalf("failed parent sync result: protected=%v operation=%v err=%v", protected, failed, err)
	}
	assetsDirectory := filepath.Join(root, "private", "Note.assets")
	if entries, readErr := os.ReadDir(assetsDirectory); readErr != nil || len(entries) != 0 {
		t.Fatalf("failed barrier left a nonempty or missing asset directory: entries=%d err=%v", len(entries), readErr)
	}
	if err := journal.Recover(key); err != nil {
		t.Fatalf("no transaction should need recovery after parent sync failure: %v", err)
	}
	unchanged, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.Objects) != len(catalog.Objects) {
		t.Fatalf("catalog changed despite failed parent sync: objects=%d want=%d", len(unchanged.Objects), len(catalog.Objects))
	}

	// Retry with the already visible directory. The parent barrier must run
	// again before this attempt can prepare or commit the catalog transaction.
	operation := prepareProtectedAssetUpload(t, repository, store, journal, unchanged, key)
	if parentSyncs != 2 {
		operation.Abort()
		t.Fatalf("parent sync attempts after retry = %d, want 2", parentSyncs)
	}
	if err := operation.Commit(nil); err != nil {
		t.Fatalf("retry protected upload after parent sync recovery: %v", err)
	}
}

func TestProtectedAssetUploadCommittedRestartKeepsAssetAndCatalog(t *testing.T) {
	if root := os.Getenv(assetRestartRootEnv); root != "" {
		runProtectedAssetUploadRestartChild(t, root, os.Getenv(assetRestartMetadataEnv))
		return
	}

	_, _, _, _, key, root, metadata := protectedAssetUploadFixture(t)
	command := exec.Command(os.Args[0], "-test.run=^TestProtectedAssetUploadCommittedRestartKeepsAssetAndCatalog$")
	command.Env = append(os.Environ(), assetRestartRootEnv+"="+root, assetRestartMetadataEnv+"="+metadata)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 86 {
		t.Fatalf("upload child did not stop after the durable commit marker: err=%v\n%s", err, output)
	}

	store, err := encryption.NewCatalogStore(filepath.Join(metadata, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := encryption.NewTransactionJournal(root, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(key); err != nil {
		t.Fatalf("recover committed asset upload after process restart: %v", err)
	}
	catalog, err := store.Load(key, mustVaultID(t, store))
	if err != nil {
		t.Fatal(err)
	}
	var assetPath string
	for _, object := range catalog.Objects {
		if object.Kind == encryption.CatalogObjectAsset && strings.HasPrefix(object.LogicalPath, "private/Note.assets/") {
			assetPath = object.LogicalPath
		}
	}
	if assetPath == "" {
		t.Fatal("recovered catalog has no asset object under Note.assets")
	}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	asset, protected, err := repository.ReadAssetWithCatalog("private/Note.md", "Note.assets/"+filepath.Base(assetPath), catalog, key)
	if err != nil || !protected || !bytes.Equal(asset.Content, testPNG) {
		t.Fatalf("recovered protected asset: protected=%v bytesMatch=%v err=%v", protected, bytes.Equal(asset.Content, testPNG), err)
	}
}

func runProtectedAssetUploadRestartChild(t *testing.T, root, metadata string) {
	t.Helper()
	store, err := encryption.NewCatalogStore(filepath.Join(metadata, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	vaultID, err := store.ReadVaultID()
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x6b}, encryption.VaultKeySize)
	catalog, err := store.Load(key, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	parentSynced := false
	repository.content.fault = func(phase, relative string) error {
		if phase == "directory.sync" && relative == "private" {
			parentSynced = true
		}
		return nil
	}
	journal, err := encryption.NewTransactionJournal(root, store, encryption.TransactionJournalOptions{
		Fault: func(phase string) error {
			if phase == "transaction.commit.before" && !parentSynced {
				return errors.New("asset parent was not synced before transaction commit")
			}
			if phase == "transaction.commit.after" {
				// This exits after the commit marker is synced but before the
				// transaction publishes its catalog and ciphertext.
				os.Exit(86)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := prepareProtectedAssetUpload(t, repository, store, journal, catalog, key)
	if err := operation.Commit(nil); err != nil {
		t.Fatalf("commit upload after parent barrier: %v", err)
	}
	t.Fatal("expected process exit at committed transaction boundary")
}

func mustVaultID(t *testing.T, store *encryption.CatalogStore) string {
	t.Helper()
	vaultID, err := store.ReadVaultID()
	if err != nil {
		t.Fatal(err)
	}
	return vaultID
}

package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestCompleteBackupRecoversFilesWithStandaloneAgeTool(t *testing.T) {
	_, err := exec.LookPath("age")
	if err != nil {
		t.Skip("CI builds the official age CLI for full external-recovery coverage")
	}

	base := t.TempDir()
	vaultRoot := filepath.Join(base, "live-vault")
	metadataRoot := filepath.Join(base, "live-app-metadata")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "private", "Note.assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(metadataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	note := []byte("# P17 external recovery\nUnicode: Grüße, 世界\n")
	asset := append([]byte(nil), testPNG...)
	public := []byte("ordinary Markdown remains ordinary\n")
	write := func(filename string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filename, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vaultRoot, "private", "Note.md"), note)
	write(filepath.Join(vaultRoot, "private", "Note.assets", "image.png"), asset)
	write(filepath.Join(vaultRoot, "Public.md"), public)

	vaultID := "p17-external-recovery-vault"
	registry := struct {
		ActiveID string `json:"active_id"`
		Entries  []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			LocalPath string `json:"local_path"`
		} `json:"entries"`
	}{ActiveID: vaultID}
	registry.Entries = append(registry.Entries, struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		LocalPath string `json:"local_path"`
	}{ID: vaultID, Name: "P17 backup fixture", LocalPath: vaultRoot})
	registryBytes, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(metadataRoot, "notebooks.json"), registryBytes)
	metadataID := sha256.Sum256([]byte(vaultID))
	encryptionMetadata := filepath.Join(metadataRoot, "encryption", hex.EncodeToString(metadataID[:]))
	key, err := encryption.NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	store, err := encryption.NewCatalogStore(filepath.Join(encryptionMetadata, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(catalog, key); err != nil {
		t.Fatal(err)
	}
	const password = "p17 complete backup password"
	envelope, err := encryption.EncryptVaultKey(password, key)
	if err != nil {
		t.Fatal(err)
	}
	envelopeStore, err := encryption.NewVaultKeyEnvelopeStore(filepath.Join(encryptionMetadata, "key-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := envelopeStore.Save(envelope); err != nil {
		t.Fatal(err)
	}
	journal, err := encryption.NewTransactionJournal(vaultRoot, store)
	if err != nil {
		t.Fatal(err)
	}
	repository := mustRepository(t, vaultRoot)
	conversion, err := repository.PrepareExistingConversion(context.Background(), "private", catalog, key, store, journal)
	if err != nil {
		t.Fatal(err)
	}
	if conversion.Summary.Notes != 1 || conversion.Summary.Attachments != 1 {
		t.Fatalf("fixture conversion summary = %#v", conversion.Summary)
	}
	if err := conversion.Commit(); err != nil {
		t.Fatal(err)
	}

	// The backup pairs the complete notebook tree with the matching application
	// metadata, including the authenticated catalog and password envelope.
	backupRoot := filepath.Join(base, "backup")
	backupVault := filepath.Join(backupRoot, "vault")
	backupMetadata := filepath.Join(backupRoot, "app")
	copyP17BackupTree(t, vaultRoot, backupVault)
	copyP17BackupTree(t, metadataRoot, backupMetadata)
	output := filepath.Join(base, "externally-restored-vault")
	command := exec.Command("go", "run", "./externalrestore",
		"--vault", backupVault,
		"--encryption-metadata", filepath.Join(backupMetadata, "encryption", hex.EncodeToString(metadataID[:])),
		"--password", password,
		"--output", output,
	)
	result, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("standalone external recovery failed: %v\n%s", err, strings.TrimSpace(string(result)))
	}
	for name, want := range map[string][]byte{
		"private/Note.md":               note,
		"private/Note.assets/image.png": asset,
		"Public.md":                     public,
	} {
		got, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("external recovery for %q changed bytes: got %d bytes, want %d, err=%v", name, len(got), len(want), err)
		}
	}
	if err := filepath.WalkDir(output, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".age") {
			t.Errorf("external recovery left ciphertext in the restored tree: %s", filename)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func copyP17BackupTree(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o700)
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return os.ErrInvalid
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy full backup: %v", err)
	}
}

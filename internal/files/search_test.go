package files

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestProtectedSearchUsesCatalogWithoutPersistentPlaintext(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "vault")
	metadata := filepath.Join(base, "metadata")
	temporary := filepath.Join(base, "temporary")
	for _, directory := range []string{root, metadata, temporary, filepath.Join(root, "private")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", temporary)
	const sentinel = "protected-search-sentinel-7e3f1"
	const password = "search-password-sentinel-9c2a"
	key := bytes.Repeat([]byte{0x64}, encryption.VaultKeySize)
	envelope, err := encryption.EncryptVaultKey(password, key)
	if err != nil {
		t.Fatal(err)
	}
	envelopeStore, err := encryption.NewVaultKeyEnvelopeStore(filepath.Join(metadata, "key-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := envelopeStore.Save(envelope); err != nil {
		t.Fatal(err)
	}
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	repository, err := NewRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	store, journal := protectedFixture(t, root, metadata, catalog, key)
	create, protected, err := repository.PrepareMarkdownCreateWithCatalog("private/Hidden.md", catalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected note: protected=%t err=%v", protected, err)
	}
	if err := create.Commit(nil); err != nil {
		t.Fatal(err)
	}
	createdCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := repository.ReadMarkdownWithCatalog("private/Hidden.md", createdCatalog, key)
	if err != nil {
		t.Fatal(err)
	}
	write, protected, err := repository.PrepareMarkdownWriteWithCatalog("private/Hidden.md", "# Protected\n"+sentinel, created.Version, createdCatalog, key, store, journal)
	if err != nil || !protected {
		t.Fatalf("prepare protected search content: protected=%t err=%v", protected, err)
	}
	if err := write.Commit(nil); err != nil {
		t.Fatal(err)
	}
	currentCatalog, err := store.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}

	results, err := repository.SearchWithCatalog("sentinel", currentCatalog, key)
	if err != nil || len(results) != 1 || results[0].Path != "private/Hidden.md" || results[0].Type != "content" || results[0].Line != 2 || !strings.Contains(results[0].Excerpt, sentinel) {
		t.Fatalf("protected search results=%#v err=%v", results, err)
	}

	for _, directory := range []string{root, metadata, temporary} {
		err := filepath.WalkDir(directory, func(filename string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			content, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			if bytes.Contains(content, []byte(sentinel)) || bytes.Contains(content, []byte(password)) {
				t.Errorf("plaintext search or password sentinel persisted in %s", filename)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	ciphertextPath := filepath.Join(root, "private", "Hidden.md.age")
	ciphertext, err := os.ReadFile(ciphertextPath)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)-1] ^= 0xff
	if err := os.WriteFile(ciphertextPath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err = repository.SearchWithCatalog("sentinel", currentCatalog, key)
	if !errors.Is(err, ErrProtectedContentCorrupt) || results != nil {
		t.Fatalf("corrupt protected note returned results=%#v err=%v", results, err)
	}
	if _, err := repository.Search(strings.Repeat("q", maxSearchQueryBytes+1)); !errors.Is(err, ErrSearchQueryTooLong) {
		t.Fatalf("oversized search query error=%v", err)
	}
}

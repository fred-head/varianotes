package encryption

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogRoundTripIsAuthenticatedAndCanonical(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, VaultKeySize)
	catalog := catalogFixture(t)
	first, err := MarshalCatalog(catalog, key)
	if err != nil {
		t.Fatal(err)
	}
	second := catalog
	second.Objects = []CatalogObject{catalog.Objects[1], catalog.Objects[0]}
	second.FolderRules = []FolderProtectionRule{{Path: "empty"}, {Path: "private"}}
	canonical, err := MarshalCatalog(second, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, canonical) {
		t.Fatal("equivalent catalogs did not have a stable representation")
	}

	decoded, err := ParseCatalog(first, key, catalog.VaultID)
	if err != nil {
		t.Fatalf("parse authenticated catalog: %v", err)
	}
	if decoded.VaultID != catalog.VaultID || len(decoded.Objects) != 2 || len(decoded.FolderRules) != 2 {
		t.Fatalf("decoded catalog lost metadata: %#v", decoded)
	}
	if decoded.FolderRules[0].Path != "empty" || decoded.Objects[0].LogicalPath != "private/note.md" {
		t.Fatalf("decoded catalog was not canonical: %#v", decoded)
	}
}

func TestCatalogIDsAreRandomOpaqueValues(t *testing.T) {
	first, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	objectID, err := NewCatalogObjectID()
	if err != nil {
		t.Fatal(err)
	}
	if first.VaultID == second.VaultID || first.VaultID == objectID || !isCatalogID(objectID) {
		t.Fatalf("generated IDs were not distinct valid opaque IDs: %q, %q, %q", first.VaultID, second.VaultID, objectID)
	}
}

func TestCatalogRejectsTamperingWrongVaultAndUnsupportedVersion(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, VaultKeySize)
	catalog := catalogFixture(t)
	encoded, err := MarshalCatalog(catalog, key)
	if err != nil {
		t.Fatal(err)
	}

	originalDigest := CiphertextSHA256([]byte("encrypted note"))
	tamperedDigest := strings.Repeat("0", len(originalDigest))
	if tamperedDigest == originalDigest {
		tamperedDigest = strings.Repeat("1", len(originalDigest))
	}
	tampered := bytes.Replace(encoded, []byte(originalDigest), []byte(tamperedDigest), 1)
	if _, err := ParseCatalog(tampered, key, catalog.VaultID); !errors.Is(err, ErrCatalogAuthentication) {
		t.Fatalf("tampered digest error = %v", err)
	}

	var missingWire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &missingWire); err != nil {
		t.Fatal(err)
	}
	missingWire["objects"] = json.RawMessage("[]")
	missingEntry, err := json.Marshal(missingWire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(missingEntry, key, catalog.VaultID); !errors.Is(err, ErrCatalogAuthentication) {
		t.Fatalf("missing object entry error = %v", err)
	}
	if _, err := ParseCatalog(encoded, key, "00000000000000000000000000000001"); !errors.Is(err, ErrCatalogAuthentication) {
		t.Fatalf("wrong vault ID error = %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	wire["version"] = float64(CatalogFormatVersion + 1)
	unsupported, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(unsupported, key, catalog.VaultID); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("unsupported version error = %v", err)
	}

	duplicateField := bytes.Replace(encoded, []byte(`"kind":"note"`), []byte(`"kind":"note","kind":"note"`), 1)
	if _, err := ParseCatalog(duplicateField, key, catalog.VaultID); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("duplicate nested field error = %v", err)
	}
}

func TestCatalogStoreRetainsEmptyProtectedFolderRulesAcrossRestart(t *testing.T) {
	metadataDirectory := filepath.Join(t.TempDir(), "application", "vaults")
	storePath := filepath.Join(metadataDirectory, "catalog.json")
	store, err := NewCatalogStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x24}, VaultKeySize)
	catalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []FolderProtectionRule{{Path: "empty-protected-folder"}}
	if err := store.Save(catalog, key); err != nil {
		t.Fatalf("save catalog: %v", err)
	}

	// A new store represents a process restart; the folder rule remains durable
	// despite there being no object below the folder.
	restarted, err := NewCatalogStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatalf("load catalog after restart: %v", err)
	}
	if len(loaded.Objects) != 0 || len(loaded.FolderRules) != 1 || loaded.FolderRules[0].Path != "empty-protected-folder" {
		t.Fatalf("empty folder rule did not persist: %#v", loaded)
	}
	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestCatalogStoreMissingFileFailsClosed(t *testing.T) {
	store, err := NewCatalogStore(filepath.Join(t.TempDir(), "missing", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load(bytes.Repeat([]byte{1}, VaultKeySize), "00000000000000000000000000000001")
	if !errors.Is(err, ErrCatalogMissing) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing catalog error = %v", err)
	}
}

func TestCatalogFolderRulesApplyRecursivelyToFuturePaths(t *testing.T) {
	catalog := catalogFixture(t)
	for _, test := range []struct {
		path      string
		protected bool
	}{
		{path: "private", protected: true},
		{path: "private/new/deep/note.md", protected: true},
		{path: "empty/future.md", protected: true},
		{path: "public.md", protected: false},
		{path: "outside/note.md", protected: false},
	} {
		protected, err := catalog.IsPathProtected(test.path)
		if err != nil {
			t.Fatalf("IsPathProtected(%q): %v", test.path, err)
		}
		if protected != test.protected {
			t.Errorf("IsPathProtected(%q) = %v, want %v", test.path, protected, test.protected)
		}
	}
}

func TestCatalogDetectsAgePathCollisionsAndPlaintextRuleExceptions(t *testing.T) {
	catalog := catalogFixture(t)
	catalog.Objects = append(catalog.Objects, CatalogObject{
		ObjectID: "00000000000000000000000000000003", Kind: CatalogObjectNote,
		LogicalPath: "private/note.md.age", PhysicalPath: "private/note.md.age",
	})
	if _, err := MarshalCatalog(catalog, bytes.Repeat([]byte{0x42}, VaultKeySize)); !errors.Is(err, ErrCatalogPathCollision) {
		t.Fatalf(".age collision error = %v", err)
	}

	catalog = catalogFixture(t)
	catalog.Objects[0].Protected = false
	catalog.Objects[0].PhysicalPath = catalog.Objects[0].LogicalPath
	catalog.Objects[0].CiphertextHash = ""
	if _, err := MarshalCatalog(catalog, bytes.Repeat([]byte{0x42}, VaultKeySize)); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("plaintext exception under recursive rule error = %v", err)
	}
}

func TestProtectedCatalogObjectRequiresObjectBoundContentKeyEnvelope(t *testing.T) {
	catalog := catalogFixture(t)
	catalog.Objects[0].ContentKeyEnvelope = nil
	if _, err := MarshalCatalog(catalog, bytes.Repeat([]byte{0x42}, VaultKeySize)); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("missing per-file key envelope error = %v", err)
	}
	catalog = catalogFixture(t)
	wrongObject := *catalog.Objects[0].ContentKeyEnvelope
	wrongObject.ObjectID = "00000000000000000000000000000002"
	catalog.Objects[0].ContentKeyEnvelope = &wrongObject
	if _, err := MarshalCatalog(catalog, bytes.Repeat([]byte{0x42}, VaultKeySize)); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("misbound per-file key envelope error = %v", err)
	}
}

func TestCatalogVerifiesSwappedCiphertextAndContainsMetadataOnly(t *testing.T) {
	firstCiphertext := []byte("ciphertext for first object")
	secondCiphertext := []byte("ciphertext for second object")
	object := CatalogObject{
		ObjectID: "00000000000000000000000000000001", Kind: CatalogObjectNote,
		LogicalPath: "private/note.md", PhysicalPath: "private/note.md.age",
		CiphertextHash: CiphertextSHA256(firstCiphertext), ContentKeyEnvelope: testContentKeyEnvelope("00000000000000000000000000000001"), Protected: true,
	}
	if err := VerifyCatalogCiphertext(object, firstCiphertext); err != nil {
		t.Fatalf("matching ciphertext rejected: %v", err)
	}
	if err := VerifyCatalogCiphertext(object, secondCiphertext); !errors.Is(err, ErrCatalogCiphertextCheck) {
		t.Fatalf("swapped ciphertext error = %v", err)
	}

	catalog := catalogFixture(t)
	encoded, err := MarshalCatalog(catalog, bytes.Repeat([]byte{0x42}, VaultKeySize))
	if err != nil {
		t.Fatal(err)
	}
	plaintextFingerprint := CiphertextSHA256([]byte("note plaintext sentinel"))
	for _, forbidden := range []string{"note plaintext sentinel", "preview sentinel", "plaintext fingerprint", "plaintext_sha256", "preview", plaintextFingerprint} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("catalog contains forbidden plaintext metadata %q", forbidden)
		}
	}
}

func TestCatalogStoreRejectsSymlinkTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "catalog.json")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store, err := NewCatalogStore(target)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(catalog, bytes.Repeat([]byte{0x42}, VaultKeySize)); !errors.Is(err, ErrInvalidCatalogStore) {
		t.Fatalf("symlink target error = %v", err)
	}
	content, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "outside" {
		t.Fatalf("symlink target was modified: %q", content)
	}
}

func catalogFixture(t *testing.T) Catalog {
	t.Helper()
	catalog := Catalog{
		Version: CatalogFormatVersion,
		VaultID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Objects: []CatalogObject{
			{
				ObjectID: "00000000000000000000000000000001", Kind: CatalogObjectNote,
				LogicalPath: "private/note.md", PhysicalPath: "private/note.md.age",
				CiphertextHash: CiphertextSHA256([]byte("encrypted note")), ContentKeyEnvelope: testContentKeyEnvelope("00000000000000000000000000000001"), Protected: true,
			},
			{
				ObjectID: "00000000000000000000000000000002", Kind: CatalogObjectNote,
				LogicalPath: "public.md", PhysicalPath: "public.md",
			},
		},
		FolderRules: []FolderProtectionRule{{Path: "private"}, {Path: "empty"}},
	}
	if _, err := MarshalCatalog(catalog, bytes.Repeat([]byte{0x42}, VaultKeySize)); err != nil {
		t.Fatalf("invalid catalog fixture: %v", err)
	}
	return catalog
}

func testContentKeyEnvelope(objectID string) *ContentKeyEnvelope {
	return &ContentKeyEnvelope{Version: FormatVersion, ObjectID: objectID, Nonce: make([]byte, 24), Ciphertext: []byte("wrapped test key")}
}

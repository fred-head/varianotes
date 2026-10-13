package encryption

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// CatalogStore persists one authenticated catalog in application metadata.
// The caller supplies a path in the server-owned metadata area, outside the
// ordinary vault tree. A single backend writer per vault is supported.
type CatalogStore struct {
	directory string
	name      string
	mu        sync.Mutex
}

// NewCatalogStore binds a catalog to a server-owned metadata file. The file is
// opened relative to its parent directory so temporary writes and replacement
// remain in that directory.
func NewCatalogStore(filename string) (*CatalogStore, error) {
	if filename == "" {
		return nil, ErrInvalidCatalogStore
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve encryption catalog path: %w", err)
	}
	clean := filepath.Clean(absolute)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, ErrInvalidCatalogStore
	}
	return &CatalogStore{directory: filepath.Dir(clean), name: name}, nil
}

// Save atomically replaces the authenticated catalog file with owner-only
// permissions. The vault key is used only to authenticate serialized metadata;
// it is never written to the catalog.
func (s *CatalogStore) Save(catalog Catalog, vaultKey []byte) error {
	if s == nil {
		return ErrInvalidCatalogStore
	}
	encoded, err := MarshalCatalog(catalog, vaultKey)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("create encryption metadata directory: %w", err)
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return fmt.Errorf("open encryption metadata directory: %w", err)
	}
	defer root.Close()
	if err := validateCatalogStoreTarget(root, s.name); err != nil {
		return err
	}
	temporaryName, err := createCatalogTemporary(root, encoded)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(temporaryName)
		}
	}()
	if err := root.Rename(temporaryName, s.name); err != nil {
		return fmt.Errorf("replace encryption catalog: %w", err)
	}
	committed = true
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open encryption metadata directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync encryption metadata directory: %w", err)
	}
	return nil
}

// Load reads and authenticates the catalog. Missing, malformed, unsupported,
// or unauthenticated metadata is returned as an error so callers cannot fall
// back to treating protected content as plaintext.
func (s *CatalogStore) Load(vaultKey []byte, expectedVaultID string) (Catalog, error) {
	if s == nil {
		return Catalog{}, ErrInvalidCatalogStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Catalog{}, errors.Join(ErrCatalogMissing, err)
		}
		return Catalog{}, fmt.Errorf("open encryption metadata directory: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(s.name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Catalog{}, errors.Join(ErrCatalogMissing, err)
		}
		return Catalog{}, fmt.Errorf("inspect encryption catalog: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Catalog{}, ErrInvalidCatalogStore
	}
	if info.Size() < 0 {
		return Catalog{}, ErrInvalidCatalog
	}
	if info.Size() > maxCatalogBytes {
		return Catalog{}, ErrCatalogTooLarge
	}
	file, err := root.Open(s.name)
	if err != nil {
		return Catalog{}, fmt.Errorf("open encryption catalog: %w", err)
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return Catalog{}, fmt.Errorf("inspect opened encryption catalog: %w", statErr)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return Catalog{}, ErrInvalidCatalogStore
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return Catalog{}, fmt.Errorf("read encryption catalog: %w", err)
	}
	if len(data) > maxCatalogBytes {
		return Catalog{}, ErrCatalogTooLarge
	}
	return ParseCatalog(data, vaultKey, expectedVaultID)
}

// ReadVaultID returns the untrusted identity field needed to select the
// expected ID for Load. Callers must still call Load with the vault key; this
// method alone does not authenticate the catalog.
func (s *CatalogStore) ReadVaultID() (string, error) {
	if s == nil {
		return "", ErrInvalidCatalogStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.Join(ErrCatalogMissing, err)
		}
		return "", fmt.Errorf("open encryption metadata directory: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(s.name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.Join(ErrCatalogMissing, err)
		}
		return "", fmt.Errorf("inspect encryption catalog: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCatalogBytes {
		return "", ErrInvalidCatalogStore
	}
	file, err := root.Open(s.name)
	if err != nil {
		return "", fmt.Errorf("open encryption catalog: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return "", ErrInvalidCatalogStore
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", fmt.Errorf("read encryption catalog: %w", err)
	}
	var parsed authenticatedCatalog
	if err := decodeCatalogJSON(data, &parsed); err != nil {
		return "", err
	}
	if parsed.Version != CatalogFormatVersion || !isCatalogID(parsed.VaultID) {
		return "", ErrInvalidCatalog
	}
	return parsed.VaultID, nil
}

func validateCatalogStoreTarget(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect encryption catalog target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidCatalogStore
	}
	return nil
}

func createCatalogTemporary(root *os.Root, encoded []byte) (string, error) {
	for range 8 {
		identifier, err := newCatalogID()
		if err != nil {
			return "", err
		}
		name := ".catalog-" + identifier + ".tmp"
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create temporary encryption catalog: %w", err)
		}
		written, writeErr := file.Write(encoded)
		if writeErr == nil && written != len(encoded) {
			writeErr = io.ErrShortWrite
		}
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_ = root.Remove(name)
			return "", fmt.Errorf("write temporary encryption catalog: %w", err)
		}
		return name, nil
	}
	return "", errors.New("could not allocate a temporary encryption catalog name")
}

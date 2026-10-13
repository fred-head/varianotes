package encryption

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

var ErrInvalidKeyEnvelopeStore = errors.New("invalid vault key-envelope store")

// VaultKeyEnvelopeStore persists one password envelope in server-owned
// application metadata. It never stores the plaintext vault key or password.
type VaultKeyEnvelopeStore struct {
	directory string
	name      string
	mu        sync.Mutex
}

func NewVaultKeyEnvelopeStore(filename string) (*VaultKeyEnvelopeStore, error) {
	if filename == "" {
		return nil, ErrInvalidKeyEnvelopeStore
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve vault key-envelope path: %w", err)
	}
	clean := filepath.Clean(absolute)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return nil, ErrInvalidKeyEnvelopeStore
	}
	return &VaultKeyEnvelopeStore{directory: filepath.Dir(clean), name: name}, nil
}

func (s *VaultKeyEnvelopeStore) Load() (VaultKeyEnvelope, error) {
	if s == nil {
		return VaultKeyEnvelope{}, ErrInvalidKeyEnvelopeStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateKeyEnvelopeDirectory(s.directory); err != nil {
		return VaultKeyEnvelope{}, err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return VaultKeyEnvelope{}, fmt.Errorf("open key-envelope metadata directory: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(s.name)
	if err != nil {
		return VaultKeyEnvelope{}, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxEnvelopeBytes*2 {
		return VaultKeyEnvelope{}, ErrInvalidKeyEnvelopeStore
	}
	file, err := root.Open(s.name)
	if err != nil {
		return VaultKeyEnvelope{}, fmt.Errorf("open vault key envelope: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return VaultKeyEnvelope{}, ErrInvalidKeyEnvelopeStore
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxEnvelopeBytes*2+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return VaultKeyEnvelope{}, fmt.Errorf("read vault key envelope: %w", err)
	}
	if len(data) > maxEnvelopeBytes*2 {
		return VaultKeyEnvelope{}, ErrInvalidKeyEnvelopeStore
	}
	if err := rejectDuplicateJSONFieldsRecursive(data); err != nil {
		return VaultKeyEnvelope{}, errors.Join(ErrInvalidKeyEnvelopeStore, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope VaultKeyEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return VaultKeyEnvelope{}, errors.Join(ErrInvalidKeyEnvelopeStore, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return VaultKeyEnvelope{}, ErrInvalidKeyEnvelopeStore
	}
	return envelope, nil
}

// Save atomically replaces an envelope using owner-only permissions.
func (s *VaultKeyEnvelopeStore) Save(envelope VaultKeyEnvelope) error {
	if s == nil {
		return ErrInvalidKeyEnvelopeStore
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(encoded) > maxEnvelopeBytes*2 {
		return ErrInvalidKeyEnvelopeStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("create key-envelope metadata directory: %w", err)
	}
	if err := validateKeyEnvelopeDirectory(s.directory); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return fmt.Errorf("open key-envelope metadata directory: %w", err)
	}
	defer root.Close()
	if err := validateKeyEnvelopeTarget(root, s.name); err != nil {
		return err
	}
	temporary, err := createKeyEnvelopeTemporary(root, encoded)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(temporary)
		}
	}()
	if err := root.Rename(temporary, s.name); err != nil {
		return fmt.Errorf("replace vault key envelope: %w", err)
	}
	committed = true
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open key-envelope metadata directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync key-envelope metadata directory: %w", err)
	}
	return nil
}

// Delete removes an envelope created by a recovery-code transaction and
// syncs the metadata directory. A missing target is already deleted.
func (s *VaultKeyEnvelopeStore) Delete() error {
	if s == nil {
		return ErrInvalidKeyEnvelopeStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateKeyEnvelopeDirectory(s.directory); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return fmt.Errorf("open key-envelope metadata directory: %w", err)
	}
	defer root.Close()
	if err := validateKeyEnvelopeTarget(root, s.name); err != nil {
		return err
	}
	if err := root.Remove(s.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove vault key envelope: %w", err)
	}
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open key-envelope metadata directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync key-envelope metadata directory: %w", err)
	}
	return nil
}

func validateKeyEnvelopeDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidKeyEnvelopeStore
	}
	return nil
}

func validateKeyEnvelopeTarget(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect vault key envelope: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidKeyEnvelopeStore
	}
	return nil
}

func createKeyEnvelopeTemporary(root *os.Root, encoded []byte) (string, error) {
	for range 8 {
		id, err := newCatalogID()
		if err != nil {
			return "", err
		}
		name := ".key-envelope-" + id + ".tmp"
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create temporary vault key envelope: %w", err)
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
			return "", fmt.Errorf("write temporary vault key envelope: %w", err)
		}
		return name, nil
	}
	return "", errors.New("could not allocate a temporary vault key-envelope name")
}

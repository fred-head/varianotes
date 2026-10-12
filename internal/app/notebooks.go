package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var notebookRegistryMu sync.Mutex

type notebookRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	LocalPath string `json:"localPath"`
}

type notebookRegistry struct {
	ActiveID string           `json:"activeId"`
	Entries  []notebookRecord `json:"notebooks"`
}

func loadActiveNotebook(metadataPath string) (notebookRecord, error) {
	if metadataPath == "" {
		return notebookRecord{}, os.ErrNotExist
	}
	// #nosec G304,G703 -- this is an operator-configured server metadata path, never an HTTP/request path.
	content, err := os.ReadFile(metadataPath)
	if err != nil {
		return notebookRecord{}, err
	}
	var registry notebookRegistry
	if err := json.Unmarshal(content, &registry); err != nil {
		return notebookRecord{}, err
	}
	for _, entry := range registry.Entries {
		if entry.ID == registry.ActiveID {
			return entry, nil
		}
	}
	return notebookRecord{}, errors.New("active notebook metadata is missing")
}

func loadNotebookRegistry(metadataPath string) (notebookRegistry, error) {
	if metadataPath == "" {
		return notebookRegistry{}, os.ErrNotExist
	}
	// #nosec G304,G703 -- this is an operator-configured server metadata path, never an HTTP/request path.
	content, err := os.ReadFile(metadataPath)
	if err != nil {
		return notebookRegistry{}, err
	}
	var registry notebookRegistry
	if err := json.Unmarshal(content, &registry); err != nil {
		return notebookRegistry{}, err
	}
	return registry, nil
}

func registerActiveNotebook(metadataPath string, record notebookRecord) error {
	notebookRegistryMu.Lock()
	defer notebookRegistryMu.Unlock()
	if metadataPath == "" {
		return errors.New("notebook metadata path is not configured")
	}
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	registry.ActiveID = record.ID
	replaced := false
	for index, entry := range registry.Entries {
		if entry.ID == record.ID {
			registry.Entries[index] = record
			replaced = true
			break
		}
	}
	if !replaced {
		registry.Entries = append(registry.Entries, record)
	}
	return writeNotebookRegistry(metadataPath, registry)
}

func registerNotebookWithoutActivation(metadataPath string, record notebookRecord) error {
	notebookRegistryMu.Lock()
	defer notebookRegistryMu.Unlock()
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil {
		return err
	}
	for _, entry := range registry.Entries {
		if entry.ID == record.ID {
			return nil
		}
	}
	registry.Entries = append(registry.Entries, record)
	return writeNotebookRegistry(metadataPath, registry)
}

func removeManagedNotebookDirectory(baseDirectory, notebookID, localPath string) {
	if !isManagedNotebookID(notebookID) {
		return
	}
	base, err := filepath.EvalSymlinks(baseDirectory)
	if err != nil {
		return
	}
	target := filepath.Join(base, notebookID)
	providedPath, err := filepath.Abs(localPath)
	if err != nil {
		return
	}
	expectedPath, err := filepath.Abs(target)
	if err != nil || filepath.Clean(providedPath) != filepath.Clean(expectedPath) {
		return
	}
	// #nosec G703 -- notebookID must be a generated 128-bit hex ID and localPath must equal its direct child of the canonical managed root.
	info, err := os.Lstat(expectedPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	// #nosec G703 -- notebookID must be a generated 128-bit hex ID and localPath must equal its direct child of the canonical managed root.
	_ = os.RemoveAll(expectedPath)
}

func isManagedNotebookID(notebookID string) bool {
	if len(notebookID) != 32 || notebookID != strings.ToLower(notebookID) {
		return false
	}
	_, err := hex.DecodeString(notebookID)
	return err == nil
}

func findNotebook(metadataPath, notebookID string) (notebookRecord, error) {
	notebookRegistryMu.Lock()
	defer notebookRegistryMu.Unlock()
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil {
		return notebookRecord{}, err
	}
	for _, entry := range registry.Entries {
		if entry.ID == notebookID {
			return entry, nil
		}
	}
	return notebookRecord{}, os.ErrNotExist
}

func setActiveNotebook(metadataPath, notebookID string) error {
	notebookRegistryMu.Lock()
	defer notebookRegistryMu.Unlock()
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil {
		return err
	}
	registry.ActiveID = notebookID
	return writeNotebookRegistry(metadataPath, registry)
}

func removeLocalNotebook(metadataPath, notebookID string) error {
	notebookRegistryMu.Lock()
	defer notebookRegistryMu.Unlock()
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil {
		return err
	}
	if registry.ActiveID == notebookID && len(registry.Entries) > 1 {
		return errors.New("active notebook cannot be removed")
	}
	for index, entry := range registry.Entries {
		if entry.ID != notebookID {
			continue
		}
		registry.Entries = append(registry.Entries[:index], registry.Entries[index+1:]...)
		if registry.ActiveID == notebookID {
			registry.ActiveID = ""
		}
		return writeNotebookRegistry(metadataPath, registry)
	}
	return os.ErrNotExist
}

func renameNotebook(metadataPath, notebookID, name string) (notebookRecord, error) {
	notebookRegistryMu.Lock()
	defer notebookRegistryMu.Unlock()
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil {
		return notebookRecord{}, err
	}
	for index := range registry.Entries {
		if registry.Entries[index].ID != notebookID {
			continue
		}
		registry.Entries[index].Name = name
		if err := writeNotebookRegistry(metadataPath, registry); err != nil {
			return notebookRecord{}, err
		}
		return registry.Entries[index], nil
	}
	return notebookRecord{}, os.ErrNotExist
}

func createNotebookDirectory(baseDirectory string) (string, string, error) {
	base, err := filepath.Abs(baseDirectory)
	if err != nil {
		return "", "", err
	}
	// #nosec G703 -- baseDirectory is operator-configured server storage, never a request path.
	if err := os.MkdirAll(base, 0o750); err != nil {
		return "", "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", "", err
	}
	for range 4 {
		id, err := newNotebookID()
		if err != nil {
			return "", "", err
		}
		target := filepath.Join(base, id)
		// #nosec G703 -- id is a generated 128-bit hex identifier and base is the canonical configured notebook root.
		if err := os.Mkdir(target, 0o750); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", "", err
		}
		return id, target, nil
	}
	return "", "", errors.New("could not allocate a unique notebook directory")
}

func newNotebookID() (string, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", err
	}
	return hex.EncodeToString(identifier), nil
}

// adoptNotebookDirectory accepts an existing direct child of the configured
// notebook root. Refusing nested paths and symlinks keeps later deletion
// checks confined to a single filesystem component.
func adoptNotebookDirectory(baseDirectory, folderName string) (string, error) {
	if folderName == "" || folderName == "." || strings.Contains(folderName, "..") || strings.ContainsAny(folderName, "/\\\x00") || filepath.IsAbs(folderName) {
		return "", errors.New("choose a direct child folder inside the notebook directory")
	}
	base, err := filepath.Abs(baseDirectory)
	if err != nil {
		return "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, folderName)
	// #nosec G703 -- folderName is validated as one path component, and base is the canonical configured notebook root.
	info, err := os.Lstat(target)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("the selected vault must be an existing folder, not a symlink")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(resolved))
	if err != nil || filepath.Clean(parent) != filepath.Clean(base) {
		return "", errors.New("the selected vault must be directly inside the notebook directory")
	}
	return resolved, nil
}

func isManagedNotebookDirectory(baseDirectory, localPath string) bool {
	base, err := filepath.Abs(baseDirectory)
	if err != nil {
		return false
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return false
	}
	target, err := filepath.Abs(localPath)
	if err != nil {
		return false
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(resolved))
	if err != nil || filepath.Clean(parent) != filepath.Clean(base) {
		return false
	}
	return filepath.Clean(resolved) != filepath.Clean(base)
}

func writeNotebookRegistry(metadataPath string, registry notebookRegistry) error {
	content, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return writeNotebookRegistryContent(metadataPath, content)
}

func writeNotebookRegistryContent(metadataPath string, content []byte) error {
	// #nosec G301,G703 -- the operator-selected metadata parent contains no canonical notes; the file itself is created 0600.
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(metadataPath), ".notebooks-*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// #nosec G703 -- both names are in the same operator-configured directory; the temporary name is generated by os.CreateTemp.
	return os.Rename(temporaryName, metadataPath)
}

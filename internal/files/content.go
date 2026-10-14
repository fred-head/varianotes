package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

const internalTransactionStagePrefix = ".vnotes-txn-"

// contentStore is the shared boundary for reading and replacing ordinary vault
// files. Higher-level operations choose which paths and sizes are appropriate;
// this layer owns confinement, version checks, atomic replacement, and
// coordination with other repository mutations.
type contentStore struct {
	root string
	mu   sync.RWMutex
	// Private fault seam for filesystem interruption regressions.
	fault func(phase, relative string) error
}

type contentFile struct {
	Bytes   []byte
	Version string
	mode    fs.FileMode
	info    os.FileInfo
}

// contentEntry describes one confined path in the vault. Info refers to the
// directory entry itself, so symlinks remain visible to callers and are never
// followed during enumeration.
type contentEntry struct {
	Path string
	Name string
	Info os.FileInfo
}

func (s *contentStore) Read(relative string, maximumSize int64) (contentFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.read(relative, maximumSize)
}

func (s *contentStore) read(relative string, maximumSize int64) (contentFile, error) {
	root, info, err := s.openExisting(relative, maximumSize)
	if err != nil {
		return contentFile{}, err
	}
	defer root.Close()

	file, openedInfo, err := openConfinedRegularFile(root, filepath.FromSlash(relative), info, maximumSize)
	if err != nil {
		return contentFile{}, err
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maximumSize+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return contentFile{}, err
	}
	if int64(len(content)) > maximumSize {
		return contentFile{}, ErrFileTooLarge
	}
	return contentFile{Bytes: content, Version: contentVersion(content), mode: openedInfo.Mode(), info: openedInfo}, nil
}

func (s *contentStore) Write(relative string, content []byte, expectedVersion string, maximumSize int64) (contentFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(relative, content, expectedVersion, maximumSize)
}

func (s *contentStore) write(relative string, content []byte, expectedVersion string, maximumSize int64) (contentFile, error) {

	if int64(len(content)) > maximumSize {
		return contentFile{}, ErrFileTooLarge
	}
	current, err := s.read(relative, maximumSize)
	if err != nil {
		return contentFile{}, err
	}
	if expectedVersion == "" || current.Version != expectedVersion {
		return contentFile{}, ErrConflict
	}

	root, err := os.OpenRoot(s.root)
	if err != nil {
		return contentFile{}, err
	}
	defer root.Close()

	parent := path.Dir(relative)
	temporaryRelative, temporary, err := s.createTemporary(root, parent, current.mode.Perm())
	if err != nil {
		return contentFile{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(temporaryRelative)
		}
	}()

	written, writeErr := temporary.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return contentFile{}, err
	}

	// Recheck after staging so a file changed outside this process cannot be
	// silently overwritten during the potentially slow write.
	latest, err := s.read(relative, maximumSize)
	if err != nil {
		return contentFile{}, err
	}
	if latest.Version != expectedVersion || !os.SameFile(current.info, latest.info) {
		return contentFile{}, ErrConflict
	}
	if err := root.Rename(temporaryRelative, filepath.FromSlash(relative)); err != nil {
		return contentFile{}, err
	}
	committed = true
	if directory, err := root.Open(filepath.FromSlash(parent)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}

	result := append([]byte(nil), content...)
	return contentFile{Bytes: result, Version: contentVersion(result), mode: current.mode}, nil
}

// Open returns a confined regular file for streaming paths such as ZIP export.
// The caller supplies the previously observed entry info to detect replacement
// between enumeration and open.
func (s *contentStore) Open(relative string, expected os.FileInfo, maximumSize int64) (*os.File, os.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.open(relative, expected, maximumSize)
}

func (s *contentStore) open(relative string, expected os.FileInfo, maximumSize int64) (*os.File, os.FileInfo, error) {
	root, _, err := s.openExisting(relative, maximumSize)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	return openConfinedRegularFile(root, filepath.FromSlash(relative), expected, maximumSize)
}

func (s *contentStore) Lstat(relative string) (os.FileInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lstat(relative)
}

func (s *contentStore) lstat(relative string) (os.FileInfo, error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := s.validateParents(root, relative); err != nil {
		return nil, err
	}
	return root.Lstat(filepath.FromSlash(relative))
}

func (s *contentStore) ReadDir(relative string) ([]contentEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readDir(relative)
}

func (s *contentStore) readDir(relative string) ([]contentEntry, error) {
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if relative != "." && relative != "" {
		if err := s.validateExisting(root, relative, true); err != nil {
			return nil, err
		}
	}
	name := relative
	if name == "" {
		name = "."
	}
	directory, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrInvalidPath
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	result := make([]contentEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), internalTransactionStagePrefix) {
			continue
		}
		entryPath := entry.Name()
		if relative != "" && relative != "." {
			entryPath = path.Join(relative, entry.Name())
		}
		entryInfo, err := root.Lstat(filepath.FromSlash(entryPath))
		if err != nil {
			return nil, err
		}
		result = append(result, contentEntry{Path: entryPath, Name: entry.Name(), Info: entryInfo})
	}
	return result, nil
}

func (s *contentStore) Entries() ([]contentEntry, error) {
	return s.WalkEntries(nil)
}

func (s *contentStore) WalkEntries(skipDirectory func(contentEntry) bool) ([]contentEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.walkEntries(skipDirectory)
}

func (s *contentStore) entries() ([]contentEntry, error) {
	return s.walkEntries(nil)
}

func (s *contentStore) EntriesUnder(relative string) ([]contentEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entriesUnder(relative)
}

func (s *contentStore) entriesUnder(relative string) ([]contentEntry, error) {
	if err := validateStorePath(relative); err != nil {
		return nil, err
	}
	if _, err := s.lstat(relative); err != nil {
		return nil, err
	}
	return s.walkEntriesFrom(relative, nil)
}

func (s *contentStore) walkEntries(skipDirectory func(contentEntry) bool) ([]contentEntry, error) {
	return s.walkEntriesFrom(".", skipDirectory)
}

func (s *contentStore) walkEntriesFrom(root string, skipDirectory func(contentEntry) bool) ([]contentEntry, error) {
	result := make([]contentEntry, 0)
	var visit func(string) error
	visit = func(directory string) error {
		entries, err := s.readDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			result = append(result, entry)
			if entry.Info.IsDir() && entry.Info.Mode()&fs.ModeSymlink == 0 && (skipDirectory == nil || !skipDirectory(entry)) {
				if validateStorePath(entry.Path) != nil {
					continue
				}
				if err := visit(entry.Path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *contentStore) Mkdir(relative string, mode fs.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mkdir(relative, mode)
}

func (s *contentStore) mkdir(relative string, mode fs.FileMode) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.validateParents(root, relative); err != nil {
		return err
	}
	if err := root.Mkdir(filepath.FromSlash(relative), mode); err != nil {
		return err
	}
	return s.fail("directory.created", relative)
}

func (s *contentStore) WriteNew(relative string, content []byte, mode fs.FileMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeNew(relative, content, mode)
}

func (s *contentStore) writeNew(relative string, content []byte, mode fs.FileMode) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.validateParents(root, relative); err != nil {
		return err
	}
	file, err := root.OpenFile(filepath.FromSlash(relative), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = root.Remove(filepath.FromSlash(relative))
		return err
	}
	return s.fail("file.created", relative)
}

func (s *contentStore) Remove(relative string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove(relative)
}

func (s *contentStore) remove(relative string) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.validateExisting(root, relative, false); err != nil {
		return err
	}
	return root.Remove(filepath.FromSlash(relative))
}

func (s *contentStore) removeEmptyDirectory(relative string) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.validateExisting(root, relative, true); err != nil {
		return err
	}
	return root.Remove(filepath.FromSlash(relative))
}

func (s *contentStore) RemoveAll(relative string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeAll(relative)
}

func (s *contentStore) removeAll(relative string) error {
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.validateEntry(root, relative); err != nil {
		return err
	}
	return root.RemoveAll(filepath.FromSlash(relative))
}

func (s *contentStore) Rename(source, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rename(source, target)
}

func (s *contentStore) rename(source, target string) error {
	if err := s.fail("file.rename", target); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.validateEntry(root, source); err != nil {
		return err
	}
	if err := s.validateParents(root, target); err != nil {
		return err
	}
	if _, err := root.Lstat(filepath.FromSlash(target)); err == nil {
		return ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Rename(filepath.FromSlash(source), filepath.FromSlash(target)); err != nil {
		return err
	}
	return s.fail("file.renamed", target)
}

func (s *contentStore) SyncDir(relative string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.syncDir(relative)
}

func (s *contentStore) syncDir(relative string) error {
	if err := s.fail("directory.sync", relative); err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	name := relative
	if name == "" {
		name = "."
	}
	if name != "." {
		if err := s.validateExisting(root, name, true); err != nil {
			return err
		}
	}
	directory, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *contentStore) fail(phase, relative string) error {
	if s.fault != nil {
		return s.fault(phase, relative)
	}
	return nil
}

func (s *contentStore) openRoot() (*os.Root, error) {
	if strings.TrimSpace(s.root) == "" {
		return nil, ErrNotConfigured
	}
	return os.OpenRoot(s.root)
}

func (s *contentStore) validateParents(root *os.Root, relative string) error {
	if err := validateStorePath(relative); err != nil {
		return err
	}
	parent := path.Dir(relative)
	if parent == "." {
		return nil
	}
	return s.validateExisting(root, parent, true)
}

func (s *contentStore) validateExisting(root *os.Root, relative string, directory bool) error {
	if err := s.validateEntry(root, relative); err != nil {
		return err
	}
	info, err := root.Lstat(filepath.FromSlash(relative))
	if err != nil {
		return err
	}
	if directory && !info.IsDir() || !directory && info.IsDir() {
		return ErrInvalidPath
	}
	return nil
}

func (s *contentStore) validateEntry(root *os.Root, relative string) error {
	if err := validateStorePath(relative); err != nil {
		return err
	}
	parts := strings.Split(relative, "/")
	for index := range parts {
		partial := strings.Join(parts[:index+1], "/")
		info, err := root.Lstat(filepath.FromSlash(partial))
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return ErrInvalidPath
		}
		if index < len(parts)-1 && !info.IsDir() {
			return ErrInvalidPath
		}
	}
	info, err := root.Lstat(filepath.FromSlash(relative))
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return ErrInvalidPath
	}
	return nil
}

func validateStorePath(relative string) error {
	if relative == "" || relative == "." || hasControlCharacter(relative) || strings.Contains(relative, `\`) || path.IsAbs(relative) || path.Clean(relative) != relative {
		return ErrInvalidPath
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, internalTransactionStagePrefix) {
			return ErrInvalidPath
		}
	}
	return nil
}

func (s *contentStore) openExisting(relative string, maximumSize int64) (*os.Root, os.FileInfo, error) {
	if strings.TrimSpace(s.root) == "" {
		return nil, nil, ErrNotConfigured
	}
	if maximumSize < 0 {
		return nil, nil, errors.New("maximum content size must not be negative")
	}
	if err := validateStorePath(relative); err != nil {
		return nil, nil, err
	}

	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, nil, err
	}
	parts := strings.Split(relative, "/")
	for index := range parts {
		partial := strings.Join(parts[:index+1], "/")
		info, err := root.Lstat(filepath.FromSlash(partial))
		if err != nil {
			_ = root.Close()
			return nil, nil, err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			_ = root.Close()
			return nil, nil, ErrInvalidPath
		}
		if index < len(parts)-1 {
			if !info.IsDir() {
				_ = root.Close()
				return nil, nil, ErrInvalidPath
			}
			continue
		}
		if !info.Mode().IsRegular() {
			_ = root.Close()
			return nil, nil, ErrInvalidPath
		}
		if info.Size() < 0 {
			_ = root.Close()
			return nil, nil, errors.New("file size is invalid")
		}
		if info.Size() > maximumSize {
			_ = root.Close()
			return nil, nil, ErrFileTooLarge
		}
		return root, info, nil
	}
	_ = root.Close()
	return nil, nil, ErrInvalidPath
}

func (s *contentStore) createTemporary(root *os.Root, parent string, mode fs.FileMode) (string, *os.File, error) {
	for range 8 {
		identifier := make([]byte, 16)
		if _, err := rand.Read(identifier); err != nil {
			return "", nil, err
		}
		name := ".varianotes-write-" + hex.EncodeToString(identifier) + ".tmp"
		relative := name
		if parent != "." {
			relative = path.Join(parent, name)
		}
		file, err := root.OpenFile(filepath.FromSlash(relative), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		return relative, file, nil
	}
	return "", nil, errors.New("could not allocate a unique temporary content file")
}

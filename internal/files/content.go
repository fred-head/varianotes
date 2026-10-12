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

// contentStore is the shared boundary for reading and replacing ordinary vault
// files. Higher-level operations choose which paths and sizes are appropriate;
// this layer owns confinement, version checks, atomic replacement, and
// coordination with other repository mutations.
type contentStore struct {
	root string
	mu   sync.RWMutex
}

type contentFile struct {
	Bytes   []byte
	Version string
	mode    fs.FileMode
	info    os.FileInfo
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

func (s *contentStore) openExisting(relative string, maximumSize int64) (*os.Root, os.FileInfo, error) {
	if strings.TrimSpace(s.root) == "" {
		return nil, nil, ErrNotConfigured
	}
	if maximumSize < 0 {
		return nil, nil, errors.New("maximum content size must not be negative")
	}
	if err := validatePortableEntryPath(relative, false); err != nil {
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

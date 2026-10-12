package files

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"strings"
)

const (
	maxExportEntries = 100_000
	maxExportBytes   = int64(2 << 30)
)

type archiveEntry struct {
	path      string
	info      fs.FileInfo
	directory bool
}

// ValidateArchive checks the file count and total size before an HTTP response
// starts streaming. WriteArchive repeats these checks while building the ZIP.
func (r *Repository) ValidateArchive() error {
	if !r.Configured() {
		return ErrNotConfigured
	}
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	_, err := r.archiveEntries()
	return err
}

// WriteArchive writes the current ordinary vault files as a ZIP archive. VariaNotes
// metadata, recoverable Trash, and legacy Git internals are intentionally omitted.
func (r *Repository) WriteArchive(destination io.Writer) error {
	if !r.Configured() {
		return ErrNotConfigured
	}
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	entries, err := r.archiveEntries()
	if err != nil {
		return err
	}

	archive := zip.NewWriter(destination)
	var writeErr error
	for _, item := range entries {
		if item.directory {
			header := &zip.FileHeader{Name: item.path + "/", Method: zip.Store}
			header.SetMode(item.info.Mode())
			_, writeErr = archive.CreateHeader(header)
		} else {
			file, openedInfo, openErr := r.content.open(item.path, item.info, item.info.Size())
			if openErr != nil {
				writeErr = openErr
				break
			}
			header, headerErr := zip.FileInfoHeader(openedInfo)
			if headerErr != nil {
				_ = file.Close()
				writeErr = headerErr
				break
			}
			header.Name = item.path
			header.Method = zip.Deflate
			writer, createErr := archive.CreateHeader(header)
			if createErr != nil {
				_ = file.Close()
				writeErr = createErr
				break
			}
			_, copyErr := io.CopyN(writer, file, openedInfo.Size())
			writeErr = errors.Join(copyErr, file.Close())
		}
		if writeErr != nil {
			break
		}
	}
	return errors.Join(writeErr, archive.Close())
}

func (r *Repository) archiveEntries() ([]archiveEntry, error) {
	entries := make([]archiveEntry, 0)
	var totalBytes int64
	storeEntries, err := r.content.walkEntries(func(entry contentEntry) bool {
		return entry.Info.IsDir() && ignoredArchivePath(entry.Path)
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range storeEntries {
		if entry.Info.Mode()&fs.ModeSymlink != 0 || ignoredArchivePath(entry.Path) || validatePortableEntryPath(entry.Path, false) != nil {
			continue
		}
		if !entry.Info.IsDir() && !entry.Info.Mode().IsRegular() {
			continue
		}
		if len(entries) >= maxExportEntries {
			return nil, ErrExportTooManyEntries
		}
		if !entry.Info.IsDir() {
			if entry.Info.Size() < 0 || entry.Info.Size() > maxExportBytes-totalBytes {
				return nil, ErrExportTooLarge
			}
			totalBytes += entry.Info.Size()
		}
		entries = append(entries, archiveEntry{path: entry.Path, info: entry.Info, directory: entry.Info.IsDir()})
	}
	return entries, nil
}

func ignoredArchivePath(relative string) bool {
	for _, part := range strings.Split(relative, "/") {
		switch strings.ToLower(part) {
		case ".git", ".trash", "node_modules":
			return true
		}
	}
	return false
}

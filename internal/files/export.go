package files

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
	_, err := r.archiveEntries()
	return err
}

// WriteArchive writes the current ordinary vault files as a ZIP archive. VariaNotes
// metadata, recoverable Trash, and legacy Git internals are intentionally omitted.
func (r *Repository) WriteArchive(destination io.Writer) error {
	if !r.Configured() {
		return ErrNotConfigured
	}
	entries, err := r.archiveEntries()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return err
	}
	defer root.Close()

	archive := zip.NewWriter(destination)
	var writeErr error
	for _, item := range entries {
		if item.directory {
			header := &zip.FileHeader{Name: item.path + "/", Method: zip.Store}
			header.SetMode(item.info.Mode())
			_, writeErr = archive.CreateHeader(header)
		} else {
			file, openedInfo, openErr := openConfinedRegularFile(root, filepath.FromSlash(item.path), item.info, item.info.Size())
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
	err := filepath.WalkDir(r.root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == r.root {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if name == ".git" || name == ".trash" || name == "node_modules" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		relative, err := filepath.Rel(r.root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if err := validatePortableEntryPath(relative, false); err != nil {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !entry.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		if len(entries) >= maxExportEntries {
			return ErrExportTooManyEntries
		}
		if !entry.IsDir() {
			if info.Size() < 0 || info.Size() > maxExportBytes-totalBytes {
				return ErrExportTooLarge
			}
			totalBytes += info.Size()
		}
		entries = append(entries, archiveEntry{path: relative, info: info, directory: entry.IsDir()})
		return nil
	})
	return entries, err
}

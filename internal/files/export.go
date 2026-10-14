package files

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/fred-head/varianotes/internal/encryption"
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

// ArchiveSnapshot contains only derived, process-memory metadata used to bind
// a reauthenticated export to the contents inspected before the download.
type ArchiveSnapshot struct {
	Fingerprint       string
	ContainsProtected bool
}

type archivePlanItem struct {
	entry     archiveEntry
	object    *encryption.CatalogObject
	plainHash string
}

type archivePlan struct {
	items    []archivePlanItem
	snapshot ArchiveSnapshot
}

var ErrExportSnapshotChanged = errors.New("vault contents changed after export reauthentication")

// InspectArchiveWithCatalog validates the current portable archive, verifies
// every protected object, and returns a fingerprint without retaining note or
// attachment plaintext.
func (r *Repository) InspectArchiveWithCatalog(catalog encryption.Catalog, vaultKey []byte) (ArchiveSnapshot, error) {
	if !r.Configured() {
		return ArchiveSnapshot{}, ErrNotConfigured
	}
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	plan, err := r.buildArchivePlanWithCatalog(catalog, vaultKey)
	if err != nil {
		return ArchiveSnapshot{}, err
	}
	return plan.snapshot, nil
}

// WriteArchiveWithCatalog streams logical paths and plaintext bytes from both
// ordinary and authenticated protected objects. It validates the complete
// archive before writing its first byte and rechecks the snapshot before use.
func (r *Repository) WriteArchiveWithCatalog(destination io.Writer, catalog encryption.Catalog, vaultKey []byte, expectedFingerprint string, stillValid func() error) error {
	if !r.Configured() {
		return ErrNotConfigured
	}
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	plan, err := r.buildArchivePlanWithCatalog(catalog, vaultKey)
	if err != nil {
		return err
	}
	if expectedFingerprint != "" && plan.snapshot.Fingerprint != expectedFingerprint {
		return ErrExportSnapshotChanged
	}
	if stillValid != nil {
		if err := stillValid(); err != nil {
			return err
		}
	}

	archive := zip.NewWriter(destination)
	var writeErr error
	for _, item := range plan.items {
		if stillValid != nil {
			if writeErr = stillValid(); writeErr != nil {
				break
			}
		}
		if item.entry.directory {
			header := &zip.FileHeader{Name: item.entry.path + "/", Method: zip.Store}
			header.SetMode(item.entry.info.Mode())
			_, writeErr = archive.CreateHeader(header)
		} else if item.object != nil {
			plaintext, readErr := r.readArchiveProtectedObject(*item.object, vaultKey)
			if readErr != nil {
				writeErr = readErr
				break
			}
			header := &zip.FileHeader{Name: item.entry.path, Method: zip.Deflate}
			header.SetMode(fs.FileMode(0o600))
			writer, createErr := archive.CreateHeader(header)
			if createErr == nil {
				_, createErr = io.Copy(writer, newArchiveCheckedReader(bytesReader(plaintext), stillValid))
			}
			clear(plaintext)
			writeErr = createErr
		} else {
			file, openedInfo, openErr := r.content.open(item.entry.path, item.entry.info, maxExportBytes)
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
			header.Name = item.entry.path
			header.Method = zip.Deflate
			writer, createErr := archive.CreateHeader(header)
			if createErr == nil {
				_, createErr = io.CopyN(writer, newArchiveCheckedReader(file, stillValid), openedInfo.Size())
			}
			writeErr = errors.Join(createErr, file.Close())
		}
		if writeErr != nil {
			break
		}
	}
	return errors.Join(writeErr, archive.Close())
}

func (r *Repository) buildArchivePlanWithCatalog(catalog encryption.Catalog, vaultKey []byte) (archivePlan, error) {
	plan := archivePlan{items: make([]archivePlanItem, 0)}
	protectedByPhysical := make(map[string]encryption.CatalogObject)
	protectedByLogical := make(map[string]encryption.CatalogObject)
	for _, object := range catalog.Objects {
		if !object.Protected || ignoredArchivePath(object.LogicalPath) || ignoredArchivePath(object.PhysicalPath) {
			continue
		}
		if err := validatePortableEntryPath(object.LogicalPath, object.Kind == encryption.CatalogObjectNote); err != nil {
			return archivePlan{}, errors.Join(ErrProtectedContentCorrupt, err)
		}
		if object.Kind != encryption.CatalogObjectNote && object.Kind != encryption.CatalogObjectAsset {
			return archivePlan{}, ErrProtectedContentCorrupt
		}
		if _, duplicate := protectedByPhysical[object.PhysicalPath]; duplicate {
			return archivePlan{}, ErrProtectedContentCorrupt
		}
		protectedByPhysical[object.PhysicalPath] = object
		protectedByLogical[object.LogicalPath] = object
	}

	entries, err := r.content.walkEntries(func(entry contentEntry) bool {
		return entry.Info.IsDir() && ignoredArchivePath(entry.Path)
	})
	if err != nil {
		return archivePlan{}, err
	}
	var totalBytes int64
	addSize := func(size int64) error {
		if size < 0 || size > maxExportBytes-totalBytes {
			return ErrExportTooLarge
		}
		totalBytes += size
		return nil
	}
	for _, entry := range entries {
		if entry.Info.Mode()&fs.ModeSymlink != 0 || ignoredArchivePath(entry.Path) || validatePortableEntryPath(entry.Path, false) != nil {
			continue
		}
		if entry.Info.IsDir() {
			plan.items = append(plan.items, archivePlanItem{entry: archiveEntry{path: entry.Path, info: entry.Info, directory: true}})
			continue
		}
		if !entry.Info.Mode().IsRegular() {
			continue
		}
		if _, isProtectedLogical := protectedByLogical[entry.Path]; isProtectedLogical {
			return archivePlan{}, ErrProtectedContentCorrupt
		}
		if strings.EqualFold(path.Ext(entry.Path), ".age") {
			if _, known := protectedByPhysical[entry.Path]; known {
				continue
			}
			return archivePlan{}, ErrProtectedObjectMissing
		}
		protected, policyErr := catalog.IsPathProtected(entry.Path)
		if policyErr != nil {
			return archivePlan{}, policyErr
		}
		if protected {
			return archivePlan{}, ErrProtectedObjectMissing
		}
		if err := addSize(entry.Info.Size()); err != nil {
			return archivePlan{}, err
		}
		if validAssetLogicalPath(entry.Path) {
			protectedReference, referenceErr := r.protectedNoteReferencesAssetLocked(entry.Path, catalog, vaultKey)
			if referenceErr != nil {
				return archivePlan{}, referenceErr
			}
			if protectedReference {
				return archivePlan{}, ErrProtectedObjectMissing
			}
		}
		item := archivePlanItem{entry: archiveEntry{path: entry.Path, info: entry.Info}}
		item.plainHash, err = r.hashArchiveFile(item.entry)
		if err != nil {
			return archivePlan{}, err
		}
		plan.items = append(plan.items, item)
	}

	for _, object := range protectedByLogical {
		plaintext, readErr := r.readArchiveProtectedObject(object, vaultKey)
		if readErr != nil {
			return archivePlan{}, readErr
		}
		plainSize := int64(len(plaintext))
		clear(plaintext)
		if err := addSize(plainSize); err != nil {
			return archivePlan{}, err
		}
		info, statErr := r.content.lstat(object.PhysicalPath)
		if statErr != nil || !info.Mode().IsRegular() {
			return archivePlan{}, errors.Join(ErrProtectedContentCorrupt, statErr)
		}
		copyObject := object
		plan.items = append(plan.items, archivePlanItem{
			entry:  archiveEntry{path: object.LogicalPath, info: info},
			object: &copyObject,
		})
		plan.snapshot.ContainsProtected = true
	}
	if len(plan.items) > maxExportEntries {
		return archivePlan{}, ErrExportTooManyEntries
	}
	sort.Slice(plan.items, func(i, j int) bool { return plan.items[i].entry.path < plan.items[j].entry.path })
	plan.snapshot.Fingerprint, err = r.archivePlanFingerprint(plan.items)
	if err != nil {
		return archivePlan{}, err
	}
	return plan, nil
}

func (r *Repository) readArchiveProtectedObject(object encryption.CatalogObject, vaultKey []byte) ([]byte, error) {
	if object.Kind == encryption.CatalogObjectAsset {
		return r.readProtectedBytesLimit(object, vaultKey, maxAssetSize, maxAssetSize+(1<<20))
	}
	return r.readProtectedBytes(object, vaultKey)
}

func (r *Repository) hashArchiveFile(item archiveEntry) (string, error) {
	file, _, err := r.content.open(item.path, item.info, maxExportBytes)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	_, copyErr := io.CopyN(hasher, file, item.info.Size())
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (r *Repository) archivePlanFingerprint(items []archivePlanItem) (string, error) {
	hasher := sha256.New()
	for _, item := range items {
		writeFingerprintField(hasher, item.entry.path)
		if item.entry.directory {
			writeFingerprintField(hasher, "directory")
			continue
		}
		if item.object != nil {
			writeFingerprintField(hasher, "protected")
			writeFingerprintField(hasher, item.object.ObjectID)
			writeFingerprintField(hasher, item.object.CiphertextHash)
		} else {
			writeFingerprintField(hasher, "plain")
			writeFingerprintField(hasher, item.plainHash)
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeFingerprintField(destination hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = destination.Write(length[:])
	_, _ = io.WriteString(destination, value)
}

type archiveCheckedReader struct {
	reader io.Reader
	check  func() error
}

func newArchiveCheckedReader(reader io.Reader, check func() error) io.Reader {
	if check == nil {
		return reader
	}
	return archiveCheckedReader{reader: reader, check: check}
}

func (r archiveCheckedReader) Read(buffer []byte) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }

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

package files

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	trashDirectoryName   = ".trash"
	trashMetadataName    = "metadata.json"
	trashContentName     = "content"
	maximumTrashMetadata = 16 << 10
)

var (
	ErrInvalidTrashID   = errors.New("invalid trash item ID")
	ErrRestoreCollision = errors.New("the original location is already in use")
	trashIDPattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type TrashItem struct {
	ID           string `json:"id"`
	OriginalPath string `json:"originalPath"`
	Type         string `json:"type"`
	DeletedAt    string `json:"deletedAt"`
	Size         int64  `json:"size"`
}

type trashMetadata struct {
	OriginalPath string `json:"originalPath"`
	Type         string `json:"type"`
	DeletedAt    string `json:"deletedAt"`
}

type resolvedTrashItem struct {
	TrashItem
	root        string
	contentPath string
	assetsPath  string
	assetsExist bool
}

func (r *Repository) MoveToTrash(relative string) (TrashItem, error) {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.contentMu.Lock()
	defer r.contentMu.Unlock()

	source, info, err := r.resolveEntry(relative)
	if err != nil {
		return TrashItem{}, err
	}
	entryType := "directory"
	if !info.IsDir() {
		if !strings.EqualFold(filepath.Ext(source), ".md") {
			return TrashItem{}, ErrNotMarkdown
		}
		entryType = "file"
	} else if err := rejectTrashUnsafeEntries(source); err != nil {
		return TrashItem{}, err
	}

	var sourceAssets string
	assetsExist := false
	if entryType == "file" {
		sourceAssets = noteAssetsPath(source)
		assetsExist, err = validateOwnedAssets(r.root, sourceAssets)
		if err != nil {
			return TrashItem{}, err
		}
		if assetsExist {
			if err := rejectTrashUnsafeEntries(sourceAssets); err != nil {
				return TrashItem{}, err
			}
		}
	}

	trashRoot, err := r.ensureTrashRoot(true)
	if err != nil {
		return TrashItem{}, err
	}
	id, err := newTrashID()
	if err != nil {
		return TrashItem{}, err
	}
	pendingRoot := filepath.Join(trashRoot, ".pending-"+id)
	finalRoot := filepath.Join(trashRoot, id)
	// #nosec G301 -- Trash holds ordinary portable notebook content inside the repository boundary.
	if err := os.Mkdir(pendingRoot, 0o755); err != nil {
		return TrashItem{}, err
	}
	cleanupPending := func() { _ = os.RemoveAll(pendingRoot) }

	contentRoot := filepath.Join(pendingRoot, trashContentName)
	trashedPath := filepath.Join(contentRoot, "item")
	// #nosec G301 -- Trash content retains ordinary notebook directory permissions inside the repository boundary.
	if err := os.Mkdir(contentRoot, 0o755); err != nil {
		cleanupPending()
		return TrashItem{}, err
	}
	metadata := trashMetadata{OriginalPath: relative, Type: entryType, DeletedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeTrashMetadata(pendingRoot, metadata); err != nil {
		cleanupPending()
		return TrashItem{}, err
	}
	if err := os.Rename(source, trashedPath); err != nil {
		cleanupPending()
		return TrashItem{}, err
	}
	rollback := func(assetsMoved bool) error {
		var rollbackErrors []error
		if assetsMoved {
			if err := os.Rename(noteAssetsPath(trashedPath), sourceAssets); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
		if err := os.Rename(trashedPath, source); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
		if len(rollbackErrors) == 0 {
			cleanupPending()
		}
		return errors.Join(rollbackErrors...)
	}
	if assetsExist {
		trashedAssets := noteAssetsPath(trashedPath)
		if err := os.Rename(sourceAssets, trashedAssets); err != nil {
			return TrashItem{}, errors.Join(err, rollback(false))
		}
	}
	if err := os.Rename(pendingRoot, finalRoot); err != nil {
		return TrashItem{}, errors.Join(err, rollback(assetsExist))
	}
	_ = syncDirectory(filepath.Dir(source))
	_ = syncDirectory(trashRoot)
	resolved, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	return resolved.TrashItem, nil
}

func (r *Repository) TrashItems() ([]TrashItem, error) {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()

	trashRoot, err := r.ensureTrashRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return []TrashItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(trashRoot)
	if err != nil {
		return nil, err
	}
	items := make([]TrashItem, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pending-") {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.IsDir() || !trashIDPattern.MatchString(entry.Name()) {
			return nil, ErrInvalidPath
		}
		resolved, err := r.loadTrashItem(entry.Name())
		if err != nil {
			return nil, err
		}
		items = append(items, resolved.TrashItem)
	}
	sort.Slice(items, func(left, right int) bool { return items[left].DeletedAt > items[right].DeletedAt })
	return items, nil
}

func (r *Repository) RestoreTrashItem(id string) (TrashItem, error) {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.contentMu.Lock()
	defer r.contentMu.Unlock()

	item, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	if err := r.ensureSafeRestoreParent(path.Dir(item.OriginalPath)); err != nil {
		return TrashItem{}, err
	}
	target := filepath.Join(r.root, filepath.FromSlash(item.OriginalPath))
	if _, err := os.Lstat(target); err == nil {
		return TrashItem{}, ErrRestoreCollision
	} else if !errors.Is(err, os.ErrNotExist) {
		return TrashItem{}, err
	}
	var targetAssets string
	if item.assetsExist {
		targetAssets = noteAssetsPath(target)
		if _, err := os.Lstat(targetAssets); err == nil {
			return TrashItem{}, ErrRestoreCollision
		} else if !errors.Is(err, os.ErrNotExist) {
			return TrashItem{}, err
		}
	}
	if err := os.Rename(item.contentPath, target); err != nil {
		return TrashItem{}, err
	}
	if item.assetsExist {
		if err := os.Rename(item.assetsPath, targetAssets); err != nil {
			_ = os.Rename(target, item.contentPath)
			return TrashItem{}, err
		}
	}
	if err := os.RemoveAll(item.root); err != nil {
		if item.assetsExist {
			_ = os.Rename(targetAssets, item.assetsPath)
		}
		_ = os.Rename(target, item.contentPath)
		return TrashItem{}, err
	}
	_ = syncDirectory(filepath.Dir(target))
	if trashRoot, rootErr := r.ensureTrashRoot(false); rootErr == nil {
		_ = syncDirectory(trashRoot)
	}
	return item.TrashItem, nil
}

func (r *Repository) DeleteTrashItem(id string) (TrashItem, error) {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()

	item, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	if err := os.RemoveAll(item.root); err != nil {
		return TrashItem{}, err
	}
	if trashRoot, rootErr := r.ensureTrashRoot(false); rootErr == nil {
		_ = syncDirectory(trashRoot)
	}
	return item.TrashItem, nil
}

func (r *Repository) loadTrashItem(id string) (resolvedTrashItem, error) {
	if !trashIDPattern.MatchString(id) {
		return resolvedTrashItem{}, ErrInvalidTrashID
	}
	trashRoot, err := r.ensureTrashRoot(false)
	if err != nil {
		return resolvedTrashItem{}, err
	}
	itemRoot := filepath.Join(trashRoot, id)
	info, err := os.Lstat(itemRoot)
	if err != nil {
		return resolvedTrashItem{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !isWithinRoot(trashRoot, itemRoot) {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	if err := rejectTrashUnsafeEntries(itemRoot); err != nil {
		return resolvedTrashItem{}, err
	}
	// #nosec G304 -- itemRoot uses a validated random Trash ID and rejects symlinks before this fixed metadata filename is opened.
	metadataFile, err := os.Open(filepath.Join(itemRoot, trashMetadataName))
	if err != nil {
		return resolvedTrashItem{}, err
	}
	metadataInfo, err := metadataFile.Stat()
	if err != nil || !metadataInfo.Mode().IsRegular() || metadataInfo.Size() > maximumTrashMetadata {
		_ = metadataFile.Close()
		return resolvedTrashItem{}, ErrInvalidPath
	}
	decoder := json.NewDecoder(io.LimitReader(metadataFile, maximumTrashMetadata+1))
	decoder.DisallowUnknownFields()
	var metadata trashMetadata
	decodeErr := decoder.Decode(&metadata)
	if decodeErr == nil {
		decodeErr = decoder.Decode(&struct{}{})
		if errors.Is(decodeErr, io.EOF) {
			decodeErr = nil
		}
	}
	closeErr := metadataFile.Close()
	if decodeErr != nil || closeErr != nil || validateTrashMetadata(metadata) != nil {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	contentRoot := filepath.Join(itemRoot, trashContentName)
	contentPath := filepath.Join(contentRoot, "item")
	resolved, err := filepath.EvalSymlinks(contentPath)
	if err != nil || resolved != filepath.Clean(contentPath) || !isWithinRoot(contentRoot, resolved) {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	contentInfo, err := os.Stat(resolved)
	if err != nil || (metadata.Type == "file" && !contentInfo.Mode().IsRegular()) || (metadata.Type == "directory" && !contentInfo.IsDir()) {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	assetsPath := ""
	assetsExist := false
	if metadata.Type == "file" {
		assetsPath = noteAssetsPath(contentPath)
		assetsExist, err = validateOwnedAssets(contentRoot, assetsPath)
		if err != nil {
			return resolvedTrashItem{}, err
		}
	}
	size, err := trashItemSize(contentPath, assetsPath, assetsExist)
	if err != nil {
		return resolvedTrashItem{}, err
	}
	return resolvedTrashItem{TrashItem: TrashItem{ID: id, OriginalPath: metadata.OriginalPath, Type: metadata.Type, DeletedAt: metadata.DeletedAt, Size: size}, root: itemRoot, contentPath: contentPath, assetsPath: assetsPath, assetsExist: assetsExist}, nil
}

func (r *Repository) ensureTrashRoot(create bool) (string, error) {
	if !r.Configured() {
		return "", ErrNotConfigured
	}
	trashRoot := filepath.Join(r.root, trashDirectoryName)
	info, err := os.Lstat(trashRoot)
	if errors.Is(err, os.ErrNotExist) && create {
		// #nosec G301 -- .trash is repository content and intentionally uses ordinary portable directory permissions.
		if err := os.Mkdir(trashRoot, 0o755); err != nil {
			return "", err
		}
		_ = syncDirectory(r.root)
		return trashRoot, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrInvalidPath
	}
	return trashRoot, nil
}

func (r *Repository) ensureSafeRestoreParent(relative string) error {
	if relative == "." || relative == "" {
		return nil
	}
	current := r.root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// #nosec G301 -- restored notebook directories intentionally use ordinary Git-portable permissions.
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !isWithinRoot(r.root, current) {
			return ErrInvalidPath
		}
	}
	return nil
}

func validateTrashMetadata(metadata trashMetadata) error {
	if metadata.Type != "file" && metadata.Type != "directory" {
		return ErrInvalidPath
	}
	if err := validatePortableEntryPath(metadata.OriginalPath, metadata.Type == "file"); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, metadata.DeletedAt); err != nil {
		return ErrInvalidPath
	}
	return nil
}

func validatePortableEntryPath(relative string, markdown bool) error {
	if relative == "" || hasControlCharacter(relative) || strings.Contains(relative, `\`) || path.IsAbs(relative) || path.Clean(relative) != relative || relative == "." {
		return ErrInvalidPath
	}
	for _, part := range strings.Split(relative, "/") {
		lower := strings.ToLower(part)
		if part == "" || part == "." || part == ".." || lower == ".git" || lower == trashDirectoryName || lower == "node_modules" {
			return ErrInvalidPath
		}
	}
	if markdown && !strings.EqualFold(path.Ext(relative), ".md") {
		return ErrNotMarkdown
	}
	return nil
}

func writeTrashMetadata(root string, metadata trashMetadata) error {
	// #nosec G302,G304 -- root is a generated and validated Trash directory; metadata contains no credentials and remains Git-portable.
	file, err := os.OpenFile(filepath.Join(root, trashMetadataName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func newTrashID() (string, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", err
	}
	return hex.EncodeToString(identifier), nil
}

func rejectTrashUnsafeEntries(root string) error {
	return filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return ErrInvalidPath
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return ErrInvalidPath
			}
		}
		return nil
	})
}

func trashItemSize(contentPath, assetsPath string, assetsExist bool) (int64, error) {
	var size int64
	for _, root := range []string{contentPath, assetsPath} {
		if root == "" || root == assetsPath && !assetsExist {
			continue
		}
		err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return ErrInvalidPath
			}
			if !entry.IsDir() {
				info, err := entry.Info()
				if err != nil || !info.Mode().IsRegular() {
					return ErrInvalidPath
				}
				size += info.Size()
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return size, nil
}

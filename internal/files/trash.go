package files

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
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
	ErrTrashEncrypted   = errors.New("encrypted Trash content requires the protected restore operation")
	trashIDPattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type TrashItem struct {
	ID                       string `json:"id"`
	OriginalPath             string `json:"originalPath"`
	Type                     string `json:"type"`
	DeletedAt                string `json:"deletedAt"`
	Size                     int64  `json:"size"`
	Protected                bool   `json:"protected,omitempty"`
	ProtectionChoiceRequired bool   `json:"protectionChoiceRequired,omitempty"`
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
	encrypted   bool
}

func (r *Repository) MoveToTrash(relative string) (TrashItem, error) {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()

	source, info, err := r.resolveEntry(relative)
	if err != nil {
		return TrashItem{}, err
	}
	entryType := "directory"
	if !info.IsDir() {
		if !strings.EqualFold(path.Ext(source), ".md") {
			return TrashItem{}, ErrNotMarkdown
		}
		entryType = "file"
	} else if err := r.rejectTrashUnsafeEntries(source); err != nil {
		return TrashItem{}, err
	}

	var sourceAssets string
	assetsExist := false
	if entryType == "file" {
		sourceAssets = path.Join(path.Dir(source), noteBase(source)+".assets")
		assetsExist, err = validateOwnedAssets(r.content, sourceAssets)
		if err != nil {
			return TrashItem{}, err
		}
		if assetsExist {
			if err := r.rejectTrashUnsafeEntries(sourceAssets); err != nil {
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
	pendingRoot := path.Join(trashRoot, ".pending-"+id)
	finalRoot := path.Join(trashRoot, id)
	// #nosec G301 -- Trash holds ordinary portable notebook content inside the repository boundary.
	if err := r.content.mkdir(pendingRoot, 0o755); err != nil {
		return TrashItem{}, err
	}
	cleanupPending := func() { _ = r.content.removeAll(pendingRoot) }

	contentRoot := path.Join(pendingRoot, trashContentName)
	trashedPath := path.Join(contentRoot, "item")
	// #nosec G301 -- Trash content retains ordinary notebook directory permissions inside the repository boundary.
	if err := r.content.mkdir(contentRoot, 0o755); err != nil {
		cleanupPending()
		return TrashItem{}, err
	}
	metadata := trashMetadata{OriginalPath: relative, Type: entryType, DeletedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := r.writeTrashMetadata(pendingRoot, metadata); err != nil {
		cleanupPending()
		return TrashItem{}, err
	}
	if err := r.content.rename(source, trashedPath); err != nil {
		cleanupPending()
		return TrashItem{}, err
	}
	rollback := func(assetsMoved bool) error {
		var rollbackErrors []error
		if assetsMoved {
			if err := r.content.rename(path.Join(path.Dir(trashedPath), noteBase(trashedPath)+".assets"), sourceAssets); err != nil {
				rollbackErrors = append(rollbackErrors, err)
			}
		}
		if err := r.content.rename(trashedPath, source); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
		if len(rollbackErrors) == 0 {
			cleanupPending()
		}
		return errors.Join(rollbackErrors...)
	}
	if assetsExist {
		trashedAssets := path.Join(path.Dir(trashedPath), noteBase(trashedPath)+".assets")
		if err := r.content.rename(sourceAssets, trashedAssets); err != nil {
			return TrashItem{}, errors.Join(err, rollback(false))
		}
	}
	if err := r.content.rename(pendingRoot, finalRoot); err != nil {
		return TrashItem{}, errors.Join(err, rollback(assetsExist))
	}
	_ = r.content.syncDir(path.Dir(source))
	_ = r.content.syncDir(trashRoot)
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
	entries, err := r.content.readDir(trashRoot)
	if err != nil {
		return nil, err
	}
	items := make([]TrashItem, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".pending-") {
			continue
		}
		if entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.IsDir() || !trashIDPattern.MatchString(entry.Name) {
			return nil, ErrInvalidPath
		}
		resolved, err := r.loadTrashItem(entry.Name)
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
	r.content.mu.Lock()
	defer r.content.mu.Unlock()

	item, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	if item.encrypted {
		return TrashItem{}, ErrTrashEncrypted
	}
	if err := r.ensureSafeRestoreParent(path.Dir(item.OriginalPath)); err != nil {
		return TrashItem{}, err
	}
	target := item.OriginalPath
	if _, err := r.content.lstat(target); err == nil {
		return TrashItem{}, ErrRestoreCollision
	} else if !errors.Is(err, os.ErrNotExist) {
		return TrashItem{}, err
	}
	var targetAssets string
	if item.assetsExist {
		targetAssets = noteAssetsPath(target)
		if _, err := r.content.lstat(targetAssets); err == nil {
			return TrashItem{}, ErrRestoreCollision
		} else if !errors.Is(err, os.ErrNotExist) {
			return TrashItem{}, err
		}
	}
	if err := r.content.rename(item.contentPath, target); err != nil {
		return TrashItem{}, err
	}
	if item.assetsExist {
		if err := r.content.rename(item.assetsPath, targetAssets); err != nil {
			_ = r.content.rename(target, item.contentPath)
			return TrashItem{}, err
		}
	}
	if err := r.content.removeAll(item.root); err != nil {
		if item.assetsExist {
			_ = r.content.rename(targetAssets, item.assetsPath)
		}
		_ = r.content.rename(target, item.contentPath)
		return TrashItem{}, err
	}
	_ = r.content.syncDir(path.Dir(target))
	if trashRoot, rootErr := r.ensureTrashRoot(false); rootErr == nil {
		_ = r.content.syncDir(trashRoot)
	}
	return item.TrashItem, nil
}

func (r *Repository) DeleteTrashItem(id string) (TrashItem, error) {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()

	item, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	if item.encrypted {
		return TrashItem{}, ErrTrashEncrypted
	}
	if err := r.content.removeAll(item.root); err != nil {
		return TrashItem{}, err
	}
	if trashRoot, rootErr := r.ensureTrashRoot(false); rootErr == nil {
		_ = r.content.syncDir(trashRoot)
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
	itemRoot := path.Join(trashRoot, id)
	info, err := r.content.lstat(itemRoot)
	if err != nil {
		return resolvedTrashItem{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	if err := r.rejectTrashUnsafeEntries(itemRoot); err != nil {
		return resolvedTrashItem{}, err
	}
	metadataPath := path.Join(itemRoot, trashMetadataName)
	metadataContent, err := r.content.read(metadataPath, maximumTrashMetadata)
	if err != nil {
		return resolvedTrashItem{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(metadataContent.Bytes))
	decoder.DisallowUnknownFields()
	var metadata trashMetadata
	decodeErr := decoder.Decode(&metadata)
	if decodeErr == nil {
		decodeErr = decoder.Decode(&struct{}{})
		if errors.Is(decodeErr, io.EOF) {
			decodeErr = nil
		}
	}
	if decodeErr != nil || validateTrashMetadata(metadata) != nil {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	contentRoot := path.Join(itemRoot, trashContentName)
	contentPath := path.Join(contentRoot, "item")
	contentInfo, err := r.content.lstat(contentPath)
	encrypted := false
	if errors.Is(err, os.ErrNotExist) && metadata.Type == "file" {
		contentPath += ".age"
		contentInfo, err = r.content.lstat(contentPath)
		if err == nil {
			encrypted = true
		}
	}
	if err != nil || contentInfo.Mode()&os.ModeSymlink != 0 {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	if (metadata.Type == "file" && !contentInfo.Mode().IsRegular()) || (metadata.Type == "directory" && !contentInfo.IsDir()) {
		return resolvedTrashItem{}, ErrInvalidPath
	}
	assetsPath := ""
	assetsExist := false
	if metadata.Type == "file" {
		assetsPath = path.Join(path.Dir(contentPath), noteBase(contentPath)+".assets")
		assetsExist, err = validateOwnedAssets(r.content, assetsPath)
		if err != nil {
			return resolvedTrashItem{}, err
		}
	}
	if !encrypted {
		encrypted, err = r.trashPathHasCiphertext(contentPath)
		if err != nil {
			return resolvedTrashItem{}, err
		}
	}
	if !encrypted && assetsExist {
		encrypted, err = r.trashPathHasCiphertext(assetsPath)
		if err != nil {
			return resolvedTrashItem{}, err
		}
	}
	size, err := r.trashItemSize(contentPath, assetsPath, assetsExist)
	if err != nil {
		return resolvedTrashItem{}, err
	}
	return resolvedTrashItem{TrashItem: TrashItem{ID: id, OriginalPath: metadata.OriginalPath, Type: metadata.Type, DeletedAt: metadata.DeletedAt, Size: size, Protected: encrypted}, root: itemRoot, contentPath: contentPath, assetsPath: assetsPath, assetsExist: assetsExist, encrypted: encrypted}, nil
}

func (r *Repository) trashPathHasCiphertext(relative string) (bool, error) {
	info, err := r.content.lstat(relative)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return strings.EqualFold(path.Ext(relative), ".age"), nil
	}
	entries, err := r.content.entriesUnder(relative)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if strings.EqualFold(path.Ext(entry.Name), ".age") {
			return true, nil
		}
	}
	return false, nil
}

func (r *Repository) ensureTrashRoot(create bool) (string, error) {
	if !r.Configured() {
		return "", ErrNotConfigured
	}
	trashRoot := trashDirectoryName
	info, err := r.content.lstat(trashRoot)
	if errors.Is(err, os.ErrNotExist) && create {
		// #nosec G301 -- .trash is repository content and intentionally uses ordinary portable directory permissions.
		if err := r.content.mkdir(trashRoot, 0o755); err != nil {
			return "", err
		}
		_ = r.content.syncDir(".")
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
	current := ""
	for _, part := range strings.Split(relative, "/") {
		current = path.Join(current, part)
		info, err := r.content.lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// #nosec G301 -- restored notebook directories intentionally use ordinary Git-portable permissions.
			if err := r.content.mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
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

func (r *Repository) writeTrashMetadata(root string, metadata trashMetadata) error {
	content, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return r.content.writeNew(path.Join(root, trashMetadataName), content, 0o644)
}

func newTrashID() (string, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", err
	}
	return hex.EncodeToString(identifier), nil
}

func (r *Repository) rejectTrashUnsafeEntries(root string) error {
	entries, err := r.content.entriesUnder(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.IsDir() && !entry.Info.Mode().IsRegular() {
			return ErrInvalidPath
		}
	}
	return nil
}

func (r *Repository) trashItemSize(contentPath, assetsPath string, assetsExist bool) (int64, error) {
	var size int64
	roots := []string{contentPath}
	if assetsExist {
		roots = append(roots, assetsPath)
	}
	for _, root := range roots {
		info, err := r.content.lstat(root)
		if err != nil {
			return 0, err
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return 0, ErrInvalidPath
			}
			size += info.Size()
			continue
		}
		entries, err := r.content.entriesUnder(root)
		if err != nil {
			return 0, err
		}
		for _, entry := range entries {
			if entry.Info.Mode()&os.ModeSymlink != 0 {
				return 0, ErrInvalidPath
			}
			if !entry.Info.IsDir() {
				if !entry.Info.Mode().IsRegular() {
					return 0, ErrInvalidPath
				}
				size += entry.Info.Size()
			}
		}
	}
	return size, nil
}

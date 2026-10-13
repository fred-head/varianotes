package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/fred-head/varianotes/internal/encryption"
)

var (
	ErrConversionCollision = errors.New("existing content conversion found a path collision")
	ErrConversionUnsafe    = errors.New("existing content conversion found an unsafe or unsupported entry")
	ErrConversionConflict  = errors.New("existing content changed during conversion")
)

// ExistingConversionSummary contains only counts and byte totals. Paths and
// content remain in the vault and are not copied into application metadata.
type ExistingConversionSummary struct {
	Notes          int   `json:"notes"`
	Attachments    int   `json:"attachments"`
	TrashItems     int   `json:"trashItems"`
	PlaintextBytes int64 `json:"plaintextBytes"`
}

// PreparedExistingConversion keeps repository mutations serialized between
// preflight and commit. Until Commit crosses the transaction's durable point,
// all plaintext originals remain in place. Abort removes only staged
// ciphertext and the uncommitted catalog stage.
type PreparedExistingConversion struct {
	Summary     ExistingConversionSummary
	transaction *encryption.PreparedTransaction
	ctx         context.Context
	release     func()
	done        bool
}

func (operation *PreparedExistingConversion) Commit() error {
	if operation == nil || operation.done || operation.release == nil {
		return errors.New("invalid prepared existing-content conversion")
	}
	operation.done = true
	defer operation.release()
	if err := operation.ctx.Err(); err != nil {
		if operation.transaction != nil {
			return errors.Join(err, operation.transaction.Abort())
		}
		return err
	}
	if operation.transaction == nil {
		return nil
	}
	return operation.transaction.CommitContext(operation.ctx)
}

func (operation *PreparedExistingConversion) Abort() error {
	if operation == nil || operation.done || operation.release == nil {
		return nil
	}
	operation.done = true
	defer operation.release()
	if operation.transaction == nil {
		return nil
	}
	return operation.transaction.Abort()
}

type conversionFile struct {
	object    encryption.CatalogObject
	plaintext []byte
	plainHash string
	protected bool
}

type conversionInventory struct {
	repository *Repository
	catalog    encryption.Catalog
	vaultKey   []byte
	files      map[string]*conversionFile
	physical   map[string]encryption.CatalogObject
}

// PrepareExistingConversion inventories and stages an existing note or folder
// plus Trash entries whose original paths are in the selection. Every regular
// file in the selection must be a Markdown note or a supported attachment.
// The caller must commit or abort the returned operation.
func (r *Repository) PrepareExistingConversion(ctx context.Context, selectedPath string, catalog encryption.Catalog, vaultKey []byte, catalogStore *encryption.CatalogStore, journal *encryption.TransactionJournal) (*PreparedExistingConversion, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validatePortableEntryPath(selectedPath, false); err != nil && !validTrashConversionSelection(selectedPath) {
		return nil, err
	}
	if len(vaultKey) != encryption.VaultKeySize || catalogStore == nil || journal == nil {
		return nil, encryption.ErrInvalidTransaction
	}

	r.trashMu.Lock()
	r.content.mu.Lock()
	locked := true
	release := func() {
		if !locked {
			return
		}
		locked = false
		r.content.mu.Unlock()
		r.trashMu.Unlock()
	}
	fail := func(err error) (*PreparedExistingConversion, error) {
		release()
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := ensureCurrentCatalog(catalogStore, catalog, vaultKey); err != nil {
		return fail(err)
	}
	inventory := &conversionInventory{
		repository: r, catalog: catalog, vaultKey: vaultKey,
		files: make(map[string]*conversionFile), physical: make(map[string]encryption.CatalogObject),
	}
	defer clearInventory(inventory.files)
	for _, object := range catalog.Objects {
		if object.Protected {
			if _, exists := inventory.physical[object.PhysicalPath]; exists {
				return fail(encryption.ErrCatalogPathCollision)
			}
			inventory.physical[object.PhysicalPath] = object
		}
	}

	trashItems, err := r.trashItemsForConversion()
	if err != nil {
		return fail(err)
	}
	associatedTrash := make([]resolvedTrashItem, 0)
	selectionInfo, selectionIsFolder, err := r.resolveConversionSelection(selectedPath, catalog, trashItems)
	if err != nil {
		return fail(err)
	}
	if selectionInfo != nil && selectionIsFolder && strings.HasSuffix(strings.ToLower(path.Base(selectedPath)), ".assets") {
		return fail(ErrConversionUnsafe)
	}
	for _, item := range trashItems {
		if conversionPathSelected(item.OriginalPath, selectedPath, selectionIsFolder) || selectedPath == trashLogicalRoot(item) {
			associatedTrash = append(associatedTrash, item)
		}
	}
	if selectionInfo == nil && len(associatedTrash) == 0 {
		return fail(os.ErrNotExist)
	}
	if selectionInfo != nil {
		if selectionIsFolder {
			if err := inventory.scanRoot(ctx, selectedPath, ""); err != nil {
				return fail(err)
			}
		} else {
			physicalPath := selectedPath
			if object, exists := catalogNote(catalog, selectedPath); exists && object.Protected {
				physicalPath = object.PhysicalPath
			}
			if err := inventory.addPhysical(ctx, physicalPath, encryption.CatalogObjectNote); err != nil {
				return fail(err)
			}
			assetsPath := noteAssetsPath(selectedPath)
			if _, statErr := r.content.lstat(assetsPath); statErr == nil {
				if err := inventory.scanRoot(ctx, assetsPath, ""); err != nil {
					return fail(err)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return fail(statErr)
			}
		}
	}
	for _, item := range associatedTrash {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if item.Type == "file" {
			if err := inventory.addPhysical(ctx, item.contentPath, encryption.CatalogObjectNote); err != nil {
				return fail(err)
			}
			if item.assetsExist {
				if err := inventory.scanRoot(ctx, item.assetsPath, ""); err != nil {
					return fail(err)
				}
			}
		} else if err := inventory.scanRoot(ctx, item.contentPath, ""); err != nil {
			return fail(err)
		}
	}

	// An authenticated object can be missing from enumeration only when its
	// physical file is missing or its logical path is outside the selected tree.
	// For every in-scope object, load it explicitly so absence and corruption
	// block conversion before any staging begins.
	for _, object := range catalog.Objects {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if !object.Protected || (!conversionLogicalPathSelected(object.LogicalPath, selectedPath, selectionIsFolder) && !conversionPathInTrashItem(object.PhysicalPath, associatedTrash)) {
			continue
		}
		if _, exists := inventory.files[object.LogicalPath]; exists {
			continue
		}
		if err := inventory.addPhysical(ctx, object.PhysicalPath, object.Kind); err != nil {
			return fail(err)
		}
	}

	// Protect every valid existing attachment referenced by selected notes,
	// including a shared image that lives outside the selected folder.
	for _, note := range inventory.files {
		if note.object.Kind != encryption.CatalogObjectNote {
			continue
		}
		for _, destination := range markdownDestinations(string(note.plaintext)) {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			referenceBase := note.object.LogicalPath
			for _, item := range trashItems {
				if mapped, ok := trashPathToOriginal(item, note.object.LogicalPath); ok {
					referenceBase = mapped
					break
				}
			}
			logicalPath, _, ok := resolveAssetReference(referenceBase, destination)
			if !ok {
				continue
			}
			physicalLogicalPath := logicalPath
			for _, item := range trashItems {
				if mapped, ok := trashOriginalPathToLogical(item, logicalPath); ok {
					physicalLogicalPath = mapped
					break
				}
			}
			if _, exists := inventory.files[physicalLogicalPath]; exists {
				continue
			}
			if object, exists := catalogNote(catalog, physicalLogicalPath); exists && object.Protected {
				if err := inventory.addPhysical(ctx, object.PhysicalPath, encryption.CatalogObjectAsset); err != nil {
					return fail(err)
				}
				continue
			}
			if _, statErr := r.content.lstat(physicalLogicalPath); errors.Is(statErr, os.ErrNotExist) {
				if _, ageErr := r.content.lstat(physicalLogicalPath + ".age"); ageErr == nil {
					return fail(ErrConversionCollision)
				} else if !errors.Is(ageErr, os.ErrNotExist) {
					return fail(ageErr)
				}
				continue
			} else if statErr != nil {
				return fail(statErr)
			}
			if err := inventory.addPhysical(ctx, physicalLogicalPath, encryption.CatalogObjectAsset); err != nil {
				return fail(err)
			}
		}
		if note.protected {
			clear(note.plaintext)
			note.plaintext = nil
		}
	}

	nextCatalog := cloneCatalog(catalog)
	if selectionInfo != nil && selectionIsFolder && !hasFolderRule(nextCatalog, selectedPath) {
		nextCatalog.FolderRules = append(nextCatalog.FolderRules, encryption.FolderProtectionRule{Path: selectedPath})
	}
	changes := make([]encryption.CiphertextChange, 0, len(inventory.files)*2)
	summary := ExistingConversionSummary{TrashItems: len(associatedTrash)}
	for logicalPath, file := range inventory.files {
		if err := ctx.Err(); err != nil {
			clearInventory(inventory.files)
			return fail(err)
		}
		if file.protected {
			clear(file.plaintext)
			file.plaintext = nil
			continue
		}
		objectID := file.object.ObjectID
		if objectID == "" {
			objectID, err = encryption.NewCatalogObjectID()
			if err != nil {
				clearInventory(inventory.files)
				return fail(err)
			}
		}
		ciphertext, envelope, encryptErr := encryption.EncryptContent(vaultKey, objectID, file.plaintext)
		if encryptErr != nil {
			clearInventory(inventory.files)
			return fail(encryptErr)
		}
		updated := encryption.CatalogObject{
			ObjectID: objectID, Kind: file.object.Kind, LogicalPath: logicalPath,
			PhysicalPath: logicalPath + ".age", CiphertextHash: encryption.CiphertextSHA256(ciphertext),
			ContentKeyEnvelope: cloneContentKeyEnvelope(&envelope), Protected: true,
		}
		if !replaceCatalogObject(&nextCatalog, updated) {
			nextCatalog.Objects = append(nextCatalog.Objects, updated)
		}
		changes = append(changes,
			encryption.CiphertextChange{Path: updated.PhysicalPath, ObjectID: objectID, Ciphertext: ciphertext, ContentKeyEnvelope: envelope},
			encryption.CiphertextChange{Path: logicalPath, Delete: true, ExpectedSHA256: file.plainHash, RejectHardlinks: true},
		)
		if updated.Kind == encryption.CatalogObjectNote {
			summary.Notes++
		} else {
			summary.Attachments++
		}
		summary.PlaintextBytes += int64(len(file.plaintext))
		clear(file.plaintext)
		file.plaintext = nil
	}
	clearInventory(inventory.files)

	transaction, err := journal.PrepareContext(ctx, changes, nextCatalog, vaultKey)
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		abortErr := transaction.Abort()
		return fail(errors.Join(err, abortErr))
	}
	locked = true
	return &PreparedExistingConversion{Summary: summary, transaction: transaction, ctx: ctx, release: release}, nil
}

func (r *Repository) resolveConversionSelection(selectedPath string, catalog encryption.Catalog, trash []resolvedTrashItem) (os.FileInfo, bool, error) {
	for _, item := range trash {
		if selectedPath != trashLogicalRoot(item) {
			continue
		}
		if item.Type == "directory" {
			info, err := r.content.lstat(item.contentPath)
			if err != nil {
				return nil, false, err
			}
			return info, true, nil
		}
		return nil, false, nil
	}
	info, err := r.content.lstat(selectedPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, false, ErrConversionUnsafe
		}
		if info.IsDir() {
			return info, true, nil
		}
		if !info.Mode().IsRegular() || !strings.EqualFold(path.Ext(selectedPath), ".md") {
			return nil, false, ErrConversionUnsafe
		}
		return info, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if object, exists := catalogNote(catalog, selectedPath); exists {
		if object.Kind != encryption.CatalogObjectNote {
			return nil, false, ErrProtectedObjectMissing
		}
		if object.Protected {
			physicalInfo, physicalErr := r.content.lstat(object.PhysicalPath)
			if physicalErr != nil {
				return nil, false, errors.Join(ErrProtectedContentCorrupt, physicalErr)
			}
			return physicalInfo, false, nil
		}
	}
	var selectedTrash *resolvedTrashItem
	for index := range trash {
		if trash[index].OriginalPath == selectedPath {
			if selectedTrash != nil && selectedTrash.Type != trash[index].Type {
				return nil, false, ErrConversionUnsafe
			}
			selectedTrash = &trash[index]
		}
	}
	if selectedTrash == nil {
		return nil, false, err
	}
	return nil, selectedTrash.Type == "directory", nil
}

func validTrashConversionSelection(selectedPath string) bool {
	parts := strings.Split(selectedPath, "/")
	return len(parts) == 4 && parts[0] == trashDirectoryName && trashIDPattern.MatchString(parts[1]) && parts[2] == trashContentName && parts[3] == "item"
}

func conversionPathSelected(candidate, selected string, folder bool) bool {
	if candidate == selected {
		return true
	}
	return folder && strings.HasPrefix(candidate, selected+"/")
}

func conversionLogicalPathSelected(candidate, selected string, folder bool) bool {
	return conversionPathSelected(candidate, selected, folder)
}

func conversionPathInTrashItem(candidate string, items []resolvedTrashItem) bool {
	for _, item := range items {
		if candidate == item.contentPath || candidate == item.assetsPath || strings.HasPrefix(candidate, item.contentPath+"/") || strings.HasPrefix(candidate, item.assetsPath+"/") {
			return true
		}
		if strings.HasSuffix(item.contentPath, ".age") && candidate == strings.TrimSuffix(item.contentPath, ".age") {
			return true
		}
	}
	return false
}

func (inventory *conversionInventory) scanRoot(ctx context.Context, root, forcedKind string) error {
	info, err := inventory.repository.content.lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrConversionUnsafe
	}
	if !info.IsDir() {
		kind := encryption.CatalogObjectKind(forcedKind)
		if kind == "" {
			kind = conversionKindForPath(root)
		}
		return inventory.addPhysical(ctx, root, kind)
	}
	entries, err := inventory.repository.content.entriesUnder(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.IsDir() && !entry.Info.Mode().IsRegular() {
			return errors.Join(ErrConversionUnsafe, fmt.Errorf("unsafe entry %q", entry.Path))
		}
		if entry.Info.IsDir() {
			lowerName := strings.ToLower(entry.Name)
			if strings.HasSuffix(lowerName, ".assets") {
				if err := inventory.scanRoot(ctx, entry.Path, string(encryption.CatalogObjectAsset)); err != nil {
					return err
				}
				continue
			}
			if lowerName == ".git" || lowerName == ".trash" || lowerName == "node_modules" || pathWithinAssetDirectory(entry.Path) {
				return errors.Join(ErrConversionUnsafe, fmt.Errorf("unsupported directory %q", entry.Path))
			}
			continue
		}
		if pathHasReservedDirectory(entry.Path, strings.HasPrefix(root, trashDirectoryName+"/")) {
			return errors.Join(ErrConversionUnsafe, fmt.Errorf("unsupported path %q", entry.Path))
		}
		if err := inventory.addPhysical(ctx, entry.Path, ""); err != nil {
			return err
		}
	}
	return nil
}

func pathWithinAssetDirectory(relative string) bool {
	for _, component := range strings.Split(path.Dir(relative), "/") {
		if strings.HasSuffix(strings.ToLower(component), ".assets") {
			return true
		}
	}
	return false
}

func pathHasReservedDirectory(relative string, allowTrash bool) bool {
	for _, component := range strings.Split(relative, "/") {
		switch strings.ToLower(component) {
		case ".git", "node_modules":
			return true
		case ".trash":
			if !allowTrash {
				return true
			}
		}
	}
	return false
}

func conversionKindForPath(relative string) encryption.CatalogObjectKind {
	if strings.EqualFold(path.Ext(relative), ".md") {
		return encryption.CatalogObjectNote
	}
	if validAssetLogicalPath(relative) {
		return encryption.CatalogObjectAsset
	}
	return ""
}

func (inventory *conversionInventory) addPhysical(ctx context.Context, physicalPath string, forcedKind encryption.CatalogObjectKind) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if object, protected := inventory.physical[physicalPath]; protected {
		if forcedKind != "" && forcedKind != object.Kind {
			return ErrProtectedObjectMissing
		}
		if _, exists := inventory.files[object.LogicalPath]; exists {
			return nil
		}
		maximum := int64(maxMarkdownSize)
		if object.Kind == encryption.CatalogObjectAsset {
			maximum = maxAssetSize
		}
		plaintext, err := inventory.repository.readProtectedBytesLimit(object, inventory.vaultKey, maximum, maximum+(1<<20))
		if err != nil {
			return err
		}
		if object.Kind == encryption.CatalogObjectAsset {
			if _, err := supportedImageType(plaintext); err != nil {
				clear(plaintext)
				return errors.Join(ErrConversionUnsafe, err)
			}
			clear(plaintext)
			plaintext = nil
		}
		inventory.files[object.LogicalPath] = &conversionFile{object: object, plaintext: plaintext, protected: true}
		return nil
	}
	if strings.EqualFold(path.Ext(physicalPath), ".age") {
		return errors.Join(ErrConversionCollision, fmt.Errorf("untracked encrypted file %q", physicalPath))
	}
	kind := forcedKind
	if kind == "" {
		kind = conversionKindForPath(physicalPath)
	}
	if kind != encryption.CatalogObjectNote && kind != encryption.CatalogObjectAsset {
		return errors.Join(ErrConversionUnsafe, fmt.Errorf("unsupported file %q", physicalPath))
	}
	if object, exists := catalogNote(inventory.catalog, physicalPath); exists {
		if object.Kind != kind || object.Protected || object.PhysicalPath != physicalPath {
			return ErrProtectedObjectMissing
		}
	}
	if _, exists := inventory.files[physicalPath]; exists {
		return nil
	}
	info, err := inventory.repository.content.lstat(physicalPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !conversionHasSingleLink(info) {
		return errors.Join(ErrConversionUnsafe, fmt.Errorf("file %q is not a unique regular file", physicalPath))
	}
	if info.Size() < 0 {
		return ErrConversionUnsafe
	}
	maximum := int64(maxMarkdownSize)
	if kind == encryption.CatalogObjectAsset {
		maximum = maxAssetSize
	}
	stored, err := inventory.repository.content.read(physicalPath, maximum)
	if err != nil {
		return err
	}
	if !os.SameFile(info, stored.info) {
		clear(stored.Bytes)
		return ErrConversionConflict
	}
	if kind == encryption.CatalogObjectAsset {
		if _, err := supportedImageType(stored.Bytes); err != nil {
			clear(stored.Bytes)
			return errors.Join(ErrConversionUnsafe, err)
		}
	}
	if _, err := inventory.repository.content.lstat(physicalPath + ".age"); err == nil {
		clear(stored.Bytes)
		return ErrConversionCollision
	} else if !errors.Is(err, os.ErrNotExist) {
		clear(stored.Bytes)
		return err
	}
	if object, exists := catalogNote(inventory.catalog, physicalPath); exists && object.ObjectID != "" {
		if object.Kind != kind {
			clear(stored.Bytes)
			return ErrProtectedObjectMissing
		}
		if _, duplicate := inventory.files[object.LogicalPath]; duplicate {
			clear(stored.Bytes)
			return encryption.ErrCatalogPathCollision
		}
		inventory.files[physicalPath] = &conversionFile{
			object: object, plaintext: stored.Bytes, plainHash: conversionDigest(stored.Bytes),
		}
	} else {
		inventory.files[physicalPath] = &conversionFile{
			object:    encryption.CatalogObject{Kind: kind, LogicalPath: physicalPath, PhysicalPath: physicalPath},
			plaintext: stored.Bytes, plainHash: conversionDigest(stored.Bytes),
		}
	}
	return nil
}

func (r *Repository) trashItemsForConversion() ([]resolvedTrashItem, error) {
	trashRoot, err := r.ensureTrashRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return []resolvedTrashItem{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := r.content.readDir(trashRoot)
	if err != nil {
		return nil, err
	}
	items := make([]resolvedTrashItem, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".pending-") {
			return nil, errors.Join(ErrConversionUnsafe, fmt.Errorf("incomplete Trash entry %q", entry.Name))
		}
		if entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.IsDir() || !trashIDPattern.MatchString(entry.Name) {
			return nil, ErrConversionUnsafe
		}
		item, err := r.loadTrashItem(entry.Name)
		if err != nil {
			return nil, errors.Join(ErrConversionUnsafe, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func clearInventory(files map[string]*conversionFile) {
	for _, file := range files {
		clear(file.plaintext)
		file.plaintext = nil
	}
}

func hasFolderRule(catalog encryption.Catalog, folder string) bool {
	for _, rule := range catalog.FolderRules {
		if rule.Path == folder {
			return true
		}
	}
	return false
}

func replaceCatalogObject(catalog *encryption.Catalog, updated encryption.CatalogObject) bool {
	for index, object := range catalog.Objects {
		if object.LogicalPath == updated.LogicalPath {
			catalog.Objects[index] = updated
			return true
		}
	}
	return false
}

func conversionDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func conversionHasSingleLink(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	links := value.FieldByName("Nlink")
	if !links.IsValid() {
		return false
	}
	switch links.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return links.Uint() == 1
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return links.Int() == 1
	default:
		return false
	}
}

package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
)

var ErrTrashReferencedAsset = errors.New("permanent Trash deletion would remove an attachment that another note still references")

// MoveToTrashWithCatalog applies the protection policy before a selected entry
// is moved. Protected bytes and the catalog change commit through one journal.
func (r *Repository) MoveToTrashWithCatalog(relative string, catalog encryption.Catalog, vaultKey []byte, store *encryption.CatalogStore, journal *encryption.TransactionJournal) (TrashItem, error) {
	if err := r.validateRelative(relative, false); err != nil {
		return TrashItem{}, err
	}
	if err := r.reconcileProtectedTrash(catalog); err != nil {
		return TrashItem{}, err
	}
	protected, _, err := selectionProtection(relative, catalog, r.content)
	if err != nil {
		return TrashItem{}, err
	}
	if !protected {
		return r.moveToTrashGuarded(relative, journal)
	}
	if store == nil || journal == nil || len(vaultKey) != encryption.VaultKeySize {
		return TrashItem{}, encryption.ErrInvalidTransaction
	}
	conversion, err := r.PrepareExistingConversion(context.Background(), relative, catalog, vaultKey, store, journal)
	if err != nil {
		return TrashItem{}, err
	}
	if err := conversion.Commit(); err != nil {
		return TrashItem{}, err
	}
	catalog, err = store.Load(vaultKey, catalog.VaultID)
	if err != nil {
		return TrashItem{}, err
	}

	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.moveMu.Lock()
	defer r.moveMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	if err := ensureCurrentCatalog(store, catalog, vaultKey); err != nil {
		return TrashItem{}, err
	}
	_, folder, err := r.resolveMoveSourceWithCatalog(relative, catalog)
	if err != nil {
		return TrashItem{}, err
	}
	if folder {
		if err := r.rejectTrashUnsafeEntries(relative); err != nil {
			return TrashItem{}, err
		}
	} else if !strings.EqualFold(path.Ext(relative), ".md") {
		return TrashItem{}, ErrNotMarkdown
	}

	selected := selectedProtectedObjects(catalog, relative, folder)
	if len(selected) == 0 && !folder {
		return TrashItem{}, ErrProtectedObjectMissing
	}
	refs, err := r.assetReferencesLocked(catalog, vaultKey, nil, relative, folder)
	if err != nil {
		return TrashItem{}, err
	}
	moved := make([]encryption.CatalogObject, 0, len(selected))
	for _, object := range selected {
		if object.Kind == encryption.CatalogObjectAsset && refs[object.LogicalPath] {
			// Shared attachments stay at their logical paths so other notes keep
			// working while this selection is in Trash.
			continue
		}
		moved = append(moved, object)
	}

	trashRoot, err := r.ensureTrashRoot(true)
	if err != nil {
		return TrashItem{}, err
	}
	id, err := newTrashID()
	if err != nil {
		return TrashItem{}, err
	}
	finalRoot := path.Join(trashRoot, id)
	pendingRoot := path.Join(trashRoot, ".pending-"+id)
	if err := r.content.mkdir(finalRoot, 0o755); err != nil {
		return TrashItem{}, err
	}
	cleanup := func() {
		_ = r.content.removeAll(finalRoot)
		_ = r.content.removeAll(pendingRoot)
	}
	if err := r.content.mkdir(pendingRoot, 0o755); err != nil {
		cleanup()
		return TrashItem{}, err
	}
	if err := r.writeTrashMetadata(pendingRoot, trashMetadata{OriginalPath: relative, Type: map[bool]string{true: "directory", false: "file"}[folder], DeletedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		cleanup()
		return TrashItem{}, err
	}
	contentRoot := path.Join(finalRoot, trashContentName)
	if err := r.content.mkdir(contentRoot, 0o755); err != nil {
		cleanup()
		return TrashItem{}, err
	}
	targetRoot := path.Join(contentRoot, "item")
	if folder {
		if err := r.content.mkdir(targetRoot, 0o755); err != nil {
			cleanup()
			return TrashItem{}, err
		}
		if err := r.mirrorEmptyDirectoryTree(relative, targetRoot); err != nil {
			cleanup()
			return TrashItem{}, err
		}
	} else {
		assets := noteAssetsPath(relative)
		moveAssets := false
		for _, object := range moved {
			moveAssets = moveAssets || object.Kind == encryption.CatalogObjectAsset && strings.HasPrefix(object.LogicalPath, assets+"/")
		}
		if exists, assetsErr := validateOwnedAssets(r.content, assets); assetsErr != nil {
			cleanup()
			return TrashItem{}, assetsErr
		} else if exists && moveAssets {
			if err := r.content.mkdir(noteAssetsPath(targetRoot), 0o755); err != nil {
				cleanup()
				return TrashItem{}, err
			}
			if err := r.mirrorEmptyDirectoryTree(assets, noteAssetsPath(targetRoot)); err != nil {
				cleanup()
				return TrashItem{}, err
			}
		}
	}

	next := cloneCatalog(catalog)
	selectedIDs := make(map[string]bool, len(moved))
	for _, object := range moved {
		selectedIDs[object.ObjectID] = true
	}
	changes := make([]encryption.CiphertextChange, 0, len(moved)*2)
	for index := range next.Objects {
		object := &next.Objects[index]
		if !selectedIDs[object.ObjectID] {
			continue
		}
		old := *object
		newPath := remapSelectedMovePath(old.LogicalPath, relative, targetRoot, folder)
		if _, err := r.content.lstat(newPath + ".age"); err == nil {
			cleanup()
			return TrashItem{}, ErrRestoreCollision
		} else if !errors.Is(err, fs.ErrNotExist) {
			cleanup()
			return TrashItem{}, err
		}
		stored, err := r.content.read(old.PhysicalPath, transactionPlaintextLimit(old.Kind)+(1<<20))
		if err != nil {
			cleanup()
			return TrashItem{}, protectedContentError(err)
		}
		if err := encryption.VerifyCatalogCiphertext(old, stored.Bytes); err != nil {
			clear(stored.Bytes)
			cleanup()
			return TrashItem{}, errors.Join(ErrProtectedContentCorrupt, err)
		}
		*object = old
		object.LogicalPath = newPath
		object.PhysicalPath = newPath + ".age"
		changes = append(changes,
			encryption.CiphertextChange{Path: object.PhysicalPath, ObjectID: old.ObjectID, Ciphertext: stored.Bytes, ContentKeyEnvelope: *cloneContentKeyEnvelope(old.ContentKeyEnvelope)},
			encryption.CiphertextChange{Path: old.PhysicalPath, Delete: true, ExpectedSHA256: encryption.CiphertextSHA256(stored.Bytes)},
		)
	}
	if folder {
		for index := range next.FolderRules {
			rule := &next.FolderRules[index]
			if rule.Path == relative || strings.HasPrefix(rule.Path, relative+"/") {
				rule.Path = remapMovedPath(rule.Path, relative, targetRoot)
			}
		}
		if protected, _ := catalog.IsPathProtected(relative); protected && !hasFolderRule(catalog, relative) {
			next.FolderRules = append(next.FolderRules, encryption.FolderProtectionRule{Path: targetRoot})
		}
	}
	next.FolderRules = uniqueFolderRules(next.FolderRules)
	tx, err := journal.Prepare(changes, next, vaultKey)
	if err == nil {
		err = tx.Commit()
	}
	for _, change := range changes {
		clear(change.Ciphertext)
	}
	if err != nil {
		if !errors.Is(err, encryption.ErrTransactionNeedsRecovery) {
			cleanup()
		}
		return TrashItem{}, err
	}
	if err := r.finalizePendingTrash(id); err != nil {
		return TrashItem{}, err
	}
	if folder {
		r.removeEmptyDirectories(relative)
	} else {
		r.removeEmptyDirectories(noteAssetsPath(relative))
	}
	resolved, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	return resolved.TrashItem, nil
}

// TrashItemsWithCatalog completes or discards an interrupted protected Trash
// staging operation after the encryption journal has recovered.
func (r *Repository) TrashItemsWithCatalog(catalog encryption.Catalog, vaultKey []byte) ([]TrashItem, error) {
	if len(vaultKey) != encryption.VaultKeySize {
		return nil, encryption.ErrInvalidTransaction
	}
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	if err := r.reconcilePendingTrash(catalog); err != nil {
		return nil, err
	}
	return r.trashItemsLocked(&catalog)
}

func (r *Repository) reconcileProtectedTrash(catalog encryption.Catalog) error {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	return r.reconcilePendingTrash(catalog)
}

func (r *Repository) trashItemsLocked(catalog *encryption.Catalog) ([]TrashItem, error) {
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
		if catalog != nil {
			resolved.Protected = resolved.encrypted || trashSelectionProtected(resolved, *catalog)
			if resolved.Protected {
				targetProtected, policyErr := catalog.IsPathProtected(resolved.OriginalPath)
				if policyErr != nil {
					return nil, policyErr
				}
				resolved.ProtectionChoiceRequired = !targetProtected
			}
		}
		items = append(items, resolved.TrashItem)
	}
	sort.Slice(items, func(left, right int) bool { return items[left].DeletedAt > items[right].DeletedAt })
	return items, nil
}

// RestoreTrashItemWithCatalog restores protected Trash content while applying
// the same explicit keep/decrypt decision used by protected moves.
func (r *Repository) RestoreTrashItemWithCatalog(id, protectionChoice string, catalog encryption.Catalog, vaultKey []byte, store *encryption.CatalogStore, journal *encryption.TransactionJournal) (TrashItem, error) {
	if err := r.reconcileProtectedTrash(catalog); err != nil {
		return TrashItem{}, err
	}
	item, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	targetProtected, err := catalog.IsPathProtected(item.OriginalPath)
	if err != nil {
		return TrashItem{}, err
	}
	if protectionChoice != "" && protectionChoice != MoveProtectionKeep && protectionChoice != MoveProtectionDecrypt {
		return TrashItem{}, ErrInvalidMoveProtectionChoice
	}
	protectedSelection := item.encrypted || trashSelectionProtected(item, catalog)
	if !protectedSelection && !targetProtected {
		return r.restoreTrashItemGuarded(id, journal)
	}
	if store == nil || journal == nil || len(vaultKey) != encryption.VaultKeySize {
		return TrashItem{}, encryption.ErrInvalidTransaction
	}
	if protectedSelection && !targetProtected && protectionChoice == "" {
		return TrashItem{}, ErrMoveProtectionChoiceRequired
	}
	if targetProtected && protectionChoice == MoveProtectionDecrypt {
		return TrashItem{}, ErrMoveDecryptIntoProtected
	}
	conversion, err := r.PrepareExistingConversion(context.Background(), trashLogicalRoot(item), catalog, vaultKey, store, journal)
	if err != nil {
		return TrashItem{}, err
	}
	if err := conversion.Commit(); err != nil {
		return TrashItem{}, err
	}
	catalog, err = store.Load(vaultKey, catalog.VaultID)
	if err != nil {
		return TrashItem{}, err
	}

	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.moveMu.Lock()
	defer r.moveMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	if err := ensureCurrentCatalog(store, catalog, vaultKey); err != nil {
		return TrashItem{}, err
	}
	item, err = r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	if err := r.ensureSafeRestoreParent(path.Dir(item.OriginalPath)); err != nil {
		return TrashItem{}, err
	}
	sourceRoot := trashLogicalRoot(item)
	folder := item.Type == "directory"
	selected := trashObjects(catalog, item)
	if len(selected) == 0 && !folder {
		return TrashItem{}, ErrProtectedObjectMissing
	}
	if err := r.checkTrashRestoreCollision(item, selected, folder, catalog); err != nil {
		return TrashItem{}, err
	}
	refs, err := r.assetReferencesLocked(catalog, vaultKey, &item, "", false)
	if err != nil {
		return TrashItem{}, err
	}
	keepShared := make(map[string]bool)
	if protectionChoice == MoveProtectionDecrypt {
		for _, object := range selected {
			if object.Kind == encryption.CatalogObjectAsset {
				if logical, ok := trashPathToOriginal(item, object.LogicalPath); ok && refs[logical] {
					keepShared[object.ObjectID] = true
				}
			}
		}
	}

	createDirectories, removeDirectories, err := r.planRestoreDirectories(item, sourceRoot, folder)
	if err != nil {
		return TrashItem{}, err
	}
	next := cloneCatalog(catalog)
	selectedIDs := make(map[string]bool, len(selected))
	for _, object := range selected {
		selectedIDs[object.ObjectID] = true
	}
	changes := make([]encryption.CiphertextChange, 0, len(selected)*2+1)
	for index := range next.Objects {
		object := &next.Objects[index]
		if !selectedIDs[object.ObjectID] {
			continue
		}
		old := *object
		targetPath, ok := trashPathToOriginal(item, old.LogicalPath)
		if !ok {
			return TrashItem{}, ErrProtectedObjectMissing
		}
		stored, err := r.content.read(old.PhysicalPath, transactionPlaintextLimit(old.Kind)+(1<<20))
		if err != nil {
			return TrashItem{}, protectedContentError(err)
		}
		if err := encryption.VerifyCatalogCiphertext(old, stored.Bytes); err != nil {
			clear(stored.Bytes)
			return TrashItem{}, errors.Join(ErrProtectedContentCorrupt, err)
		}
		decrypt := protectionChoice == MoveProtectionDecrypt && !keepShared[old.ObjectID]
		if decrypt {
			if _, err := r.content.lstat(targetPath); err == nil {
				clear(stored.Bytes)
				return TrashItem{}, ErrRestoreCollision
			} else if !errors.Is(err, fs.ErrNotExist) {
				clear(stored.Bytes)
				return TrashItem{}, err
			}
			if _, err := r.content.lstat(targetPath + ".age"); err == nil {
				clear(stored.Bytes)
				return TrashItem{}, ErrRestoreCollision
			} else if !errors.Is(err, fs.ErrNotExist) {
				clear(stored.Bytes)
				return TrashItem{}, err
			}
			plaintext, decryptErr := encryption.DecryptContent(vaultKey, old.ObjectID, stored.Bytes, *old.ContentKeyEnvelope)
			clear(stored.Bytes)
			if decryptErr != nil {
				return TrashItem{}, errors.Join(ErrProtectedContentCorrupt, decryptErr)
			}
			if int64(len(plaintext)) > transactionPlaintextLimit(old.Kind) {
				clear(plaintext)
				return TrashItem{}, ErrFileTooLarge
			}
			if old.Kind == encryption.CatalogObjectAsset {
				if _, typeErr := supportedImageType(plaintext); typeErr != nil {
					clear(plaintext)
					return TrashItem{}, typeErr
				}
			}
			ciphertext, envelope, encryptErr := encryption.EncryptContent(vaultKey, old.ObjectID, plaintext)
			clear(plaintext)
			if encryptErr != nil {
				return TrashItem{}, encryptErr
			}
			changes = append(changes,
				encryption.CiphertextChange{Path: targetPath, ObjectID: old.ObjectID, Ciphertext: ciphertext, ContentKeyEnvelope: envelope, PublishPlaintext: true},
				encryption.CiphertextChange{Path: old.PhysicalPath, Delete: true, ExpectedSHA256: old.CiphertextHash},
			)
			*object = encryption.CatalogObject{}
			continue
		}
		for _, target := range []string{targetPath, targetPath + ".age"} {
			if _, err := r.content.lstat(target); err == nil {
				clear(stored.Bytes)
				return TrashItem{}, ErrRestoreCollision
			} else if !errors.Is(err, fs.ErrNotExist) {
				clear(stored.Bytes)
				return TrashItem{}, err
			}
		}
		object.LogicalPath = targetPath
		object.PhysicalPath = targetPath + ".age"
		changes = append(changes,
			encryption.CiphertextChange{Path: object.PhysicalPath, ObjectID: old.ObjectID, Ciphertext: stored.Bytes, ContentKeyEnvelope: *cloneContentKeyEnvelope(old.ContentKeyEnvelope)},
			encryption.CiphertextChange{Path: old.PhysicalPath, Delete: true, ExpectedSHA256: encryption.CiphertextSHA256(stored.Bytes)},
		)
	}
	objects := next.Objects[:0]
	for _, object := range next.Objects {
		if object.ObjectID != "" {
			objects = append(objects, object)
		}
	}
	next.Objects = objects
	if folder {
		mappedRules := next.FolderRules[:0]
		for _, rule := range next.FolderRules {
			if rule.Path == sourceRoot || strings.HasPrefix(rule.Path, sourceRoot+"/") {
				if protectionChoice != MoveProtectionDecrypt {
					if mapped, ok := trashPathToOriginal(item, rule.Path); ok {
						rule.Path = mapped
						mappedRules = append(mappedRules, rule)
					}
				}
				continue
			}
			mappedRules = append(mappedRules, rule)
		}
		next.FolderRules = mappedRules
	}
	next.FolderRules = uniqueFolderRules(next.FolderRules)
	metadata, err := r.content.read(path.Join(item.root, trashMetadataName), maximumTrashMetadata)
	if err != nil {
		return TrashItem{}, err
	}
	changes = append(changes, encryption.CiphertextChange{Path: path.Join(item.root, trashMetadataName), Delete: true, ExpectedSHA256: encryption.CiphertextSHA256(metadata.Bytes)})
	tx, err := journal.PrepareFilesystem(changes, next, vaultKey, createDirectories, removeDirectories)
	if err == nil && r.transactionPrepared != nil {
		err = r.transactionPrepared()
	}
	clear(metadata.Bytes)
	if err == nil {
		err = tx.Commit()
	}
	for _, change := range changes {
		clear(change.Ciphertext)
	}
	if err != nil {
		return TrashItem{}, err
	}

	return item.TrashItem, nil
}

// DeleteTrashItemWithCatalog permanently removes protected Trash through the
// encryption journal. Referenced attachments block deletion before mutation.
func (r *Repository) DeleteTrashItemWithCatalog(id string, catalog encryption.Catalog, vaultKey []byte, store *encryption.CatalogStore, journal *encryption.TransactionJournal) (TrashItem, error) {
	if err := r.reconcileProtectedTrash(catalog); err != nil {
		return TrashItem{}, err
	}
	item, err := r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	if !item.encrypted && !trashSelectionProtected(item, catalog) {
		return r.deleteTrashItemGuarded(id, journal)
	}
	conversion, err := r.PrepareExistingConversion(context.Background(), trashLogicalRoot(item), catalog, vaultKey, store, journal)
	if err != nil {
		return TrashItem{}, err
	}
	if err := conversion.Commit(); err != nil {
		return TrashItem{}, err
	}
	catalog, err = store.Load(vaultKey, catalog.VaultID)
	if err != nil {
		return TrashItem{}, err
	}
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	if err := ensureCurrentCatalog(store, catalog, vaultKey); err != nil {
		return TrashItem{}, err
	}
	item, err = r.loadTrashItem(id)
	if err != nil {
		return TrashItem{}, err
	}
	selected := trashObjects(catalog, item)
	refs, err := r.assetReferencesLocked(catalog, vaultKey, &item, "", false)
	if err != nil {
		return TrashItem{}, err
	}
	for _, object := range selected {
		if object.Kind == encryption.CatalogObjectAsset {
			if logical, ok := trashPathToOriginal(item, object.LogicalPath); !ok || refs[logical] {
				return TrashItem{}, ErrTrashReferencedAsset
			}
		}
	}
	next := cloneCatalog(catalog)
	selectedIDs := make(map[string]bool, len(selected))
	for _, object := range selected {
		selectedIDs[object.ObjectID] = true
	}
	objects := next.Objects[:0]
	changes := make([]encryption.CiphertextChange, 0, len(selected)+1)
	for _, object := range next.Objects {
		if selectedIDs[object.ObjectID] {
			stored, readErr := r.content.read(object.PhysicalPath, transactionPlaintextLimit(object.Kind)+(1<<20))
			if readErr != nil {
				return TrashItem{}, protectedContentError(readErr)
			}
			if verifyErr := encryption.VerifyCatalogCiphertext(object, stored.Bytes); verifyErr != nil {
				clear(stored.Bytes)
				return TrashItem{}, errors.Join(ErrProtectedContentCorrupt, verifyErr)
			}
			changes = append(changes, encryption.CiphertextChange{Path: object.PhysicalPath, Delete: true, ExpectedSHA256: encryption.CiphertextSHA256(stored.Bytes)})
			clear(stored.Bytes)
			continue
		}
		objects = append(objects, object)
	}
	next.Objects = objects
	trashRoot := trashLogicalRoot(item)
	remainingRules := next.FolderRules[:0]
	for _, rule := range next.FolderRules {
		if rule.Path != trashRoot && !strings.HasPrefix(rule.Path, trashRoot+"/") {
			remainingRules = append(remainingRules, rule)
		}
	}
	next.FolderRules = remainingRules
	metadata, err := r.content.read(path.Join(item.root, trashMetadataName), maximumTrashMetadata)
	if err != nil {
		return TrashItem{}, err
	}
	changes = append(changes, encryption.CiphertextChange{Path: path.Join(item.root, trashMetadataName), Delete: true, ExpectedSHA256: encryption.CiphertextSHA256(metadata.Bytes)})
	clear(metadata.Bytes)
	tx, err := journal.Prepare(changes, next, vaultKey)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return TrashItem{}, err
	}
	_ = r.content.removeAll(item.root)
	return item.TrashItem, nil
}

func selectionProtection(source string, catalog encryption.Catalog, content *contentStore) (bool, bool, error) {
	info, err := content.lstat(source)
	folder := false
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return false, false, ErrInvalidPath
		}
		folder = info.IsDir()
		if !folder && !strings.EqualFold(path.Ext(source), ".md") {
			return false, false, ErrNotMarkdown
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, false, err
	} else if object, found := catalogNote(catalog, source); found && object.Kind == encryption.CatalogObjectNote && object.Protected {
		folder = false
	} else {
		return false, false, err
	}
	protected, err := catalog.IsPathProtected(source)
	if err != nil || protected {
		return protected, folder, err
	}
	for _, object := range catalog.Objects {
		if object.Protected && (pathSelectedForMove(object.LogicalPath, source, folder) || folder && strings.HasPrefix(object.LogicalPath, source+"/")) {
			return true, folder, nil
		}
	}
	for _, rule := range catalog.FolderRules {
		if rule.Path == source || folder && strings.HasPrefix(rule.Path, source+"/") {
			return true, folder, nil
		}
	}
	return false, folder, nil
}

func selectedProtectedObjects(catalog encryption.Catalog, source string, folder bool) []encryption.CatalogObject {
	objects := make([]encryption.CatalogObject, 0)
	for _, object := range catalog.Objects {
		if object.Protected && (pathSelectedForMove(object.LogicalPath, source, folder) || folder && strings.HasPrefix(object.LogicalPath, source+"/")) {
			objects = append(objects, object)
		}
	}
	return objects
}

func trashObjects(catalog encryption.Catalog, item resolvedTrashItem) []encryption.CatalogObject {
	root := trashLogicalRoot(item)
	objects := make([]encryption.CatalogObject, 0)
	for _, object := range catalog.Objects {
		if object.Protected && trashPathSelected(item, root, object.LogicalPath) {
			objects = append(objects, object)
		}
	}
	return objects
}

func trashSelectionProtected(item resolvedTrashItem, catalog encryption.Catalog) bool {
	root := trashLogicalRoot(item)
	if protected, err := catalog.IsPathProtected(root); err == nil && protected {
		return true
	}
	for _, object := range catalog.Objects {
		if object.Protected && trashPathSelected(item, root, object.LogicalPath) {
			return true
		}
	}
	for _, rule := range catalog.FolderRules {
		if rule.Path == root || strings.HasPrefix(rule.Path, root+"/") {
			return true
		}
	}
	return false
}

func uniqueFolderRules(rules []encryption.FolderProtectionRule) []encryption.FolderProtectionRule {
	seen := make(map[string]bool, len(rules))
	result := rules[:0]
	for _, rule := range rules {
		if seen[rule.Path] {
			continue
		}
		seen[rule.Path] = true
		result = append(result, rule)
	}
	return result
}

func trashLogicalRoot(item resolvedTrashItem) string {
	return strings.TrimSuffix(item.contentPath, ".age")
}

func trashPathSelected(item resolvedTrashItem, root, candidate string) bool {
	if item.Type == "file" {
		return candidate == root || strings.HasPrefix(candidate, noteAssetsPath(root)+"/")
	}
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func trashPathToOriginal(item resolvedTrashItem, candidate string) (string, bool) {
	root := trashLogicalRoot(item)
	if item.Type == "file" {
		if candidate == root {
			return item.OriginalPath, true
		}
		assets := noteAssetsPath(root)
		if strings.HasPrefix(candidate, assets+"/") {
			return noteAssetsPath(item.OriginalPath) + strings.TrimPrefix(candidate, assets), true
		}
		return "", false
	}
	if candidate == root {
		return item.OriginalPath, true
	}
	if strings.HasPrefix(candidate, root+"/") {
		return item.OriginalPath + strings.TrimPrefix(candidate, root), true
	}
	return "", false
}

func trashOriginalPathToLogical(item resolvedTrashItem, original string) (string, bool) {
	root := trashLogicalRoot(item)
	if item.Type == "file" {
		if original == item.OriginalPath {
			return root, true
		}
		assets := noteAssetsPath(item.OriginalPath)
		if strings.HasPrefix(original, assets+"/") {
			return noteAssetsPath(root) + strings.TrimPrefix(original, assets), true
		}
		return "", false
	}
	if original == item.OriginalPath {
		return root, true
	}
	if strings.HasPrefix(original, item.OriginalPath+"/") {
		return root + strings.TrimPrefix(original, item.OriginalPath), true
	}
	return "", false
}

func (r *Repository) assetReferencesLocked(catalog encryption.Catalog, vaultKey []byte, excludedTrash *resolvedTrashItem, excludedSource string, excludedFolder bool) (map[string]bool, error) {
	items, err := r.trashItemsForConversion()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	refs := make(map[string]bool)
	notes := make(map[string]bool)
	for _, object := range catalog.Objects {
		if object.Kind == encryption.CatalogObjectNote {
			notes[object.LogicalPath] = true
		}
	}
	entries, err := r.content.entries()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Info.IsDir() || entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.Mode().IsRegular() {
			continue
		}
		if strings.EqualFold(path.Ext(entry.Name), ".md") {
			notes[entry.Path] = true
		}
	}
	for _, item := range items {
		if item.Type == "file" && !item.encrypted {
			notes[item.contentPath] = true
		}
	}
	for notePath := range notes {
		if excludedSource != "" && (notePath == excludedSource || pathSelectedForMove(notePath, excludedSource, excludedFolder) || excludedFolder && strings.HasPrefix(notePath, excludedSource+"/")) {
			continue
		}
		if excludedTrash != nil && trashPathSelected(*excludedTrash, trashLogicalRoot(*excludedTrash), notePath) {
			continue
		}
		markdown, _, readErr := r.readMarkdownWithCatalogLocked(notePath, catalog, vaultKey)
		if readErr != nil {
			// Every unreadable note could contain a reference. Fail closed before
			// moving or deleting an attachment.
			return nil, fmt.Errorf("inspect attachment references in %q: %w", notePath, readErr)
		}
		base := notePath
		for _, item := range items {
			if mapped, ok := trashPathToOriginal(item, notePath); ok {
				base = mapped
				break
			}
		}
		for _, destination := range markdownDestinations(markdown.Content) {
			if logical, _, ok := resolveAssetReference(base, destination); ok {
				refs[logical] = true
			}
		}
	}
	return refs, nil
}

func (r *Repository) mirrorEmptyDirectoryTree(source, target string) error {
	entries, err := r.content.entriesUnder(source)
	if err != nil {
		return err
	}
	directories := make([]string, 0)
	for _, entry := range entries {
		if entry.Info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidPath
		}
		if entry.Info.IsDir() {
			directories = append(directories, entry.Path)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], "/") < strings.Count(directories[j], "/")
	})
	for _, directory := range directories {
		destination := target + strings.TrimPrefix(directory, source)
		if err := r.content.mkdir(destination, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) ensureMirroredDirectoryTree(source, target string) error {
	entries, err := r.content.entriesUnder(source)
	if err != nil {
		return err
	}
	directories := make([]string, 0)
	for _, entry := range entries {
		if entry.Info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidPath
		}
		if entry.Info.IsDir() {
			directories = append(directories, entry.Path)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], "/") < strings.Count(directories[j], "/")
	})
	for _, directory := range directories {
		destination := target + strings.TrimPrefix(directory, source)
		if err := r.ensureDirectory(destination); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) ensureDirectory(relative string) error {
	info, err := r.content.lstat(relative)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrInvalidPath
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return r.content.mkdir(relative, 0o755)
}

func (r *Repository) checkTrashRestoreCollision(item resolvedTrashItem, selected []encryption.CatalogObject, folder bool, catalog encryption.Catalog) error {
	if folder {
		if err := r.ensureSafeRestoreParent(path.Dir(item.OriginalPath)); err != nil {
			return err
		}
		if info, err := r.content.lstat(item.OriginalPath); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrRestoreCollision
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, object := range catalog.Objects {
			if object.LogicalPath == item.OriginalPath || strings.HasPrefix(object.LogicalPath, item.OriginalPath+"/") {
				selectedID := false
				for _, candidate := range selected {
					selectedID = selectedID || candidate.ObjectID == object.ObjectID
				}
				if !selectedID && object.Kind != encryption.CatalogObjectAsset {
					return ErrRestoreCollision
				}
			}
		}
		return nil
	}
	targets := []string{item.OriginalPath, item.OriginalPath + ".age"}
	for _, target := range targets {
		if _, err := r.content.lstat(target); err == nil {
			return ErrRestoreCollision
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if item.assetsExist {
		if _, err := validateOwnedAssets(r.content, noteAssetsPath(item.OriginalPath)); err != nil {
			return err
		}
	}
	for _, object := range catalog.Objects {
		if object.LogicalPath == item.OriginalPath {
			return ErrRestoreCollision
		}
	}
	return nil
}

func (r *Repository) removeEmptyDirectories(root string) {
	info, err := r.content.lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	entries, err := r.content.entriesUnder(root)
	if err != nil {
		return
	}
	directories := []string{root}
	for _, entry := range entries {
		if entry.Info.IsDir() && entry.Info.Mode()&os.ModeSymlink == 0 {
			directories = append(directories, entry.Path)
		}
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		children, readErr := r.content.readDir(directory)
		if readErr == nil && len(children) == 0 {
			_ = r.content.removeAll(directory)
		}
	}
}

func (r *Repository) reconcilePendingTrash(catalog encryption.Catalog) error {
	trashRoot, err := r.ensureTrashRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := r.content.readDir(trashRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name, ".pending-") {
			continue
		}
		id := strings.TrimPrefix(entry.Name, ".pending-")
		if !trashIDPattern.MatchString(id) || entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.IsDir() {
			return ErrInvalidPath
		}
		pending := path.Join(trashRoot, entry.Name)
		metadata, readErr := r.content.read(path.Join(pending, trashMetadataName), maximumTrashMetadata)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				if _, finalErr := r.content.lstat(path.Join(trashRoot, id, trashMetadataName)); finalErr == nil {
					if err := r.content.removeAll(pending); err != nil {
						return err
					}
					continue
				} else if !errors.Is(finalErr, os.ErrNotExist) {
					return finalErr
				}
				// A crash before writing the staged metadata cannot have reached
				// the journal commit point. The staging directory should contain
				// no payload; preserve and reject anything unexpected.
				staged, stagedErr := r.content.entriesUnder(pending)
				if stagedErr != nil {
					return stagedErr
				}
				for _, content := range staged {
					if content.Info.Mode()&os.ModeSymlink != 0 || !content.Info.IsDir() {
						return ErrInvalidPath
					}
				}
				if err := r.content.removeAll(pending); err != nil {
					return err
				}
				continue
			}
			return ErrInvalidPath
		}
		var value trashMetadata
		if err := json.Unmarshal(metadata.Bytes, &value); err != nil || validateTrashMetadata(value) != nil {
			clear(metadata.Bytes)
			return ErrInvalidPath
		}
		clear(metadata.Bytes)
		root := path.Join(trashRoot, id)
		logicalRoot := path.Join(root, trashContentName, "item")
		committed := false
		for _, object := range catalog.Objects {
			if object.Protected && (object.LogicalPath == logicalRoot || strings.HasPrefix(object.LogicalPath, logicalRoot+"/")) {
				committed = true
				break
			}
		}
		for _, rule := range catalog.FolderRules {
			committed = committed || rule.Path == logicalRoot || strings.HasPrefix(rule.Path, logicalRoot+"/")
		}
		if committed {
			if _, err := r.content.lstat(root); errors.Is(err, fs.ErrNotExist) {
				if err := r.content.mkdir(root, 0o755); err != nil {
					return err
				}
			}
			if err := r.content.rename(path.Join(pending, trashMetadataName), path.Join(root, trashMetadataName)); err != nil {
				return err
			}
			if err := r.content.removeAll(pending); err != nil {
				return err
			}
			if value.Type == "directory" {
				r.removeEmptyDirectories(value.OriginalPath)
			} else {
				r.removeEmptyDirectories(noteAssetsPath(value.OriginalPath))
			}
		} else {
			if err := r.content.removeAll(pending); err != nil {
				return err
			}
			r.removeEmptyDirectories(root)
		}
	}
	return r.reconcileCompletedTrashRoots(catalog)
}

// reconcileCompletedTrashRoots removes empty directories left behind if a
// protected restore or permanent delete committed its journal transaction but
// stopped before removing the Trash item's directory tree.
func (r *Repository) reconcileCompletedTrashRoots(catalog encryption.Catalog) error {
	trashRoot, err := r.ensureTrashRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := r.content.readDir(trashRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".pending-") {
			continue
		}
		if !trashIDPattern.MatchString(entry.Name) || entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.IsDir() {
			return ErrInvalidPath
		}
		itemRoot := path.Join(trashRoot, entry.Name)
		_, statErr := r.content.lstat(path.Join(itemRoot, trashMetadataName))
		if statErr == nil {
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		logicalRoot := path.Join(itemRoot, trashContentName, "item")
		for _, object := range catalog.Objects {
			if object.LogicalPath == logicalRoot || strings.HasPrefix(object.LogicalPath, logicalRoot+"/") ||
				object.PhysicalPath == logicalRoot || strings.HasPrefix(object.PhysicalPath, logicalRoot+"/") {
				return ErrInvalidPath
			}
		}
		for _, rule := range catalog.FolderRules {
			if rule.Path == logicalRoot || strings.HasPrefix(rule.Path, logicalRoot+"/") {
				return ErrInvalidPath
			}
		}
		contents, err := r.content.entriesUnder(itemRoot)
		if err != nil {
			return err
		}
		for _, content := range contents {
			if content.Info.Mode()&os.ModeSymlink != 0 || !content.Info.IsDir() && !content.Info.Mode().IsRegular() {
				return ErrInvalidPath
			}
			if !content.Info.IsDir() {
				// Missing metadata with remaining files is ambiguous. Preserve the
				// data and fail closed instead of treating it as completed work.
				return ErrInvalidPath
			}
		}
		if err := r.content.removeAll(itemRoot); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) finalizePendingTrash(id string) error {
	trashRoot, err := r.ensureTrashRoot(false)
	if err != nil {
		return err
	}
	pending := path.Join(trashRoot, ".pending-"+id)
	root := path.Join(trashRoot, id)
	if err := r.content.rename(path.Join(pending, trashMetadataName), path.Join(root, trashMetadataName)); err != nil {
		return err
	}
	return r.content.removeAll(pending)
}

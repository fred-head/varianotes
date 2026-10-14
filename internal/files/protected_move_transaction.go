package files

import (
	"errors"
	"io/fs"
	"path"
	"strings"

	"github.com/fred-head/varianotes/internal/encryption"
)

// Prepare every file at its destination as ciphertext and journal source
// deletions. No live rename, plaintext publication, or backlink update precedes
// the durable decision. Directory-only changes participate in recovery too.
func (r *Repository) prepareDurableMoveChangesLocked(source, target string, folder bool, catalog, next encryption.Catalog, key []byte, planned []plannedLinkFile, initial []encryption.CiphertextChange) ([]encryption.CiphertextChange, []string, []string, error) {
	changes := make(map[string]encryption.CiphertextChange)
	for _, change := range initial {
		changes[change.Path] = change
	}
	success := false
	defer func() {
		if !success {
			for _, change := range changes {
				clear(change.Ciphertext)
			}
		}
	}()
	updated := make(map[string]string)
	for _, item := range planned {
		updated[item.newPath] = item.updated
	}
	physical := make(map[string]encryption.CatalogObject)
	for _, object := range catalog.Objects {
		physical[object.PhysicalPath] = object
	}
	nextObjects := make(map[string]encryption.CatalogObject)
	for _, object := range next.Objects {
		nextObjects[object.ObjectID] = object
	}
	entries := make([]contentEntry, 0)
	create, remove := make([]string, 0), make([]string, 0)
	addRoot := func(root, destination string) error {
		info, err := r.content.lstat(root)
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return ErrInvalidPath
		}
		if info.IsDir() {
			create = append(create, destination)
			remove = append(remove, root)
			children, err := r.content.entriesUnder(root)
			if err != nil {
				return err
			}
			for _, child := range children {
				if child.Info.Mode()&fs.ModeSymlink != 0 {
					return ErrInvalidPath
				}
				if child.Info.IsDir() {
					create = append(create, destination+strings.TrimPrefix(child.Path, root))
					remove = append(remove, child.Path)
				} else {
					entries = append(entries, child)
				}
			}
		} else {
			entries = append(entries, contentEntry{Path: root, Info: info})
		}
		return nil
	}
	physicalSource := source
	if !folder {
		if object, found := catalogNote(catalog, source); found && object.Protected {
			physicalSource = object.PhysicalPath
		}
	}
	if err := addRoot(physicalSource, target); err != nil {
		return nil, nil, nil, err
	}
	if !folder {
		if _, err := r.content.lstat(noteAssetsPath(source)); err == nil {
			if err := addRoot(noteAssetsPath(source), noteAssetsPath(target)); err != nil {
				return nil, nil, nil, err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil, err
		}
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if !entry.Info.Mode().IsRegular() || !conversionHasSingleLink(entry.Info) {
			return nil, nil, nil, ErrConversionUnsafe
		}
		object, tracked := physical[entry.Path]
		logical := entry.Path
		if tracked {
			logical = object.LogicalPath
		}
		if !tracked && strings.EqualFold(path.Ext(entry.Path), ".age") {
			return nil, nil, nil, ErrProtectedObjectMissing
		}
		if protected, err := catalog.IsPathProtected(logical); err != nil {
			return nil, nil, nil, err
		} else if protected && (!tracked || !object.Protected) {
			return nil, nil, nil, ErrProtectedObjectMissing
		}
		maximum := int64(64 << 20)
		if tracked {
			maximum = transactionPlaintextLimit(object.Kind) + (1 << 20)
		}
		stored, err := r.content.read(entry.Path, maximum)
		if err != nil {
			return nil, nil, nil, err
		}
		if tracked && object.Protected {
			if err := encryption.VerifyCatalogCiphertext(object, stored.Bytes); err != nil {
				clear(stored.Bytes)
				return nil, nil, nil, ErrProtectedContentCorrupt
			}
		}
		newLogical := remapSelectedMovePath(logical, source, target, folder)
		newPhysical := newLogical
		nextObject, retained := nextObjects[object.ObjectID]
		if tracked && object.Protected && retained && nextObject.Protected {
			newPhysical = nextObject.PhysicalPath
		}
		if _, prepared := changes[newPhysical]; !prepared {
			var change encryption.CiphertextChange
			if tracked && object.Protected && retained && nextObject.Protected {
				change = encryption.CiphertextChange{Path: newPhysical, ObjectID: object.ObjectID, Ciphertext: append([]byte(nil), stored.Bytes...), ContentKeyEnvelope: *cloneContentKeyEnvelope(object.ContentKeyEnvelope)}
			} else {
				plain := stored.Bytes
				if tracked && object.Protected {
					plain, err = encryption.DecryptContent(key, object.ObjectID, stored.Bytes, *object.ContentKeyEnvelope)
					if err != nil {
						clear(stored.Bytes)
						return nil, nil, nil, err
					}
					if object.Kind == encryption.CatalogObjectAsset {
						if _, err := supportedImageType(plain); err != nil {
							clear(plain)
							clear(stored.Bytes)
							return nil, nil, nil, err
						}
					}
				}
				if text, exists := updated[newLogical]; exists {
					clear(plain)
					plain = []byte(text)
				}
				id, idErr := encryption.NewCatalogObjectID()
				if idErr != nil {
					clear(plain)
					clear(stored.Bytes)
					return nil, nil, nil, idErr
				}
				ciphertext, envelope, encryptErr := encryption.EncryptContent(key, id, plain)
				clear(plain)
				if encryptErr != nil {
					clear(stored.Bytes)
					return nil, nil, nil, encryptErr
				}
				change = encryption.CiphertextChange{Path: newPhysical, ObjectID: id, Ciphertext: ciphertext, ContentKeyEnvelope: envelope, PublishPlaintext: true}
			}
			changes[newPhysical] = change
		}
		changes[entry.Path] = encryption.CiphertextChange{Path: entry.Path, Delete: true, ExpectedSHA256: stored.Version, RejectHardlinks: true}
		clear(stored.Bytes)
		seen[entry.Path] = true
	}
	for _, object := range catalog.Objects {
		if pathSelectedForMove(object.LogicalPath, source, folder) && !seen[object.PhysicalPath] {
			return nil, nil, nil, ErrProtectedObjectMissing
		}
	}
	for _, item := range planned {
		if pathSelectedForMove(item.oldPath, source, folder) {
			continue
		}
		if object, found := catalogNote(catalog, item.oldPath); found && object.Protected {
			continue
		}
		stored, err := r.content.read(item.oldPath, maxMarkdownSize)
		if err != nil {
			return nil, nil, nil, err
		}
		if string(stored.Bytes) != item.content {
			clear(stored.Bytes)
			return nil, nil, nil, ErrConflict
		}
		clear(stored.Bytes)
		id, err := encryption.NewCatalogObjectID()
		if err != nil {
			return nil, nil, nil, err
		}
		ciphertext, envelope, err := encryption.EncryptContent(key, id, []byte(item.updated))
		if err != nil {
			return nil, nil, nil, err
		}
		changes[item.newPath] = encryption.CiphertextChange{Path: item.newPath, ObjectID: id, Ciphertext: ciphertext, ContentKeyEnvelope: envelope, PublishPlaintext: true}
	}
	result := make([]encryption.CiphertextChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	success = true
	return result, create, remove, nil
}

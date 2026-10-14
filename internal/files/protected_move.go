package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/fred-head/varianotes/internal/encryption"
)

const (
	MoveProtectionKeep    = "keep"
	MoveProtectionDecrypt = "decrypt"
)

// PreviewMoveLinksWithCatalog builds the same link preview as PreviewMoveLinks,
// while resolving protected notes through the authenticated catalog.
func (r *Repository) PreviewMoveLinksWithCatalog(source, target string, catalog encryption.Catalog, vaultKey []byte) (MoveLinkPreview, error) {
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	preview, _, err := r.planMoveLinksWithCatalogLocked(source, target, catalog, vaultKey)
	return preview, err
}

// NoteLinksWithCatalog inventories links without exposing protected content on
// disk or treating physical .age paths as logical note paths.
func (r *Repository) NoteLinksWithCatalog(notePath string, catalog encryption.Catalog, vaultKey []byte) ([]NoteLink, error) {
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	markdown, _, err := r.readMarkdownWithCatalogLocked(notePath, catalog, vaultKey)
	if err != nil {
		return nil, err
	}
	notePaths, err := r.markdownNotePaths()
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(notePaths)+len(catalog.Objects))
	for _, candidate := range notePaths {
		known[candidate] = true
	}
	for _, object := range catalog.Objects {
		if object.Kind == encryption.CatalogObjectNote {
			known[object.LogicalPath] = true
		}
	}
	result := make([]NoteLink, 0)
	for _, occurrence := range parseMarkdownLinks(notePath, markdown.Content) {
		result = append(result, NoteLink{
			Href: occurrence.href, TargetPath: occurrence.targetPath, Line: occurrence.line,
			Exists: occurrence.internal && known[occurrence.targetPath], Internal: occurrence.internal,
		})
	}
	return result, nil
}

// MoveWithCatalog moves a note or folder in an encrypted vault. Protected
// objects keep their identities; link changes to protected notes and catalog
// path changes are committed together through the encryption journal.
func (r *Repository) MoveWithCatalog(source, target, expectedToken, protectionChoice string, catalog encryption.Catalog, vaultKey []byte, catalogStore *encryption.CatalogStore, journal *encryption.TransactionJournal) (MoveLinkPreview, error) {
	if len(vaultKey) != encryption.VaultKeySize || catalogStore == nil || journal == nil {
		return MoveLinkPreview{}, encryption.ErrInvalidTransaction
	}
	preview, err := r.PreviewMoveLinksWithCatalog(source, target, catalog, vaultKey)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	if expectedToken != "" && expectedToken != preview.Token {
		return MoveLinkPreview{}, fmt.Errorf("link rewrite preview is stale: %w", ErrConflict)
	}
	if len(preview.Rewrites) > 0 && expectedToken == "" {
		return MoveLinkPreview{}, ErrLinkPreviewRequired
	}

	targetProtected, err := catalog.IsPathProtected(target)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	if protectionChoice != "" && protectionChoice != MoveProtectionKeep && protectionChoice != MoveProtectionDecrypt {
		return MoveLinkPreview{}, ErrInvalidMoveProtectionChoice
	}
	if protectionChoice == MoveProtectionDecrypt && targetProtected {
		return MoveLinkPreview{}, ErrMoveDecryptIntoProtected
	}
	if targetProtected {
		// Use the existing prepared conversion so new protected descendants are
		// encrypted before they become visible under the protected destination.
		// This also converts plaintext siblings and assets when a partially
		// protected subtree enters a recursively protected destination.
		conversion, convertErr := r.PrepareExistingConversion(context.Background(), source, catalog, vaultKey, catalogStore, journal)
		if convertErr != nil {
			return MoveLinkPreview{}, convertErr
		}
		currentPreview, _, previewErr := r.planMoveLinksWithCatalogLocked(source, target, catalog, vaultKey)
		if previewErr != nil || currentPreview.Token != preview.Token {
			_ = conversion.Abort()
			if previewErr != nil {
				return MoveLinkPreview{}, previewErr
			}
			return MoveLinkPreview{}, fmt.Errorf("link rewrite preview is stale: %w", ErrConflict)
		}
		if convertErr = conversion.Commit(); convertErr != nil {
			_ = conversion.Abort()
			return MoveLinkPreview{}, convertErr
		}
		catalog, err = catalogStore.Load(vaultKey, catalog.VaultID)
		if err != nil {
			return MoveLinkPreview{}, err
		}
		// The token is content based, so conversion does not invalidate an
		// otherwise unchanged preview.
		return r.moveWithCatalogPrepared(source, target, expectedToken, protectionChoice, preview.Token, catalog, vaultKey, catalogStore, journal)
	}
	return r.moveWithCatalogPrepared(source, target, expectedToken, protectionChoice, preview.Token, catalog, vaultKey, catalogStore, journal)
}

func (r *Repository) moveWithCatalogPrepared(source, target, expectedToken, protectionChoice, approvedToken string, catalog encryption.Catalog, vaultKey []byte, catalogStore *encryption.CatalogStore, journal *encryption.TransactionJournal) (MoveLinkPreview, error) {
	r.moveMu.Lock()
	defer r.moveMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()

	if err := ensureCurrentCatalog(catalogStore, catalog, vaultKey); err != nil {
		return MoveLinkPreview{}, err
	}
	preview, planned, err := r.planMoveLinksWithCatalogLocked(source, target, catalog, vaultKey)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	if preview.Token != approvedToken || expectedToken != "" && expectedToken != preview.Token {
		return MoveLinkPreview{}, fmt.Errorf("link rewrite preview is stale: %w", ErrConflict)
	}

	_, sourceIsFolder, err := r.resolveMoveSourceWithCatalog(source, catalog)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	selectionProtected, err := selectionHasProtection(source, catalog)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	targetProtected, err := catalog.IsPathProtected(target)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	if selectionProtected && !targetProtected {
		switch protectionChoice {
		case MoveProtectionKeep, MoveProtectionDecrypt:
		default:
			return MoveLinkPreview{}, ErrMoveProtectionChoiceRequired
		}
		if protectionChoice == MoveProtectionDecrypt && targetProtected {
			return MoveLinkPreview{}, ErrMoveDecryptIntoProtected
		}
	} else if protectionChoice != "" && protectionChoice != MoveProtectionKeep && protectionChoice != MoveProtectionDecrypt {
		return MoveLinkPreview{}, ErrInvalidMoveProtectionChoice
	}
	if protectionChoice == MoveProtectionDecrypt && targetProtected {
		return MoveLinkPreview{}, ErrMoveDecryptIntoProtected
	}

	selectedObjects := make(map[string]encryption.CatalogObject)
	for _, object := range catalog.Objects {
		if pathSelectedForMove(object.LogicalPath, source, sourceIsFolder) {
			selectedObjects[object.ObjectID] = object
		}
	}
	selectedRules := make([]encryption.FolderProtectionRule, 0)
	if sourceIsFolder {
		for _, rule := range catalog.FolderRules {
			if rule.Path == source || strings.HasPrefix(rule.Path, source+"/") {
				selectedRules = append(selectedRules, rule)
			}
		}
	}
	keepProtectedAssets := make(map[string]bool)
	if protectionChoice == MoveProtectionDecrypt {
		referencedAssets, referenceErr := r.protectedAssetReferencesOutsideSelectionLocked(catalog, vaultKey, source, sourceIsFolder)
		if referenceErr != nil {
			return MoveLinkPreview{}, referenceErr
		}
		for _, object := range selectedObjects {
			if object.Protected && object.Kind == encryption.CatalogObjectAsset && referencedAssets[object.LogicalPath] {
				// Shared protected assets stay one encrypted object for every
				// reference, including references outside the moved selection.
				keepProtectedAssets[object.ObjectID] = true
			}
		}
	}

	nextCatalog := cloneCatalog(catalog)
	changes := make([]encryption.CiphertextChange, 0, len(selectedObjects)+len(planned))
	updatedContent := make(map[string]string)
	for _, item := range planned {
		updatedContent[item.newPath] = item.updated
	}
	newObjects := make([]encryption.CatalogObject, 0, len(nextCatalog.Objects))
	for _, object := range nextCatalog.Objects {
		_, selected := selectedObjects[object.ObjectID]
		if selected && object.Protected && protectionChoice == MoveProtectionDecrypt && !keepProtectedAssets[object.ObjectID] {
			continue
		}
		if selected {
			object.LogicalPath = remapSelectedMovePath(object.LogicalPath, source, target, sourceIsFolder)
			if object.Protected {
				object.PhysicalPath = object.LogicalPath + ".age"
			} else {
				object.PhysicalPath = object.LogicalPath
			}
		}
		if object.Kind == encryption.CatalogObjectNote {
			if content, hasUpdated := updatedContent[object.LogicalPath]; hasUpdated && object.Protected {
				ciphertext, envelope, encryptErr := encryption.EncryptContent(vaultKey, object.ObjectID, []byte(content))
				if encryptErr != nil {
					return MoveLinkPreview{}, encryptErr
				}
				object.CiphertextHash = encryption.CiphertextSHA256(ciphertext)
				object.ContentKeyEnvelope = cloneContentKeyEnvelope(&envelope)
				changes = append(changes, encryption.CiphertextChange{Path: object.PhysicalPath, ObjectID: object.ObjectID, Ciphertext: ciphertext, ContentKeyEnvelope: envelope})
			}
		}
		newObjects = append(newObjects, object)
	}
	nextCatalog.Objects = newObjects
	if sourceIsFolder {
		for index := range nextCatalog.FolderRules {
			rule := &nextCatalog.FolderRules[index]
			if strings.HasPrefix(rule.Path, source+"/") || rule.Path == source {
				rule.Path = remapMovedPath(rule.Path, source, target)
			}
		}
		if protectionChoice == MoveProtectionDecrypt {
			remaining := nextCatalog.FolderRules[:0]
			for _, rule := range nextCatalog.FolderRules {
				if rule.Path != target && !strings.HasPrefix(rule.Path, target+"/") {
					remaining = append(remaining, rule)
				}
			}
			nextCatalog.FolderRules = remaining
		} else if selectionProtected && !targetProtected && protectionChoice == MoveProtectionKeep && len(selectedRules) == 0 {
			// Preserve a recursive source policy inherited from an ancestor when
			// the moved subtree is kept protected outside that ancestor.
			inherited, _ := catalog.IsPathProtected(source)
			if inherited {
				nextCatalog.FolderRules = append(nextCatalog.FolderRules, encryption.FolderProtectionRule{Path: target})
			}
		}
	}

	changes, createDirectories, removeDirectories, err := r.prepareDurableMoveChangesLocked(source, target, sourceIsFolder, catalog, nextCatalog, vaultKey, planned, changes)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	defer func() {
		for _, change := range changes {
			clear(change.Ciphertext)
		}
	}()
	transaction, err := journal.PrepareFilesystem(changes, nextCatalog, vaultKey, createDirectories, removeDirectories)
	if err == nil && r.transactionPrepared != nil {
		err = r.transactionPrepared()
	}
	if err == nil {
		err = transaction.Commit()
	}
	if err != nil {
		return MoveLinkPreview{}, err
	}

	return preview, nil
}

func (r *Repository) protectedAssetReferencesOutsideSelectionLocked(catalog encryption.Catalog, vaultKey []byte, source string, folder bool) (map[string]bool, error) {
	referenced := make(map[string]bool)
	for _, object := range catalog.Objects {
		if object.Kind != encryption.CatalogObjectNote || !object.Protected || pathSelectedForMove(object.LogicalPath, source, folder) {
			continue
		}
		markdown, protected, err := r.readMarkdownWithCatalogLocked(object.LogicalPath, catalog, vaultKey)
		if err != nil {
			return nil, err
		}
		if !protected {
			continue
		}
		for _, destination := range markdownDestinations(markdown.Content) {
			assetPath, _, ok := resolveAssetReference(object.LogicalPath, destination)
			if ok {
				referenced[assetPath] = true
			}
		}
	}
	return referenced, nil
}

var (
	ErrMoveProtectionChoiceRequired = errors.New("choose whether to keep protection or decrypt before moving protected content")
	ErrMoveDecryptIntoProtected     = errors.New("protected content cannot be decrypted inside a protected folder")
	ErrInvalidMoveProtectionChoice  = errors.New("invalid move protection choice")
	ErrMoveRollbackFailed           = errors.New("move rollback failed; inspect the source and destination before retrying")
)

func moveRollbackError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrMoveRollbackFailed, err)
}

func transactionPlaintextLimit(kind encryption.CatalogObjectKind) int64 {
	if kind == encryption.CatalogObjectAsset {
		return maxAssetSize
	}
	return maxMarkdownSize
}

func selectionHasProtection(source string, catalog encryption.Catalog) (bool, error) {
	if protected, err := catalog.IsPathProtected(source); err != nil || protected {
		return protected, err
	}
	for _, object := range catalog.Objects {
		if object.Protected && pathSelectedForMove(object.LogicalPath, source, false) {
			return true, nil
		}
		if object.Protected && strings.HasPrefix(object.LogicalPath, source+"/") {
			return true, nil
		}
	}
	return false, nil
}

func pathSelectedForMove(value, source string, folder bool) bool {
	if value == source || folder && strings.HasPrefix(value, source+"/") {
		return true
	}
	if !folder {
		return strings.HasPrefix(value, noteAssetsPath(source)+"/")
	}
	return false
}

func remapSelectedMovePath(value, source, target string, folder bool) string {
	if folder {
		return remapMovedPath(value, source, target)
	}
	if value == source {
		return target
	}
	oldAssets, newAssets := noteAssetsPath(source), noteAssetsPath(target)
	if strings.HasPrefix(value, oldAssets+"/") {
		return newAssets + strings.TrimPrefix(value, oldAssets)
	}
	return value
}

func (r *Repository) resolveMoveSourceWithCatalog(source string, catalog encryption.Catalog) (os.FileInfo, bool, error) {
	if info, err := r.content.lstat(source); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return nil, false, ErrInvalidPath
		}
		if !info.IsDir() && !strings.EqualFold(path.Ext(source), ".md") {
			return nil, false, ErrNotMarkdown
		}
		return info, info.IsDir(), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	object, found := catalogNote(catalog, source)
	if !found || object.Kind != encryption.CatalogObjectNote || !object.Protected {
		return nil, false, fs.ErrNotExist
	}
	if _, err := r.content.lstat(object.PhysicalPath); err != nil {
		return nil, false, protectedContentError(err)
	}
	return nil, false, nil
}

func (r *Repository) planMoveLinksWithCatalogLocked(source, target string, catalog encryption.Catalog, vaultKey []byte) (MoveLinkPreview, []plannedLinkFile, error) {
	_, folder, err := r.resolveMoveSourceWithCatalog(source, catalog)
	if err != nil {
		return MoveLinkPreview{}, nil, err
	}
	if err := r.validateRelative(target, !folder); err != nil {
		return MoveLinkPreview{}, nil, err
	}
	if folder && (target == source || strings.HasPrefix(target, source+"/")) {
		return MoveLinkPreview{}, nil, ErrInvalidPath
	}
	if _, err := r.content.lstat(target); err == nil {
		return MoveLinkPreview{}, nil, ErrAlreadyExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return MoveLinkPreview{}, nil, err
	}
	if _, exists := catalogNote(catalog, target); exists {
		return MoveLinkPreview{}, nil, ErrAlreadyExists
	}
	if _, err := r.content.lstat(target + ".age"); err == nil {
		return MoveLinkPreview{}, nil, ErrAlreadyExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return MoveLinkPreview{}, nil, err
	}

	notePaths, err := r.markdownNotePaths()
	if err != nil {
		return MoveLinkPreview{}, nil, err
	}
	seen := make(map[string]bool, len(notePaths)+len(catalog.Objects))
	for _, notePath := range notePaths {
		seen[notePath] = true
	}
	for _, object := range catalog.Objects {
		if object.Kind == encryption.CatalogObjectNote {
			seen[object.LogicalPath] = true
		}
	}
	notePaths = notePaths[:0]
	for notePath := range seen {
		notePaths = append(notePaths, notePath)
	}
	sort.Strings(notePaths)
	noteSet := make(map[string]bool, len(notePaths))
	for _, notePath := range notePaths {
		noteSet[notePath] = true
	}
	preview := MoveLinkPreview{Source: source, Target: target, Rewrites: []LinkRewrite{}}
	plannedFiles := make([]plannedLinkFile, 0)
	hash := sha256.New()
	_, _ = hash.Write([]byte(source + "\x00" + target + "\x00"))
	for _, oldNotePath := range notePaths {
		markdown, _, readErr := r.readMarkdownWithCatalogLocked(oldNotePath, catalog, vaultKey)
		if readErr != nil {
			return MoveLinkPreview{}, nil, readErr
		}
		newNotePath := remapMovedPath(oldNotePath, source, target)
		occurrences := parseMarkdownLinks(oldNotePath, markdown.Content)
		replacements := make([]linkReplacement, 0)
		for _, occurrence := range occurrences {
			if !occurrence.internal || !noteSet[occurrence.targetPath] {
				continue
			}
			newTargetPath := remapMovedPath(occurrence.targetPath, source, target)
			if newNotePath == oldNotePath && newTargetPath == occurrence.targetPath {
				continue
			}
			nextHref, relativeErr := portableRelativeLink(newNotePath, newTargetPath, occurrence.suffix, occurrence.angle)
			if relativeErr != nil {
				return MoveLinkPreview{}, nil, relativeErr
			}
			if nextHref == occurrence.href {
				continue
			}
			replacements = append(replacements, linkReplacement{start: occurrence.hrefStart, end: occurrence.hrefEnd, value: nextHref})
			preview.Rewrites = append(preview.Rewrites, LinkRewrite{NotePath: oldNotePath, NextNotePath: newNotePath, Line: occurrence.line, Before: occurrence.href, After: nextHref})
		}
		for _, occurrence := range parseMarkdownAssetLinks(oldNotePath, markdown.Content) {
			if !occurrence.internal {
				continue
			}
			newAssetPath := remapSelectedMovePath(occurrence.targetPath, source, target, folder)
			if newNotePath == oldNotePath && newAssetPath == occurrence.targetPath {
				continue
			}
			nextHref, relativeErr := portableRelativeLink(newNotePath, newAssetPath, occurrence.suffix, occurrence.angle)
			if relativeErr != nil {
				return MoveLinkPreview{}, nil, relativeErr
			}
			if nextHref == occurrence.href {
				continue
			}
			replacements = append(replacements, linkReplacement{start: occurrence.hrefStart, end: occurrence.hrefEnd, value: nextHref})
			preview.Rewrites = append(preview.Rewrites, LinkRewrite{NotePath: oldNotePath, NextNotePath: newNotePath, Line: occurrence.line, Before: occurrence.href, After: nextHref})
		}
		updated := applyLinkReplacements(markdown.Content, replacements)
		afterMove := markdown.Content
		if oldNotePath == source && strings.EqualFold(path.Ext(source), ".md") {
			oldBase, newBase := noteBase(source), noteBase(target)
			if oldBase != newBase {
				afterMove = rewriteAssetLinksContent(afterMove, oldBase, newBase)
				updated = rewriteAssetLinksContent(updated, oldBase, newBase)
			}
		}
		if updated != markdown.Content {
			plannedFiles = append(plannedFiles, plannedLinkFile{oldPath: oldNotePath, newPath: newNotePath, content: markdown.Content, afterMove: afterMove, updated: updated})
		}
		_, _ = hash.Write([]byte(oldNotePath + "\x00" + markdown.Content + "\x00" + updated + "\x00"))
	}
	sort.Slice(preview.Rewrites, func(left, right int) bool {
		if preview.Rewrites[left].NotePath != preview.Rewrites[right].NotePath {
			return preview.Rewrites[left].NotePath < preview.Rewrites[right].NotePath
		}
		return preview.Rewrites[left].Line < preview.Rewrites[right].Line
	})
	preview.Token = hex.EncodeToString(hash.Sum(nil))
	return preview, plannedFiles, nil
}

func parseMarkdownAssetLinks(notePath, content string) []linkOccurrence {
	ignored := markdownCodeMask(content)
	matches := inlineMarkdownLinkPattern.FindAllStringSubmatchIndex(content, -1)
	result := make([]linkOccurrence, 0)
	for _, match := range matches {
		start, hrefStart, hrefEnd := match[0], match[2], match[3]
		if start == 0 || content[start-1] != '!' || ignored[hrefStart] {
			continue
		}
		raw := content[hrefStart:hrefEnd]
		angle := strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">")
		href := raw
		if angle {
			href = raw[1 : len(raw)-1]
		}
		assetPath, _, internal := resolveAssetReference(notePath, href)
		if !internal {
			continue
		}
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		suffix := ""
		if parsed.RawQuery != "" {
			suffix += "?" + parsed.RawQuery
		}
		if parsed.Fragment != "" {
			suffix += "#" + parsed.Fragment
		}
		result = append(result, linkOccurrence{
			hrefStart: hrefStart, hrefEnd: hrefEnd, href: raw, angle: angle,
			line: 1 + strings.Count(content[:hrefStart], "\n"), targetPath: assetPath,
			suffix: suffix, internal: true,
		})
	}
	return result
}

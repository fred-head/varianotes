package files

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/fred-head/varianotes/internal/encryption"
)

const maxMarkdownCiphertextSize = maxMarkdownSize + (1 << 20)

var (
	ErrProtectedObjectMissing  = errors.New("protected note metadata is missing")
	ErrProtectedContentCorrupt = errors.New("protected note failed integrity verification")
)

// PreparedMarkdownOperation holds the repository write lock between policy
// evaluation and commit. Commit publishes its result only after durable storage
// succeeds; Abort releases the lock when a session lock invalidates the caller.
type PreparedMarkdownOperation struct {
	Markdown Markdown
	commit   func() error
	abort    func() error
	release  func()
	done     bool
}

func (operation *PreparedMarkdownOperation) Commit(publish func(Markdown) error) error {
	if operation == nil || operation.done || operation.commit == nil {
		return errors.New("invalid prepared Markdown operation")
	}
	operation.done = true
	defer operation.release()
	if err := operation.commit(); err != nil {
		return err
	}
	if publish != nil {
		return publish(operation.Markdown)
	}
	return nil
}

func (operation *PreparedMarkdownOperation) Abort() error {
	if operation == nil || operation.done {
		return nil
	}
	operation.done = true
	defer operation.release()
	if operation.abort != nil {
		return operation.abort()
	}
	return nil
}

// ReadMarkdownWithCatalog resolves a note through authenticated catalog policy.
// The boolean reports whether the path has a protected rule or object. Callers
// must publish its result through their key lease to reject late responses.
func (r *Repository) ReadMarkdownWithCatalog(relative string, catalog encryption.Catalog, vaultKey []byte) (Markdown, bool, error) {
	if err := r.validateRelative(relative, true); err != nil {
		return Markdown{}, false, err
	}
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	return r.readMarkdownWithCatalogLocked(relative, catalog, vaultKey)
}

func (r *Repository) readMarkdownWithCatalogLocked(relative string, catalog encryption.Catalog, vaultKey []byte) (Markdown, bool, error) {
	object, found := catalogNote(catalog, relative)
	protectedByRule, err := catalog.IsPathProtected(relative)
	if err != nil {
		return Markdown{}, false, err
	}
	if found {
		if object.Kind != encryption.CatalogObjectNote {
			return Markdown{}, false, ErrProtectedObjectMissing
		}
		if object.Protected {
			plaintext, err := r.readProtectedBytes(object, vaultKey)
			if err != nil {
				return Markdown{}, true, err
			}
			content := string(plaintext)
			clear(plaintext)
			return Markdown{Content: content, Version: object.CiphertextHash}, true, nil
		}
		if _, err := r.content.lstat(relative + ".age"); err == nil {
			return Markdown{}, false, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return Markdown{}, false, err
		}
		stored, readErr := r.content.read(relative, maxMarkdownSize)
		if readErr != nil {
			return Markdown{}, false, readErr
		}
		return Markdown{Content: string(stored.Bytes), Version: stored.Version}, false, nil
	}
	if protectedByRule {
		if _, err := r.content.lstat(relative); err == nil {
			return Markdown{}, true, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return Markdown{}, true, err
		}
		if _, err := r.content.lstat(relative + ".age"); err == nil {
			return Markdown{}, true, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return Markdown{}, true, err
		}
		return Markdown{}, true, os.ErrNotExist
	}
	if _, err := r.content.lstat(relative + ".age"); err == nil {
		return Markdown{}, true, ErrProtectedObjectMissing
	} else if !errors.Is(err, os.ErrNotExist) {
		return Markdown{}, false, err
	}
	stored, err := r.content.read(relative, maxMarkdownSize)
	if err != nil {
		return Markdown{}, false, err
	}
	return Markdown{Content: string(stored.Bytes), Version: stored.Version}, false, nil
}

// PrepareMarkdownWriteWithCatalog checks the authenticated policy and stages
// protected updates as ciphertext plus a matching catalog. Plain notes retain
// the ordinary version-checked atomic writer.
func (r *Repository) PrepareMarkdownWriteWithCatalog(relative, content, expectedVersion string, catalog encryption.Catalog, vaultKey []byte, catalogStore *encryption.CatalogStore, journal *encryption.TransactionJournal) (*PreparedMarkdownOperation, bool, error) {
	if err := r.validateRelative(relative, true); err != nil {
		return nil, false, err
	}
	if int64(len(content)) > maxMarkdownSize {
		return nil, false, ErrFileTooLarge
	}
	r.content.mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			r.content.mu.Unlock()
		}
	}()
	if err := ensureCurrentCatalog(catalogStore, catalog, vaultKey); err != nil {
		return nil, false, err
	}

	object, found := catalogNote(catalog, relative)
	protectedByRule, err := catalog.IsPathProtected(relative)
	if err != nil {
		return nil, false, err
	}
	if found && object.Kind != encryption.CatalogObjectNote {
		return nil, false, ErrProtectedObjectMissing
	}
	if found && object.Protected {
		plaintext, err := r.readProtectedBytes(object, vaultKey)
		if err != nil {
			return nil, true, err
		}
		clear(plaintext)
		if expectedVersion == "" || object.CiphertextHash != expectedVersion {
			return nil, true, ErrConflict
		}
		if err := r.validateProtectedReferencesLocked(relative, content, catalog, vaultKey); err != nil {
			return nil, true, err
		}
		ciphertext, envelope, err := encryption.EncryptContent(vaultKey, object.ObjectID, []byte(content))
		if err != nil {
			return nil, true, err
		}
		nextCatalog := cloneCatalog(catalog)
		nextObject := object
		nextObject.CiphertextHash = encryption.CiphertextSHA256(ciphertext)
		nextObject.ContentKeyEnvelope = cloneContentKeyEnvelope(&envelope)
		for index := range nextCatalog.Objects {
			if nextCatalog.Objects[index].ObjectID == object.ObjectID {
				nextCatalog.Objects[index] = nextObject
				break
			}
		}
		transaction, err := journal.Prepare([]encryption.CiphertextChange{{
			Path: object.PhysicalPath, ObjectID: object.ObjectID, Ciphertext: ciphertext, ContentKeyEnvelope: envelope,
		}}, nextCatalog, vaultKey)
		if err != nil {
			return nil, true, err
		}
		unlock = false
		return &PreparedMarkdownOperation{
			Markdown: Markdown{Content: content, Version: nextObject.CiphertextHash},
			commit:   transaction.Commit, abort: transaction.Abort, release: r.content.mu.Unlock,
		}, true, nil
	}
	if found {
		if _, err := r.content.lstat(relative + ".age"); err == nil {
			return nil, false, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, false, err
		}
	}
	if protectedByRule {
		if _, err := r.content.lstat(relative); err == nil {
			return nil, true, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, true, err
		}
		return nil, true, os.ErrNotExist
	}
	if _, err := r.content.lstat(relative + ".age"); err == nil {
		return nil, true, ErrProtectedObjectMissing
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	current, err := r.content.read(relative, maxMarkdownSize)
	if err != nil {
		return nil, false, err
	}
	if expectedVersion == "" || current.Version != expectedVersion {
		return nil, false, ErrConflict
	}
	unlock = false
	return &PreparedMarkdownOperation{
		Markdown: Markdown{Content: content, Version: contentVersion([]byte(content))},
		commit: func() error {
			_, err := r.content.write(relative, []byte(content), expectedVersion, maxMarkdownSize)
			return err
		}, release: r.content.mu.Unlock,
	}, false, nil
}

// PrepareMarkdownCreateWithCatalog encrypts a newly created protected note
// before its first durable byte. Ordinary creates preserve current behavior.
func (r *Repository) PrepareMarkdownCreateWithCatalog(relative string, catalog encryption.Catalog, vaultKey []byte, catalogStore *encryption.CatalogStore, journal *encryption.TransactionJournal) (*PreparedMarkdownOperation, bool, error) {
	if err := r.validateRelative(relative, true); err != nil {
		return nil, false, err
	}
	r.content.mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			r.content.mu.Unlock()
		}
	}()
	if err := ensureCurrentCatalog(catalogStore, catalog, vaultKey); err != nil {
		return nil, false, err
	}

	if _, err := r.resolveNew(relative, true); err != nil {
		return nil, false, err
	}
	if _, exists := catalogNote(catalog, relative); exists {
		return nil, false, ErrAlreadyExists
	}
	if _, err := r.content.lstat(relative + ".age"); err == nil {
		return nil, false, ErrProtectedObjectMissing
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	protected, err := catalog.IsPathProtected(relative)
	if err != nil {
		return nil, false, err
	}
	if !protected {
		unlock = false
		return &PreparedMarkdownOperation{
			commit:  func() error { return r.create(relative, "file") },
			release: r.content.mu.Unlock,
		}, false, nil
	}
	physicalPath := relative + ".age"
	if _, err := r.content.lstat(physicalPath); err == nil {
		return nil, true, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, true, err
	}
	objectID, err := encryption.NewCatalogObjectID()
	if err != nil {
		return nil, true, err
	}
	title := strings.TrimSuffix(path.Base(relative), path.Ext(relative))
	initialContent := []byte(fmt.Sprintf("# %s\n", title))
	ciphertext, envelope, err := encryption.EncryptContent(vaultKey, objectID, initialContent)
	if err != nil {
		return nil, true, err
	}
	nextCatalog := cloneCatalog(catalog)
	nextCatalog.Objects = append(nextCatalog.Objects, encryption.CatalogObject{
		ObjectID: objectID, Kind: encryption.CatalogObjectNote, LogicalPath: relative,
		PhysicalPath: physicalPath, CiphertextHash: encryption.CiphertextSHA256(ciphertext),
		ContentKeyEnvelope: cloneContentKeyEnvelope(&envelope), Protected: true,
	})
	transaction, err := journal.Prepare([]encryption.CiphertextChange{{
		Path: physicalPath, ObjectID: objectID, Ciphertext: ciphertext, ContentKeyEnvelope: envelope,
	}}, nextCatalog, vaultKey)
	if err != nil {
		return nil, true, err
	}
	unlock = false
	return &PreparedMarkdownOperation{
		Markdown: Markdown{Content: string(initialContent), Version: encryption.CiphertextSHA256(ciphertext)},
		commit:   transaction.Commit, abort: transaction.Abort, release: r.content.mu.Unlock,
	}, true, nil
}

// TreeWithCatalog adds protected note names from authenticated logical paths;
// the physical .age files remain invisible as implementation details.
func (r *Repository) TreeWithCatalog(catalog encryption.Catalog) ([]Node, error) {
	tree, err := r.Tree()
	if err != nil {
		return nil, err
	}
	for _, object := range catalog.Objects {
		if object.Kind != encryption.CatalogObjectNote || !object.Protected {
			continue
		}
		if err := insertTreeFile(&tree, object.LogicalPath); err != nil {
			return nil, err
		}
	}
	markProtectionState(tree, catalog)
	sortTree(tree)
	return tree, nil
}

func markProtectionState(nodes []Node, catalog encryption.Catalog) {
	protectedObjects := make(map[string]bool, len(catalog.Objects))
	for _, object := range catalog.Objects {
		if object.Protected {
			protectedObjects[object.LogicalPath] = true
		}
	}
	rules := make([]string, 0, len(catalog.FolderRules))
	for _, rule := range catalog.FolderRules {
		rules = append(rules, rule.Path)
	}
	markTreeProtection(nodes, protectedObjects, rules, "")
}

func markTreeProtection(nodes []Node, protectedObjects map[string]bool, rules []string, inheritedFrom string) {
	for index := range nodes {
		node := &nodes[index]
		protectedByRule := false
		directRule := false
		for _, rule := range rules {
			if node.Path == rule || strings.HasPrefix(node.Path, rule+"/") {
				protectedByRule = true
				if node.Path == rule {
					directRule = true
				}
				break
			}
		}
		node.Protected = protectedByRule || protectedObjects[node.Path]
		node.ProtectionInherited = protectedByRule && inheritedFrom != ""
		nextInherited := inheritedFrom
		if directRule && node.Type == "directory" {
			nextInherited = node.Path
		}
		markTreeProtection(node.Children, protectedObjects, rules, nextInherited)
	}
}

func (r *Repository) HasAgeFiles() (bool, error) {
	if !r.Configured() {
		return false, ErrNotConfigured
	}
	entries, err := r.content.Entries()
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

func (r *Repository) readProtectedBytes(object encryption.CatalogObject, vaultKey []byte) ([]byte, error) {
	return r.readProtectedBytesLimit(object, vaultKey, maxMarkdownSize, maxMarkdownCiphertextSize)
}

func (r *Repository) readProtectedBytesLimit(object encryption.CatalogObject, vaultKey []byte, maximumPlaintext, maximumCiphertext int64) ([]byte, error) {
	if _, err := r.content.lstat(object.LogicalPath); err == nil {
		return nil, ErrProtectedContentCorrupt
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stored, err := r.content.read(object.PhysicalPath, maximumCiphertext)
	if err != nil {
		return nil, protectedContentError(err)
	}
	if err := encryption.VerifyCatalogCiphertext(object, stored.Bytes); err != nil {
		return nil, errors.Join(ErrProtectedContentCorrupt, err)
	}
	if object.ContentKeyEnvelope == nil {
		return nil, ErrProtectedContentCorrupt
	}
	plaintext, err := encryption.DecryptContent(vaultKey, object.ObjectID, stored.Bytes, *object.ContentKeyEnvelope)
	if err != nil {
		return nil, errors.Join(ErrProtectedContentCorrupt, err)
	}
	if int64(len(plaintext)) > maximumPlaintext {
		clear(plaintext)
		return nil, ErrFileTooLarge
	}
	return plaintext, nil
}

// PreparedAssetOperation stages an asset upload while holding the repository
// mutation lock. Commit publishes either an ordinary single-file write or a
// journaled ciphertext/catalog update; Abort leaves no live asset behind.
type PreparedAssetOperation struct {
	Asset   Asset
	commit  func() error
	abort   func()
	release func()
	done    bool
}

func (operation *PreparedAssetOperation) Commit(publish func(Asset) error) error {
	if operation == nil || operation.done || operation.commit == nil {
		return errors.New("invalid prepared asset operation")
	}
	operation.done = true
	defer operation.release()
	if err := operation.commit(); err != nil {
		return err
	}
	if publish != nil {
		return publish(operation.Asset)
	}
	return nil
}

func (operation *PreparedAssetOperation) Abort() {
	if operation == nil || operation.done {
		return
	}
	operation.done = true
	if operation.abort != nil {
		operation.abort()
	}
	operation.release()
}

// PrepareAssetUploadWithCatalog creates a stable attachment object and applies
// note, path-rule, and shared-reference protection before its first durable
// byte. Protected uploads enter the same ciphertext transaction as their
// authenticated catalog entry.
func (r *Repository) PrepareAssetUploadWithCatalog(noteRelative string, content []byte, catalog encryption.Catalog, vaultKey []byte, catalogStore *encryption.CatalogStore, journal *encryption.TransactionJournal) (*PreparedAssetOperation, bool, error) {
	if err := r.validateRelative(noteRelative, true); err != nil {
		return nil, false, err
	}
	if int64(len(content)) > maxAssetSize {
		return nil, false, ErrAssetTooLarge
	}
	contentType := http.DetectContentType(content)
	if _, supported := imageExtensions[contentType]; !supported {
		return nil, false, ErrUnsupportedMedia
	}
	r.content.mu.Lock()
	unlock := true
	defer func() {
		if unlock {
			r.content.mu.Unlock()
		}
	}()
	if err := ensureCurrentCatalog(catalogStore, catalog, vaultKey); err != nil {
		return nil, false, err
	}
	_, protectedNote, err := r.readMarkdownWithCatalogLocked(noteRelative, catalog, vaultKey)
	if err != nil {
		return nil, protectedNote, err
	}
	assetsDirectory := noteAssetsPath(noteRelative)
	exists, err := validateOwnedAssets(r.content, assetsDirectory)
	if err != nil {
		return nil, protectedNote, err
	}
	createdDirectory := false
	rollbackDirectory := func() {
		if !createdDirectory {
			return
		}
		entries, readErr := r.content.readDir(assetsDirectory)
		if readErr == nil && len(entries) == 0 {
			_ = r.content.removeAll(assetsDirectory)
			_ = r.content.syncDir(path.Dir(assetsDirectory))
		}
	}
	if !exists {
		if err := r.content.mkdir(assetsDirectory, 0o755); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return nil, protectedNote, err
			}
		} else {
			createdDirectory = true
		}
		if _, err := validateOwnedAssets(r.content, assetsDirectory); err != nil {
			rollbackDirectory()
			return nil, protectedNote, err
		}
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		rollbackDirectory()
		return nil, protectedNote, err
	}
	filename := hex.EncodeToString(identifier) + imageExtensions[contentType]
	logicalPath := path.Join(assetsDirectory, filename)
	relativePath := path.Join(noteBase(noteRelative)+".assets", filename)
	if _, err := r.content.lstat(logicalPath); err == nil {
		rollbackDirectory()
		return nil, protectedNote, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		rollbackDirectory()
		return nil, protectedNote, err
	}
	if _, err := r.content.lstat(logicalPath + ".age"); err == nil {
		rollbackDirectory()
		return nil, true, ErrProtectedObjectMissing
	} else if !errors.Is(err, os.ErrNotExist) {
		rollbackDirectory()
		return nil, protectedNote, err
	}
	protectedByRule, err := catalog.IsPathProtected(logicalPath)
	if err != nil {
		rollbackDirectory()
		return nil, protectedNote, err
	}
	protected := protectedNote || protectedByRule
	asset := Asset{RelativePath: relativePath, ContentType: contentType}
	if !protected {
		unlock = false
		return &PreparedAssetOperation{
			Asset: asset,
			commit: func() error {
				if err := r.content.writeNew(logicalPath, content, 0o644); err != nil {
					rollbackDirectory()
					return err
				}
				return r.content.syncDir(assetsDirectory)
			},
			abort:   rollbackDirectory,
			release: r.content.mu.Unlock,
		}, false, nil
	}
	objectID, err := encryption.NewCatalogObjectID()
	if err != nil {
		rollbackDirectory()
		return nil, true, err
	}
	ciphertext, envelope, err := encryption.EncryptContent(vaultKey, objectID, content)
	if err != nil {
		rollbackDirectory()
		return nil, true, err
	}
	nextCatalog := cloneCatalog(catalog)
	nextCatalog.Objects = append(nextCatalog.Objects, encryption.CatalogObject{
		ObjectID: objectID, Kind: encryption.CatalogObjectAsset, LogicalPath: logicalPath,
		PhysicalPath: logicalPath + ".age", CiphertextHash: encryption.CiphertextSHA256(ciphertext),
		ContentKeyEnvelope: cloneContentKeyEnvelope(&envelope), Protected: true,
	})
	transaction, err := journal.Prepare([]encryption.CiphertextChange{{
		Path: logicalPath + ".age", ObjectID: objectID, Ciphertext: ciphertext, ContentKeyEnvelope: envelope,
	}}, nextCatalog, vaultKey)
	if err != nil {
		rollbackDirectory()
		return nil, true, err
	}
	unlock = false
	return &PreparedAssetOperation{
		Asset: asset, commit: transaction.Commit,
		abort: func() { _ = transaction.Abort() }, release: r.content.mu.Unlock,
	}, true, nil
}

// ReadAssetWithCatalog resolves shared Markdown-relative references and
// decrypts only authenticated asset objects. A plaintext asset referenced by a
// protected note fails closed until its P11 conversion has completed.
func (r *Repository) ReadAssetWithCatalog(noteRelative, assetRelative string, catalog encryption.Catalog, vaultKey []byte) (Asset, bool, error) {
	if err := r.validateRelative(noteRelative, true); err != nil {
		return Asset{}, false, err
	}
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	_, protectedNote, err := r.readMarkdownWithCatalogLocked(noteRelative, catalog, vaultKey)
	if err != nil {
		return Asset{}, protectedNote, err
	}
	logicalPath, err := r.resolveCatalogAssetPath(noteRelative, assetRelative)
	if err != nil {
		return Asset{}, protectedNote, err
	}
	object, found := catalogNote(catalog, logicalPath)
	if found {
		if object.Kind != encryption.CatalogObjectAsset {
			return Asset{}, false, ErrProtectedObjectMissing
		}
		if object.Protected {
			plaintext, err := r.readProtectedBytesLimit(object, vaultKey, maxAssetSize, maxAssetSize+(1<<20))
			if err != nil {
				return Asset{}, true, err
			}
			defer clear(plaintext)
			contentType, err := supportedImageType(plaintext)
			if err != nil {
				return Asset{}, true, err
			}
			return Asset{RelativePath: assetRelative, ContentType: contentType, Content: append([]byte(nil), plaintext...)}, true, nil
		}
		if _, err := r.content.lstat(logicalPath + ".age"); err == nil {
			return Asset{}, false, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return Asset{}, false, err
		}
	}
	protectedByRule, err := catalog.IsPathProtected(logicalPath)
	if err != nil {
		return Asset{}, false, err
	}
	if protectedByRule || protectedNote {
		return Asset{}, true, ErrProtectedObjectMissing
	}
	protectedReference, err := r.protectedNoteReferencesAssetLocked(logicalPath, catalog, vaultKey)
	if err != nil {
		return Asset{}, false, err
	}
	if protectedReference {
		return Asset{}, true, ErrProtectedObjectMissing
	}
	if !found {
		if _, err := r.content.lstat(logicalPath + ".age"); err == nil {
			return Asset{}, true, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return Asset{}, false, err
		}
	}
	stored, err := r.content.read(logicalPath, maxAssetSize)
	if err != nil {
		return Asset{}, false, err
	}
	contentType, err := supportedImageType(stored.Bytes)
	if err != nil {
		return Asset{}, false, err
	}
	return Asset{RelativePath: assetRelative, ContentType: contentType, Content: stored.Bytes}, false, nil
}

func (r *Repository) resolveCatalogAssetPath(noteRelative, assetRelative string) (string, error) {
	if hasControlCharacter(noteRelative) || path.IsAbs(noteRelative) || path.Clean(noteRelative) != noteRelative || strings.Contains(noteRelative, `\`) {
		return "", ErrInvalidPath
	}
	if err := r.validateRelative(noteRelative, true); err != nil {
		return "", err
	}
	if assetRelative == "" || hasControlCharacter(assetRelative) || strings.Contains(assetRelative, `\`) {
		return "", ErrInvalidPath
	}
	parsed, err := url.Parse(assetRelative)
	if err != nil || parsed.IsAbs() || parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidPath
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || decoded == "" || path.IsAbs(decoded) || strings.Contains(decoded, `\`) || hasControlCharacter(decoded) {
		return "", ErrInvalidPath
	}
	resolved := path.Clean(path.Join(path.Dir(noteRelative), decoded))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") || !validAssetLogicalPath(resolved) {
		return "", ErrInvalidPath
	}
	return resolved, nil
}

func (r *Repository) protectedNoteReferencesAssetLocked(assetPath string, catalog encryption.Catalog, vaultKey []byte) (bool, error) {
	notePaths := make(map[string]struct{})
	for _, object := range catalog.Objects {
		if object.Kind == encryption.CatalogObjectNote {
			notePaths[object.LogicalPath] = struct{}{}
		}
	}
	entries, err := r.content.walkEntries(func(entry contentEntry) bool {
		return entry.Info.IsDir() && (ignoredCleanupPath(entry.Path) || strings.HasSuffix(strings.ToLower(entry.Name), ".assets"))
	})
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.Mode().IsRegular() || !strings.EqualFold(path.Ext(entry.Name), ".md") || ignoredCleanupPath(entry.Path) {
			continue
		}
		notePaths[entry.Path] = struct{}{}
	}
	for notePath := range notePaths {
		markdown, protected, err := r.readMarkdownWithCatalogLocked(notePath, catalog, vaultKey)
		if err != nil {
			return false, err
		}
		if !protected {
			continue
		}
		for _, destination := range markdownDestinations(markdown.Content) {
			resolved, _, ok := resolveAssetReference(notePath, destination)
			if ok && resolved == assetPath {
				return true, nil
			}
		}
	}
	return false, nil
}

func supportedImageType(content []byte) (string, error) {
	contentType := http.DetectContentType(content)
	if _, supported := imageExtensions[contentType]; !supported {
		return "", ErrUnsupportedMedia
	}
	return contentType, nil
}

func catalogNote(catalog encryption.Catalog, logicalPath string) (encryption.CatalogObject, bool) {
	for _, object := range catalog.Objects {
		if object.LogicalPath == logicalPath {
			return object, true
		}
	}
	return encryption.CatalogObject{}, false
}

func cloneCatalog(catalog encryption.Catalog) encryption.Catalog {
	copy := catalog
	copy.Objects = append([]encryption.CatalogObject(nil), catalog.Objects...)
	copy.FolderRules = append([]encryption.FolderProtectionRule(nil), catalog.FolderRules...)
	for index := range copy.Objects {
		copy.Objects[index].ContentKeyEnvelope = cloneContentKeyEnvelope(copy.Objects[index].ContentKeyEnvelope)
	}
	return copy
}

func cloneContentKeyEnvelope(envelope *encryption.ContentKeyEnvelope) *encryption.ContentKeyEnvelope {
	if envelope == nil {
		return nil
	}
	copy := *envelope
	copy.Nonce = append([]byte(nil), envelope.Nonce...)
	copy.Ciphertext = append([]byte(nil), envelope.Ciphertext...)
	return &copy
}

func ensureCurrentCatalog(store *encryption.CatalogStore, catalog encryption.Catalog, vaultKey []byte) error {
	if store == nil {
		return ErrProtectedObjectMissing
	}
	current, err := store.Load(vaultKey, catalog.VaultID)
	if err != nil {
		return err
	}
	expectedBytes, err := encryption.MarshalCatalog(catalog, vaultKey)
	if err != nil {
		return err
	}
	currentBytes, err := encryption.MarshalCatalog(current, vaultKey)
	if err != nil {
		return err
	}
	if !bytes.Equal(expectedBytes, currentBytes) {
		return ErrConflict
	}
	return nil
}

func protectedContentError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errors.Join(ErrProtectedContentCorrupt, err)
	}
	return errors.Join(ErrProtectedContentCorrupt, err)
}

func insertTreeFile(tree *[]Node, relative string) error {
	parts := strings.Split(relative, "/")
	current := tree
	for index, name := range parts {
		found := -1
		for childIndex := range *current {
			if (*current)[childIndex].Name == name {
				found = childIndex
				break
			}
		}
		if index == len(parts)-1 {
			if found >= 0 {
				if (*current)[found].Type != "file" {
					return ErrInvalidPath
				}
				return nil
			}
			*current = append(*current, Node{Name: name, Path: relative, Type: "file"})
			return nil
		}
		if found < 0 {
			folderPath := strings.Join(parts[:index+1], "/")
			*current = append(*current, Node{Name: name, Path: folderPath, Type: "directory"})
			found = len(*current) - 1
		}
		if (*current)[found].Type != "directory" {
			return ErrInvalidPath
		}
		current = &(*current)[found].Children
	}
	return nil
}

func sortTree(nodes []Node) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Type != nodes[j].Type {
			return nodes[i].Type == "directory"
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	for index := range nodes {
		sortTree(nodes[index].Children)
	}
}

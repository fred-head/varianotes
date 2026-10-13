package files

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
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

func (operation *PreparedMarkdownOperation) Abort() {
	if operation == nil || operation.done {
		return
	}
	operation.done = true
	operation.release()
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
			commit:   transaction.Commit, release: r.content.mu.Unlock,
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
		commit:   transaction.Commit, release: r.content.mu.Unlock,
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
	sortTree(tree)
	return tree, nil
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
	if _, err := r.content.lstat(object.LogicalPath); err == nil {
		return nil, ErrProtectedContentCorrupt
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stored, err := r.content.read(object.PhysicalPath, maxMarkdownCiphertextSize)
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
	if len(plaintext) > maxMarkdownSize {
		clear(plaintext)
		return nil, ErrFileTooLarge
	}
	return plaintext, nil
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

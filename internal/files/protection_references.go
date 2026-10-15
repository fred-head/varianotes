package files

import (
	"errors"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/fred-head/varianotes/internal/encryption"
)

// protectionAttachmentReference recognizes local destinations independently
// of the upload convention. Unsupported local attachments must block protection
// rather than disappear from the inventory. Markdown links select other notes,
// not attachments; remote URLs and fragment-only links have no local payload.
func protectionAttachmentReference(note, destination string) (string, bool, error) {
	u, err := url.Parse(strings.TrimSpace(destination))
	if err != nil {
		return "", false, ErrConversionUnsafe
	}
	if u.Scheme != "" || u.Host != "" {
		if strings.EqualFold(u.Scheme, "file") {
			return "", false, ErrConversionUnsafe
		}
		return "", false, nil
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", false, ErrConversionUnsafe
	}
	if decoded == "" {
		return "", false, nil
	}
	if path.IsAbs(decoded) || strings.Contains(decoded, `\`) || hasControlCharacter(decoded) {
		return "", false, ErrConversionUnsafe
	}
	resolved := path.Clean(path.Join(path.Dir(note), decoded))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || pathHasReservedDirectory(resolved, false) {
		return "", false, ErrConversionUnsafe
	}
	if strings.EqualFold(path.Ext(resolved), ".md") {
		return "", false, nil
	}
	return resolved, true, nil
}

// A save must not claim protection for a reference whose bytes remain plain.
// Existing ordinary attachments need conversion first. This preflight runs
// before staging and preserves both the current note and the browser draft.
func (r *Repository) validateProtectedReferencesLocked(note, content string, catalog encryption.Catalog, key []byte) error {
	for _, destination := range markdownDestinations(content) {
		relative, attachment, err := protectionAttachmentReference(note, destination)
		if err != nil {
			return err
		}
		if !attachment {
			continue
		}
		if object, found := catalogNote(catalog, relative); found {
			if object.Kind != encryption.CatalogObjectAsset || !object.Protected {
				return ErrProtectedObjectMissing
			}
			plaintext, err := r.readProtectedBytesLimit(object, key, maxAssetSize, maxAssetSize+(1<<20))
			clear(plaintext)
			if err != nil {
				return err
			}
			continue
		}
		if _, err := r.content.lstat(relative); err == nil {
			return ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := r.content.lstat(relative + ".age"); err == nil {
			return ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

package files

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"
)

var (
	ErrAlreadyExists = errors.New("repository entry already exists")
	ErrInvalidType   = errors.New("invalid repository entry type")
)

func (r *Repository) Create(relative, entryType string) error {
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	return r.create(relative, entryType)
}

func (r *Repository) create(relative, entryType string) error {
	markdown := entryType == "file"
	if !markdown && entryType != "directory" {
		return ErrInvalidType
	}
	target, err := r.resolveNew(relative, markdown)
	if err != nil {
		return err
	}

	if entryType == "directory" {
		// #nosec G301 -- notebook directories intentionally use ordinary Git-portable permissions.
		if err := r.content.mkdir(target, 0o755); err != nil {
			if errors.Is(err, os.ErrExist) {
				return ErrAlreadyExists
			}
			return err
		}
		return r.content.syncDir(pathDir(target))
	}

	title := strings.TrimSuffix(pathBase(target), pathExt(target))
	if err := r.content.writeNew(target, []byte(fmt.Sprintf("# %s\n", title)), 0o644); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrAlreadyExists
		}
		return err
	}
	return r.content.syncDir(pathDir(target))
}

func (r *Repository) Delete(relative string) error {
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	return r.delete(relative)
}

func (r *Repository) delete(relative string) error {
	resolved, info, err := r.resolveEntry(relative)
	if err != nil {
		return err
	}
	if !info.IsDir() && !strings.EqualFold(path.Ext(resolved), ".md") {
		return ErrNotMarkdown
	}

	if info.IsDir() {
		if err := r.content.removeAll(resolved); err != nil {
			return err
		}
		return r.content.syncDir(pathDir(resolved))
	}

	assets := noteAssetsPath(resolved)
	assetsExist, err := validateOwnedAssets(r.content, assets)
	if err != nil {
		return err
	}
	if err := r.content.remove(resolved); err != nil {
		return err
	}
	if assetsExist {
		if err := r.content.removeAll(assets); err != nil {
			return err
		}
	}
	return r.content.syncDir(pathDir(resolved))
}

func (r *Repository) Move(sourceRelative, targetRelative string) error {
	r.moveMu.Lock()
	defer r.moveMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	return r.move(sourceRelative, targetRelative)
}

func (r *Repository) move(sourceRelative, targetRelative string) error {
	source, info, err := r.resolveEntry(sourceRelative)
	if err != nil {
		return err
	}
	markdown := !info.IsDir()
	if markdown && !strings.EqualFold(path.Ext(source), ".md") {
		return ErrNotMarkdown
	}
	target, err := r.resolveNew(targetRelative, markdown)
	if err != nil {
		return err
	}
	if info.IsDir() && (target == source || strings.HasPrefix(target, source+"/")) {
		return ErrInvalidPath
	}

	var sourceAssets, targetAssets string
	assetsExist := false
	if markdown {
		sourceAssets = noteAssetsPath(source)
		targetAssets = noteAssetsPath(target)
		assetsExist, err = validateOwnedAssets(r.content, sourceAssets)
		if err != nil {
			return err
		}
		if assetsExist {
			if _, err := r.content.lstat(targetAssets); err == nil {
				return ErrAlreadyExists
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}

	if err := r.content.rename(source, target); err != nil {
		return err
	}
	if assetsExist {
		if err := r.content.rename(sourceAssets, targetAssets); err != nil {
			_ = r.content.rename(target, source)
			return err
		}
		if oldBase, newBase := noteBase(source), noteBase(target); oldBase != newBase {
			if err := r.rewriteAssetLinks(target, oldBase, newBase); err != nil {
				return err
			}
		}
	}
	_ = r.content.syncDir(pathDir(source))
	return r.content.syncDir(pathDir(target))
}

func (r *Repository) resolveEntry(relative string) (string, os.FileInfo, error) {
	if err := r.validateRelative(relative, false); err != nil {
		return "", nil, err
	}
	info, err := r.content.lstat(relative)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return "", nil, ErrInvalidPath
	}
	return relative, info, nil
}

func (r *Repository) resolveNew(relative string, markdown bool) (string, error) {
	if err := r.validateRelative(relative, markdown); err != nil {
		return "", err
	}
	if _, err := r.content.lstat(relative); err == nil {
		return "", ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return relative, nil
}

func (r *Repository) validateRelative(relative string, markdown bool) error {
	if !r.Configured() {
		return ErrNotConfigured
	}
	return validatePortableEntryPath(relative, markdown)
}

func noteBase(markdownPath string) string {
	return strings.TrimSuffix(path.Base(markdownPath), path.Ext(markdownPath))
}

func noteAssetsPath(markdownPath string) string {
	return path.Join(path.Dir(markdownPath), noteBase(markdownPath)+".assets")
}

func validateOwnedAssets(content *contentStore, assets string) (bool, error) {
	info, err := content.lstat(assets)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, ErrInvalidPath
	}
	return true, nil
}

func (r *Repository) rewriteAssetLinks(markdownPath, oldBase, newBase string) error {
	content, err := r.content.read(markdownPath, maxMarkdownSize)
	if err != nil {
		return err
	}
	updated := rewriteAssetLinksContent(string(content.Bytes), oldBase, newBase)
	if updated == string(content.Bytes) {
		return nil
	}
	_, err = r.content.write(markdownPath, []byte(updated), content.Version, maxMarkdownSize)
	return err
}

func rewriteAssetLinksContent(content, oldBase, newBase string) string {
	oldEscaped := url.PathEscape(oldBase)
	newEscaped := url.PathEscape(newBase)
	return strings.NewReplacer(
		"(<"+oldBase+".assets/", "(<"+newBase+".assets/",
		"("+oldBase+".assets/", "("+newBase+".assets/",
		"(<"+oldEscaped+".assets/", "(<"+newEscaped+".assets/",
		"("+oldEscaped+".assets/", "("+newEscaped+".assets/",
	).Replace(content)
}

func pathDir(relative string) string {
	return path.Dir(relative)
}
func pathBase(relative string) string { return path.Base(relative) }
func pathExt(relative string) string  { return path.Ext(relative) }

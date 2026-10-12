package files

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrAlreadyExists = errors.New("repository entry already exists")
	ErrInvalidType   = errors.New("invalid repository entry type")
)

func (r *Repository) Create(relative, entryType string) error {
	r.contentMu.Lock()
	defer r.contentMu.Unlock()
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
		if err := os.Mkdir(target, 0o755); err != nil {
			if errors.Is(err, os.ErrExist) {
				return ErrAlreadyExists
			}
			return err
		}
		return syncDirectory(filepath.Dir(target))
	}

	title := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	// #nosec G302,G304 -- resolveNew confines the new path; Markdown remains an ordinary portable repository file.
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrAlreadyExists
		}
		return err
	}
	if _, err := fmt.Fprintf(file, "# %s\n", title); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func (r *Repository) Delete(relative string) error {
	r.contentMu.Lock()
	defer r.contentMu.Unlock()
	return r.delete(relative)
}

func (r *Repository) delete(relative string) error {
	resolved, info, err := r.resolveEntry(relative)
	if err != nil {
		return err
	}
	if !info.IsDir() && !strings.EqualFold(filepath.Ext(resolved), ".md") {
		return ErrNotMarkdown
	}

	if info.IsDir() {
		if err := os.RemoveAll(resolved); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(resolved))
	}

	assets := noteAssetsPath(resolved)
	assetsExist, err := validateOwnedAssets(r.root, assets)
	if err != nil {
		return err
	}
	if err := os.Remove(resolved); err != nil {
		return err
	}
	if assetsExist {
		if err := os.RemoveAll(assets); err != nil {
			return err
		}
	}
	return syncDirectory(filepath.Dir(resolved))
}

func (r *Repository) Move(sourceRelative, targetRelative string) error {
	r.moveMu.Lock()
	defer r.moveMu.Unlock()
	r.contentMu.Lock()
	defer r.contentMu.Unlock()
	return r.move(sourceRelative, targetRelative)
}

func (r *Repository) move(sourceRelative, targetRelative string) error {
	source, info, err := r.resolveEntry(sourceRelative)
	if err != nil {
		return err
	}
	markdown := !info.IsDir()
	if markdown && !strings.EqualFold(filepath.Ext(source), ".md") {
		return ErrNotMarkdown
	}
	target, err := r.resolveNew(targetRelative, markdown)
	if err != nil {
		return err
	}
	if info.IsDir() && (target == source || strings.HasPrefix(target, source+string(filepath.Separator))) {
		return ErrInvalidPath
	}

	var sourceAssets, targetAssets string
	assetsExist := false
	if markdown {
		sourceAssets = noteAssetsPath(source)
		targetAssets = noteAssetsPath(target)
		assetsExist, err = validateOwnedAssets(r.root, sourceAssets)
		if err != nil {
			return err
		}
		if assetsExist {
			if _, err := os.Lstat(targetAssets); err == nil {
				return ErrAlreadyExists
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}

	if err := os.Rename(source, target); err != nil {
		return err
	}
	if assetsExist {
		if err := os.Rename(sourceAssets, targetAssets); err != nil {
			_ = os.Rename(target, source)
			return err
		}
		if oldBase, newBase := noteBase(source), noteBase(target); oldBase != newBase {
			if err := rewriteAssetLinks(target, oldBase, newBase); err != nil {
				return err
			}
		}
	}
	_ = syncDirectory(filepath.Dir(source))
	return syncDirectory(filepath.Dir(target))
}

func (r *Repository) resolveEntry(relative string) (string, os.FileInfo, error) {
	if err := r.validateRelative(relative, false); err != nil {
		return "", nil, err
	}
	current := r.root
	parts := strings.Split(relative, "/")
	var info os.FileInfo
	for index, part := range parts {
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", nil, err
		}
		var matched fs.DirEntry
		for _, entry := range entries {
			if entry.Name() == part {
				matched = entry
				break
			}
		}
		if matched == nil {
			return "", nil, os.ErrNotExist
		}
		if matched.Type()&fs.ModeSymlink != 0 || index < len(parts)-1 && !matched.IsDir() {
			return "", nil, ErrInvalidPath
		}
		current = filepath.Join(current, matched.Name())
		info, err = matched.Info()
		if err != nil {
			return "", nil, err
		}
	}
	if info == nil || !isWithinRoot(r.root, current) || current == r.root {
		return "", nil, ErrInvalidPath
	}
	return current, info, nil
}

func (r *Repository) resolveNew(relative string, markdown bool) (string, error) {
	if err := r.validateRelative(relative, markdown); err != nil {
		return "", err
	}
	target := filepath.Join(r.root, filepath.FromSlash(relative))
	parent := filepath.Dir(target)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if resolvedParent != filepath.Clean(parent) || !isWithinRoot(r.root, resolvedParent) {
		return "", ErrInvalidPath
	}
	info, err := os.Stat(resolvedParent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrInvalidPath
	}
	if _, err := os.Lstat(target); err == nil {
		return "", ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return target, nil
}

func (r *Repository) validateRelative(relative string, markdown bool) error {
	if !r.Configured() {
		return ErrNotConfigured
	}
	return validatePortableEntryPath(relative, markdown)
}

func noteBase(markdownPath string) string {
	return strings.TrimSuffix(filepath.Base(markdownPath), filepath.Ext(markdownPath))
}

func noteAssetsPath(markdownPath string) string {
	return filepath.Join(filepath.Dir(markdownPath), noteBase(markdownPath)+".assets")
}

func validateOwnedAssets(root, assets string) (bool, error) {
	info, err := os.Lstat(assets)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !isWithinRoot(root, assets) {
		return false, ErrInvalidPath
	}
	return true, nil
}

func rewriteAssetLinks(markdownPath, oldBase, newBase string) error {
	// #nosec G304 -- markdownPath originates from a previously root-confined resolved note path.
	content, err := os.ReadFile(markdownPath)
	if err != nil {
		return err
	}
	updated := rewriteAssetLinksContent(string(content), oldBase, newBase)
	if updated == string(content) {
		return nil
	}
	info, err := os.Stat(markdownPath)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(markdownPath), "."+filepath.Base(markdownPath)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	cleanup := func() { _ = temporary.Close(); _ = os.Remove(temporaryName) }
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.WriteString(updated); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(temporaryName, markdownPath); err != nil {
		cleanup()
		return err
	}
	return nil
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

func syncDirectory(directory string) error {
	// #nosec G304 -- callers pass only repository directories already resolved and confined by their operation.
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

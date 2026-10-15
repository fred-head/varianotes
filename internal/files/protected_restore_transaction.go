package files

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
)

func (r *Repository) planRestoreDirectories(item resolvedTrashItem, sourceRoot string, folder bool) ([]string, []string, error) {
	required := make(map[string]bool)
	addParents := func(relative string) {
		for relative != "." {
			required[relative] = true
			relative = path.Dir(relative)
		}
	}
	addParents(path.Dir(item.OriginalPath))
	roots := make(map[string]string)
	if folder {
		roots[sourceRoot] = item.OriginalPath
	}
	if item.assetsExist {
		roots[item.assetsPath] = noteAssetsPath(item.OriginalPath)
	}
	for source, destination := range roots {
		addParents(destination)
		entries, err := r.content.entriesUnder(source)
		if err != nil {
			return nil, nil, err
		}
		for _, entry := range entries {
			if entry.Info.Mode()&fs.ModeSymlink != 0 {
				return nil, nil, ErrInvalidPath
			}
			if entry.Info.IsDir() {
				addParents(destination + strings.TrimPrefix(entry.Path, source))
			}
		}
	}
	create := make([]string, 0)
	for relative := range required {
		info, err := r.content.lstat(relative)
		if errors.Is(err, fs.ErrNotExist) {
			create = append(create, relative)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return nil, nil, ErrInvalidPath
		}
	}
	remove := []string{item.root}
	entries, err := r.content.entriesUnder(item.root)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if entry.Info.Mode()&fs.ModeSymlink != 0 {
			return nil, nil, ErrInvalidPath
		}
		if entry.Info.IsDir() {
			remove = append(remove, entry.Path)
		}
	}
	sort.Strings(create)
	sort.Strings(remove)
	return create, remove, nil
}

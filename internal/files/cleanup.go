package files

import (
	"bufio"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type UnreferencedAsset struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type AssetCleanupFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type AssetCleanupResult struct {
	Deleted  []string              `json:"deleted"`
	Failures []AssetCleanupFailure `json:"failures"`
}

var (
	inlineDestinationPattern    = regexp.MustCompile(`!?\[[^\]\n]*\]\(\s*(?:<([^>\n]+)>|([^\s)\n]+))`)
	referenceDestinationPattern = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:\s*(?:<([^>\n]+)>|([^\s\n]+))`)
)

func (r *Repository) UnreferencedAssets() ([]UnreferencedAsset, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}
	candidates, err := r.cleanupCandidates()
	if err != nil {
		return nil, err
	}
	referenced, conservativeNames, err := r.assetReferences()
	if err != nil {
		return nil, err
	}

	result := make([]UnreferencedAsset, 0, len(candidates))
	for _, candidate := range candidates {
		if referenced[candidate.Path] || conservativeNames[path.Base(candidate.Path)] {
			continue
		}
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func (r *Repository) DeleteUnreferencedAssets(paths []string) (AssetCleanupResult, error) {
	result := AssetCleanupResult{Deleted: []string{}, Failures: []AssetCleanupFailure{}}
	seen := make(map[string]bool, len(paths))
	for _, relative := range paths {
		if !validCleanupPath(relative) {
			return result, ErrInvalidPath
		}
		if seen[relative] {
			continue
		}
		seen[relative] = true

		current, err := r.UnreferencedAssets()
		if err != nil {
			return result, err
		}
		eligible := false
		for _, candidate := range current {
			if candidate.Path == relative {
				eligible = true
				break
			}
		}
		if !eligible {
			result.Failures = append(result.Failures, AssetCleanupFailure{Path: relative, Error: "asset is referenced or no longer eligible"})
			continue
		}

		target := filepath.Join(r.root, filepath.FromSlash(relative))
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !isWithinRoot(r.root, target) {
			result.Failures = append(result.Failures, AssetCleanupFailure{Path: relative, Error: "asset is no longer a safe regular file"})
			continue
		}
		if err := os.Remove(target); err != nil {
			result.Failures = append(result.Failures, AssetCleanupFailure{Path: relative, Error: err.Error()})
			continue
		}
		result.Deleted = append(result.Deleted, relative)
		directory := filepath.Dir(target)
		entries, readErr := os.ReadDir(directory)
		if readErr == nil && len(entries) == 0 {
			_ = os.Remove(directory)
			_ = syncDirectory(filepath.Dir(directory))
		} else {
			_ = syncDirectory(directory)
		}
	}
	return result, nil
}

func (r *Repository) cleanupCandidates() ([]UnreferencedAsset, error) {
	result := []UnreferencedAsset{}
	err := filepath.WalkDir(r.root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if current != r.root && (name == ".git" || name == ".trash" || name == "node_modules") {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(name, ".assets") {
			return nil
		}

		note := strings.TrimSuffix(current, filepath.Ext(current)) + ".md"
		noteInfo, err := os.Lstat(note)
		if err != nil || !noteInfo.Mode().IsRegular() || noteInfo.Mode()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		children, err := os.ReadDir(current)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.IsDir() || child.Type()&os.ModeSymlink != 0 || !supportedCleanupExtension(child.Name()) {
				continue
			}
			info, err := child.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			relative, err := filepath.Rel(r.root, filepath.Join(current, child.Name()))
			if err != nil || !validCleanupPath(filepath.ToSlash(relative)) {
				continue
			}
			result = append(result, UnreferencedAsset{Path: filepath.ToSlash(relative), Size: info.Size()})
		}
		return filepath.SkipDir
	})
	return result, err
}

func (r *Repository) assetReferences() (map[string]bool, map[string]bool, error) {
	referenced := map[string]bool{}
	conservativeNames := map[string]bool{}
	confinedRoot, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, nil, err
	}
	defer confinedRoot.Close()
	err = filepath.WalkDir(r.root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			name := strings.ToLower(entry.Name())
			if current != r.root && (name == ".git" || name == ".trash" || name == "node_modules" || strings.HasSuffix(name, ".assets")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		noteRelative, err := filepath.Rel(r.root, current)
		if err != nil {
			return err
		}
		expected, err := entry.Info()
		if err != nil {
			return err
		}
		file, _, err := openConfinedRegularFile(confinedRoot, noteRelative, expected, maxMarkdownSize)
		if err != nil {
			return err
		}
		content, err := io.ReadAll(io.LimitReader(file, maxMarkdownSize+1))
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if len(content) > maxMarkdownSize {
			return ErrFileTooLarge
		}
		if closeErr != nil {
			return closeErr
		}
		for _, destination := range markdownDestinations(string(content)) {
			resolved, name, ok := resolveAssetReference(filepath.ToSlash(noteRelative), destination)
			if !ok {
				continue
			}
			referenced[resolved] = true
			conservativeNames[name] = true
		}
		return nil
	})
	return referenced, conservativeNames, err
}

func markdownDestinations(content string) []string {
	result := []string{}
	for _, pattern := range []*regexp.Regexp{inlineDestinationPattern, referenceDestinationPattern} {
		for _, match := range pattern.FindAllStringSubmatch(content, -1) {
			if match[1] != "" {
				result = append(result, match[1])
			} else if match[2] != "" {
				result = append(result, match[2])
			}
		}
	}
	// A filename mentioned in unusual Markdown is treated conservatively later.
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		for _, field := range strings.Fields(scanner.Text()) {
			field = strings.Trim(field, `<>[](){}'"`)
			if supportedCleanupExtension(field) {
				result = append(result, field)
			}
		}
	}
	return result
}

func resolveAssetReference(noteRelative, destination string) (string, string, bool) {
	decoded, err := url.PathUnescape(strings.TrimSpace(destination))
	if err != nil {
		return "", "", false
	}
	if index := strings.IndexAny(decoded, "?#"); index >= 0 {
		decoded = decoded[:index]
	}
	decoded = strings.ReplaceAll(decoded, `\`, "/")
	if decoded == "" || path.IsAbs(decoded) || strings.Contains(decoded, "://") {
		return "", "", false
	}
	resolved := path.Clean(path.Join(path.Dir(noteRelative), decoded))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || !strings.Contains(path.Dir(resolved), ".assets") || !supportedCleanupExtension(resolved) {
		return "", "", false
	}
	return resolved, path.Base(resolved), true
}

func validCleanupPath(relative string) bool {
	if relative == "" || hasControlCharacter(relative) || relative == ".." || strings.HasPrefix(relative, "../") || path.IsAbs(relative) || path.Clean(relative) != relative || strings.Contains(relative, `\`) {
		return false
	}
	parts := strings.Split(relative, "/")
	return len(parts) >= 2 && strings.HasSuffix(strings.ToLower(parts[len(parts)-2]), ".assets") && supportedCleanupExtension(parts[len(parts)-1])
}

func supportedCleanupExtension(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

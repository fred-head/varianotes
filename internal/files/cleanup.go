package files

import (
	"bufio"
	"net/url"
	"os"
	"path"
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
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	return r.unreferencedAssets()
}

func (r *Repository) unreferencedAssets() ([]UnreferencedAsset, error) {
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
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	seen := make(map[string]bool, len(paths))
	for _, relative := range paths {
		if !validCleanupPath(relative) {
			return result, ErrInvalidPath
		}
		if seen[relative] {
			continue
		}
		seen[relative] = true

		current, err := r.unreferencedAssets()
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

		info, err := r.content.lstat(relative)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			result.Failures = append(result.Failures, AssetCleanupFailure{Path: relative, Error: "asset is no longer a safe regular file"})
			continue
		}
		if err := r.content.remove(relative); err != nil {
			result.Failures = append(result.Failures, AssetCleanupFailure{Path: relative, Error: err.Error()})
			continue
		}
		result.Deleted = append(result.Deleted, relative)
		directory := path.Dir(relative)
		entries, readErr := r.content.readDir(directory)
		if readErr == nil && len(entries) == 0 {
			_ = r.content.removeAll(directory)
			_ = r.content.syncDir(path.Dir(directory))
		} else {
			_ = r.content.syncDir(directory)
		}
	}
	return result, nil
}

func (r *Repository) cleanupCandidates() ([]UnreferencedAsset, error) {
	result := []UnreferencedAsset{}
	entries, err := r.content.walkEntries(func(entry contentEntry) bool {
		if !entry.Info.IsDir() {
			return false
		}
		return ignoredCleanupPath(entry.Path) || strings.HasSuffix(strings.ToLower(entry.Name), ".assets")
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Info.Mode()&os.ModeSymlink != 0 || !entry.Info.IsDir() || ignoredCleanupPath(entry.Path) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name), ".assets") {
			continue
		}
		note := strings.TrimSuffix(entry.Path, path.Ext(entry.Path)) + ".md"
		noteInfo, err := r.content.lstat(note)
		if err != nil || !noteInfo.Mode().IsRegular() || noteInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		children, err := r.content.readDir(entry.Path)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if child.Info.IsDir() || child.Info.Mode()&os.ModeSymlink != 0 || !supportedCleanupExtension(child.Name) {
				continue
			}
			if !child.Info.Mode().IsRegular() || !validCleanupPath(child.Path) {
				continue
			}
			result = append(result, UnreferencedAsset{Path: child.Path, Size: child.Info.Size()})
		}
	}
	return result, nil
}

func (r *Repository) assetReferences() (map[string]bool, map[string]bool, error) {
	referenced := map[string]bool{}
	conservativeNames := map[string]bool{}
	entries, err := r.content.walkEntries(func(entry contentEntry) bool {
		return entry.Info.IsDir() && (ignoredCleanupPath(entry.Path) || strings.HasSuffix(strings.ToLower(entry.Name), ".assets"))
	})
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if entry.Info.Mode()&os.ModeSymlink != 0 || entry.Info.IsDir() || !entry.Info.Mode().IsRegular() || ignoredCleanupPath(entry.Path) || strings.HasSuffix(strings.ToLower(entry.Name), ".assets") || !strings.EqualFold(path.Ext(entry.Name), ".md") {
			continue
		}
		content, err := r.content.read(entry.Path, maxMarkdownSize)
		if err != nil {
			return nil, nil, err
		}
		for _, destination := range markdownDestinations(string(content.Bytes)) {
			resolved, name, ok := resolveAssetReference(entry.Path, destination)
			if !ok {
				continue
			}
			referenced[resolved] = true
			conservativeNames[name] = true
		}
	}
	return referenced, conservativeNames, err
}

func ignoredCleanupPath(relative string) bool {
	for _, part := range strings.Split(relative, "/") {
		switch strings.ToLower(part) {
		case ".git", ".trash", "node_modules":
			return true
		}
	}
	return false
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

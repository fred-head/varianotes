package files

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fred-head/varianotes/internal/encryption"
)

const (
	maxSearchResults    = 100
	maxSearchQueryBytes = 1024
)

// ErrSearchQueryTooLong reports a query that exceeds the bounded search input.
var ErrSearchQueryTooLong = errors.New("search query is too long")

type SearchResult struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Line    int    `json:"line,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
}

func (r *Repository) Search(query string) ([]SearchResult, error) {
	return r.search(query, nil, nil)
}

// SearchWithCatalog searches logical note paths and protected Markdown using
// the authenticated catalog. Protected plaintext is read only for this call
// and is never indexed or written to disk.
func (r *Repository) SearchWithCatalog(query string, catalog encryption.Catalog, vaultKey []byte) ([]SearchResult, error) {
	return r.search(query, &catalog, vaultKey)
}

func (r *Repository) search(query string, catalog *encryption.Catalog, vaultKey []byte) ([]SearchResult, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []SearchResult{}, nil
	}
	if len(query) > maxSearchQueryBytes {
		return nil, ErrSearchQueryTooLong
	}
	lowerQuery := strings.ToLower(query)

	// Keep the file inventory and all reads on one repository snapshot. Writers
	// and journal recovery use the write side of this same lock.
	r.content.mu.RLock()
	defer r.content.mu.RUnlock()
	entries, err := r.content.walkEntries(func(entry contentEntry) bool {
		return entry.Info.IsDir() && ignoredSearchPath(entry.Path)
	})
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0)
	notes := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Info.Mode()&os.ModeSymlink != 0 || ignoredSearchPath(entry.Path) {
			continue
		}
		if entry.Info.IsDir() {
			if len(results) < maxSearchResults && strings.Contains(strings.ToLower(entry.Name), lowerQuery) {
				results = append(results, SearchResult{Path: entry.Path, Type: "directory"})
			}
			continue
		}
		if !entry.Info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name), ".md") {
			continue
		}
		notes[entry.Path] = struct{}{}
	}
	if catalog != nil {
		for _, object := range catalog.Objects {
			if object.Kind == encryption.CatalogObjectNote && object.Protected && !ignoredSearchPath(object.LogicalPath) {
				notes[object.LogicalPath] = struct{}{}
			}
		}
	}

	paths := make([]string, 0, len(notes))
	for notePath := range notes {
		paths = append(paths, notePath)
	}
	sort.Slice(paths, func(i, j int) bool {
		lowerI, lowerJ := strings.ToLower(paths[i]), strings.ToLower(paths[j])
		if lowerI != lowerJ {
			return lowerI < lowerJ
		}
		return paths[i] < paths[j]
	})
	for _, notePath := range paths {
		if len(results) >= maxSearchResults {
			break
		}
		if strings.Contains(strings.ToLower(filepath.Base(notePath)), lowerQuery) {
			results = append(results, SearchResult{Path: notePath, Type: "file"})
		}

		if len(results) >= maxSearchResults {
			break
		}
		content, err := r.readSearchMarkdownLocked(notePath, catalog, vaultKey)
		if err != nil {
			return nil, err
		}
		matches, err := searchMarkdownContent(notePath, content, lowerQuery, maxSearchResults-len(results))
		if err != nil {
			return nil, err
		}
		results = append(results, matches...)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Path != results[j].Path {
			return strings.ToLower(results[i].Path) < strings.ToLower(results[j].Path)
		}
		if results[i].Type != results[j].Type {
			return results[i].Type != "content"
		}
		return results[i].Line < results[j].Line
	})
	if len(results) > maxSearchResults {
		results = results[:maxSearchResults]
	}
	return results, nil
}

func (r *Repository) readSearchMarkdownLocked(relative string, catalog *encryption.Catalog, vaultKey []byte) ([]byte, error) {
	if catalog != nil {
		object, found := catalogNote(*catalog, relative)
		if found {
			if object.Kind != encryption.CatalogObjectNote {
				return nil, ErrProtectedObjectMissing
			}
			if object.Protected {
				return r.readProtectedBytes(object, vaultKey)
			}
		}
		protectedByRule, err := catalog.IsPathProtected(relative)
		if err != nil {
			return nil, err
		}
		if protectedByRule {
			return nil, ErrProtectedObjectMissing
		}
		if _, err := r.content.lstat(relative + ".age"); err == nil {
			return nil, ErrProtectedObjectMissing
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	stored, err := r.content.read(relative, maxMarkdownSize)
	if err != nil {
		return nil, err
	}
	return stored.Bytes, nil
}

func searchMarkdownContent(notePath string, content []byte, lowerQuery string, remaining int) ([]SearchResult, error) {
	defer clear(content)
	if remaining <= 0 {
		return nil, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), maxMarkdownSize)
	matches := make([]SearchResult, 0)
	line := 0
	for scanner.Scan() && len(matches) < remaining {
		line++
		text := scanner.Text()
		if strings.Contains(strings.ToLower(text), lowerQuery) {
			matches = append(matches, SearchResult{Path: notePath, Type: "content", Line: line, Excerpt: searchExcerpt(text)})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return matches, nil
}

func ignoredSearchPath(relative string) bool {
	for _, part := range strings.Split(relative, "/") {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".trash" || lower == "node_modules" || strings.HasSuffix(lower, ".assets") {
			return true
		}
	}
	return false
}

func searchExcerpt(line string) string {
	line = strings.TrimSpace(line)
	const limit = 180
	if len([]rune(line)) <= limit {
		return line
	}
	return string([]rune(line)[:limit]) + "…"
}

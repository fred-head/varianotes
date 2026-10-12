package files

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxSearchResults = 100

type SearchResult struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Line    int    `json:"line,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
}

func (r *Repository) Search(query string) ([]SearchResult, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []SearchResult{}, nil
	}
	lowerQuery := strings.ToLower(query)
	results := make([]SearchResult, 0)
	entries, err := r.content.WalkEntries(func(entry contentEntry) bool {
		return entry.Info.IsDir() && ignoredSearchPath(entry.Path)
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name)
		if entry.Info.Mode()&os.ModeSymlink != 0 || ignoredSearchPath(entry.Path) {
			continue
		}
		if entry.Info.IsDir() {
			if strings.Contains(name, lowerQuery) {
				results = append(results, SearchResult{Path: entry.Path, Type: "directory"})
				if len(results) >= maxSearchResults {
					break
				}
			}
			continue
		}
		if !entry.Info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name), ".md") {
			continue
		}
		if strings.Contains(name, lowerQuery) {
			results = append(results, SearchResult{Path: entry.Path, Type: "file"})
		}
		if len(results) >= maxSearchResults {
			break
		}
		content, err := r.content.Read(entry.Path, maxMarkdownSize)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(strings.NewReader(string(content.Bytes)))
		scanner.Buffer(make([]byte, 64*1024), maxMarkdownSize)
		line := 0
		for scanner.Scan() && len(results) < maxSearchResults {
			line++
			text := scanner.Text()
			if strings.Contains(strings.ToLower(text), lowerQuery) {
				results = append(results, SearchResult{Path: entry.Path, Type: "content", Line: line, Excerpt: searchExcerpt(text)})
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			return nil, scanErr
		}
		if len(results) >= maxSearchResults {
			break
		}
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

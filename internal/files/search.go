package files

import (
	"bufio"
	"io/fs"
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
	confinedRoot, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, err
	}
	defer confinedRoot.Close()

	err = filepath.WalkDir(r.root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == r.root {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && (name == ".git" || name == ".trash" || name == "node_modules" || strings.HasSuffix(name, ".assets")) {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(r.root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if strings.Contains(name, lowerQuery) {
				results = append(results, SearchResult{Path: relative, Type: "directory"})
				if len(results) >= maxSearchResults {
					return filepath.SkipAll
				}
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		if strings.Contains(name, lowerQuery) {
			results = append(results, SearchResult{Path: relative, Type: "file"})
		}
		if len(results) >= maxSearchResults {
			return filepath.SkipAll
		}
		expected, err := entry.Info()
		if err != nil {
			return err
		}
		file, _, err := openConfinedRegularFile(confinedRoot, filepath.FromSlash(relative), expected, maxMarkdownSize)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), maxMarkdownSize)
		line := 0
		for scanner.Scan() && len(results) < maxSearchResults {
			line++
			text := scanner.Text()
			if strings.Contains(strings.ToLower(text), lowerQuery) {
				results = append(results, SearchResult{Path: relative, Type: "content", Line: line, Excerpt: searchExcerpt(text)})
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return scanErr
		}
		if len(results) >= maxSearchResults {
			return filepath.SkipAll
		}
		return closeErr
	})
	if err != nil {
		return nil, err
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

func searchExcerpt(line string) string {
	line = strings.TrimSpace(line)
	const limit = 180
	if len([]rune(line)) <= limit {
		return line
	}
	return string([]rune(line)[:limit]) + "…"
}

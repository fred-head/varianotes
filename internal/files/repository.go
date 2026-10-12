package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const maxMarkdownSize = 10 << 20

var (
	ErrNotConfigured        = errors.New("repository is not configured")
	ErrInvalidPath          = errors.New("invalid repository path")
	ErrNotMarkdown          = errors.New("file is not Markdown")
	ErrFileTooLarge         = errors.New("Markdown file is too large")
	ErrConflict             = errors.New("Markdown file changed since it was loaded")
	ErrExportTooLarge       = errors.New("vault archive exceeds the export size limit")
	ErrExportTooManyEntries = errors.New("vault archive exceeds the entry limit")
)

type Node struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Children []Node `json:"children,omitempty"`
}

type Repository struct {
	root    string
	trashMu sync.Mutex
	moveMu  sync.Mutex
	content *contentStore
}

type Markdown struct {
	Content string
	Version string
}

func NewRepository(root string) (*Repository, error) {
	if strings.TrimSpace(root) == "" {
		return &Repository{content: &contentStore{}}, nil
	}

	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("inspect repository root: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("repository root is not a directory")
	}

	cleanRoot := filepath.Clean(resolved)
	return &Repository{root: cleanRoot, content: &contentStore{root: cleanRoot}}, nil
}

func (r *Repository) Configured() bool {
	return r.root != ""
}

func (r *Repository) Tree() ([]Node, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}

	nodes, err := r.readDirectory("")
	if err != nil {
		return nil, fmt.Errorf("read repository tree: %w", err)
	}
	return nodes, nil
}

func (r *Repository) readDirectory(relative string) ([]Node, error) {
	entries, err := r.content.ReadDir(relative)
	if err != nil {
		return nil, err
	}

	nodes := make([]Node, 0, len(entries))
	for _, entry := range entries {
		lowerName := strings.ToLower(entry.Name)
		if lowerName == ".git" || lowerName == ".trash" || lowerName == "node_modules" || strings.HasSuffix(lowerName, ".assets") || entry.Info.Mode()&fs.ModeSymlink != 0 {
			continue
		}

		childPath := entry.Path
		if entry.Info.IsDir() {
			children, err := r.readDirectory(childPath)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, Node{Name: entry.Name, Path: childPath, Type: "directory", Children: children})
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name), ".md") {
			nodes = append(nodes, Node{Name: entry.Name, Path: childPath, Type: "file"})
		}
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Type != nodes[j].Type {
			return nodes[i].Type == "directory"
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	return nodes, nil
}

func (r *Repository) ReadMarkdown(relative string) (Markdown, error) {
	if err := r.validateRelative(relative, true); err != nil {
		return Markdown{}, err
	}
	content, err := r.content.Read(relative, maxMarkdownSize)
	if err != nil {
		return Markdown{}, err
	}
	return Markdown{Content: string(content.Bytes), Version: content.Version}, nil
}

func (r *Repository) WriteMarkdown(relative, content, expectedVersion string) (Markdown, error) {
	if err := r.validateRelative(relative, true); err != nil {
		return Markdown{}, err
	}
	written, err := r.content.Write(relative, []byte(content), expectedVersion, maxMarkdownSize)
	if err != nil {
		return Markdown{}, err
	}
	return Markdown{Content: string(written.Bytes), Version: written.Version}, nil
}

func (r *Repository) resolveMarkdown(relative string) (string, os.FileInfo, error) {
	if err := r.validateRelative(relative, true); err != nil {
		return "", nil, err
	}
	info, err := r.content.Lstat(relative)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, ErrInvalidPath
	}
	if info.Size() > maxMarkdownSize {
		return "", nil, ErrFileTooLarge
	}
	return relative, info, nil
}

func hasControlCharacter(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func contentVersion(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func isWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

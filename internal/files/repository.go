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
	ErrNotConfigured = errors.New("repository is not configured")
	ErrInvalidPath   = errors.New("invalid repository path")
	ErrNotMarkdown   = errors.New("file is not Markdown")
	ErrFileTooLarge  = errors.New("Markdown file is too large")
	ErrConflict      = errors.New("Markdown file changed since it was loaded")
	ErrExportTooLarge = errors.New("vault archive exceeds the export size limit")
	ErrExportTooManyEntries = errors.New("vault archive exceeds the entry limit")
)

type Node struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Children []Node `json:"children,omitempty"`
}

type Repository struct {
	root      string
	trashMu   sync.Mutex
	moveMu    sync.Mutex
	contentMu sync.Mutex
}

type Markdown struct {
	Content string
	Version string
}

func NewRepository(root string) (*Repository, error) {
	if strings.TrimSpace(root) == "" {
		return &Repository{}, nil
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

	return &Repository{root: filepath.Clean(resolved)}, nil
}

func (r *Repository) Configured() bool {
	return r.root != ""
}

func (r *Repository) Tree() ([]Node, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}

	nodes, err := readDirectory(r.root, "")
	if err != nil {
		return nil, fmt.Errorf("read repository tree: %w", err)
	}
	return nodes, nil
}

func readDirectory(root, relative string) ([]Node, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return nil, err
	}

	nodes := make([]Node, 0, len(entries))
	for _, entry := range entries {
		lowerName := strings.ToLower(entry.Name())
		if lowerName == ".git" || lowerName == ".trash" || lowerName == "node_modules" || strings.HasSuffix(lowerName, ".assets") || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}

		childPath := filepath.ToSlash(filepath.Join(relative, entry.Name()))
		if entry.IsDir() {
			children, err := readDirectory(root, childPath)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, Node{Name: entry.Name(), Path: childPath, Type: "directory", Children: children})
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			nodes = append(nodes, Node{Name: entry.Name(), Path: childPath, Type: "file"})
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
	resolved, _, err := r.resolveMarkdown(relative)
	if err != nil {
		return Markdown{}, err
	}
	// #nosec G304 -- resolveMarkdown rejects traversal, symlinks, non-regular files, and paths outside the repository root.
	content, err := os.ReadFile(resolved)
	if err != nil {
		return Markdown{}, err
	}
	return Markdown{Content: string(content), Version: contentVersion(content)}, nil
}

func (r *Repository) WriteMarkdown(relative, content, expectedVersion string) (Markdown, error) {
	r.contentMu.Lock()
	defer r.contentMu.Unlock()
	return r.writeMarkdown(relative, content, expectedVersion)
}

func (r *Repository) writeMarkdown(relative, content, expectedVersion string) (Markdown, error) {
	resolved, info, err := r.resolveMarkdown(relative)
	if err != nil {
		return Markdown{}, err
	}
	if len(content) > maxMarkdownSize {
		return Markdown{}, ErrFileTooLarge
	}
	// #nosec G304 -- resolveMarkdown rejects traversal, symlinks, non-regular files, and paths outside the repository root.
	current, err := os.ReadFile(resolved)
	if err != nil {
		return Markdown{}, err
	}
	if expectedVersion == "" || contentVersion(current) != expectedVersion {
		return Markdown{}, ErrConflict
	}

	temporary, err := os.CreateTemp(filepath.Dir(resolved), "."+filepath.Base(resolved)+".*.tmp")
	if err != nil {
		return Markdown{}, err
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		cleanup()
		return Markdown{}, err
	}
	if _, err := temporary.WriteString(content); err != nil {
		cleanup()
		return Markdown{}, err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return Markdown{}, err
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return Markdown{}, err
	}
	if err := os.Rename(temporaryName, resolved); err != nil {
		cleanup()
		return Markdown{}, err
	}
	if directory, err := os.Open(filepath.Dir(resolved)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}

	return Markdown{Content: content, Version: contentVersion([]byte(content))}, nil
}

func (r *Repository) resolveMarkdown(relative string) (string, os.FileInfo, error) {
	if err := r.validateRelative(relative, true); err != nil {
		return "", nil, err
	}

	candidate := filepath.Join(r.root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", nil, err
	}
	if resolved != filepath.Clean(candidate) || !isWithinRoot(r.root, resolved) {
		return "", nil, ErrInvalidPath
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, ErrInvalidPath
	}
	if info.Size() > maxMarkdownSize {
		return "", nil, ErrFileTooLarge
	}
	return resolved, info, nil
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

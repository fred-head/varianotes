package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var inlineMarkdownLinkPattern = regexp.MustCompile(`\[[^\]\r\n]*\]\(\s*(<[^>\r\n]+>|[^)\s\r\n]+)`)

var ErrLinkPreviewRequired = errors.New("link rewrite preview is required")

type NoteLink struct {
	Href       string `json:"href"`
	TargetPath string `json:"targetPath,omitempty"`
	Line       int    `json:"line"`
	Exists     bool   `json:"exists"`
	Internal   bool   `json:"internal"`
}

type LinkRewrite struct {
	NotePath     string `json:"notePath"`
	NextNotePath string `json:"nextNotePath"`
	Line         int    `json:"line"`
	Before       string `json:"before"`
	After        string `json:"after"`
}

type MoveLinkPreview struct {
	Source   string        `json:"source"`
	Target   string        `json:"target"`
	Token    string        `json:"token"`
	Rewrites []LinkRewrite `json:"rewrites"`
}

type linkOccurrence struct {
	hrefStart  int
	hrefEnd    int
	href       string
	angle      bool
	line       int
	targetPath string
	suffix     string
	internal   bool
}

type plannedLinkFile struct {
	oldPath string
	newPath string
	content string
	updated string
}

func (r *Repository) NoteLinks(notePath string) ([]NoteLink, error) {
	markdown, err := r.ReadMarkdown(notePath)
	if err != nil {
		return nil, err
	}
	notes, err := r.markdownNoteSet()
	if err != nil {
		return nil, err
	}
	occurrences := parseMarkdownLinks(notePath, markdown.Content)
	result := make([]NoteLink, 0, len(occurrences))
	for _, occurrence := range occurrences {
		result = append(result, NoteLink{
			Href:       occurrence.href,
			TargetPath: occurrence.targetPath,
			Line:       occurrence.line,
			Exists:     occurrence.internal && notes[occurrence.targetPath],
			Internal:   occurrence.internal,
		})
	}
	return result, nil
}

func (r *Repository) PreviewMoveLinks(source, target string) (MoveLinkPreview, error) {
	_, info, err := r.resolveEntry(source)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	if _, err := r.resolveNew(target, !info.IsDir()); err != nil {
		return MoveLinkPreview{}, err
	}
	preview, _, err := r.planMoveLinks(source, target)
	return preview, err
}

func (r *Repository) MoveWithLinkUpdates(source, target, expectedToken string) (MoveLinkPreview, error) {
	r.moveMu.Lock()
	defer r.moveMu.Unlock()
	r.contentMu.Lock()
	defer r.contentMu.Unlock()

	preview, files, err := r.planMoveLinks(source, target)
	if err != nil {
		return MoveLinkPreview{}, err
	}
	if expectedToken == "" || expectedToken != preview.Token {
		return MoveLinkPreview{}, ErrConflict
	}
	if err := r.move(source, target); err != nil {
		return MoveLinkPreview{}, err
	}

	written := make([]plannedLinkFile, 0, len(files))
	for _, planned := range files {
		if planned.updated == planned.content {
			continue
		}
		destination, info, resolveErr := r.resolveEntry(planned.newPath)
		if resolveErr != nil || !info.Mode().IsRegular() {
			err = errors.Join(ErrInvalidPath, resolveErr)
			break
		}
		// #nosec G304 -- destination was re-resolved beneath the repository and checked as a regular file immediately above.
		current, readErr := os.ReadFile(destination)
		if readErr != nil {
			err = readErr
			break
		}
		planned.content = string(current)
		if writeErr := writeMarkdownAtomically(destination, planned.updated); writeErr != nil {
			err = writeErr
			break
		}
		written = append(written, planned)
	}
	if err == nil {
		return preview, nil
	}
	for index := len(written) - 1; index >= 0; index-- {
		if destination, info, resolveErr := r.resolveEntry(written[index].newPath); resolveErr == nil && info.Mode().IsRegular() {
			_ = writeMarkdownAtomically(destination, written[index].content)
		}
	}
	_ = r.move(target, source)
	return MoveLinkPreview{}, err
}

func (r *Repository) planMoveLinks(source, target string) (MoveLinkPreview, []plannedLinkFile, error) {
	_, sourceInfo, err := r.resolveEntry(source)
	if err != nil {
		return MoveLinkPreview{}, nil, err
	}
	if _, err := r.resolveNew(target, !sourceInfo.IsDir()); err != nil {
		return MoveLinkPreview{}, nil, err
	}
	notePaths, err := r.markdownNotePaths()
	if err != nil {
		return MoveLinkPreview{}, nil, err
	}
	noteSet := make(map[string]bool, len(notePaths))
	for _, notePath := range notePaths {
		noteSet[notePath] = true
	}

	preview := MoveLinkPreview{Source: source, Target: target, Rewrites: []LinkRewrite{}}
	plannedFiles := make([]plannedLinkFile, 0)
	hash := sha256.New()
	_, _ = hash.Write([]byte(source + "\x00" + target + "\x00"))
	for _, oldNotePath := range notePaths {
		newNotePath := remapMovedPath(oldNotePath, source, target)
		content, readErr := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(oldNotePath)))
		if readErr != nil {
			return MoveLinkPreview{}, nil, readErr
		}
		occurrences := parseMarkdownLinks(oldNotePath, string(content))
		replacements := make([]linkReplacement, 0)
		for _, occurrence := range occurrences {
			if !occurrence.internal || !noteSet[occurrence.targetPath] {
				continue
			}
			newTargetPath := remapMovedPath(occurrence.targetPath, source, target)
			if newNotePath == oldNotePath && newTargetPath == occurrence.targetPath {
				continue
			}
			nextHref, relativeErr := portableRelativeLink(newNotePath, newTargetPath, occurrence.suffix, occurrence.angle)
			if relativeErr != nil {
				return MoveLinkPreview{}, nil, relativeErr
			}
			if nextHref == occurrence.href {
				continue
			}
			replacements = append(replacements, linkReplacement{start: occurrence.hrefStart, end: occurrence.hrefEnd, value: nextHref})
			preview.Rewrites = append(preview.Rewrites, LinkRewrite{NotePath: oldNotePath, NextNotePath: newNotePath, Line: occurrence.line, Before: occurrence.href, After: nextHref})
		}
		updated := applyLinkReplacements(string(content), replacements)
		if oldNotePath == source && strings.EqualFold(path.Ext(source), ".md") {
			oldBase, newBase := noteBase(filepath.FromSlash(source)), noteBase(filepath.FromSlash(target))
			if oldBase != newBase {
				updated = rewriteAssetLinksContent(updated, oldBase, newBase)
			}
		}
		if updated != string(content) {
			plannedFiles = append(plannedFiles, plannedLinkFile{oldPath: oldNotePath, newPath: newNotePath, content: string(content), updated: updated})
		}
		_, _ = hash.Write([]byte(oldNotePath + "\x00" + contentVersion(content) + "\x00" + updated + "\x00"))
	}
	sort.Slice(preview.Rewrites, func(left, right int) bool {
		if preview.Rewrites[left].NotePath != preview.Rewrites[right].NotePath {
			return preview.Rewrites[left].NotePath < preview.Rewrites[right].NotePath
		}
		return preview.Rewrites[left].Line < preview.Rewrites[right].Line
	})
	preview.Token = hex.EncodeToString(hash.Sum(nil))
	return preview, plannedFiles, nil
}

type linkReplacement struct {
	start int
	end   int
	value string
}

func applyLinkReplacements(content string, replacements []linkReplacement) string {
	sort.Slice(replacements, func(left, right int) bool { return replacements[left].start > replacements[right].start })
	for _, replacement := range replacements {
		content = content[:replacement.start] + replacement.value + content[replacement.end:]
	}
	return content
}

func parseMarkdownLinks(notePath, content string) []linkOccurrence {
	ignored := markdownCodeMask(content)
	matches := inlineMarkdownLinkPattern.FindAllStringSubmatchIndex(content, -1)
	result := make([]linkOccurrence, 0, len(matches))
	for _, match := range matches {
		start, hrefStart, hrefEnd := match[0], match[2], match[3]
		if start > 0 && content[start-1] == '!' || ignored[hrefStart] {
			continue
		}
		raw := content[hrefStart:hrefEnd]
		angle := strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">")
		href := raw
		if angle {
			href = raw[1 : len(raw)-1]
		}
		target, suffix, internal := resolvePortableLink(notePath, href)
		result = append(result, linkOccurrence{hrefStart: hrefStart, hrefEnd: hrefEnd, href: raw, angle: angle, line: 1 + strings.Count(content[:hrefStart], "\n"), targetPath: target, suffix: suffix, internal: internal})
	}
	return result
}

func resolvePortableLink(notePath, href string) (string, string, bool) {
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "/") || strings.Contains(href, `\`) {
		return "", "", false
	}
	parsed, err := url.Parse(href)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Path == "" {
		return "", "", false
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || !strings.EqualFold(path.Ext(decoded), ".md") {
		return "", "", false
	}
	target := path.Clean(path.Join(path.Dir(notePath), decoded))
	if target == "." || target == ".." || strings.HasPrefix(target, "../") || validatePortableEntryPath(target, true) != nil {
		return "", "", false
	}
	suffix := ""
	if parsed.RawQuery != "" {
		suffix += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		suffix += "#" + parsed.Fragment
	}
	return target, suffix, true
}

func portableRelativeLink(sourceNote, targetNote, suffix string, angle bool) (string, error) {
	relative, err := filepath.Rel(filepath.FromSlash(path.Dir(sourceNote)), filepath.FromSlash(targetNote))
	if err != nil {
		return "", err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	for index, part := range parts {
		if part != ".." && part != "." {
			parts[index] = url.PathEscape(part)
		}
	}
	result := strings.Join(parts, "/") + suffix
	if angle {
		result = "<" + result + ">"
	}
	return result, nil
}

func remapMovedPath(value, source, target string) string {
	if value == source {
		return target
	}
	if strings.HasPrefix(value, source+"/") {
		return target + strings.TrimPrefix(value, source)
	}
	return value
}

func (r *Repository) markdownNoteSet() (map[string]bool, error) {
	paths, err := r.markdownNotePaths()
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(paths))
	for _, notePath := range paths {
		result[notePath] = true
	}
	return result, nil
}

func (r *Repository) markdownNotePaths() ([]string, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}
	result := []string{}
	err := filepath.WalkDir(r.root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
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
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		relative, err := filepath.Rel(r.root, current)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(relative))
		return nil
	})
	sort.Strings(result)
	return result, err
}

func markdownCodeMask(content string) []bool {
	mask := make([]bool, len(content)+1)
	inFence := false
	for offset := 0; offset < len(content); {
		lineEnd := strings.IndexByte(content[offset:], '\n')
		if lineEnd < 0 {
			lineEnd = len(content)
		} else {
			lineEnd += offset + 1
		}
		line := content[offset:lineEnd]
		trimmed := strings.TrimLeft(line, " \t")
		fence := strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
		if inFence || fence {
			for index := offset; index < lineEnd; index++ {
				mask[index] = true
			}
		}
		if fence {
			inFence = !inFence
		}
		if !inFence && !fence {
			markInlineCode(line, offset, mask)
		}
		offset = lineEnd
	}
	return mask
}

func markInlineCode(line string, base int, mask []bool) {
	for index := 0; index < len(line); {
		if line[index] != '`' {
			index++
			continue
		}
		run := 1
		for index+run < len(line) && line[index+run] == '`' {
			run++
		}
		closing := strings.Index(line[index+run:], strings.Repeat("`", run))
		if closing < 0 {
			return
		}
		end := index + run + closing + run
		for position := index; position < end; position++ {
			mask[base+position] = true
		}
		index = end
	}
}

func writeMarkdownAtomically(target, content string) error {
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return errors.Join(ErrInvalidPath, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".varianotes-link-rewrite-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	cleanup := func() { _ = temporary.Close(); _ = os.Remove(temporaryName) }
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.WriteString(content); err != nil {
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
	if err := os.Rename(temporaryName, target); err != nil {
		cleanup()
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

// MaxAssetSize bounds a single uploaded or read image attachment.
const MaxAssetSize = 10 << 20

const maxAssetSize = MaxAssetSize

var (
	ErrUnsupportedMedia = errors.New("unsupported image type")
	ErrAssetTooLarge    = errors.New("image is too large")
)

type Asset struct {
	RelativePath string
	ContentType  string
	Content      []byte
}

var imageExtensions = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func (r *Repository) SaveAsset(noteRelative string, source io.Reader) (Asset, error) {
	r.content.mu.Lock()
	defer r.content.mu.Unlock()

	if err := r.validateRelative(noteRelative, true); err != nil {
		return Asset{}, err
	}
	noteInfo, err := r.content.lstat(noteRelative)
	if err != nil {
		return Asset{}, err
	}
	if !noteInfo.Mode().IsRegular() {
		return Asset{}, ErrInvalidPath
	}
	if noteInfo.Size() > maxMarkdownSize {
		return Asset{}, ErrFileTooLarge
	}
	notePath := noteRelative
	content, err := io.ReadAll(io.LimitReader(source, maxAssetSize+1))
	if err != nil {
		return Asset{}, err
	}
	if len(content) > maxAssetSize {
		return Asset{}, ErrAssetTooLarge
	}
	contentType := http.DetectContentType(content)
	extension, supported := imageExtensions[contentType]
	if !supported {
		return Asset{}, ErrUnsupportedMedia
	}

	assetsDirectory := path.Join(path.Dir(notePath), noteBase(notePath)+".assets")
	exists, err := validateOwnedAssets(r.content, assetsDirectory)
	if err != nil {
		return Asset{}, err
	}
	if !exists {
		// #nosec G301 -- assets are ordinary portable notebook files; access control is provided by the repository/container boundary.
		if err := r.content.mkdir(assetsDirectory, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return Asset{}, err
		}
		if _, err := validateOwnedAssets(r.content, assetsDirectory); err != nil {
			return Asset{}, err
		}
	}

	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return Asset{}, err
	}
	filename := hex.EncodeToString(identifier) + extension
	target := path.Join(assetsDirectory, filename)
	if err := r.content.writeNew(target, content, 0o644); err != nil {
		return Asset{}, err
	}
	if err := r.content.syncDir(assetsDirectory); err != nil {
		return Asset{}, err
	}

	relative := path.Join(noteBase(notePath)+".assets", filename)
	return Asset{RelativePath: relative, ContentType: contentType, Content: content}, nil
}

func (r *Repository) ReadAsset(noteRelative, assetRelative string) (Asset, error) {
	notePath, err := r.resolveAssetPath(noteRelative, assetRelative)
	if err != nil {
		return Asset{}, err
	}
	assetsDirectory := path.Dir(notePath)
	exists, err := validateOwnedAssets(r.content, assetsDirectory)
	if err != nil {
		return Asset{}, err
	}
	if !exists {
		return Asset{}, os.ErrNotExist
	}
	content, err := r.content.Read(notePath, maxAssetSize)
	if errors.Is(err, ErrFileTooLarge) {
		return Asset{}, ErrInvalidPath
	}
	if err != nil {
		return Asset{}, err
	}
	contentType := http.DetectContentType(content.Bytes)
	if _, supported := imageExtensions[contentType]; !supported {
		return Asset{}, ErrUnsupportedMedia
	}
	return Asset{RelativePath: assetRelative, ContentType: contentType, Content: content.Bytes}, nil
}

// resolveAssetPath turns a Markdown-relative image destination into one stable
// vault path. Attachments can be shared by notes in different directories, but
// must remain ordinary files directly inside a *.assets directory.
func (r *Repository) resolveAssetPath(noteRelative, assetRelative string) (string, error) {
	notePath, _, err := r.resolveMarkdown(noteRelative)
	if err != nil {
		return "", err
	}
	if assetRelative == "" || hasControlCharacter(assetRelative) || strings.Contains(assetRelative, `\`) {
		return "", ErrInvalidPath
	}
	parsed, err := url.Parse(assetRelative)
	if err != nil || parsed.IsAbs() || parsed.Scheme != "" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidPath
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || decoded == "" || path.IsAbs(decoded) || strings.Contains(decoded, `\`) || hasControlCharacter(decoded) {
		return "", ErrInvalidPath
	}
	resolved := path.Clean(path.Join(path.Dir(notePath), decoded))
	if resolved == "." || resolved == ".." || strings.HasPrefix(resolved, "../") || !validAssetLogicalPath(resolved) {
		return "", ErrInvalidPath
	}
	return resolved, nil
}

func validAssetLogicalPath(relative string) bool {
	if relative == "" || hasControlCharacter(relative) || path.IsAbs(relative) || path.Clean(relative) != relative || strings.Contains(relative, `\`) {
		return false
	}
	parts := strings.Split(relative, "/")
	if len(parts) < 2 || !strings.HasSuffix(strings.ToLower(parts[len(parts)-2]), ".assets") {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return supportedCleanupExtension(parts[len(parts)-1])
}

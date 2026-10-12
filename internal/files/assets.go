package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
)

const maxAssetSize = 10 << 20

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
	notePath, _, err := r.resolveMarkdown(noteRelative)
	if err != nil {
		return Asset{}, err
	}
	expectedDirectoryName := noteBase(notePath) + ".assets"
	if hasControlCharacter(assetRelative) || path.IsAbs(assetRelative) || path.Clean(assetRelative) != assetRelative || strings.Contains(assetRelative, `\`) {
		return Asset{}, ErrInvalidPath
	}
	parts := strings.Split(assetRelative, "/")
	if len(parts) != 2 || parts[0] != expectedDirectoryName || parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return Asset{}, ErrInvalidPath
	}

	assetsDirectory := path.Join(path.Dir(notePath), expectedDirectoryName)
	exists, err := validateOwnedAssets(r.content, assetsDirectory)
	if err != nil {
		return Asset{}, err
	}
	if !exists {
		return Asset{}, os.ErrNotExist
	}
	target := path.Join(assetsDirectory, parts[1])
	content, err := r.content.Read(target, maxAssetSize)
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

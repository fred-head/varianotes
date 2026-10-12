package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
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
	notePath, _, err := r.resolveMarkdown(noteRelative)
	if err != nil {
		return Asset{}, err
	}
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

	assetsDirectory := noteAssetsPath(notePath)
	exists, err := validateOwnedAssets(r.root, assetsDirectory)
	if err != nil {
		return Asset{}, err
	}
	if !exists {
		// #nosec G301 -- assets are ordinary portable notebook files; access control is provided by the repository/container boundary.
		if err := os.Mkdir(assetsDirectory, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return Asset{}, err
		}
		if _, err := validateOwnedAssets(r.root, assetsDirectory); err != nil {
			return Asset{}, err
		}
	}

	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return Asset{}, err
	}
	filename := hex.EncodeToString(identifier) + extension
	target := filepath.Join(assetsDirectory, filename)
	// #nosec G302,G304 -- target uses a random filename in a validated note-owned directory; assets intentionally remain portable.
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Asset{}, err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return Asset{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return Asset{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return Asset{}, err
	}
	if err := syncDirectory(assetsDirectory); err != nil {
		return Asset{}, err
	}

	relative := noteBase(notePath) + ".assets/" + filename
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

	assetsDirectory := noteAssetsPath(notePath)
	exists, err := validateOwnedAssets(r.root, assetsDirectory)
	if err != nil {
		return Asset{}, err
	}
	if !exists {
		return Asset{}, os.ErrNotExist
	}
	target := filepath.Join(assetsDirectory, parts[1])
	info, err := os.Lstat(target)
	if err != nil {
		return Asset{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxAssetSize {
		return Asset{}, ErrInvalidPath
	}
	// #nosec G304 -- target was confined to the validated note-owned asset directory and lstat-checked as a non-symlink regular file.
	content, err := os.ReadFile(target)
	if err != nil {
		return Asset{}, err
	}
	contentType := http.DetectContentType(content)
	if _, supported := imageExtensions[contentType]; !supported {
		return Asset{}, ErrUnsupportedMedia
	}
	return Asset{RelativePath: assetRelative, ContentType: contentType, Content: content}, nil
}

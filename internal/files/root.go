package files

import (
	"errors"
	"os"
)

// openConfinedRegularFile keeps the final open operation beneath an already
// opened repository root, even if a path component is replaced concurrently.
func openConfinedRegularFile(root *os.Root, relative string, expected os.FileInfo, maximumSize int64) (*os.File, os.FileInfo, error) {
	file, err := root.Open(relative)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, ErrInvalidPath
	}
	if expected != nil && !os.SameFile(expected, info) {
		_ = file.Close()
		return nil, nil, ErrInvalidPath
	}
	if info.Size() > maximumSize {
		_ = file.Close()
		return nil, nil, ErrFileTooLarge
	}
	if info.Size() < 0 {
		_ = file.Close()
		return nil, nil, errors.New("file size is invalid")
	}
	return file, info, nil
}

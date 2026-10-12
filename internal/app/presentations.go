package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	imagePresentationSchemaVersion  = 1
	maximumPresentationMetadataSize = 4 << 20
	maximumPresentationRecords      = 10000
)

var errInvalidPresentationSize = errors.New("invalid image presentation size")

type imagePresentationSize string

const (
	presentationSmall  imagePresentationSize = "small"
	presentationMedium imagePresentationSize = "medium"
	presentationLarge  imagePresentationSize = "large"
	presentationFull   imagePresentationSize = "full"
)

func (size imagePresentationSize) valid() bool {
	return size == presentationSmall || size == presentationMedium || size == presentationLarge || size == presentationFull
}

type imagePresentationRecord struct {
	NotebookID string                `json:"notebookId"`
	Note       string                `json:"note"`
	Image      string                `json:"image"`
	Size       imagePresentationSize `json:"size"`
}

type imagePresentationData struct {
	Version int                       `json:"version"`
	Records []imagePresentationRecord `json:"records"`
}

type imagePresentationStore struct {
	mu     sync.Mutex
	path   string
	memory imagePresentationData
}

func newImagePresentationStore(notebookMetadataPath string) *imagePresentationStore {
	metadataPath := ""
	if notebookMetadataPath != "" {
		metadataPath = filepath.Join(filepath.Dir(notebookMetadataPath), "image-presentations.json")
	}
	return &imagePresentationStore{path: metadataPath, memory: imagePresentationData{Version: imagePresentationSchemaVersion}}
}

func (store *imagePresentationStore) load() (imagePresentationData, error) {
	if store.path == "" {
		return store.memory, nil
	}
	info, err := os.Lstat(store.path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return imagePresentationData{}, errors.New("image presentation metadata must not be a symbolic link")
	}
	if err == nil && info.Size() > maximumPresentationMetadataSize {
		return imagePresentationData{}, errors.New("image presentation metadata is too large")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return imagePresentationData{}, err
	}
	content, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return imagePresentationData{Version: imagePresentationSchemaVersion}, nil
	}
	if err != nil {
		return imagePresentationData{}, err
	}
	var data imagePresentationData
	if err := json.Unmarshal(content, &data); err != nil {
		return imagePresentationData{}, err
	}
	if data.Version != imagePresentationSchemaVersion {
		return imagePresentationData{}, errors.New("unsupported image presentation metadata version")
	}
	if len(data.Records) > maximumPresentationRecords {
		return imagePresentationData{}, errors.New("too many image presentation records")
	}
	for _, record := range data.Records {
		if record.NotebookID == "" || record.Note == "" || record.Image == "" || !record.Size.valid() {
			return imagePresentationData{}, errors.New("invalid image presentation metadata")
		}
	}
	return data, nil
}

func (store *imagePresentationStore) write(data imagePresentationData) error {
	data.Version = imagePresentationSchemaVersion
	if store.path == "" {
		store.memory = data
		return nil
	}
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if len(content) > maximumPresentationMetadataSize {
		return errors.New("image presentation metadata is too large")
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".image-presentations-*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, store.path)
}

func (store *imagePresentationStore) list(notebookID, note string) (map[string]imagePresentationSize, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := store.load()
	if err != nil {
		return nil, err
	}
	result := make(map[string]imagePresentationSize)
	for _, record := range data.Records {
		if record.NotebookID == notebookID && record.Note == note {
			result[record.Image] = record.Size
		}
	}
	return result, nil
}

func (store *imagePresentationStore) set(notebookID, note, image string, size imagePresentationSize, previousImage string) error {
	if !size.valid() {
		return errInvalidPresentationSize
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := store.load()
	if err != nil {
		return err
	}
	filtered := data.Records[:0]
	for _, record := range data.Records {
		if record.NotebookID == notebookID && record.Note == note && (record.Image == image || previousImage != "" && record.Image == previousImage) {
			continue
		}
		filtered = append(filtered, record)
	}
	if len(filtered) >= maximumPresentationRecords {
		return errors.New("too many image presentation records")
	}
	data.Records = append(filtered, imagePresentationRecord{NotebookID: notebookID, Note: note, Image: image, Size: size})
	return store.write(data)
}

func (store *imagePresentationStore) deleteImage(notebookID, note, image string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := store.load()
	if err != nil {
		return err
	}
	filtered := data.Records[:0]
	for _, record := range data.Records {
		if record.NotebookID == notebookID && record.Note == note && record.Image == image {
			continue
		}
		filtered = append(filtered, record)
	}
	data.Records = filtered
	return store.write(data)
}

func (store *imagePresentationStore) move(notebookID, source, target string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := store.load()
	if err != nil {
		return err
	}
	changed := false
	for index := range data.Records {
		record := &data.Records[index]
		if record.NotebookID != notebookID || record.Note != source && !strings.HasPrefix(record.Note, source+"/") {
			continue
		}
		oldNote := record.Note
		if oldNote == source {
			record.Note = target
		} else {
			record.Note = target + strings.TrimPrefix(oldNote, source)
		}
		if oldNote == source && strings.EqualFold(filepath.Ext(source), ".md") {
			oldBase := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
			newBase := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
			if strings.HasPrefix(record.Image, oldBase+".assets/") {
				record.Image = newBase + ".assets/" + strings.TrimPrefix(record.Image, oldBase+".assets/")
			}
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return store.write(data)
}

func (store *imagePresentationStore) deletePath(notebookID, target string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := store.load()
	if err != nil {
		return err
	}
	filtered := data.Records[:0]
	for _, record := range data.Records {
		if record.NotebookID == notebookID && (record.Note == target || strings.HasPrefix(record.Note, target+"/")) {
			continue
		}
		filtered = append(filtered, record)
	}
	data.Records = filtered
	return store.write(data)
}

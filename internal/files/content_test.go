package files

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestContentStorePreservesOpaqueBytesAndChecksVersions(t *testing.T) {
	root := t.TempDir()
	payload := []byte{0x00, 0xff, 0x01, '\n', 0x80}
	if err := os.Mkdir(filepath.Join(root, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "attachments", "payload.bin"), payload, 0o640); err != nil {
		t.Fatal(err)
	}
	store := &contentStore{root: root}

	loaded, err := store.Read("attachments/payload.bin", 32)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Bytes, payload) || loaded.Version == "" {
		t.Fatalf("read changed opaque bytes or omitted the version: bytes=%v version=%q", loaded.Bytes, loaded.Version)
	}

	writtenBytes := []byte{0x03, 0x00, 0xfe}
	written, err := store.Write("attachments/payload.bin", writtenBytes, loaded.Version, 32)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written.Bytes, writtenBytes) || written.Version == loaded.Version {
		t.Fatalf("write result did not preserve bytes or advance the version: %#v", written)
	}
	actual, err := os.ReadFile(filepath.Join(root, "attachments", "payload.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, writtenBytes) {
		t.Fatalf("stored bytes differ: got %v, want %v", actual, writtenBytes)
	}

	if _, err := store.Write("attachments/payload.bin", []byte("stale"), loaded.Version, 32); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version was not rejected: %v", err)
	}
	actual, err = os.ReadFile(filepath.Join(root, "attachments", "payload.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, writtenBytes) {
		t.Fatalf("stale write changed stored bytes: %v", actual)
	}
}

func TestContentStoreRejectsUnsafePathsAndSizes(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), []byte("oversized"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkErr := os.Symlink(outside, filepath.Join(root, "escape.bin"))
	store := &contentStore{root: root}

	unsafePaths := []string{"../secret.bin", "folder"}
	if symlinkErr == nil {
		unsafePaths = append(unsafePaths, "escape.bin")
	}
	for _, unsafe := range unsafePaths {
		if _, err := store.Read(unsafe, 32); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("unsafe path %q was accepted: %v", unsafe, err)
		}
	}
	if _, err := store.Read("large.bin", 4); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized file was accepted: %v", err)
	}
	initial, err := store.Read("large.bin", 32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("large.bin", []byte("this is too large"), initial.Version, 4); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized write was accepted: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "oversized" {
		t.Fatalf("rejected write changed stored bytes: %q", actual)
	}
}

func TestContentStoreConcurrentWritesHaveOneWinner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.bin")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &contentStore{root: root}
	loaded, err := store.Read("note.bin", 64)
	if err != nil {
		t.Fatal(err)
	}

	payloads := [][]byte{[]byte("first writer"), []byte("second writer")}
	start := make(chan struct{})
	results := make(chan error, len(payloads))
	var writers sync.WaitGroup
	for _, payload := range payloads {
		payload := payload
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			_, err := store.Write("note.bin", payload, loaded.Version, 64)
			results <- err
		}()
	}
	close(start)
	writers.Wait()
	close(results)

	winners, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent write error: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent writers had %d winners and %d conflicts", winners, conflicts)
	}

	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, payloads[0]) && !bytes.Equal(actual, payloads[1]) {
		t.Fatalf("final bytes are not a complete winning write: %q", actual)
	}
}

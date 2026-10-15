package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestHTTPTrashMutationRejectsLockAfterFinalPreparation(t *testing.T) {
	for _, action := range []string{"trash", "restore", "delete"} {
		t.Run(action, func(t *testing.T) {
			prepared, resume := make(chan struct{}), make(chan struct{})
			enabled, calls := false, 0
			harness := newExportHarnessWithMutationHook(t, true, func() {
				if enabled {
					calls++
					if calls == 2 {
						close(prepared)
						<-resume
					}
				}
			})
			request := func(method, target, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, target, bytes.NewBufferString(body))
				addAuthRequestContext(r, harness.cookie, harness.csrf)
				w := httptest.NewRecorder()
				harness.handler.ServeHTTP(w, r)
				return w
			}
			method, target, body := http.MethodDelete, "/api/repository/entry?path=private%2FSecret.md", ""
			if action != "trash" {
				setup := request(method, target, body)
				if setup.Code != http.StatusOK {
					t.Fatalf("setup Trash: %d %s", setup.Code, setup.Body.String())
				}
				var item struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(setup.Body.Bytes(), &item); err != nil || item.ID == "" {
					t.Fatalf("Trash id: %v", err)
				}
				target = "/api/repository/trash/" + item.ID
				if action == "restore" {
					method, target, body = http.MethodPost, target+"/restore", `{"protectionChoice":"decrypt"}`
				}
			}
			registry := filepath.Join(harness.metadataRoot, "app", "notebooks.json")
			state, err := inspectVaultProtection(notebookRecord{ID: harness.vaultID, LocalPath: harness.root}, registry)
			if err != nil {
				t.Fatal(err)
			}
			store, err := encryption.NewCatalogStore(state.catalogPath)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := store.Load(harness.key, registeredCatalogID(harness.vaultID))
			if err != nil {
				t.Fatal(err)
			}
			physical := ""
			for _, object := range catalog.Objects {
				if object.Kind == encryption.CatalogObjectNote {
					physical = object.PhysicalPath
				}
			}
			before, err := os.ReadFile(filepath.Join(harness.root, filepath.FromSlash(physical)))
			if err != nil {
				t.Fatal(err)
			}
			enabled = true
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- request(method, target, body) }()
			select {
			case <-prepared:
			case response := <-done:
				t.Fatalf("final prepared boundary not reached: %d %s", response.Code, response.Body.String())
			case <-time.After(3 * time.Second):
				close(resume)
				t.Fatal("preparation stalled")
			}
			lock := request(http.MethodPost, "/api/encryption/lock", "")
			if lock.Code != http.StatusOK {
				close(resume)
				t.Fatalf("lock: %d %s", lock.Code, lock.Body.String())
			}
			close(resume)
			select {
			case response := <-done:
				if response.Code != http.StatusLocked {
					t.Fatalf("late mutation: %d %s", response.Code, response.Body.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("mutation stalled")
			}
			after, err := os.ReadFile(filepath.Join(harness.root, filepath.FromSlash(physical)))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("late mutation changed ciphertext: %v", err)
			}
			if _, err := os.Stat(filepath.Join(harness.root, "private", "Secret.md")); !os.IsNotExist(err) {
				t.Fatalf("late restore created plaintext: %v", err)
			}
		})
	}
}

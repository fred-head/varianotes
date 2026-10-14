package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/fred-head/varianotes/internal/files"
)

func TestHTTPExportRecoveryDuringPreparedSave(t *testing.T) {
	for _, endpoint := range []string{"preflight", "reauth", "authorized"} {
		for _, abort := range []bool{false, true} {
			name := endpoint + "/commit"
			if abort {
				name = endpoint + "/abort"
			}
			t.Run(name, func(t *testing.T) {
				h := newProtectedExportHarness(t)
				request := func(method, endpoint string, body any) *httptest.ResponseRecorder {
					t.Helper()
					encoded, err := json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
					r := httptest.NewRequest(method, "/api/notebooks/"+h.vaultID+"/export"+endpoint, bytes.NewReader(encoded))
					addAuthRequestContext(r, h.cookie, h.csrf)
					w := httptest.NewRecorder()
					h.handler.ServeHTTP(w, r)
					return w
				}
				var token map[string]string
				if endpoint == "authorized" {
					w := request(http.MethodPost, "/reauth", map[string]string{"password": exportVaultPassword})
					if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &token) != nil {
						t.Fatalf("grant: %d %s", w.Code, w.Body.String())
					}
				}
				registry := filepath.Join(h.metadataRoot, "app", "notebooks.json")
				record, err := findNotebook(registry, h.vaultID)
				if err != nil {
					t.Fatal(err)
				}
				repository, err := files.NewRepository(h.root)
				if err != nil {
					t.Fatal(err)
				}
				access := newProtectedNoteAccess(repository, registry, record)
				catalog, err := access.catalogForKey(h.key)
				if err != nil {
					t.Fatal(err)
				}
				original, _, err := repository.ReadMarkdownWithCatalog("private/Secret.md", catalog, h.key)
				if err != nil {
					t.Fatal(err)
				}
				operation, _, err := repository.PrepareMarkdownWriteWithCatalog("private/Secret.md", "saved during export", original.Version, catalog, h.key, access.store, access.journal)
				if err != nil {
					t.Fatal(err)
				}
				// Keep the real repository Save prepared while each HTTP route
				// constructs an independent repository/access/journal instance.
				var w *httptest.ResponseRecorder
				switch endpoint {
				case "preflight":
					w = request(http.MethodGet, "", nil)
				case "reauth":
					w = request(http.MethodPost, "/reauth", map[string]string{"password": exportVaultPassword})
				case "authorized":
					w = request(http.MethodPost, "", token)
				}
				expected := "saved during export"
				if abort {
					if err := operation.Abort(); err != nil {
						t.Fatal(err)
					}
					expected = original.Content
				} else if err := operation.Commit(nil); err != nil {
					t.Fatalf("B1: HTTP %s discarded a live Save: %v (export %d)", endpoint, err, w.Code)
				}
				if w.Code != http.StatusServiceUnavailable {
					t.Errorf("export should defer recovery of live work: status %d", w.Code)
				}
				restarted := newProtectedNoteAccess(repository, registry, record)
				catalog, err = restarted.catalogForKey(h.key)
				if err != nil {
					t.Fatal(err)
				}
				note, protected, err := repository.ReadMarkdownWithCatalog("private/Secret.md", catalog, h.key)
				if err != nil || !protected || note.Content != expected {
					t.Fatalf("content/envelope lost after recovery: %#v %v", note, err)
				}
				if w := request(http.MethodPost, "/reauth", map[string]string{"password": exportVaultPassword}); w.Code != http.StatusOK {
					t.Fatalf("export retry failed: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestHTTPExportRecoveryDuringPreparedMoveAndRestore(t *testing.T) {
	for _, action := range []string{"move", "restore"} {
		for _, abort := range []bool{false, true} {
			name := action + "/commit"
			if abort {
				name = action + "/lock-abort-retry"
			}
			t.Run(name, func(t *testing.T) {
				prepared, resume := make(chan struct{}), make(chan struct{})
				var release sync.Once
				t.Cleanup(func() { release.Do(func() { close(resume) }) })
				enabled, calls := false, 0
				h := newExportHarnessWithMutationHook(t, true, func() {
					if enabled {
						calls++
						// Restore commits its conversion first; this Move has
						// already protected content and only the final transaction.
						if action == "move" && calls == 1 || action == "restore" && calls == 2 {
							close(prepared)
							<-resume
						}
					}
				})
				request := func(method, endpoint, body string) *httptest.ResponseRecorder {
					r := httptest.NewRequest(method, endpoint, bytes.NewBufferString(body))
					addAuthRequestContext(r, h.cookie, h.csrf)
					w := httptest.NewRecorder()
					h.handler.ServeHTTP(w, r)
					return w
				}
				endpoint, body, finalPath := "/api/repository/move", `{"source":"private/Secret.md","target":"private/Moved.md","protectionChoice":"keep"}`, "private/Moved.md"
				if action == "restore" {
					trashed := request(http.MethodDelete, "/api/repository/entry?path=private%2FSecret.md", "")
					var item files.TrashItem
					if trashed.Code != http.StatusOK || json.Unmarshal(trashed.Body.Bytes(), &item) != nil || item.ID == "" {
						t.Fatalf("Trash setup: %d %s", trashed.Code, trashed.Body.String())
					}
					endpoint, body, finalPath = "/api/repository/trash/"+item.ID+"/restore", `{"protectionChoice":"keep"}`, "private/Secret.md"
				}
				enabled = true
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- request(http.MethodPost, endpoint, body) }()
				select {
				case <-prepared:
				case w := <-done:
					t.Fatalf("final preparation missing: %d %s", w.Code, w.Body.String())
				case <-time.After(5 * time.Second):
					t.Fatal("preparation stalled")
				}
				w := request(http.MethodGet, "/api/notebooks/"+h.vaultID+"/export", "")
				if w.Code != http.StatusServiceUnavailable {
					t.Errorf("export attempted recovery of active %s: status %d", action, w.Code)
				}
				if abort {
					if err := h.manager.LockVault(h.vaultID, encryption.LockReasonManual); err != nil {
						t.Fatal(err)
					}
				}
				release.Do(func() { close(resume) })
				select {
				case w = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("commit/abort stalled")
				}
				expectedStatus := http.StatusOK
				if abort {
					expectedStatus = http.StatusLocked
				}
				if w.Code != expectedStatus {
					t.Fatalf("mutation: %d %s", w.Code, w.Body.String())
				}
				enabled = false
				if abort {
					if err := h.manager.Unlock(h.vaultID, h.sessionHash[:], h.key); err != nil {
						t.Fatal(err)
					}
					w = request(http.MethodPost, endpoint, body)
					if w.Code != http.StatusOK {
						t.Fatalf("mutation retry: %d %s", w.Code, w.Body.String())
					}
				}
				w = request(http.MethodGet, "/api/repository/file?path="+finalPath, "")
				var note map[string]string
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &note) != nil || note["content"] != "# Secret\n"+exportNoteSentinel {
					t.Fatalf("content/restore path lost: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

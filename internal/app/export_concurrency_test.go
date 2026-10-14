package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
					operation.Abort()
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

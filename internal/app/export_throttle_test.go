package app

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
)

func exportCredentialRequest(t *testing.T, h protectedExportHarness, target, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	addAuthRequestContext(request, h.cookie, h.csrf)
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

func TestProtectedExportCredentialFailuresThrottleAcrossRestart(t *testing.T) {
	h := newProtectedExportHarness(t)
	target := "/api/notebooks/" + h.vaultID + "/export/reauth"
	for attempt := 0; attempt < 4; attempt++ {
		response := exportCredentialRequest(t, h, target, "incorrect export password")
		if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "incorrect export password") {
			t.Fatalf("wrong password response: %d %s", response.Code, response.Body.String())
		}
	}
	config := h.service.Config()
	if err := h.service.Close(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reopened, err := auth.Open(t.Context(), config, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	sessions, err := auth.NewSessions(reopened, auth.DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	h.handler, err = newHandlerWithKeyManager(logger, "", reopened, sessions, h.manager)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{target, "/api/encryption/unlock"} {
		response := exportCredentialRequest(t, h, route, exportVaultPassword)
		if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" || !strings.Contains(response.Body.String(), `"code":"authentication_throttled"`) || strings.Contains(response.Body.String(), `"token"`) {
			t.Fatalf("export failure throttle did not survive restart/share sensitive scope: %d %s", response.Code, response.Body.String())
		}
	}
}

// Hold one request before its password derivation; never run expensive work in
// parallel to test the shared credential slot.
type heldExportBody struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	first   bool
}

func (b *heldExportBody) Read(p []byte) (int, error) {
	if !b.first {
		b.first = true
		close(b.entered)
		<-b.release
	}
	return b.reader.Read(p)
}
func (*heldExportBody) Close() error { return nil }

func TestProtectedExportSharesBoundedCredentialWork(t *testing.T) {
	h := newProtectedExportHarness(t)
	target := "/api/notebooks/" + h.vaultID + "/export/reauth"
	body, err := json.Marshal(map[string]string{"password": exportVaultPassword})
	if err != nil {
		t.Fatal(err)
	}
	held := &heldExportBody{reader: bytes.NewReader(body), entered: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, target, nil)
	request.Body = held
	request.ContentLength = int64(len(body))
	addAuthRequestContext(request, h.cookie, h.csrf)
	done := make(chan *httptest.ResponseRecorder, 1)
	finished := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(held.release) }) }
	// Cleanup runs before the harness closes its database. A failing assertion
	// must still release and join the request that owns the credential slot.
	t.Cleanup(func() {
		release()
		<-finished
	})
	go func() {
		defer close(finished)
		response := httptest.NewRecorder()
		h.handler.ServeHTTP(response, request)
		done <- response
	}()
	select {
	case <-held.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("held export did not reach request decoding")
	}
	responses := []*httptest.ResponseRecorder{
		exportCredentialRequest(t, h, target, exportVaultPassword),
		exportCredentialRequest(t, h, "/api/encryption/unlock", exportVaultPassword),
	}
	release()
	// Real scrypt remains enabled; race instrumentation on a loaded CI runner
	// can take longer than five seconds even with only one active derivation.
	select {
	case first := <-done:
		if first.Code != http.StatusOK {
			t.Fatalf("held export failed: %d %s", first.Code, first.Body.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("held export did not complete")
	}
	for _, response := range responses {
		if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" || response.Header().Get("Cache-Control") != "no-store, private" || strings.Contains(response.Body.String(), `"token"`) {
			t.Fatalf("concurrent credential derivation was allowed: %d %s", response.Code, response.Body.String())
		}
	}
	if response := exportCredentialRequest(t, h, target, exportVaultPassword); response.Code != http.StatusOK {
		t.Fatalf("credential slot was not released: %d %s", response.Code, response.Body.String())
	}
}

func TestProtectedExportSuccessfulReauthenticationClearsCredentialFailures(t *testing.T) {
	h := newProtectedExportHarness(t)
	target := "/api/notebooks/" + h.vaultID + "/export/reauth"
	for round := 0; round < 2; round++ {
		for attempt := 0; attempt < 3; attempt++ {
			if response := exportCredentialRequest(t, h, target, "wrong export password"); response.Code != http.StatusUnauthorized {
				t.Fatalf("failed attempts were not reset by success: %d %s", response.Code, response.Body.String())
			}
		}
		if response := exportCredentialRequest(t, h, target, exportVaultPassword); response.Code != http.StatusOK {
			t.Fatalf("valid reauthentication failed: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestProtectedExportDoesNotCountLockedOrMalformedRequestsAsPasswordFailures(t *testing.T) {
	h := newProtectedExportHarness(t)
	target := "/api/notebooks/" + h.vaultID + "/export/reauth"
	if err := h.manager.LockVault(h.vaultID, encryption.LockReasonManual); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 4; attempt++ {
		if response := exportCredentialRequest(t, h, target, exportVaultPassword); response.Code != http.StatusLocked {
			t.Fatalf("locked request: %d %s", response.Code, response.Body.String())
		}
		if response := exportCredentialRequest(t, h, target, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("malformed request: %d %s", response.Code, response.Body.String())
		}
	}
	if err := h.manager.Unlock(h.vaultID, h.sessionHash[:], h.key); err != nil {
		t.Fatal(err)
	}
	if response := exportCredentialRequest(t, h, target, exportVaultPassword); response.Code != http.StatusOK {
		t.Fatalf("non-credential failures consumed password budget: %d %s", response.Code, response.Body.String())
	}
}

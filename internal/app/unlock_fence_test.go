package app

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestPendingHTTPUnlockCannotOvertakeLockOrRevocation(t *testing.T) {
	for _, action := range []string{"manual", "manual-already-locked", "logout", "revoke-current", "revoke-all", "revoke-before-first-grant", "revoke-other-session"} {
		t.Run(action, func(t *testing.T) {
			ready, resume := make(chan struct{}), make(chan struct{})
			harness := newExportHarnessWithHooks(t, true, nil, func() { close(ready); <-resume })
			if action == "manual-already-locked" || action == "revoke-before-first-grant" {
				if err := harness.manager.LockVault(harness.vaultID, encryption.LockReasonManual); err != nil {
					t.Fatal(err)
				}
			}
			request := func(method, target, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, target, bytes.NewBufferString(body))
				addAuthRequestContext(r, cookie, csrf)
				w := httptest.NewRecorder()
				harness.handler.ServeHTTP(w, r)
				return w
			}
			adminCookie, adminCSRF := harness.cookie, harness.csrf
			if action == "revoke-other-session" {
				login := request(http.MethodPost, "/api/auth/login", `{"password":"a sufficiently long owner password"}`, nil, "")
				if login.Code != http.StatusOK {
					t.Fatalf("admin login: %d %s", login.Code, login.Body.String())
				}
				adminCookie, adminCSRF = authRequestContextFromResponse(t, login)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- request(http.MethodPost, "/api/encryption/unlock", `{"password":"`+exportVaultPassword+`"}`, harness.cookie, harness.csrf)
			}()
			select {
			case <-ready:
			case <-time.After(15 * time.Second):
				close(resume)
				t.Fatal("unlock did not reach paused grant")
			}
			var response *httptest.ResponseRecorder
			switch action {
			case "manual", "manual-already-locked":
				response = request(http.MethodPost, "/api/encryption/lock", `{}`, harness.cookie, harness.csrf)
			case "logout":
				response = request(http.MethodPost, "/api/auth/logout", `{}`, harness.cookie, harness.csrf)
			case "revoke-current":
				response = request(http.MethodDelete, "/api/auth/session", "", harness.cookie, harness.csrf)
			case "revoke-all":
				response = request(http.MethodDelete, "/api/auth/sessions", "", harness.cookie, harness.csrf)
			case "revoke-before-first-grant":
				if err := harness.manager.RevokeSessionEverywhere(harness.sessionHash[:]); err != nil {
					close(resume)
					t.Fatal(err)
				}
			case "revoke-other-session":
				response = request(http.MethodDelete, "/api/auth/sessions/"+base64.RawURLEncoding.EncodeToString(harness.sessionHash[:]), "", adminCookie, adminCSRF)
			}
			if response != nil && response.Code != http.StatusOK {
				close(resume)
				t.Fatalf("transition: %d %s", response.Code, response.Body.String())
			}
			close(resume)
			select {
			case response := <-done:
				if response.Code == http.StatusOK {
					t.Errorf("R4: paused unlock overtook completed %s", action)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("unlock stalled after transition")
			}
			if harness.manager.IsUnlocked(harness.vaultID, harness.sessionHash[:]) {
				t.Fatal("R4: stale request restored a key grant")
			}
		})
	}
}

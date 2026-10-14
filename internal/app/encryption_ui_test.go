package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestEncryptionStatusAndUnlockEndpointsAreSessionScoped(t *testing.T) {
	harness := newProtectedExportHarness(t)
	if err := harness.manager.LockVault(harness.vaultID, encryption.LockReasonManual); err != nil {
		t.Fatal(err)
	}
	request := func(method, target string, body any, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		var encoded *bytes.Reader
		if body == nil {
			encoded = bytes.NewReader(nil)
		} else {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			encoded = bytes.NewReader(data)
		}
		req := httptest.NewRequest(method, target, encoded)
		if authenticated {
			addAuthRequestContext(req, harness.cookie, harness.csrf)
		}
		response := httptest.NewRecorder()
		harness.handler.ServeHTTP(response, req)
		return response
	}

	unauthenticated := request(http.MethodGet, "/api/encryption/status", nil, false)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status returned %d: %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	status := request(http.MethodGet, "/api/encryption/status", nil, true)
	if status.Code != http.StatusOK || status.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("protected vault status returned %d: %s", status.Code, status.Body.String())
	}
	var state struct {
		VaultID                string `json:"vaultId"`
		Protected              bool   `json:"protected"`
		Locked                 bool   `json:"locked"`
		ActivationEnabled      bool   `json:"activationEnabled"`
		RecoveryCodeConfigured bool   `json:"recoveryCodeConfigured"`
		UnlockPasswordMode     string `json:"unlockPasswordMode"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.VaultID != harness.vaultID || !state.Protected || !state.Locked || state.ActivationEnabled || state.RecoveryCodeConfigured || state.UnlockPasswordMode != "login" {
		t.Fatalf("unexpected protected vault status: %#v", state)
	}

	wrongPassword := request(http.MethodPost, "/api/encryption/unlock", map[string]string{"password": "wrong vault password"}, true)
	if wrongPassword.Code != http.StatusUnauthorized || !strings.Contains(wrongPassword.Body.String(), `"code":"invalid_credentials"`) || strings.Contains(wrongPassword.Body.String(), "wrong vault password") {
		t.Fatalf("wrong unlock password response was unsafe: %d %s", wrongPassword.Code, wrongPassword.Body.String())
	}
	if harness.manager.IsUnlocked(harness.vaultID, harness.sessionHash[:]) {
		t.Fatal("wrong password unlocked the vault")
	}

	unlocked := request(http.MethodPost, "/api/encryption/unlock", map[string]string{"password": exportVaultPassword}, true)
	if unlocked.Code != http.StatusOK || !strings.Contains(unlocked.Body.String(), `"unlocked":true`) || !harness.manager.IsUnlocked(harness.vaultID, harness.sessionHash[:]) {
		t.Fatalf("correct password did not unlock the vault: %d %s", unlocked.Code, unlocked.Body.String())
	}
	var display struct {
		IdleRemainingMs int64  `json:"idleRemainingMs"`
		VaultID         string `json:"vaultId"`
		Generation      uint64 `json:"generation"`
	}
	if err := json.Unmarshal(unlocked.Body.Bytes(), &display); err != nil || display.IdleRemainingMs <= 0 || display.IdleRemainingMs > 900_000 {
		t.Fatalf("missing unlock display deadline: %#v %v", display, err)
	}
	activity := request(http.MethodPost, "/api/encryption/activity?vaultId="+harness.vaultID, nil, true)
	if err := json.Unmarshal(activity.Body.Bytes(), &display); err != nil || activity.Code != http.StatusOK || display.VaultID != harness.vaultID || display.Generation == 0 || display.IdleRemainingMs <= 0 || display.IdleRemainingMs > 900_000 || activity.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("activity display binding: %#v %v %s", display, err, activity.Body.String())
	}
	tree := request(http.MethodGet, "/api/repository/tree", nil, true)
	if tree.Code != http.StatusOK || !strings.Contains(tree.Body.String(), `"protected":true`) {
		t.Fatalf("unlock did not expose protected tree state: %d %s", tree.Code, tree.Body.String())
	}

	locked := request(http.MethodPost, "/api/encryption/lock", map[string]bool{}, true)
	if locked.Code != http.StatusOK || !strings.Contains(locked.Body.String(), `"locked":true`) || harness.manager.IsUnlocked(harness.vaultID, harness.sessionHash[:]) {
		t.Fatalf("manual lock failed: %d %s", locked.Code, locked.Body.String())
	}
	status = request(http.MethodGet, "/api/encryption/status", nil, true)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"locked":true`) {
		t.Fatalf("status did not follow the manual lock: %d %s", status.Code, status.Body.String())
	}
	if err := json.Unmarshal(status.Body.Bytes(), &display); err != nil || display.IdleRemainingMs != 0 {
		t.Fatalf("locked status exposed idle grant: %#v %v", display, err)
	}
}

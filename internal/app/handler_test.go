package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/pquerna/otp/totp"
)

func TestHealth(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != `{"status":"ok","version":"dev"}` {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestSecureOwnerSetupAPI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer authService.Close()
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), authService)
	if err != nil {
		t.Fatal(err)
	}

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"setupRequired":true`) || strings.Contains(status.Body.String(), "owner") {
		t.Fatalf("unexpected public auth status: %d %s", status.Code, status.Body.String())
	}
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil))
	if blocked.Code != http.StatusForbidden || !strings.Contains(blocked.Body.String(), `"code":"setup_required"`) {
		t.Fatalf("notebook API was exposed before owner setup: %d %s", blocked.Code, blocked.Body.String())
	}

	invalidBody := strings.NewReader(`{"bootstrapToken":"wrong","password":"a sufficiently long password"}`)
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/api/auth/setup", invalidBody))
	if invalid.Code != http.StatusUnauthorized || strings.Contains(invalid.Body.String(), "wrong") {
		t.Fatalf("setup leaked or accepted invalid authorization: %d %s", invalid.Code, invalid.Body.String())
	}

	token, err := authService.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(map[string]string{"bootstrapToken": token.Value, "password": "a sufficiently long password"})
	if err != nil {
		t.Fatal(err)
	}
	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(requestBody)))
	if setup.Code != http.StatusOK || !strings.Contains(setup.Body.String(), `"setupCompleted":true`) {
		t.Fatalf("owner setup failed: %d %s", setup.Code, setup.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range setup.Result().Cookies() {
		if strings.Contains(cookie.Name, "varianotes_session") {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.Path != "/api" || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("setup did not issue a confined HttpOnly SameSite session cookie: %#v", sessionCookie)
	}

	after := httptest.NewRecorder()
	handler.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"setupRequired":false`) {
		t.Fatalf("setup status did not change: %d %s", after.Code, after.Body.String())
	}
	unlocked := httptest.NewRecorder()
	unlockedRequest := httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil)
	unlockedRequest.AddCookie(sessionCookie)
	handler.ServeHTTP(unlocked, unlockedRequest)
	if unlocked.Code == http.StatusForbidden || unlocked.Code == http.StatusUnauthorized {
		t.Fatalf("setup boundary remained locked after successful setup: %d %s", unlocked.Code, unlocked.Body.String())
	}
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil))
	if unauthenticated.Code != http.StatusUnauthorized || strings.TrimSpace(unauthenticated.Body.String()) != `{"code":"authentication_required","error":"authentication required"}` {
		t.Fatalf("protected API did not return stable JSON 401: %d %s", unauthenticated.Code, unauthenticated.Body.String())
	}

	_, currentCSRF := authRequestContext(t, handler, sessionCookie)
	repeated := httptest.NewRecorder()
	repeatedRequest := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(requestBody))
	addAuthRequestContext(repeatedRequest, sessionCookie, currentCSRF)
	handler.ServeHTTP(repeated, repeatedRequest)
	if repeated.Code != http.StatusConflict {
		t.Fatalf("repeated setup was not rejected: %d %s", repeated.Code, repeated.Body.String())
	}

	oversized := httptest.NewRecorder()
	oversizedBody := strings.NewReader(`{"bootstrapToken":"` + strings.Repeat("x", 5000) + `","password":"a sufficiently long password"}`)
	oversizedRequest := httptest.NewRequest(http.MethodPost, "/api/auth/setup", oversizedBody)
	addAuthRequestContext(oversizedRequest, sessionCookie, currentCSRF)
	handler.ServeHTTP(oversized, oversizedRequest)
	if oversized.Code != http.StatusBadRequest {
		t.Fatalf("oversized auth request was accepted: %d", oversized.Code)
	}
}

func TestLoginSessionPersistsAndCanBeRevoked(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metadataPath := filepath.Join(t.TempDir(), "auth.db")
	config := auth.Config{Mode: auth.ModeLocal, MetadataPath: metadataPath}
	password := "a sufficiently long password"

	service, err := auth.Open(t.Context(), config, logger)
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, password); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), service)
	if err != nil {
		t.Fatal(err)
	}

	wrong := httptest.NewRecorder()
	wrongRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong password","rememberDevice":false}`))
	handler.ServeHTTP(wrong, wrongRequest)
	if wrong.Code != http.StatusUnauthorized || strings.Contains(wrong.Body.String(), "wrong password") {
		t.Fatalf("invalid login response: %d %s", wrong.Code, wrong.Body.String())
	}

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"a sufficiently long password","rememberDevice":true}`))
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	var cookie *http.Cookie
	for _, candidate := range login.Result().Cookies() {
		if strings.Contains(candidate.Name, "varianotes_session") {
			cookie = candidate
		}
	}
	if cookie == nil || cookie.MaxAge <= 0 {
		t.Fatalf("remembered login did not issue a persistent cookie: %#v", cookie)
	}
	_, firstCSRF := authRequestContext(t, handler, cookie)
	reauthRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"a sufficiently long password","rememberDevice":true}`))
	addAuthRequestContext(reauthRequest, cookie, firstCSRF)
	reauth := httptest.NewRecorder()
	handler.ServeHTTP(reauth, reauthRequest)
	if reauth.Code != http.StatusOK {
		t.Fatalf("authenticated re-login failed: %d %s", reauth.Code, reauth.Body.String())
	}
	newCookie, newCSRF := authRequestContextFromResponse(t, reauth)
	if newCookie == nil || newCookie.Value == cookie.Value || newCSRF == firstCSRF {
		t.Fatal("authentication-level change did not rotate session and CSRF tokens")
	}
	cookie = newCookie
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	service, err = auth.Open(t.Context(), config, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	handler, err = NewHandlerWithAuth(logger, t.TempDir(), service)
	if err != nil {
		t.Fatal(err)
	}
	protectedRequest := httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil)
	protectedRequest.AddCookie(cookie)
	protected := httptest.NewRecorder()
	handler.ServeHTTP(protected, protectedRequest)
	if protected.Code == http.StatusUnauthorized {
		t.Fatalf("session did not survive service restart: %s", protected.Body.String())
	}
	missingCSRFRequest := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)
	missingCSRFRequest.AddCookie(cookie)
	missingCSRF := httptest.NewRecorder()
	handler.ServeHTTP(missingCSRF, missingCSRFRequest)
	if missingCSRF.Code != http.StatusForbidden || !strings.Contains(missingCSRF.Body.String(), `"code":"csrf_invalid"`) {
		t.Fatalf("state-changing authenticated request bypassed CSRF protection: %d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	missingRecoveryCSRFRequest := httptest.NewRequest(http.MethodPost, "/api/encryption/recovery-code", strings.NewReader(`{}`))
	missingRecoveryCSRFRequest.AddCookie(cookie)
	missingRecoveryCSRF := httptest.NewRecorder()
	handler.ServeHTTP(missingRecoveryCSRF, missingRecoveryCSRFRequest)
	if missingRecoveryCSRF.Code != http.StatusForbidden || !strings.Contains(missingRecoveryCSRF.Body.String(), `"code":"csrf_invalid"`) {
		t.Fatalf("recovery-code setup bypassed CSRF protection: %d %s", missingRecoveryCSRF.Code, missingRecoveryCSRF.Body.String())
	}

	_, authenticatedCSRF := authRequestContext(t, handler, cookie)
	revokeRequest := httptest.NewRequest(http.MethodDelete, "/api/auth/sessions", nil)
	revokeRequest.AddCookie(cookie)
	revokeRequest.Header.Set("X-CSRF-Token", authenticatedCSRF)
	revoked := httptest.NewRecorder()
	handler.ServeHTTP(revoked, revokeRequest)
	if revoked.Code != http.StatusOK {
		t.Fatalf("session revocation failed: %d %s", revoked.Code, revoked.Body.String())
	}
	requestAfterRevoke := httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil)
	requestAfterRevoke.AddCookie(cookie)
	afterRevoke := httptest.NewRecorder()
	handler.ServeHTTP(afterRevoke, requestAfterRevoke)
	if afterRevoke.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session remained authorized: %d", afterRevoke.Code)
	}
}

func TestEncryptionLockAPIAndLogoutRevokeInMemoryGrants(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewSessions(service, auth.DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	manager := encryption.NewKeyManager()
	defer manager.Close()
	handler, err := newHandlerWithKeyManager(logger, t.TempDir(), service, sessions, manager)
	if err != nil {
		t.Fatal(err)
	}
	setupBody, err := json.Marshal(map[string]string{"bootstrapToken": token.Value, "password": "a sufficiently long password"})
	if err != nil {
		t.Fatal(err)
	}
	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(setupBody)))
	if setup.Code != http.StatusOK {
		t.Fatalf("owner setup failed: %d %s", setup.Code, setup.Body.String())
	}
	cookie, csrf := authRequestContextFromResponse(t, setup)
	if cookie == nil {
		t.Fatal("owner setup did not issue a session cookie")
	}
	sessionHash := sha256.Sum256([]byte(cookie.Value))
	events, cancel, err := manager.Subscribe("local", sessionHash[:])
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	initialEvent := <-events
	if !initialEvent.Locked {
		t.Fatalf("initial encryption event = %#v, want locked", initialEvent)
	}
	streamContext, stopStream := context.WithCancel(t.Context())
	streamRequest := httptest.NewRequest(http.MethodGet, "/api/encryption/lock-events?vaultId=local", nil).WithContext(streamContext)
	streamRequest.AddCookie(cookie)
	streamWriter := newSynchronizedEventWriter()
	streamDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(streamWriter, streamRequest)
		close(streamDone)
	}()
	defer func() {
		stopStream()
		select {
		case <-streamDone:
		case <-time.After(time.Second):
			t.Error("lock event stream did not stop after cancellation")
		}
	}()
	waitForStreamContent(t, streamWriter, `"locked":true`)
	key := bytes.Repeat([]byte{12}, encryption.VaultKeySize)
	if err := manager.Unlock("local", sessionHash[:], key); err != nil {
		t.Fatal(err)
	}
	if unlockEvent := <-events; unlockEvent.Locked {
		t.Fatalf("unlock event = %#v, want unlocked", unlockEvent)
	}
	waitForStreamContent(t, streamWriter, `"locked":false`)

	lockRequest := httptest.NewRequest(http.MethodPost, "/api/encryption/lock", nil)
	addAuthRequestContext(lockRequest, cookie, csrf)
	lockResponse := httptest.NewRecorder()
	handler.ServeHTTP(lockResponse, lockRequest)
	if lockResponse.Code != http.StatusOK || !strings.Contains(lockResponse.Body.String(), `"locked":true`) {
		t.Fatalf("manual lock failed: %d %s", lockResponse.Code, lockResponse.Body.String())
	}
	if manager.IsUnlocked("local", sessionHash[:]) {
		t.Fatal("manual lock endpoint retained an encryption grant")
	}
	if event := <-events; !event.Locked || event.Reason != encryption.LockReasonManual {
		t.Fatalf("manual lock event = %#v", event)
	}
	waitForStreamContent(t, streamWriter, `"reason":"manual"`)
	activityRequest := httptest.NewRequest(http.MethodPost, "/api/encryption/activity?vaultId=local", nil)
	addAuthRequestContext(activityRequest, cookie, csrf)
	activityResponse := httptest.NewRecorder()
	handler.ServeHTTP(activityResponse, activityRequest)
	if activityResponse.Code != http.StatusLocked {
		t.Fatalf("activity request while locked = %d %s, want 423", activityResponse.Code, activityResponse.Body.String())
	}
	stopStream()
	select {
	case <-streamDone:
	case <-time.After(time.Second):
		t.Fatal("lock event stream did not stop after cancellation")
	}

	if err := manager.Unlock("local", sessionHash[:], key); err != nil {
		t.Fatal(err)
	}
	_ = <-events
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"a sufficiently long password","rememberDevice":false}`))
	addAuthRequestContext(loginRequest, cookie, csrf)
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("session renewal failed: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	if event := <-events; !event.Locked || event.Reason != encryption.LockReasonSession {
		t.Fatalf("session renewal lock event = %#v", event)
	}
	newCookie, newCSRF := authRequestContextFromResponse(t, loginResponse)
	if newCookie == nil || newCookie.Value == cookie.Value {
		t.Fatal("successful login did not rotate the authenticated session")
	}
	newSessionHash := sha256.Sum256([]byte(newCookie.Value))
	if manager.IsUnlocked("local", sessionHash[:]) || manager.IsUnlocked("local", newSessionHash[:]) {
		t.Fatal("session renewal carried an encryption grant into the new session")
	}
	newStreamContext, stopNewStream := context.WithCancel(t.Context())
	newStreamRequest := httptest.NewRequest(http.MethodGet, "/api/encryption/lock-events?vaultId=local", nil).WithContext(newStreamContext)
	newStreamRequest.AddCookie(newCookie)
	newStreamWriter := newSynchronizedEventWriter()
	newStreamDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(newStreamWriter, newStreamRequest)
		close(newStreamDone)
	}()
	defer func() {
		stopNewStream()
		select {
		case <-newStreamDone:
		case <-time.After(time.Second):
			t.Error("new lock event stream did not stop after cancellation")
		}
	}()
	waitForStreamContent(t, newStreamWriter, `"locked":true`)
	if err := manager.Unlock("local", newSessionHash[:], key); err != nil {
		t.Fatal(err)
	}
	waitForStreamContent(t, newStreamWriter, `"locked":false`)
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	addAuthRequestContext(logoutRequest, newCookie, newCSRF)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout failed: %d %s", logoutResponse.Code, logoutResponse.Body.String())
	}
	if manager.IsUnlocked("local", newSessionHash[:]) {
		t.Fatal("logout endpoint retained an encryption grant")
	}
	if event := <-events; !event.Locked || event.Reason != encryption.LockReasonLogout {
		t.Fatalf("logout lock event = %#v", event)
	}
	waitForStreamContent(t, newStreamWriter, `"reason":"logout"`)
	select {
	case <-newStreamDone:
	case <-time.After(time.Second):
		t.Fatal("logout did not close the lock event stream")
	}
}

func TestSecurityAdministrationAPI(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	bootstrap, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const oldPassword = "a sufficiently long password"
	const newPassword = "a different secure password"
	if err := service.CompleteSetup(t.Context(), bootstrap.Value, oldPassword); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), service)
	if err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"a sufficiently long password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	cookie, csrf := authRequestContextFromResponse(t, login)

	securityRequest := httptest.NewRequest(http.MethodGet, "/api/auth/security", nil)
	securityRequest.AddCookie(cookie)
	security := httptest.NewRecorder()
	handler.ServeHTTP(security, securityRequest)
	if security.Code != http.StatusOK || !strings.Contains(security.Body.String(), `"idleHours":168`) {
		t.Fatalf("security settings unavailable: %d %s", security.Code, security.Body.String())
	}

	settingsRequest := httptest.NewRequest(http.MethodPut, "/api/auth/security/session-settings", strings.NewReader(`{"currentPassword":"a sufficiently long password","idleHours":48,"lifetimeHours":8,"rememberDays":14}`))
	addAuthRequestContext(settingsRequest, cookie, csrf)
	settings := httptest.NewRecorder()
	handler.ServeHTTP(settings, settingsRequest)
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `"idleHours":48`) {
		t.Fatalf("session settings update failed: %d %s", settings.Code, settings.Body.String())
	}

	passwordBody, _ := json.Marshal(map[string]string{"currentPassword": oldPassword, "newPassword": newPassword})
	passwordRequest := httptest.NewRequest(http.MethodPut, "/api/auth/password", bytes.NewReader(passwordBody))
	addAuthRequestContext(passwordRequest, cookie, csrf)
	password := httptest.NewRecorder()
	handler.ServeHTTP(password, passwordRequest)
	if password.Code != http.StatusOK {
		t.Fatalf("password change failed: %d %s", password.Code, password.Body.String())
	}
	rotatedCookie, rotatedCSRF := authRequestContextFromResponse(t, password)
	if rotatedCookie == nil || rotatedCookie.Value == cookie.Value || rotatedCSRF == csrf {
		t.Fatal("password change did not rotate session credentials")
	}
	if err := service.VerifyPassword(t.Context(), oldPassword); !errors.Is(err, auth.ErrAuthentication) {
		t.Fatalf("old password still verifies: %v", err)
	}
	if err := service.VerifyPassword(t.Context(), newPassword); err != nil {
		t.Fatalf("new password does not verify: %v", err)
	}

	sessionsRequest := httptest.NewRequest(http.MethodGet, "/api/auth/sessions", nil)
	sessionsRequest.AddCookie(rotatedCookie)
	sessionList := httptest.NewRecorder()
	handler.ServeHTTP(sessionList, sessionsRequest)
	if sessionList.Code != http.StatusOK || !strings.Contains(sessionList.Body.String(), `"current":true`) {
		t.Fatalf("session list unavailable after rotation: %d %s", sessionList.Code, sessionList.Body.String())
	}
}

func TestPasswordFirstMFALoginAndRecoveryCode(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	bootstrap, _ := service.CreateBootstrapToken(t.Context())
	const password = "a sufficiently long password"
	if err := service.CompleteSetup(t.Context(), bootstrap.Value, password); err != nil {
		t.Fatal(err)
	}
	testSessionHash := make([]byte, 32)
	enrollment, err := service.BeginMFAEnrollment(t.Context(), password, "", testSessionHash)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(enrollment.Secret, time.Now().UTC())
	if err := service.ConfirmMFAEnrollment(t.Context(), code, true, testSessionHash); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), service)
	if err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"a sufficiently long password","rememberDevice":true}`)))
	if login.Code != http.StatusAccepted || !strings.Contains(login.Body.String(), `"mfaRequired":true`) {
		t.Fatalf("password step did not require MFA: %d %s", login.Code, login.Body.String())
	}
	var pendingCookie *http.Cookie
	for _, candidate := range login.Result().Cookies() {
		if strings.Contains(candidate.Name, "varianotes_session") {
			pendingCookie = candidate
		}
	}
	if pendingCookie == nil {
		t.Fatal("MFA challenge did not issue a confined pending-session cookie")
	}
	pendingStatus := httptest.NewRecorder()
	pendingStatusRequest := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	pendingStatusRequest.AddCookie(pendingCookie)
	handler.ServeHTTP(pendingStatus, pendingStatusRequest)
	if pendingStatus.Code != http.StatusOK || !strings.Contains(pendingStatus.Body.String(), `"mfaRequired":true`) {
		t.Fatalf("authentication status did not preserve pending MFA: %d %s", pendingStatus.Code, pendingStatus.Body.String())
	}

	wrongRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login/mfa", strings.NewReader(`{"code":"000000"}`))
	wrongRequest.AddCookie(pendingCookie)
	wrong := httptest.NewRecorder()
	handler.ServeHTTP(wrong, wrongRequest)
	if wrong.Code != http.StatusUnauthorized || strings.TrimSpace(wrong.Body.String()) != `{"code":"invalid_credentials","error":"authentication failed"}` {
		t.Fatalf("MFA failure leaked details: %d %s", wrong.Code, wrong.Body.String())
	}

	recoveryRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login/mfa", strings.NewReader(`{"code":"`+enrollment.RecoveryCodes[0]+`"}`))
	recoveryRequest.AddCookie(pendingCookie)
	recovery := httptest.NewRecorder()
	handler.ServeHTTP(recovery, recoveryRequest)
	if recovery.Code != http.StatusOK || !strings.Contains(recovery.Body.String(), `"authenticated":true`) {
		t.Fatalf("recovery-code login failed: %d %s", recovery.Code, recovery.Body.String())
	}
	rotatedCookie, csrf := authRequestContextFromResponse(t, recovery)
	if rotatedCookie == nil || rotatedCookie.Value == pendingCookie.Value || csrf == "" {
		t.Fatal("MFA completion did not rotate session and CSRF credentials")
	}
}

func TestAuthStatusGETDoesNotCreateSessionState(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), service)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("auth status failed: %d", response.Code)
	}
	if len(response.Result().Cookies()) != 0 {
		t.Fatalf("safe auth status GET created session cookie: %#v", response.Result().Cookies())
	}
	if strings.Contains(response.Body.String(), `"csrfToken":"`) && !strings.Contains(response.Body.String(), `"csrfToken":""`) {
		t.Fatalf("safe auth status GET created CSRF state: %s", response.Body.String())
	}
}

func TestLoginThrottleReturnsStableTemporaryLimit(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	bootstrap, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), bootstrap.Value, "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), service)
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for attempt := 1; attempt <= 4; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"incorrect password","rememberDevice":false}`))
		request.RemoteAddr = "198.51.100.40:1234"
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d: %s", attempt, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"a sufficiently long password","rememberDevice":false}`))
	request.RemoteAddr = "198.51.100.40:1234"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" || !strings.Contains(response.Body.String(), `"code":"login_throttled"`) {
		t.Fatalf("progressive throttle did not block immediate retry: %d %s", response.Code, response.Body.String())
	}
}

func authRequestContext(t *testing.T, handler http.Handler, cookie *http.Cookie) (*http.Cookie, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("auth status failed: %d %s", response.Code, response.Body.String())
	}
	resultCookie, token := authRequestContextFromResponse(t, response)
	if resultCookie == nil {
		resultCookie = cookie
	}
	return resultCookie, token
}

func authRequestContextFromResponse(t *testing.T, response *httptest.ResponseRecorder) (*http.Cookie, string) {
	t.Helper()
	var payload struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CSRFToken == "" {
		t.Fatal("auth status did not provide a CSRF token")
	}
	var cookie *http.Cookie
	for _, candidate := range response.Result().Cookies() {
		if strings.Contains(candidate.Name, "varianotes_session") {
			cookie = candidate
		}
	}
	return cookie, payload.CSRFToken
}

func addAuthRequestContext(request *http.Request, cookie *http.Cookie, token string) {
	if cookie != nil {
		request.AddCookie(cookie)
	}
	request.Header.Set("X-CSRF-Token", token)
}

type synchronizedEventWriter struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	body    bytes.Buffer
	changed chan struct{}
}

func newSynchronizedEventWriter() *synchronizedEventWriter {
	return &synchronizedEventWriter{header: make(http.Header), changed: make(chan struct{}, 1)}
}

func (w *synchronizedEventWriter) Header() http.Header { return w.header }

func (w *synchronizedEventWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
	w.signal()
}

func (w *synchronizedEventWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	count, err := w.body.Write(data)
	w.signal()
	return count, err
}

func (w *synchronizedEventWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.signal()
}

func (w *synchronizedEventWriter) signal() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

func (w *synchronizedEventWriter) snapshot() (string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String(), w.status
}

func waitForStreamContent(t *testing.T, writer *synchronizedEventWriter, content string) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		body, _ := writer.snapshot()
		if strings.Contains(body, content) {
			return
		}
		select {
		case <-writer.changed:
		case <-timer.C:
			t.Fatalf("lock event stream did not contain %q; body=%s", content, body)
		}
	}
}

func TestExplicitDisabledModeDoesNotEnterSetupGate(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService, err := auth.Open(t.Context(), auth.Config{
		Mode: auth.ModeDisabled, ModeExplicit: true, MetadataPath: filepath.Join(t.TempDir(), "auth.db"),
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer authService.Close()
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), authService)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil))
	if response.Code == http.StatusForbidden || strings.Contains(response.Body.String(), "setup_required") {
		t.Fatalf("explicit disabled mode entered the local setup gate: %d %s", response.Code, response.Body.String())
	}
}

func TestSecurityHeadersAndSameOriginProtection(t *testing.T) {
	root := t.TempDir()
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	for _, header := range []string{"Content-Security-Policy", "Referrer-Policy", "Permissions-Policy", "X-Content-Type-Options", "X-Frame-Options"} {
		if health.Header().Get(header) == "" {
			t.Errorf("security header %s is missing", header)
		}
	}
	if health.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("API cache policy is unsafe: %q", health.Header().Get("Cache-Control"))
	}

	crossSite := httptest.NewRequest(http.MethodPost, "/api/notebooks/local", nil)
	crossSite.Header.Set("Origin", "https://attacker.example")
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, crossSite)
	if blocked.Code != http.StatusForbidden {
		t.Fatalf("cross-site mutation was not rejected: %d", blocked.Code)
	}

	sameSite := httptest.NewRequest(http.MethodPost, "/api/notebooks/local", nil)
	sameSite.Host = "notes.example.test"
	sameSite.Header.Set("Origin", "http://notes.example.test")
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, sameSite)
	if allowed.Code == http.StatusForbidden {
		t.Fatal("same-origin mutation was rejected")
	}
}

func TestSameOriginProtectionTrustsForwardedSchemeOnlyFromConfiguredProxy(t *testing.T) {
	identity := auth.NewRequestIdentity([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := sameOriginProtection(identity, "", next)

	trusted := httptest.NewRequest(http.MethodPost, "http://notes.example.test/api/action", nil)
	trusted.Host = "notes.example.test"
	trusted.RemoteAddr = "10.0.0.2:8080"
	trusted.Header.Set("Origin", "https://notes.example.test")
	trusted.Header.Set("X-Forwarded-Proto", "https")
	trustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(trustedResponse, trusted)
	if trustedResponse.Code != http.StatusNoContent {
		t.Fatalf("trusted proxy scheme was rejected: %d", trustedResponse.Code)
	}

	untrusted := trusted.Clone(t.Context())
	untrusted.RemoteAddr = "198.51.100.2:8080"
	untrustedResponse := httptest.NewRecorder()
	handler.ServeHTTP(untrustedResponse, untrusted)
	if untrustedResponse.Code != http.StatusForbidden {
		t.Fatalf("untrusted forwarded scheme was accepted: %d", untrustedResponse.Code)
	}

	missingOrigin := httptest.NewRequest(http.MethodPost, "http://notes.example.test/api/action", nil)
	missingOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	missingOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingOriginResponse, missingOrigin)
	if missingOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("browser mutation without origin was accepted: %d", missingOriginResponse.Code)
	}
}

func TestUnknownAPIAndTrailingJSONDoNotFallThrough(t *testing.T) {
	root := t.TempDir()
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))
	if unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown API fell through to the SPA: %d %q", unknown.Code, unknown.Header().Get("Content-Type"))
	}

	trailing := httptest.NewRecorder()
	body := strings.NewReader(`{"path":"Note.md","type":"file"}{"extra":true}`)
	handler.ServeHTTP(trailing, httptest.NewRequest(http.MethodPost, "/api/repository/entries", body))
	if trailing.Code != http.StatusBadRequest {
		t.Fatalf("request with trailing JSON was accepted: %d", trailing.Code)
	}
}

func TestRepositoryAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Welcome.md"), []byte("# Welcome"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}

	treeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(treeRecorder, httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil))
	if treeRecorder.Code != http.StatusOK || !strings.Contains(treeRecorder.Body.String(), "Welcome.md") {
		t.Fatalf("unexpected tree response: %d %s", treeRecorder.Code, treeRecorder.Body.String())
	}

	searchRecorder := httptest.NewRecorder()
	handler.ServeHTTP(searchRecorder, httptest.NewRequest(http.MethodGet, "/api/repository/search?q=welcome", nil))
	if searchRecorder.Code != http.StatusOK || !strings.Contains(searchRecorder.Body.String(), `"type":"file"`) || !strings.Contains(searchRecorder.Body.String(), `"type":"content"`) {
		t.Fatalf("unexpected search response: %d %s", searchRecorder.Code, searchRecorder.Body.String())
	}

	fileRecorder := httptest.NewRecorder()
	handler.ServeHTTP(fileRecorder, httptest.NewRequest(http.MethodGet, "/api/repository/file?path=Welcome.md", nil))
	if fileRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected file status: %d", fileRecorder.Code)
	}
	var file struct {
		Content string `json:"content"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(fileRecorder.Body).Decode(&file); err != nil {
		t.Fatal(err)
	}
	if file.Content != "# Welcome" {
		t.Fatalf("unexpected file content: %q", file.Content)
	}

	requestBody, err := json.Marshal(map[string]string{"content": "# Updated", "version": file.Version})
	if err != nil {
		t.Fatal(err)
	}
	writeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(writeRecorder, httptest.NewRequest(http.MethodPut, "/api/repository/file?path=Welcome.md", bytes.NewReader(requestBody)))
	if writeRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected write response: %d %s", writeRecorder.Code, writeRecorder.Body.String())
	}
	written, err := os.ReadFile(filepath.Join(root, "Welcome.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "# Updated" {
		t.Fatalf("unexpected saved content: %q", written)
	}

	conflictRecorder := httptest.NewRecorder()
	handler.ServeHTTP(conflictRecorder, httptest.NewRequest(http.MethodPut, "/api/repository/file?path=Welcome.md", bytes.NewReader(requestBody)))
	if conflictRecorder.Code != http.StatusConflict {
		t.Fatalf("expected stale write conflict, got %d %s", conflictRecorder.Code, conflictRecorder.Body.String())
	}
}

func TestRepositoryAPIRequiresConfiguration(t *testing.T) {
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", recorder.Code)
	}
}

func TestFreshInstallationDoesNotRegisterDefaultNotebook(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "app", "notebooks.json")
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadataPath)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}

	notebook := httptest.NewRecorder()
	handler.ServeHTTP(notebook, httptest.NewRequest(http.MethodGet, "/api/notebook", nil))
	if notebook.Code != http.StatusOK || !strings.Contains(notebook.Body.String(), `"configured":false`) {
		t.Fatalf("unexpected notebook state: %d %s", notebook.Code, notebook.Body.String())
	}

	registry := httptest.NewRecorder()
	handler.ServeHTTP(registry, httptest.NewRequest(http.MethodGet, "/api/notebooks", nil))
	if registry.Code != http.StatusOK || !strings.Contains(registry.Body.String(), `"notebooks":[]`) {
		t.Fatalf("unexpected registry state: %d %s", registry.Code, registry.Body.String())
	}
	if _, err := os.Stat(metadataPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh installation unexpectedly created notebook metadata: %v", err)
	}
}

func TestInactiveLegacyNotebookCanBeUnregisteredWithoutDeletingFiles(t *testing.T) {
	dataRoot := t.TempDir()
	localRoot := filepath.Join(dataRoot, "repos")
	activeRoot := filepath.Join(dataRoot, "notebooks", "active")
	if err := os.MkdirAll(localRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(activeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(localRoot, "Keep.md")
	if err := os.WriteFile(marker, []byte("# Keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(dataRoot, "app", "notebooks.json")
	if err := writeNotebookRegistry(metadataPath, notebookRegistry{
		ActiveID: "active",
		Entries: []notebookRecord{
			{ID: "local", Name: "repos", LocalPath: localRoot},
			{ID: "active", Name: "Notes", LocalPath: activeRoot},
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadataPath)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}

	activeRemoval := httptest.NewRecorder()
	handler.ServeHTTP(activeRemoval, httptest.NewRequest(http.MethodDelete, "/api/notebooks/active", nil))
	if activeRemoval.Code != http.StatusConflict {
		t.Fatalf("active notebook removal was not blocked: %d %s", activeRemoval.Code, activeRemoval.Body.String())
	}

	rename := httptest.NewRecorder()
	renameRequest := httptest.NewRequest(http.MethodPatch, "/api/notebooks/local", strings.NewReader(`{"name":"Archive"}`))
	renameRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rename, renameRequest)
	if rename.Code != http.StatusOK || !strings.Contains(rename.Body.String(), `"name":"Archive"`) {
		t.Fatalf("inactive notebook rename failed: %d %s", rename.Code, rename.Body.String())
	}

	removal := httptest.NewRecorder()
	handler.ServeHTTP(removal, httptest.NewRequest(http.MethodDelete, "/api/notebooks/local", nil))
	if removal.Code != http.StatusNoContent {
		t.Fatalf("legacy notebook removal failed: %d %s", removal.Code, removal.Body.String())
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "# Keep" {
		t.Fatalf("legacy notebook files were changed: %q %v", content, err)
	}
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Entries) != 1 || registry.Entries[0].ID != "active" {
		t.Fatalf("unexpected registry after removal: %#v", registry)
	}
}

func TestOnlyActiveNotebookCanBeUnregisteredWithoutDeletingFiles(t *testing.T) {
	dataRoot := t.TempDir()
	notebookRoot := filepath.Join(dataRoot, "notebooks", "only")
	if err := os.MkdirAll(notebookRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(notebookRoot, "Keep.md")
	if err := os.WriteFile(marker, []byte("# Keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(dataRoot, "app", "notebooks.json")
	if err := registerActiveNotebook(metadataPath, notebookRecord{ID: "only", Name: "Only", LocalPath: notebookRoot}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadataPath)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}

	removal := httptest.NewRecorder()
	handler.ServeHTTP(removal, httptest.NewRequest(http.MethodDelete, "/api/notebooks/only", nil))
	if removal.Code != http.StatusNoContent {
		t.Fatalf("only notebook removal failed: %d %s", removal.Code, removal.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("safe removal changed notebook files: %v", err)
	}
	registry, err := loadNotebookRegistry(metadataPath)
	if err != nil || registry.ActiveID != "" || len(registry.Entries) != 0 {
		t.Fatalf("unexpected empty registry: %#v, %v", registry, err)
	}
	tree := httptest.NewRecorder()
	handler.ServeHTTP(tree, httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil))
	if tree.Code != http.StatusServiceUnavailable || !strings.Contains(tree.Body.String(), "repository is not configured") {
		t.Fatalf("removed notebook remained active: %d %s", tree.Code, tree.Body.String())
	}
}

func TestInactiveNotebookLocalDeletionRequiresExactNameAndConfinedWorkingTree(t *testing.T) {
	dataRoot := t.TempDir()
	notebookBase := filepath.Join(dataRoot, "notebooks")
	workingTree := filepath.Join(notebookBase, "archive")
	activeRoot := filepath.Join(notebookBase, "active")
	if err := os.MkdirAll(workingTree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(activeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(dataRoot, "app", "notebooks.json")
	if err := writeNotebookRegistry(metadataPath, notebookRegistry{ActiveID: "active", Entries: []notebookRecord{{ID: "archive", Name: "Archive", LocalPath: workingTree}, {ID: "active", Name: "Active", LocalPath: activeRoot}}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadataPath)
	t.Setenv("VARIANOTES_NOTEBOOKS_DIR", notebookBase)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}

	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(http.MethodDelete, "/api/notebooks/archive?deleteLocal=true", strings.NewReader(`{"confirmation":"wrong"}`))
	deniedRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(denied, deniedRequest)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("weak confirmation accepted: %d %s", denied.Code, denied.Body.String())
	}
	if _, err := os.Stat(workingTree); err != nil {
		t.Fatalf("working tree changed after rejected confirmation: %v", err)
	}

	deleted := httptest.NewRecorder()
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/notebooks/archive?deleteLocal=true", strings.NewReader(`{"confirmation":"Archive"}`))
	deleteRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("confirmed local deletion failed: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(workingTree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("working tree still exists: %v", err)
	}
}

func TestRepositoryMutationAPI(t *testing.T) {
	root := t.TempDir()
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}

	requestJSON := func(method, target string, body map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, target, bytes.NewReader(encoded)))
		return recorder
	}

	createdFolder := requestJSON(http.MethodPost, "/api/repository/entries", map[string]string{"path": "Folder", "type": "directory"})
	if createdFolder.Code != http.StatusCreated {
		t.Fatalf("create folder failed: %d %s", createdFolder.Code, createdFolder.Body.String())
	}
	createdNote := requestJSON(http.MethodPost, "/api/repository/entries", map[string]string{"path": "Folder/Note.md", "type": "file"})
	if createdNote.Code != http.StatusCreated {
		t.Fatalf("create note failed: %d %s", createdNote.Code, createdNote.Body.String())
	}
	moved := requestJSON(http.MethodPost, "/api/repository/move", map[string]string{"source": "Folder/Note.md", "target": "Moved.md"})
	if moved.Code != http.StatusOK {
		t.Fatalf("move note failed: %d %s", moved.Code, moved.Body.String())
	}

	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/repository/entry?path=Moved.md", nil))
	var trashed struct {
		ID string `json:"id"`
	}
	if deleted.Code != http.StatusOK || json.NewDecoder(deleted.Body).Decode(&trashed) != nil || trashed.ID == "" {
		t.Fatalf("delete note failed: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "Moved.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted note still exists: %v", err)
	}
	trash := httptest.NewRecorder()
	handler.ServeHTTP(trash, httptest.NewRequest(http.MethodGet, "/api/repository/trash", nil))
	if trash.Code != http.StatusOK || !strings.Contains(trash.Body.String(), `"originalPath":"Moved.md"`) {
		t.Fatalf("trash list failed: %d %s", trash.Code, trash.Body.String())
	}
	restored := httptest.NewRecorder()
	handler.ServeHTTP(restored, httptest.NewRequest(http.MethodPost, "/api/repository/trash/"+trashed.ID+"/restore", nil))
	if restored.Code != http.StatusOK {
		t.Fatalf("restore note failed: %d %s", restored.Code, restored.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "Moved.md")); err != nil {
		t.Fatalf("restored note is missing: %v", err)
	}

	deletedAgain := httptest.NewRecorder()
	handler.ServeHTTP(deletedAgain, httptest.NewRequest(http.MethodDelete, "/api/repository/entry?path=Moved.md", nil))
	var discarded struct {
		ID string `json:"id"`
	}
	if deletedAgain.Code != http.StatusOK || json.NewDecoder(deletedAgain.Body).Decode(&discarded) != nil {
		t.Fatalf("second trash operation failed: %d %s", deletedAgain.Code, deletedAgain.Body.String())
	}
	permanent := httptest.NewRecorder()
	handler.ServeHTTP(permanent, httptest.NewRequest(http.MethodDelete, "/api/repository/trash/"+discarded.ID, nil))
	if permanent.Code != http.StatusOK {
		t.Fatalf("permanent deletion failed: %d %s", permanent.Code, permanent.Body.String())
	}
}

func TestRepositoryLinkAPIRequiresAndAppliesReviewedMoveRewrites(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "A.md"), []byte("[B](B.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "B.md"), []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}
	request := func(target string, body map[string]string) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, target, bytes.NewReader(encoded)))
		return recorder
	}

	blocked := request("/api/repository/move", map[string]string{"source": "B.md", "target": "Moved.md"})
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "Review the internal note-link updates") {
		t.Fatalf("unreviewed link rewrite was not blocked: %d %s", blocked.Code, blocked.Body.String())
	}
	previewResponse := request("/api/repository/move/preview", map[string]string{"source": "B.md", "target": "Moved.md"})
	var preview struct {
		Token    string `json:"token"`
		Rewrites []struct {
			Before string `json:"before"`
			After  string `json:"after"`
		} `json:"rewrites"`
	}
	if previewResponse.Code != http.StatusOK || json.NewDecoder(previewResponse.Body).Decode(&preview) != nil || len(preview.Rewrites) != 1 || preview.Rewrites[0].After != "Moved.md" {
		t.Fatalf("unexpected link rewrite preview: %d %s", previewResponse.Code, previewResponse.Body.String())
	}
	moved := request("/api/repository/move", map[string]string{"source": "B.md", "target": "Moved.md", "rewriteToken": preview.Token})
	if moved.Code != http.StatusOK {
		t.Fatalf("reviewed move failed: %d %s", moved.Code, moved.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(root, "A.md"))
	if err != nil || string(content) != "[B](Moved.md)\n" {
		t.Fatalf("inbound link was not updated: %q %v", content, err)
	}
	links := httptest.NewRecorder()
	handler.ServeHTTP(links, httptest.NewRequest(http.MethodGet, "/api/repository/links?path=A.md", nil))
	if links.Code != http.StatusOK || !strings.Contains(links.Body.String(), `"targetPath":"Moved.md"`) || !strings.Contains(links.Body.String(), `"exists":true`) {
		t.Fatalf("link inventory failed: %d %s", links.Code, links.Body.String())
	}
}

func TestRepositoryAssetAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Note.md"), []byte("# Note"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "screenshot.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/repository/assets?note=Note.md", &body)
	uploadRequest.Header.Set("Content-Type", writer.FormDataContentType())
	uploadRecorder := httptest.NewRecorder()
	handler.ServeHTTP(uploadRecorder, uploadRequest)
	if uploadRecorder.Code != http.StatusCreated {
		t.Fatalf("unexpected upload response: %d %s", uploadRecorder.Code, uploadRecorder.Body.String())
	}
	var uploaded struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(uploadRecorder.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}

	assetRecorder := httptest.NewRecorder()
	assetURL := "/api/repository/asset?note=Note.md&path=" + url.QueryEscape(uploaded.Path)
	handler.ServeHTTP(assetRecorder, httptest.NewRequest(http.MethodGet, assetURL, nil))
	if assetRecorder.Code != http.StatusOK || assetRecorder.Header().Get("Content-Type") != "image/png" || !bytes.Equal(assetRecorder.Body.Bytes(), png) {
		t.Fatalf("unexpected asset response: %d %s", assetRecorder.Code, assetRecorder.Body.String())
	}
}

func TestAssetMultipartReaderDoesNotCreatePlaintextTempFiles(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	content := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x5a}, 2<<20)...)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "large.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/repository/assets?note=Note.md", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	parsed, err := readAssetUpload(httptest.NewRecorder(), request)
	if err != nil || !bytes.Equal(parsed, content) {
		t.Fatalf("read bounded multipart upload: bytes=%d err=%v", len(parsed), err)
	}
	clear(parsed)
	entries, err := os.ReadDir(temporary)
	if err != nil || len(entries) != 0 {
		t.Fatalf("multipart reader left plaintext temporary files: entries=%v err=%v", entries, err)
	}
}

func TestRepositoryAssetCleanupAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Note.md"), []byte("# Note"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Note.assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Note.assets", "unused.png"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}

	scan := httptest.NewRecorder()
	handler.ServeHTTP(scan, httptest.NewRequest(http.MethodGet, "/api/repository/assets/unreferenced", nil))
	if scan.Code != http.StatusOK || !strings.Contains(scan.Body.String(), "Note.assets/unused.png") {
		t.Fatalf("unexpected cleanup scan: %d %s", scan.Code, scan.Body.String())
	}

	body := bytes.NewBufferString(`{"paths":["Note.assets/unused.png"]}`)
	cleanup := httptest.NewRecorder()
	handler.ServeHTTP(cleanup, httptest.NewRequest(http.MethodPost, "/api/repository/assets/cleanup", body))
	if cleanup.Code != http.StatusOK || !strings.Contains(cleanup.Body.String(), "Note.assets/unused.png") {
		t.Fatalf("unexpected cleanup response: %d %s", cleanup.Code, cleanup.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "Note.assets", "unused.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup did not delete asset: %v", err)
	}
}

func TestProtectedNoteAPIEncryptsFirstCreateAndPreservesConflictDraft(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	metadataRoot := t.TempDir()
	registryPath := filepath.Join(metadataRoot, "app", "notebooks.json")
	vaultID := "11111111111111111111111111111111"
	if err := os.Mkdir(filepath.Join(root, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Public.md"), []byte("# Public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNotebookRegistry(registryPath, notebookRegistry{
		ActiveID: vaultID,
		Entries:  []notebookRecord{{ID: vaultID, Name: "Protected", LocalPath: root}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", registryPath)
	key := bytes.Repeat([]byte{0x39}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	catalog.FolderRules = []encryption.FolderProtectionRule{{Path: "private"}}
	metadataDirectory := vaultEncryptionDirectory(registryPath, vaultID)
	catalogStore, err := encryption.NewCatalogStore(filepath.Join(metadataDirectory, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalogStore.Save(catalog, key); err != nil {
		t.Fatal(err)
	}
	envelope, err := encryption.EncryptVaultKey("a sufficiently long password", key)
	if err != nil {
		t.Fatal(err)
	}
	envelopeStore, err := encryption.NewVaultKeyEnvelopeStore(filepath.Join(metadataDirectory, "key-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := envelopeStore.Save(envelope); err != nil {
		t.Fatal(err)
	}
	authService, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(metadataRoot, "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer authService.Close()
	sessions, err := auth.NewSessions(authService, auth.DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	manager := encryption.NewKeyManager()
	defer manager.Close()
	handler, err := newHandlerWithKeyManager(logger, "", authService, sessions, manager)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := authService.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	setupBody, _ := json.Marshal(map[string]string{"bootstrapToken": bootstrap.Value, "password": "a sufficiently long password"})
	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(setupBody)))
	if setup.Code != http.StatusOK {
		t.Fatalf("owner setup failed: %d %s", setup.Code, setup.Body.String())
	}
	cookie, csrf := authRequestContextFromResponse(t, setup)
	if cookie == nil {
		t.Fatal("owner setup did not issue a session")
	}
	sessionHash := sha256.Sum256([]byte(cookie.Value))
	if err := manager.Unlock(vaultID, sessionHash[:], key); err != nil {
		t.Fatal(err)
	}
	requestJSON := func(method, target string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
		addAuthRequestContext(request, cookie, csrf)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	created := requestJSON(http.MethodPost, "/api/repository/entries", map[string]string{"path": "private/Note.md", "type": "file"})
	if created.Code != http.StatusCreated {
		t.Fatalf("protected note create failed: %d %s", created.Code, created.Body.String())
	}
	plaintextPath := filepath.Join(root, "private", "Note.md")
	ciphertextPath := plaintextPath + ".age"
	if _, err := os.Stat(plaintextPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected note created a plaintext file: %v", err)
	}
	ciphertext, err := os.ReadFile(ciphertextPath)
	if err != nil || bytes.Contains(ciphertext, []byte("# Note")) {
		t.Fatalf("first note bytes are not ciphertext: err=%v", err)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x4a}, 32)...)
	var uploadBody bytes.Buffer
	writer := multipart.NewWriter(&uploadBody)
	part, err := writer.CreateFormFile("file", "shared.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(png); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	assetUpload := httptest.NewRequest(http.MethodPost, "/api/repository/assets?note=private%2FNote.md", &uploadBody)
	assetUpload.Header.Set("Content-Type", writer.FormDataContentType())
	addAuthRequestContext(assetUpload, cookie, csrf)
	assetUploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetUploadResponse, assetUpload)
	if assetUploadResponse.Code != http.StatusCreated {
		t.Fatalf("protected asset upload failed: %d %s", assetUploadResponse.Code, assetUploadResponse.Body.String())
	}
	var uploaded struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(assetUploadResponse.Body.Bytes(), &uploaded); err != nil || uploaded.Path == "" {
		t.Fatalf("protected asset path missing: %#v err=%v", uploaded, err)
	}
	assetPlainPath := filepath.Join(root, "private", filepath.FromSlash(uploaded.Path))
	assetCiphertextPath := assetPlainPath + ".age"
	if _, err := os.Stat(assetPlainPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected attachment appeared in plaintext: %v", err)
	}
	assetCiphertext, err := os.ReadFile(assetCiphertextPath)
	if err != nil || bytes.Contains(assetCiphertext, png) {
		t.Fatalf("protected attachment was not stored as ciphertext: err=%v", err)
	}
	assetCatalog, err := catalogStore.Load(key, catalog.VaultID)
	if err != nil {
		t.Fatal(err)
	}
	var assetObject encryption.CatalogObject
	found := false
	for _, object := range assetCatalog.Objects {
		if object.LogicalPath == "private/"+uploaded.Path {
			assetObject, found = object, true
			break
		}
	}
	if !found || assetObject.Kind != encryption.CatalogObjectAsset || !assetObject.Protected {
		t.Fatalf("protected attachment missing from authenticated catalog: %#v found=%v", assetObject, found)
	}
	presentationSet := requestJSON(http.MethodPut, "/api/repository/image-presentation", map[string]any{
		"note": "private/Note.md", "image": uploaded.Path, "size": "medium",
	})
	if presentationSet.Code != http.StatusOK {
		t.Fatalf("protected image presentation update failed: %d %s", presentationSet.Code, presentationSet.Body.String())
	}
	presentationList := httptest.NewRecorder()
	presentationListRequest := httptest.NewRequest(http.MethodGet, "/api/repository/image-presentations?note=private%2FNote.md", nil)
	presentationListRequest.AddCookie(cookie)
	handler.ServeHTTP(presentationList, presentationListRequest)
	if presentationList.Code != http.StatusOK || !strings.Contains(presentationList.Body.String(), `"Note.assets/`) || !strings.Contains(presentationList.Body.String(), `"medium"`) {
		t.Fatalf("protected image presentation list failed: %d %s", presentationList.Code, presentationList.Body.String())
	}
	sharedAssetQuery := url.Values{"note": {"Public.md"}, "path": {"private/" + uploaded.Path}}
	sharedAssetRequest := httptest.NewRequest(http.MethodGet, "/api/repository/asset?"+sharedAssetQuery.Encode(), nil)
	sharedAssetRequest.AddCookie(cookie)
	sharedAssetResponse := httptest.NewRecorder()
	handler.ServeHTTP(sharedAssetResponse, sharedAssetRequest)
	if sharedAssetResponse.Code != http.StatusOK || sharedAssetResponse.Header().Get("Content-Type") != "image/png" || !bytes.Equal(sharedAssetResponse.Body.Bytes(), png) || sharedAssetResponse.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("shared encrypted attachment read failed: %d %s", sharedAssetResponse.Code, sharedAssetResponse.Body.String())
	}
	presentationDeleteQuery := url.Values{"note": {"private/Note.md"}, "image": {uploaded.Path}}
	presentationDelete := requestJSON(http.MethodDelete, "/api/repository/image-presentation?"+presentationDeleteQuery.Encode(), map[string]string{})
	if presentationDelete.Code != http.StatusNoContent {
		t.Fatalf("protected image presentation removal failed: %d %s", presentationDelete.Code, presentationDelete.Body.String())
	}
	if _, err := os.Stat(assetCiphertextPath); err != nil {
		t.Fatalf("removing image presentation metadata changed shared ciphertext: %v", err)
	}
	unsupportedMove := requestJSON(http.MethodPost, "/api/repository/move", map[string]string{"source": "private", "target": "moved"})
	if unsupportedMove.Code != http.StatusServiceUnavailable {
		t.Fatalf("move bypassed the unfinished encryption path: %d %s", unsupportedMove.Code, unsupportedMove.Body.String())
	}
	if _, err := os.Stat(ciphertextPath); err != nil {
		t.Fatalf("rejected move changed protected ciphertext: %v", err)
	}
	tree := httptest.NewRecorder()
	treeRequest := httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil)
	treeRequest.AddCookie(cookie)
	handler.ServeHTTP(tree, treeRequest)
	if tree.Code != http.StatusOK || !strings.Contains(tree.Body.String(), `"path":"private/Note.md"`) {
		t.Fatalf("protected logical note missing from tree: %d %s", tree.Code, tree.Body.String())
	}
	read := httptest.NewRecorder()
	readRequest := httptest.NewRequest(http.MethodGet, "/api/repository/file?path=private/Note.md", nil)
	readRequest.AddCookie(cookie)
	handler.ServeHTTP(read, readRequest)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"content":"# Note\n"`) || read.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("protected note read failed: %d %s", read.Code, read.Body.String())
	}
	var loaded struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &loaded); err != nil || loaded.Version == "" {
		t.Fatalf("protected note version missing: %#v err=%v", loaded, err)
	}
	stale := requestJSON(http.MethodPut, "/api/repository/file?path=private/Note.md", map[string]string{"content": "stale browser draft", "version": strings.Repeat("0", 64)})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale protected save returned %d: %s", stale.Code, stale.Body.String())
	}
	serverNote := httptest.NewRecorder()
	serverNoteRequest := httptest.NewRequest(http.MethodGet, "/api/repository/file?path=private/Note.md", nil)
	serverNoteRequest.AddCookie(cookie)
	handler.ServeHTTP(serverNote, serverNoteRequest)
	if serverNote.Code != http.StatusOK || !strings.Contains(serverNote.Body.String(), `"content":"# Note\n"`) {
		t.Fatalf("stale draft replaced server note: %d %s", serverNote.Code, serverNote.Body.String())
	}
	updated := requestJSON(http.MethodPut, "/api/repository/file?path=private/Note.md", map[string]string{"content": "# Updated\n", "version": loaded.Version})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"content":"# Updated\n"`) {
		t.Fatalf("protected save failed: %d %s", updated.Code, updated.Body.String())
	}
	updatedCiphertext, err := os.ReadFile(ciphertextPath)
	if err != nil || bytes.Contains(updatedCiphertext, []byte("# Updated")) {
		t.Fatalf("saved note was not ciphertext: err=%v", err)
	}
	if err := manager.LockVault(vaultID, encryption.LockReasonManual); err != nil {
		t.Fatal(err)
	}
	locked := httptest.NewRecorder()
	lockedRequest := httptest.NewRequest(http.MethodGet, "/api/repository/file?path=private/Note.md", nil)
	lockedRequest.AddCookie(cookie)
	handler.ServeHTTP(locked, lockedRequest)
	if locked.Code != http.StatusLocked || !strings.Contains(locked.Body.String(), `"code":"encryption_locked"`) {
		t.Fatalf("locked note read returned %d: %s", locked.Code, locked.Body.String())
	}
	lockedAsset := httptest.NewRecorder()
	lockedAssetRequest := httptest.NewRequest(http.MethodGet, "/api/repository/asset?"+sharedAssetQuery.Encode(), nil)
	lockedAssetRequest.AddCookie(cookie)
	handler.ServeHTTP(lockedAsset, lockedAssetRequest)
	if lockedAsset.Code != http.StatusLocked || !strings.Contains(lockedAsset.Body.String(), `"code":"encryption_locked"`) || bytes.Contains(lockedAsset.Body.Bytes(), png) {
		t.Fatalf("locked shared attachment leaked content or returned the wrong status: %d %s", lockedAsset.Code, lockedAsset.Body.String())
	}
	if err := manager.Unlock(vaultID, sessionHash[:], key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ciphertextPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := httptest.NewRecorder()
	corruptRequest := httptest.NewRequest(http.MethodGet, "/api/repository/file?path=private/Note.md", nil)
	corruptRequest.AddCookie(cookie)
	handler.ServeHTTP(corrupt, corruptRequest)
	if corrupt.Code != http.StatusInternalServerError || strings.Contains(corrupt.Body.String(), "# Updated") {
		t.Fatalf("corrupt ciphertext was served: %d %s", corrupt.Code, corrupt.Body.String())
	}
}

func TestLocalNotebookCreatesFilesystemVaultAndGitRoutesAreGone(t *testing.T) {
	base := t.TempDir()
	notebookBase := filepath.Join(base, "notebooks")
	metadata := filepath.Join(base, "app", "notebooks.json")
	t.Setenv("VARIANOTES_NOTEBOOKS_DIR", notebookBase)
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadata)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/notebooks/local", strings.NewReader(`{"name":"Private"}`)))
	if created.Code != http.StatusCreated {
		t.Fatalf("vault creation failed: %d %s", created.Code, created.Body.String())
	}
	var record notebookRecord
	if err := json.Unmarshal(created.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.ID == "" || record.Name != "Private" || record.LocalPath == "" {
		t.Fatalf("invalid vault record: %#v", record)
	}
	if _, err := os.Stat(filepath.Join(record.LocalPath, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new vault unexpectedly has Git metadata: %v", err)
	}
	registryBytes, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacyField := range []string{"remoteUrl", "branch", "authType", "keyId", "lastSyncedAt"} {
		if strings.Contains(string(registryBytes), legacyField) {
			t.Fatalf("new registry retained Git field %q: %s", legacyField, registryBytes)
		}
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/repository/git/status"},
		{http.MethodPost, "/api/repository/git/sync"},
		{http.MethodGet, "/api/repository/history?path=Secret.md"},
		{http.MethodPost, "/api/notebooks/test-connection"},
		{http.MethodGet, "/api/notebooks/ssh-keys"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("legacy route %s %s returned %d: %s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestAdoptVaultPreservesLegacyGitDirectoryAndExportOmitsIt(t *testing.T) {
	base := t.TempDir()
	notebookBase := filepath.Join(base, "notebooks")
	legacyVault := filepath.Join(notebookBase, "legacy")
	gitDirectory := filepath.Join(legacyVault, ".git")
	if err := os.MkdirAll(gitDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyVault, "Secret.md"), []byte("# Plain note"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyConfig := []byte("[remote \\\"origin\\\"]\\nurl = https://example.invalid/private.git\\n")
	if err := os.WriteFile(filepath.Join(gitDirectory, "config"), legacyConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(notebookBase, "escape")); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(base, "app", "notebooks.json")
	t.Setenv("VARIANOTES_NOTEBOOKS_DIR", notebookBase)
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadata)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}

	adopt := func(folder string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"name": "Legacy Vault", "folder": folder})
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/notebooks/adopt", bytes.NewReader(body)))
		return response
	}
	adopted := adopt("legacy")
	if adopted.Code != http.StatusCreated {
		t.Fatalf("vault adoption failed: %d %s", adopted.Code, adopted.Body.String())
	}
	var record notebookRecord
	if err := json.Unmarshal(adopted.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(record.LocalPath) != filepath.Clean(legacyVault) {
		t.Fatalf("adopted path = %q, want %q", record.LocalPath, legacyVault)
	}
	configAfter, err := os.ReadFile(filepath.Join(gitDirectory, "config"))
	if err != nil || !bytes.Equal(configAfter, legacyConfig) {
		t.Fatalf("legacy Git metadata was modified: %q, %v", configAfter, err)
	}
	tree := httptest.NewRecorder()
	handler.ServeHTTP(tree, httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil))
	if tree.Code != http.StatusOK || !strings.Contains(tree.Body.String(), "Secret.md") || strings.Contains(tree.Body.String(), ".git") {
		t.Fatalf("adopted tree exposed the wrong files: %d %s", tree.Code, tree.Body.String())
	}
	archive := httptest.NewRecorder()
	handler.ServeHTTP(archive, httptest.NewRequest(http.MethodGet, "/api/notebooks/"+record.ID+"/export", nil))
	if archive.Code != http.StatusOK || archive.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("vault export failed or is cacheable: %d %s", archive.Code, archive.Body.String())
	}
	zipReader, err := zip.NewReader(bytes.NewReader(archive.Body.Bytes()), int64(archive.Body.Len()))
	if err != nil {
		t.Fatalf("export is not a valid ZIP archive: %v", err)
	}
	foundNote := false
	for _, item := range zipReader.File {
		if strings.Contains(item.Name, ".git") {
			t.Fatalf("legacy Git files leaked into export: %q", item.Name)
		}
		if item.Name == "Secret.md" {
			foundNote = true
		}
	}
	if !foundNote {
		t.Fatal("ordinary Markdown note is missing from export")
	}
	if duplicate := adopt("legacy"); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate folder registration returned %d: %s", duplicate.Code, duplicate.Body.String())
	}
	for _, unsafeFolder := range []string{"../outside", "legacy..backup"} {
		if traversal := adopt(unsafeFolder); traversal.Code != http.StatusBadRequest {
			t.Errorf("unsafe folder %q was accepted: %d %s", unsafeFolder, traversal.Code, traversal.Body.String())
		}
	}
	if symlink := adopt("escape"); symlink.Code != http.StatusBadRequest {
		t.Fatalf("symlink escape was accepted: %d %s", symlink.Code, symlink.Body.String())
	}
}

func TestNotebookRegistryPreservesEntriesAndChangesActiveNotebook(t *testing.T) {
	base := t.TempDir()
	metadata := filepath.Join(base, "notebooks.json")
	firstPath := filepath.Join(base, "first")
	secondPath := filepath.Join(base, "second")
	if err := os.MkdirAll(firstPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secondPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registerActiveNotebook(metadata, notebookRecord{ID: "first", Name: "Private", LocalPath: firstPath}); err != nil {
		t.Fatal(err)
	}
	if err := registerActiveNotebook(metadata, notebookRecord{ID: "second", Name: "Work", LocalPath: secondPath}); err != nil {
		t.Fatal(err)
	}
	registry, err := loadNotebookRegistry(metadata)
	if err != nil || len(registry.Entries) != 2 || registry.ActiveID != "second" {
		t.Fatalf("registry did not preserve notebooks: %#v, %v", registry, err)
	}
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadata)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/notebooks/first/activate", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("activate notebook: %d %s", response.Code, response.Body.String())
	}
	registry, _ = loadNotebookRegistry(metadata)
	if registry.ActiveID != "first" {
		t.Fatalf("active notebook = %q", registry.ActiveID)
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/notebooks", nil))
	if !strings.Contains(list.Body.String(), `"name":"Private"`) || !strings.Contains(list.Body.String(), `"name":"Work"`) || strings.Contains(list.Body.String(), firstPath) {
		t.Fatalf("unsafe/incomplete notebook list: %s", list.Body.String())
	}
}

func TestFrontendFallback(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/notes/welcome", nil)
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), "")
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "VariaNotes") {
		t.Fatal("expected embedded frontend")
	}
}

func TestImagePresentationAPIValidatesAndPersistsOutsideTheRepository(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Note.md"), []byte("![](Note.assets/image.png)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Note.assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Note.assets", "image.png"), []byte("\x89PNG\r\n\x1a\nimage"), 0o644); err != nil {
		t.Fatal(err)
	}
	metadataDirectory := t.TempDir()
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", filepath.Join(metadataDirectory, "notebooks.json"))
	metadataPath := filepath.Join(metadataDirectory, "image-presentations.json")
	handler, err := NewHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), root)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, target, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(method, target, strings.NewReader(body))
		httpRequest.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(response, httpRequest)
		return response
	}

	saved := request(http.MethodPut, "/api/repository/image-presentation", `{"note":"Note.md","image":"Note.assets/image.png","size":"medium"}`)
	if saved.Code != http.StatusOK {
		t.Fatalf("save presentation = %d %s", saved.Code, saved.Body.String())
	}
	if info, err := os.Stat(metadataPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("presentation metadata file = %#v, %v", info, err)
	}
	loaded := request(http.MethodGet, "/api/repository/image-presentations?note=Note.md", "")
	if loaded.Code != http.StatusOK || !strings.Contains(loaded.Body.String(), `"Note.assets/image.png":"medium"`) {
		t.Fatalf("load presentation = %d %s", loaded.Code, loaded.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "image-presentations.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("presentation metadata leaked into the vault folder: %v", err)
	}
	invalid := request(http.MethodPut, "/api/repository/image-presentation", `{"note":"Note.md","image":"Note.assets/image.png","size":"400px"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid size = %d %s", invalid.Code, invalid.Body.String())
	}
	escape := request(http.MethodPut, "/api/repository/image-presentation", `{"note":"Note.md","image":"../outside.png","size":"small"}`)
	if escape.Code != http.StatusBadRequest {
		t.Fatalf("asset escape = %d %s", escape.Code, escape.Body.String())
	}
	missing := request(http.MethodGet, "/api/repository/image-presentations?note=Missing.md", "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing note = %d %s", missing.Code, missing.Body.String())
	}
	moved := request(http.MethodPost, "/api/repository/move", `{"source":"Note.md","target":"Renamed.md"}`)
	if moved.Code != http.StatusOK {
		t.Fatalf("move note = %d %s", moved.Code, moved.Body.String())
	}
	movedPresentation := request(http.MethodGet, "/api/repository/image-presentations?note=Renamed.md", "")
	if movedPresentation.Code != http.StatusOK || !strings.Contains(movedPresentation.Body.String(), `"Renamed.assets/image.png":"medium"`) {
		t.Fatalf("moved presentation = %d %s", movedPresentation.Code, movedPresentation.Body.String())
	}
	deleted := request(http.MethodDelete, "/api/repository/image-presentation?note=Renamed.md&image=Renamed.assets%2Fimage.png", "")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete presentation = %d %s", deleted.Code, deleted.Body.String())
	}
	withoutPresentation := request(http.MethodGet, "/api/repository/image-presentations?note=Renamed.md", "")
	if withoutPresentation.Code != http.StatusOK || !strings.Contains(withoutPresentation.Body.String(), `"presentations":{}`) {
		t.Fatalf("presentation after deletion = %d %s", withoutPresentation.Code, withoutPresentation.Body.String())
	}
}

func TestEveryApplicationAPIRouteDeniesUnauthenticatedAccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(logger, t.TempDir(), service)
	if err != nil {
		t.Fatal(err)
	}

	protected := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodPost, "/api/encryption/lock"},
		{http.MethodPost, "/api/encryption/recovery-code"},
		{http.MethodPost, "/api/encryption/recovery-code/confirm"},
		{http.MethodPost, "/api/encryption/activity?vaultId=local"},
		{http.MethodGet, "/api/encryption/lock-events?vaultId=local"},
		{http.MethodDelete, "/api/auth/session"},
		{http.MethodDelete, "/api/auth/sessions"},
		{http.MethodGet, "/api/auth/sessions"},
		{http.MethodDelete, "/api/auth/sessions/others"},
		{http.MethodDelete, "/api/auth/sessions/example"},
		{http.MethodGet, "/api/auth/security"},
		{http.MethodPut, "/api/auth/security/session-settings"},
		{http.MethodPut, "/api/auth/password"},
		{http.MethodPost, "/api/auth/mfa/enroll"},
		{http.MethodPost, "/api/auth/mfa/confirm"},
		{http.MethodDelete, "/api/auth/mfa/enrollment"},
		{http.MethodDelete, "/api/auth/mfa"},
		{http.MethodPost, "/api/auth/mfa/recovery-codes"},
		{http.MethodGet, "/api/notebook"},
		{http.MethodGet, "/api/notebooks"},
		{http.MethodPost, "/api/notebooks/example/activate"},
		{http.MethodPatch, "/api/notebooks/example"},
		{http.MethodPatch, "/api/notebooks/example/ssh-key"},
		{http.MethodGet, "/api/notebooks/example/health"},
		{http.MethodDelete, "/api/notebooks/example"},
		{http.MethodGet, "/api/repository/tree"},
		{http.MethodGet, "/api/repository/search?q=secret"},
		{http.MethodGet, "/api/repository/file?path=Secret.md"},
		{http.MethodPut, "/api/repository/file"},
		{http.MethodGet, "/api/repository/history?path=Secret.md"},
		{http.MethodGet, "/api/repository/history/version?path=Secret.md&version=abc"},
		{http.MethodPost, "/api/repository/history/restore"},
		{http.MethodPost, "/api/repository/entries"},
		{http.MethodPost, "/api/repository/move"},
		{http.MethodPost, "/api/repository/move/preview"},
		{http.MethodGet, "/api/repository/links?path=Secret.md"},
		{http.MethodDelete, "/api/repository/entry"},
		{http.MethodGet, "/api/repository/trash"},
		{http.MethodPost, "/api/repository/trash/example/restore"},
		{http.MethodDelete, "/api/repository/trash/example"},
		{http.MethodPost, "/api/repository/assets"},
		{http.MethodGet, "/api/repository/asset"},
		{http.MethodGet, "/api/repository/image-presentations?note=Secret.md"},
		{http.MethodPut, "/api/repository/image-presentation"},
		{http.MethodDelete, "/api/repository/image-presentation?note=Secret.md&image=Secret.assets%2Fimage.png"},
		{http.MethodGet, "/api/repository/assets/unreferenced"},
		{http.MethodPost, "/api/repository/assets/cleanup"},
		{http.MethodGet, "/api/repository/git/status"},
		{http.MethodPost, "/api/repository/git/sync"},
		{http.MethodPost, "/api/repository/git/sync-background"},
		{http.MethodPost, "/api/notebooks"},
		{http.MethodPost, "/api/notebooks/local"},
		{http.MethodPost, "/api/notebooks/example/remote"},
		{http.MethodPost, "/api/notebooks/example/remote/disconnect"},
		{http.MethodPost, "/api/notebooks/ssh-key"},
		{http.MethodGet, "/api/notebooks/ssh-keys"},
		{http.MethodDelete, "/api/notebooks/ssh-keys/example"},
		{http.MethodPost, "/api/notebooks/test-connection"},
		{http.MethodPost, "/api/notebooks/ssh-host/discover"},
		{http.MethodPost, "/api/notebooks/ssh-host/trust"},
		{http.MethodGet, "/api/future-protected-route"},
	}
	for _, route := range protected {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
			if response.Code != http.StatusUnauthorized || strings.TrimSpace(response.Body.String()) != `{"code":"authentication_required","error":"authentication required"}` {
				t.Fatalf("protected API response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPublicAuthSurfaceDoesNotExposeDeploymentMetadata(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := filepath.Join(t.TempDir(), "SENSITIVE-HOST-PATH")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "SENSITIVE-AUTH-DATABASE.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	token, err := service.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteSetup(t.Context(), token.Value, "a sufficiently long password"); err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandlerWithAuth(logger, root, service)
	if err != nil {
		t.Fatal(err)
	}
	public := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/health", ""},
		{http.MethodGet, "/api/auth/status", ""},
		{http.MethodPost, "/api/auth/login", `{"password":"wrong but deliberately long password","rememberDevice":false}`},
		{http.MethodPost, "/api/auth/login/mfa", `{"code":"000000"}`},
		{http.MethodPost, "/api/auth/setup", `{"bootstrapToken":"invalid","password":"another sufficiently long password"}`},
		{http.MethodPost, "/api/auth/recovery/prepare", `{"recoveryCode":"invalid","newPassword":"another sufficiently long password"}`},
		{http.MethodPost, "/api/auth/recovery/confirm", `{"challengeId":"invalid","recoveryCode":"invalid","newPassword":"another sufficiently long password","confirmed":true}`},
	}
	for _, route := range public {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)))
		body := strings.ToLower(response.Body.String())
		for _, forbidden := range []string{"sensitive-host-path", "sensitive-auth-database", "remoteurl", "private key", "known_hosts", "git@"} {
			if strings.Contains(body, strings.ToLower(forbidden)) {
				t.Fatalf("public %s exposed deployment metadata %q: %s", route.path, forbidden, response.Body.String())
			}
		}
	}
}

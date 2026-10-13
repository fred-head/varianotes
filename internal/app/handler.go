package app

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/fred-head/varianotes/internal/files"
	frontend "github.com/fred-head/varianotes/web"
)

func NewHandler(logger *slog.Logger, repositoryRoot string, versions ...string) (http.Handler, error) {
	return newHandler(logger, repositoryRoot, nil, versions...)
}

func NewHandlerWithAuth(logger *slog.Logger, repositoryRoot string, authService *auth.Service, versions ...string) (http.Handler, error) {
	if authService == nil {
		return nil, errors.New("authentication service is required")
	}
	sessions, err := auth.NewSessions(authService, auth.DefaultSessionOptions(authService.Config().CookieSecure))
	if err != nil {
		return nil, err
	}
	return newHandlerWithSessions(logger, repositoryRoot, authService, sessions, versions...)
}

func newHandler(logger *slog.Logger, repositoryRoot string, authService *auth.Service, versions ...string) (http.Handler, error) {
	return newHandlerWithSessions(logger, repositoryRoot, authService, nil, versions...)
}

func newHandlerWithSessions(logger *slog.Logger, repositoryRoot string, authService *auth.Service, sessions *auth.Sessions, versions ...string) (http.Handler, error) {
	return newHandlerWithKeyManager(logger, repositoryRoot, authService, sessions, encryption.NewKeyManager(), versions...)
}

func newHandlerWithKeyManager(logger *slog.Logger, repositoryRoot string, authService *auth.Service, sessions *auth.Sessions, keyManager *encryption.KeyManager, versions ...string) (http.Handler, error) {
	if keyManager == nil {
		return nil, errors.New("encryption key manager is required")
	}
	version := "dev"
	if len(versions) > 0 && strings.TrimSpace(versions[0]) != "" {
		version = strings.TrimSpace(versions[0])
	}
	metadataPath := os.Getenv("VARIANOTES_NOTEBOOK_METADATA")
	notebooksDirectory := os.Getenv("VARIANOTES_NOTEBOOKS_DIR")
	trustedOrigins := os.Getenv("VARIANOTES_TRUSTED_ORIGINS")
	requestIdentity := auth.NewRequestIdentity(nil)
	var loginThrottle *auth.LoginThrottle
	if authService != nil {
		requestIdentity = auth.NewRequestIdentity(authService.Config().TrustedProxies)
		loginThrottle = auth.NewLoginThrottle(authService)
	}
	activeRecord, activeLoadErr := loadActiveNotebook(metadataPath)
	activeNotebookName := ""
	if activeLoadErr == nil {
		repositoryRoot = activeRecord.LocalPath
		activeNotebookName = activeRecord.Name
	}
	if activeNotebookName == "" {
		activeNotebookName = filepath.Base(filepath.Clean(repositoryRoot))
		if activeNotebookName == "." || activeNotebookName == string(filepath.Separator) || activeNotebookName == "" {
			activeNotebookName = "Notebook"
		}
	}
	repository, err := files.NewRepository(repositoryRoot)
	if err != nil {
		return nil, err
	}
	if errors.Is(activeLoadErr, os.ErrNotExist) && metadataPath != "" && repository.Configured() {
		localRecord := notebookRecord{ID: "local", Name: activeNotebookName, LocalPath: repositoryRoot}
		if registerErr := registerActiveNotebook(metadataPath, localRecord); registerErr == nil {
			activeRecord = localRecord
			activeLoadErr = nil
		}
	}
	var activeMu sync.RWMutex
	var notebookSwitchMu sync.Mutex
	noteRecord := activeRecord
	if noteRecord.ID == "" && repository.Configured() {
		noteRecord = notebookRecord{ID: "local", LocalPath: repositoryRoot}
	}
	activeNoteAccess := newProtectedNoteAccess(repository, metadataPath, noteRecord)
	currentRepository := func() *files.Repository {
		activeMu.RLock()
		defer activeMu.RUnlock()
		return repository
	}
	currentContentContext := func() (*files.Repository, *protectedNoteAccess) {
		activeMu.RLock()
		defer activeMu.RUnlock()
		return repository, activeNoteAccess
	}
	currentNotebookName := func() string {
		activeMu.RLock()
		defer activeMu.RUnlock()
		return activeNotebookName
	}
	currentNotebookID := func() string {
		activeMu.RLock()
		defer activeMu.RUnlock()
		if activeRecord.ID != "" {
			return activeRecord.ID
		}
		return "local"
	}
	currentNotebookContext := func() (*files.Repository, *protectedNoteAccess, notebookRecord) {
		activeMu.RLock()
		defer activeMu.RUnlock()
		if activeRecord.ID != "" {
			return repository, activeNoteAccess, activeRecord
		}
		return repository, activeNoteAccess, notebookRecord{ID: "local", Name: activeNotebookName, LocalPath: repositoryRoot}
	}
	presentationStore := newImagePresentationStore(metadataPath)

	mux := http.NewServeMux()
	exportGrants := newExportGrantStore()
	beginCredentialAttempt := func(w http.ResponseWriter, r *http.Request, operation string) (netip.Addr, func(), bool) {
		clientIP := requestIdentity.ClientIP(r)
		errorMessage := "too many authentication attempts"
		errorCode := "authentication_throttled"
		if operation == auth.ThrottleOperationLogin {
			errorMessage = "too many login attempts"
			errorCode = "login_throttled"
		}
		if !loginThrottle.Begin() {
			w.Header().Set("Retry-After", "1")
			_ = authService.RecordSecurityEvent(r.Context(), operation, "throttled", "client="+auth.ClientReference(clientIP))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": errorMessage, "code": errorCode, "retryAfterSeconds": 1})
			return clientIP, func() {}, false
		}
		retryAfter, err := loginThrottle.CheckOperation(r.Context(), operation, clientIP)
		if err != nil {
			loginThrottle.End()
			logger.Error("credential throttle unavailable", "operation", operation, "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication is unavailable"})
			return clientIP, func() {}, false
		}
		if retryAfter > 0 {
			loginThrottle.End()
			seconds := int64((retryAfter + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
			_ = authService.RecordSecurityEvent(r.Context(), operation, "throttled", "client="+auth.ClientReference(clientIP))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": errorMessage, "code": errorCode, "retryAfterSeconds": seconds})
			return clientIP, func() {}, false
		}
		return clientIP, loginThrottle.End, true
	}
	recordCredentialFailure := func(w http.ResponseWriter, r *http.Request, operation string, clientIP netip.Addr) bool {
		delay, err := loginThrottle.FailureOperation(r.Context(), operation, clientIP)
		if err != nil {
			logger.Error("record credential throttle failed", "operation", operation, "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication is unavailable"})
			return false
		}
		if delay > 0 {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int64((delay+time.Second-1)/time.Second)))
		}
		return true
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version})
	})
	if authService != nil {
		mux.HandleFunc("GET /api/auth/status", func(w http.ResponseWriter, r *http.Request) {
			state, err := authService.State(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication state is unavailable"})
				return
			}
			response := map[string]any{
				"mode":          state.Mode,
				"setupRequired": state.Mode == auth.ModeLocal && !state.SetupCompleted,
				"authenticated": state.Mode == auth.ModeDisabled || (sessions != nil && sessions.Authenticated(r.Context())),
			}
			if state.Mode == auth.ModeLocal {
				response["csrfToken"] = sessions.ExistingCSRFToken(r.Context())
				response["mfaRequired"] = sessions.MFAPending(r.Context())
			}
			writeJSON(w, http.StatusOK, response)
		})
		mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Password       string `json:"password"`
				RememberDevice bool   `json:"rememberDevice"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			state, err := authService.State(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication state is unavailable"})
				return
			}
			if state.Mode != auth.ModeLocal || !state.SetupCompleted {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "login is not available", "code": "login_unavailable"})
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationLogin)
			if !allowed {
				return
			}
			defer release()
			err = sessions.Login(r.Context(), input.Password, input.RememberDevice, r.UserAgent())
			if errors.Is(err, auth.ErrMFARequired) {
				_ = keyManager.LockAll(encryption.LockReasonSession)
				_ = authService.RecordSecurityEvent(r.Context(), "login_password", "success", "client="+auth.ClientReference(clientIP))
				writeJSON(w, http.StatusAccepted, map[string]any{"authenticated": false, "mfaRequired": true})
				return
			}
			if errors.Is(err, auth.ErrAuthentication) {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationLogin, clientIP) {
					return
				}
				_ = authService.RecordSecurityEvent(r.Context(), "login", "failure", "client="+auth.ClientReference(clientIP))
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
				return
			}
			if err != nil {
				_ = authService.RecordSecurityEvent(r.Context(), "login", "error", "client="+auth.ClientReference(clientIP))
				logger.Error("login failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication is unavailable"})
				return
			}
			_ = keyManager.LockAll(encryption.LockReasonSession)
			if err := loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationLogin, clientIP); err != nil {
				logger.Warn("clear login throttle failed", "error", err)
			}
			_ = authService.RecordSecurityEvent(r.Context(), "login", "success", "client="+auth.ClientReference(clientIP))
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrfToken": sessions.ExistingCSRFToken(r.Context())})
		})
		mux.HandleFunc("POST /api/auth/login/mfa", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Code string `json:"code"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			if !sessions.MFAPending(r.Context()) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "The two-factor sign-in step expired. Enter your password again.", "code": "mfa_challenge_expired"})
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationLogin)
			if !allowed {
				return
			}
			defer release()
			if err := sessions.CompleteMFA(r.Context(), input.Code); err != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationLogin, clientIP) {
					return
				}
				_ = authService.RecordSecurityEvent(r.Context(), "login_mfa", "failure", "client="+auth.ClientReference(clientIP))
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
				return
			}
			_ = keyManager.LockAll(encryption.LockReasonSession)
			_ = loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationLogin, clientIP)
			_ = authService.RecordSecurityEvent(r.Context(), "login_mfa", "success", "client="+auth.ClientReference(clientIP))
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrfToken": sessions.ExistingCSRFToken(r.Context())})
		})
		mux.HandleFunc("POST /api/encryption/recovery-code", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				CurrentPassword            string `json:"currentPassword"`
				SeparateEncryptionPassword string `json:"encryptionPassword"`
				MFACode                    string `json:"mfaCode"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			enabled, err := authService.MFAEnabled(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication is unavailable"})
				return
			}
			if enabled && authService.VerifySecondFactor(r.Context(), input.MFACode) != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
				return
			}
			challenge, code, err := authService.BeginEncryptionRecoveryCode(r.Context(), input.CurrentPassword, input.SeparateEncryptionPassword, sessions.CurrentHash(r.Context()))
			if err != nil {
				if errors.Is(err, auth.ErrAuthentication) {
					if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
						return
					}
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
					return
				}
				logger.Error("prepare encryption recovery code failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "recovery code could not be prepared"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"challengeId": challenge, "recoveryCode": code})
		})
		mux.HandleFunc("POST /api/encryption/recovery-code/confirm", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				ChallengeID  string `json:"challengeId"`
				RecoveryCode string `json:"recoveryCode"`
				Confirmed    bool   `json:"confirmed"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			if err := authService.ConfirmEncryptionRecoveryCode(r.Context(), input.ChallengeID, input.RecoveryCode, input.Confirmed, sessions.CurrentHash(r.Context())); err != nil {
				if errors.Is(err, auth.ErrRecoveryRequired) || errors.Is(err, auth.ErrRecoveryPending) {
					_ = keyManager.LockAll(encryption.LockReasonSession)
				}
				if errors.Is(err, auth.ErrInvalidRecoveryCode) {
					if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
						return
					}
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "recovery code confirmation failed", "code": "invalid_recovery_code"})
					return
				}
				if errors.Is(err, auth.ErrRecoveryNotConfirmed) || errors.Is(err, auth.ErrRecoveryChallengeExpired) {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recovery code confirmation is required"})
					return
				}
				logger.Error("confirm encryption recovery code failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "recovery code could not be activated"})
				return
			}
			_ = loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP)
			writeJSON(w, http.StatusOK, map[string]bool{"recoveryCodeActive": true})
		})
		mux.HandleFunc("POST /api/auth/recovery/prepare", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				RecoveryCode string `json:"recoveryCode"`
				NewPassword  string `json:"newPassword"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			state, err := authService.State(r.Context())
			if err != nil || state.Mode != auth.ModeLocal || !state.SetupCompleted {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "password recovery is unavailable"})
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			if err := auth.ValidatePassword(input.NewPassword); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			challenge, code, err := authService.BeginPasswordRecovery(r.Context(), input.RecoveryCode, input.NewPassword)
			if err != nil {
				if errors.Is(err, auth.ErrInvalidRecoveryCode) {
					if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
						return
					}
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_recovery_code"})
					return
				}
				if errors.Is(err, auth.ErrPasswordTooShort) || errors.Is(err, auth.ErrPasswordTooLarge) || errors.Is(err, auth.ErrInvalidPassword) || errors.Is(err, auth.ErrPasswordTooWeak) {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
				logger.Error("prepare password recovery failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "password recovery could not be prepared"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"challengeId": challenge, "recoveryCode": code})
		})
		mux.HandleFunc("POST /api/auth/recovery/confirm", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				ChallengeID  string `json:"challengeId"`
				NewPassword  string `json:"newPassword"`
				RecoveryCode string `json:"recoveryCode"`
				Confirmed    bool   `json:"confirmed"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			if err := authService.ConfirmPasswordRecovery(r.Context(), input.ChallengeID, input.NewPassword, input.RecoveryCode, input.Confirmed); err != nil {
				if errors.Is(err, auth.ErrRecoveryRequired) || errors.Is(err, auth.ErrRecoveryPending) {
					_ = keyManager.LockAll(encryption.LockReasonSession)
				}
				switch {
				case errors.Is(err, auth.ErrInvalidRecoveryCode):
					if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
						return
					}
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "password recovery confirmation failed", "code": "invalid_recovery_code"})
				case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrPasswordTooLarge), errors.Is(err, auth.ErrInvalidPassword), errors.Is(err, auth.ErrPasswordTooWeak):
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				case errors.Is(err, auth.ErrRecoveryNotConfirmed), errors.Is(err, auth.ErrRecoveryChallengeExpired):
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password recovery confirmation is required"})
				default:
					logger.Error("confirm password recovery failed", "error", err)
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "password recovery could not be completed"})
				}
				return
			}
			_ = keyManager.LockAll(encryption.LockReasonSession)
			_ = sessions.Logout(r.Context())
			_ = loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP)
			writeJSON(w, http.StatusOK, map[string]bool{"passwordRecovered": true, "authenticated": false})
		})
		mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
			_ = keyManager.LockAll(encryption.LockReasonLogout)
			if err := sessions.Logout(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "logout failed"})
				return
			}
			_ = authService.RecordSecurityEvent(r.Context(), "logout", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
			writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		})
		mux.HandleFunc("DELETE /api/auth/session", func(w http.ResponseWriter, r *http.Request) {
			_ = keyManager.LockAll(encryption.LockReasonLogout)
			if err := sessions.RevokeCurrent(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session revocation failed"})
				return
			}
			_ = authService.RecordSecurityEvent(r.Context(), "session_revoke_current", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
			writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
		})
		mux.HandleFunc("DELETE /api/auth/sessions", func(w http.ResponseWriter, r *http.Request) {
			_ = keyManager.LockAll(encryption.LockReasonLogout)
			if err := sessions.RevokeAll(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session revocation failed"})
				return
			}
			_ = authService.RecordSecurityEvent(r.Context(), "session_revoke_all", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
			writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
		})
		mux.HandleFunc("GET /api/auth/sessions", func(w http.ResponseWriter, r *http.Request) {
			records, err := authService.Sessions(r.Context(), sessions.CurrentHash(r.Context()))
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sessions are unavailable"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"sessions": records})
		})
		mux.HandleFunc("DELETE /api/auth/sessions/others", func(w http.ResponseWriter, r *http.Request) {
			if err := authService.RevokeOtherSessions(r.Context(), sessions.CurrentHash(r.Context())); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session revocation failed"})
				return
			}
			_ = keyManager.RevokeSessionsExcept(sessions.CurrentHash(r.Context()))
			_ = authService.RecordSecurityEvent(r.Context(), "session_revoke_others", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
			writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
		})
		mux.HandleFunc("DELETE /api/auth/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
			err := authService.RevokeSession(r.Context(), r.PathValue("id"), sessions.CurrentHash(r.Context()))
			switch {
			case err == nil:
				if sessionHash, decodeErr := base64.RawURLEncoding.DecodeString(r.PathValue("id")); decodeErr == nil {
					_ = keyManager.RevokeSessionEverywhere(sessionHash)
				}
				writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
			case errors.Is(err, sql.ErrNoRows):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			}
		})
		mux.HandleFunc("GET /api/auth/security", func(w http.ResponseWriter, r *http.Request) {
			settings, err := authService.SessionSettings(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security settings are unavailable"})
				return
			}
			mfaEnabled, err := authService.MFAEnabled(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MFA state is unavailable"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"sessionSettings": settings, "mfaEnabled": mfaEnabled})
		})
		mux.HandleFunc("PUT /api/auth/security/session-settings", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				CurrentPassword string `json:"currentPassword"`
				IdleHours       int    `json:"idleHours"`
				LifetimeHours   int    `json:"lifetimeHours"`
				RememberDays    int    `json:"rememberDays"`
				MFACode         string `json:"mfaCode"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			if input.IdleHours < 1 || input.IdleHours > 720 || input.LifetimeHours < 1 || input.LifetimeHours > 24 || input.RememberDays < 1 || input.RememberDays > 90 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session durations are outside the allowed range"})
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			if err := authService.VerifyPassword(r.Context(), input.CurrentPassword); err != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
				return
			}
			enabled, stateErr := authService.MFAEnabled(r.Context())
			if stateErr != nil {
				logger.Error("read MFA state for session settings failed", "error", stateErr)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication is unavailable"})
				return
			}
			if enabled {
				if authService.VerifySecondFactor(r.Context(), input.MFACode) != nil {
					if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
						return
					}
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
					return
				}
			}
			settings := auth.SessionSettings{IdleHours: input.IdleHours, LifetimeHours: input.LifetimeHours, RememberDays: input.RememberDays}
			if err := authService.UpdateSessionSettings(r.Context(), settings); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if err := loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP); err != nil {
				logger.Warn("clear sensitive credential throttle failed", "error", err)
			}
			_ = authService.RecordSecurityEvent(r.Context(), "session_settings", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
			writeJSON(w, http.StatusOK, map[string]any{"sessionSettings": settings})
		})
		mux.HandleFunc("PUT /api/auth/password", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				CurrentPassword string `json:"currentPassword"`
				NewPassword     string `json:"newPassword"`
				MFACode         string `json:"mfaCode"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			if err := auth.ValidatePassword(input.NewPassword); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			enabled, stateErr := authService.MFAEnabled(r.Context())
			if stateErr != nil {
				logger.Error("read MFA state for password change failed", "error", stateErr)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication is unavailable"})
				return
			}
			if enabled {
				if err := authService.VerifyPassword(r.Context(), input.CurrentPassword); err != nil || authService.VerifySecondFactor(r.Context(), input.MFACode) != nil {
					if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
						return
					}
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
					return
				}
			}
			err := sessions.ChangePassword(r.Context(), input.CurrentPassword, input.NewPassword)
			switch {
			case err == nil:
				_ = keyManager.LockAll(encryption.LockReasonSession)
				if clearErr := loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP); clearErr != nil {
					logger.Warn("clear sensitive credential throttle failed", "error", clearErr)
				}
				_ = authService.RecordSecurityEvent(r.Context(), "password_change", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
				writeJSON(w, http.StatusOK, map[string]any{"changed": true, "csrfToken": sessions.ExistingCSRFToken(r.Context())})
			case errors.Is(err, auth.ErrAuthentication):
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
			case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrPasswordTooLarge), errors.Is(err, auth.ErrInvalidPassword), errors.Is(err, auth.ErrPasswordTooWeak):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			case errors.Is(err, auth.ErrPasswordChangeRecovery), errors.Is(err, auth.ErrPasswordChangePending), errors.Is(err, auth.ErrRecoveryRequired), errors.Is(err, auth.ErrRecoveryPending):
				_ = keyManager.LockAll(encryption.LockReasonSession)
				logger.Error("password change requires recovery")
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "password change requires recovery; restart VariaNotes before continuing"})
			default:
				logger.Error("password change failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "password change failed"})
			}
		})
		mux.HandleFunc("POST /api/auth/mfa/enroll", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				CurrentPassword string `json:"currentPassword"`
				CurrentFactor   string `json:"currentFactor"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			enrollment, err := authService.BeginMFAEnrollment(r.Context(), input.CurrentPassword, input.CurrentFactor, sessions.CurrentHash(r.Context()))
			if err != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
				return
			}
			_ = loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP)
			writeJSON(w, http.StatusOK, enrollment)
		})
		mux.HandleFunc("POST /api/auth/mfa/confirm", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				Code                string `json:"code"`
				RecoveryCodesStored bool   `json:"recoveryCodesStored"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			if err := authService.ConfirmMFAEnrollment(r.Context(), input.Code, input.RecoveryCodesStored, sessions.CurrentHash(r.Context())); err != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "MFA enrollment could not be confirmed"})
				return
			}
			_ = loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP)
			if err := sessions.RotateCurrent(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session rotation failed"})
				return
			}
			_ = keyManager.LockAll(encryption.LockReasonSession)
			_ = authService.RecordSecurityEvent(r.Context(), "mfa_enabled", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
			writeJSON(w, http.StatusOK, map[string]any{"mfaEnabled": true, "csrfToken": sessions.ExistingCSRFToken(r.Context())})
		})
		mux.HandleFunc("DELETE /api/auth/mfa/enrollment", func(w http.ResponseWriter, r *http.Request) {
			if err := authService.CancelMFAEnrollment(r.Context(), sessions.CurrentHash(r.Context())); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "MFA enrollment could not be cancelled"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
		})
		mux.HandleFunc("DELETE /api/auth/mfa", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				CurrentPassword string `json:"currentPassword"`
				Code            string `json:"code"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			if err := authService.DisableMFA(r.Context(), input.CurrentPassword, input.Code); err != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
				return
			}
			_ = keyManager.LockAll(encryption.LockReasonLogout)
			if err := sessions.Logout(r.Context()); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "logout failed"})
				return
			}
			_ = authService.RecordSecurityEvent(r.Context(), "mfa_disabled", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
			writeJSON(w, http.StatusOK, map[string]bool{"mfaEnabled": false})
		})
		mux.HandleFunc("POST /api/auth/mfa/recovery-codes", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				CurrentPassword string `json:"currentPassword"`
				Code            string `json:"code"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			codes, err := authService.RegenerateRecoveryCodes(r.Context(), input.CurrentPassword, input.Code)
			if err != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication failed", "code": "invalid_credentials"})
				return
			}
			_ = loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP)
			writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
		})
		mux.HandleFunc("POST /api/auth/setup", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				BootstrapToken string `json:"bootstrapToken"`
				Password       string `json:"password"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			err := authService.CompleteSetup(r.Context(), input.BootstrapToken, input.Password)
			switch {
			case err == nil:
				if establishErr := sessions.EstablishAfterSetup(r.Context(), r.UserAgent()); establishErr != nil {
					logger.Error("owner session establishment failed", "error", establishErr)
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "setup completed but login failed"})
					return
				}
				_ = authService.RecordSecurityEvent(r.Context(), "setup", "success", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
				writeJSON(w, http.StatusOK, map[string]any{"setupCompleted": true, "authenticated": true, "csrfToken": sessions.ExistingCSRFToken(r.Context())})
			case errors.Is(err, auth.ErrPasswordTooShort), errors.Is(err, auth.ErrPasswordTooLarge), errors.Is(err, auth.ErrInvalidPassword), errors.Is(err, auth.ErrPasswordTooWeak):
				_ = authService.RecordSecurityEvent(r.Context(), "setup", "failure", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			case errors.Is(err, auth.ErrInvalidBootstrap):
				_ = authService.RecordSecurityEvent(r.Context(), "setup", "failure", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "setup authorization is invalid or expired"})
			case errors.Is(err, auth.ErrSetupUnavailable):
				_ = authService.RecordSecurityEvent(r.Context(), "setup", "unavailable", "client="+auth.ClientReference(requestIdentity.ClientIP(r)))
				writeJSON(w, http.StatusConflict, map[string]string{"error": "owner setup is not available"})
			default:
				logger.Error("owner setup failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "owner setup could not be completed"})
			}
		})
		mux.HandleFunc("GET /api/encryption/status", func(w http.ResponseWriter, r *http.Request) {
			setExportNoStore(w)
			repository, noteAccess, record := currentNotebookContext()
			protected := noteAccess != nil && noteAccess.enabled
			if noteAccess != nil && noteAccess.err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault protection status is unavailable"})
				return
			}
			protection := vaultProtectionState{}
			if metadataPath != "" && record.ID != "" && repository.Configured() {
				var err error
				protection, err = inspectVaultProtection(record, metadataPath)
				if err != nil || protection.protected != protected {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault protection status is unavailable"})
					return
				}
			}
			unlocked := false
			generation := uint64(0)
			if sessions != nil && sessions.Authenticated(r.Context()) {
				var err error
				unlocked, generation, err = keyManager.SessionStatus(record.ID, sessions.CurrentHash(r.Context()))
				if err != nil && !errors.Is(err, encryption.ErrSessionRequired) {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault lock status is unavailable"})
					return
				}
			}
			passwordMode := "login"
			authState, err := authService.State(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault password mode is unavailable"})
				return
			}
			mfaEnabled, err := authService.MFAEnabled(r.Context())
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault security status is unavailable"})
				return
			}
			if authState.EncryptionPasswordSeparate {
				passwordMode = "separate"
			}
			w.Header().Set("Cache-Control", "no-store, private")
			writeJSON(w, http.StatusOK, map[string]any{
				"vaultId": record.ID, "protected": protected, "locked": !unlocked,
				"generation": generation, "recoveryCodeConfigured": protection.hasRecoveryEnvelope,
				"unlockPasswordMode": passwordMode, "activationEnabled": false,
				"autoLockMinutes": int(encryption.DefaultIdleLockTimeout.Minutes()), "mfaEnabled": mfaEnabled,
			})
		})
		mux.HandleFunc("POST /api/encryption/unlock", func(w http.ResponseWriter, r *http.Request) {
			setExportNoStore(w)
			if sessions == nil || !sessions.Authenticated(r.Context()) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
			var input struct {
				Password string `json:"password"`
			}
			if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
				return
			}
			if input.Password == "" || len(input.Password) > 1024 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vault password is required"})
				return
			}
			clientIP, release, allowed := beginCredentialAttempt(w, r, auth.ThrottleOperationSensitive)
			if !allowed {
				return
			}
			defer release()
			_, noteAccess, record := currentNotebookContext()
			if noteAccess == nil || !noteAccess.enabled {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "vault protection is not active", "code": "encryption_not_active"})
				return
			}
			if noteAccess.err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encrypted vault is unavailable"})
				return
			}
			protection, err := inspectVaultProtection(record, metadataPath)
			if err != nil || !protection.protected || !protection.hasEnvelope {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault password envelope is unavailable"})
				return
			}
			envelopeStore, err := encryption.NewVaultKeyEnvelopeStore(protection.envelopePath)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault password envelope is unavailable"})
				return
			}
			envelope, err := envelopeStore.Load()
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault password envelope is unavailable"})
				return
			}
			vaultKey, err := encryption.DecryptVaultKey(input.Password, envelope)
			input.Password = ""
			if err != nil {
				if !recordCredentialFailure(w, r, auth.ThrottleOperationSensitive, clientIP) {
					return
				}
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "vault password is incorrect", "code": "invalid_credentials"})
				return
			}
			defer clear(vaultKey)
			if _, err := noteAccess.catalogForKey(vaultKey); err != nil {
				logger.Warn("vault unlock integrity check failed", "vault", record.ID)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encrypted vault content is unavailable"})
				return
			}
			sessionHash := sessions.CurrentHash(r.Context())
			if !sessions.Authenticated(r.Context()) || len(sessionHash) != sha256.Size {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
			if err := keyManager.Unlock(record.ID, sessionHash, vaultKey); err != nil {
				if errors.Is(err, encryption.ErrVaultKeyMismatch) {
					writeJSON(w, http.StatusConflict, map[string]string{"error": "vault key state changed; retry unlock"})
					return
				}
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault could not be unlocked"})
				return
			}
			if err := loginThrottle.SuccessOperation(r.Context(), auth.ThrottleOperationSensitive, clientIP); err != nil {
				logger.Warn("clear vault unlock throttle failed", "operation", auth.ThrottleOperationSensitive)
			}
			unlocked, generation, err := keyManager.SessionStatus(record.ID, sessionHash)
			if err != nil || !unlocked {
				writeJSON(w, http.StatusLocked, map[string]string{"error": "vault lock state changed; retry unlock"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"vaultId": record.ID, "unlocked": true, "generation": generation})
		})
		mux.HandleFunc("POST /api/encryption/lock", func(w http.ResponseWriter, r *http.Request) {
			setExportNoStore(w)
			vaultID := currentNotebookID()
			if err := keyManager.LockVault(vaultID, encryption.LockReasonManual); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encryption lock is unavailable"})
				return
			}
			unlocked, generation, err := keyManager.SessionStatus(vaultID, sessions.CurrentHash(r.Context()))
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encryption lock is unavailable"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"vaultId": vaultID, "locked": !unlocked, "generation": generation})
		})
		mux.HandleFunc("POST /api/encryption/activity", func(w http.ResponseWriter, r *http.Request) {
			err := keyManager.Touch(r.URL.Query().Get("vaultId"), sessions.CurrentHash(r.Context()))
			switch {
			case err == nil:
				writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
			case errors.Is(err, encryption.ErrVaultLocked):
				writeJSON(w, http.StatusLocked, map[string]string{"error": "vault is locked"})
			case errors.Is(err, encryption.ErrSessionRequired):
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated session is required"})
			case errors.Is(err, encryption.ErrInvalidVaultID):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid vault ID"})
			default:
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encryption activity state is unavailable"})
			}
		})
		mux.HandleFunc("GET /api/encryption/lock-events", func(w http.ResponseWriter, r *http.Request) {
			events, cancel, err := keyManager.Subscribe(r.URL.Query().Get("vaultId"), sessions.CurrentHash(r.Context()))
			if err != nil {
				if errors.Is(err, encryption.ErrSessionRequired) {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated session is required"})
				} else if errors.Is(err, encryption.ErrKeyManagerClosed) {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encryption lock events are unavailable"})
				} else {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid encryption lock subscription"})
				}
				return
			}
			defer cancel()
			flusher, ok := w.(http.Flusher)
			if !ok {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock events are unavailable"})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			for {
				select {
				case event, open := <-events:
					if !open {
						return
					}
					encoded, marshalErr := json.Marshal(event)
					if marshalErr != nil {
						return
					}
					if _, writeErr := fmt.Fprintf(w, "event: encryption-lock\ndata: %s\n\n", encoded); writeErr != nil {
						return
					}
					flusher.Flush()
					if event.Reason == encryption.LockReasonLogout || event.Reason == encryption.LockReasonSession || event.Reason == encryption.LockReasonRevoked || event.Reason == encryption.LockReasonRestart {
						return
					}
				case <-r.Context().Done():
					return
				}
			}
		})
	}
	mux.HandleFunc("GET /api/notebook", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"name": currentNotebookName(), "configured": currentRepository().Configured()})
	})
	mux.HandleFunc("GET /api/notebooks", func(w http.ResponseWriter, _ *http.Request) {
		registry, err := loadNotebookRegistry(metadataPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusOK, map[string]any{"activeId": "", "notebooks": []notebookRecord{}})
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notebook registry is unavailable"})
			return
		}
		items := make([]map[string]string, 0, len(registry.Entries))
		for _, notebook := range registry.Entries {
			items = append(items, map[string]string{"id": notebook.ID, "name": notebook.Name})
		}
		writeJSON(w, http.StatusOK, map[string]any{"activeId": registry.ActiveID, "notebooks": items})
	})
	mux.HandleFunc("POST /api/notebooks/{notebookID}/activate", func(w http.ResponseWriter, r *http.Request) {
		notebookSwitchMu.Lock()
		defer notebookSwitchMu.Unlock()
		record, err := findNotebook(metadataPath, r.PathValue("notebookID"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "notebook not found"})
			} else {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notebook could not be activated"})
			}
			return
		}
		nextRepository, err := files.NewRepository(record.LocalPath)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notebook files are unavailable"})
			return
		}
		nextNoteAccess := newProtectedNoteAccess(nextRepository, metadataPath, record)
		if err := setActiveNotebook(metadataPath, record.ID); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notebook could not be activated"})
			return
		}
		activeMu.Lock()
		repository = nextRepository
		activeNoteAccess = nextNoteAccess
		activeRecord = record
		activeNotebookName = record.Name
		activeMu.Unlock()
		writeJSON(w, http.StatusOK, record)
	})
	mux.HandleFunc("PATCH /api/notebooks/{notebookID}", func(w http.ResponseWriter, r *http.Request) {
		notebookID := r.PathValue("notebookID")
		var input struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if notebookID == "" || strings.ContainsAny(notebookID, "/\\\x00") || input.Name == "" || len(input.Name) > 100 || strings.ContainsAny(input.Name, "\r\n\x00") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a valid notebook name is required"})
			return
		}
		record, err := renameNotebook(metadataPath, notebookID, input.Name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "notebook not found"})
			} else {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notebook name could not be saved"})
			}
			return
		}
		activeMu.Lock()
		if current, activeErr := loadActiveNotebook(metadataPath); activeErr == nil && current.ID == notebookID {
			activeNotebookName = input.Name
		}
		activeMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"id": record.ID, "name": record.Name})
	})
	mux.HandleFunc("GET /api/notebooks/{notebookID}/health", func(w http.ResponseWriter, r *http.Request) {
		record, err := findNotebook(metadataPath, r.PathValue("notebookID"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "notebook not found"})
			return
		}
		localState, localMessage := "ok", "Local files are available"
		if _, repositoryErr := files.NewRepository(record.LocalPath); repositoryErr != nil {
			localState, localMessage = "failed", "Local files cannot be opened"
		}
		writeState, writeMessage := "ok", "Local storage is writable"
		if localState == "ok" {
			probe, probeErr := os.CreateTemp(record.LocalPath, ".varianotes-health-*")
			if probeErr != nil {
				writeState, writeMessage = "failed", "VariaNotes cannot write to this notebook"
			} else {
				probeName := probe.Name()
				closeErr := probe.Close()
				removeErr := os.Remove(probeName)
				if closeErr != nil || removeErr != nil {
					writeState, writeMessage = "failed", "VariaNotes cannot safely update this notebook"
				}
			}
		} else {
			writeState, writeMessage = "failed", "Write access cannot be checked until local files are available"
		}
		writeJSON(w, http.StatusOK, map[string]any{"localFiles": map[string]string{"state": localState, "message": localMessage}, "writeAccess": map[string]string{"state": writeState, "message": writeMessage}})
	})
	mux.HandleFunc("GET /api/notebooks/{notebookID}/export", func(w http.ResponseWriter, r *http.Request) {
		handleNotebookExport(w, r, logger, metadataPath, sessions, keyManager, exportGrants, false)
	})
	mux.HandleFunc("POST /api/notebooks/{notebookID}/export/reauth", func(w http.ResponseWriter, r *http.Request) {
		handleNotebookExportReauthentication(w, r, metadataPath, sessions, keyManager, exportGrants)
	})
	mux.HandleFunc("POST /api/notebooks/{notebookID}/export", func(w http.ResponseWriter, r *http.Request) {
		handleNotebookExport(w, r, logger, metadataPath, sessions, keyManager, exportGrants, true)
	})
	mux.HandleFunc("DELETE /api/notebooks/{notebookID}", func(w http.ResponseWriter, r *http.Request) {
		notebookID := r.PathValue("notebookID")
		if notebookID == "" || strings.ContainsAny(notebookID, "/\\\x00") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid notebook ID"})
			return
		}
		record, findErr := findNotebook(metadataPath, notebookID)
		if findErr != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "notebook not found"})
			return
		}
		activeBeforeRemoval, activeErr := loadActiveNotebook(metadataPath)
		wasActive := activeErr == nil && activeBeforeRemoval.ID == notebookID
		deleteLocal := r.URL.Query().Get("deleteLocal") == "true"
		if deleteLocal {
			var input struct {
				Confirmation string `json:"confirmation"`
			}
			if !decodeJSON(w, r, &input) {
				return
			}
			if input.Confirmation != record.Name {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type the exact notebook name to confirm local file deletion"})
				return
			}
		}
		if err := removeLocalNotebook(metadataPath, notebookID); err != nil {
			switch {
			case errors.Is(err, os.ErrNotExist):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "notebook not found"})
			case err.Error() == "active notebook cannot be removed":
				writeJSON(w, http.StatusConflict, map[string]string{"error": "switch to another notebook before removing this local notebook"})
			default:
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "notebook registration could not be removed"})
			}
			return
		}
		if deleteLocal {
			notebookBase := strings.TrimSpace(notebooksDirectory)
			base, baseErr := filepath.Abs(notebookBase)
			target, targetErr := filepath.Abs(record.LocalPath)
			if baseErr == nil {
				base, baseErr = filepath.EvalSymlinks(base)
			}
			if notebookBase == "" || baseErr != nil || targetErr != nil || !isManagedNotebookDirectory(base, target) {
				if wasActive {
					_ = registerActiveNotebook(metadataPath, record)
				} else {
					_ = registerNotebookWithoutActivation(metadataPath, record)
				}
				writeJSON(w, http.StatusConflict, map[string]string{"error": "local files were retained because their location could not be verified safely"})
				return
			}
			if err := os.RemoveAll(target); err != nil {
				if wasActive {
					_ = registerActiveNotebook(metadataPath, record)
				} else {
					_ = registerNotebookWithoutActivation(metadataPath, record)
				}
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "registration was restored because local files could not be deleted"})
				return
			}
		}
		if wasActive {
			emptyRepository, _ := files.NewRepository("")
			activeMu.Lock()
			repository = emptyRepository
			activeNoteAccess = nil
			activeRecord = notebookRecord{}
			activeNotebookName = "Notebooks"
			activeMu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/repository/tree", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var catalog encryption.Catalog
			if err := lease.UseKey(func(key []byte) error {
				var loadErr error
				catalog, loadErr = noteAccess.catalogForKey(key)
				return loadErr
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			err := lease.Commit(func() error {
				tree, treeErr := repository.TreeWithCatalog(catalog)
				if treeErr != nil {
					return treeErr
				}
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, map[string]any{"entries": tree})
				return nil
			})
			if err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		tree, err := repository.Tree()
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": tree})
	})
	mux.HandleFunc("POST /api/repository/search", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Query string `json:"query"`
		}
		if !decodeJSONWithLimit(w, r, &input, maxSearchRequestBodySize) {
			return
		}
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			defer lease.Close()
			var results []files.SearchResult
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var searchErr error
				results, searchErr = repository.SearchWithCatalog(input.Query, catalog, key)
				return searchErr
			}); err != nil {
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, map[string]any{"results": results})
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		results, err := repository.Search(input.Query)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
	mux.HandleFunc("GET /api/repository/file", func(w http.ResponseWriter, r *http.Request) {
		filePath := r.URL.Query().Get("path")
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var markdown files.Markdown
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var readErr error
				markdown, _, readErr = repository.ReadMarkdownWithCatalog(filePath, catalog, key)
				return readErr
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, map[string]string{"path": filePath, "content": markdown.Content, "version": markdown.Version})
				return nil
			})
			if err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		markdown, err := repository.ReadMarkdown(filePath)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": filePath, "content": markdown.Content, "version": markdown.Version})
	})
	mux.HandleFunc("PUT /api/repository/file", func(w http.ResponseWriter, r *http.Request) {
		filePath := r.URL.Query().Get("path")
		var input struct {
			Content string `json:"content"`
			Version string `json:"version"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid save request"})
			return
		}
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var operation *files.PreparedMarkdownOperation
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var prepareErr error
				operation, _, prepareErr = repository.PrepareMarkdownWriteWithCatalog(filePath, input.Content, input.Version, catalog, key, noteAccess.store, noteAccess.journal)
				return prepareErr
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			err := lease.Commit(func() error {
				return operation.Commit(func(markdown files.Markdown) error {
					w.Header().Set("Cache-Control", "no-store, private")
					writeJSON(w, http.StatusOK, map[string]string{"path": filePath, "content": markdown.Content, "version": markdown.Version})
					return nil
				})
			})
			if err != nil {
				operation.Abort()
				noteAccess.needsRecovery()
				writeProtectedNoteError(w, err)
			}
			return
		}
		markdown, err := repository.WriteMarkdown(filePath, input.Content, input.Version)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": filePath, "content": markdown.Content, "version": markdown.Version})
	})
	mux.HandleFunc("POST /api/repository/file/resolve-conflict", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path            string `json:"path"`
			ExpectedVersion string `json:"expectedVersion"`
			Content         string `json:"content"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var operation *files.PreparedMarkdownOperation
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var prepareErr error
				operation, _, prepareErr = repository.PrepareMarkdownWriteWithCatalog(input.Path, input.Content, input.ExpectedVersion, catalog, key, noteAccess.store, noteAccess.journal)
				return prepareErr
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			err := lease.Commit(func() error {
				return operation.Commit(func(markdown files.Markdown) error {
					w.Header().Set("Cache-Control", "no-store, private")
					writeJSON(w, http.StatusOK, map[string]string{"path": input.Path, "content": markdown.Content, "version": markdown.Version})
					return nil
				})
			})
			if err != nil {
				operation.Abort()
				noteAccess.needsRecovery()
				writeProtectedNoteError(w, err)
			}
			return
		}
		markdown, err := repository.WriteMarkdown(input.Path, input.Content, input.ExpectedVersion)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": input.Path, "content": markdown.Content, "version": markdown.Version})
	})
	mux.HandleFunc("POST /api/repository/entries", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
			Type string `json:"type"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		repository, noteAccess := currentContentContext()
		if input.Type == "file" {
			lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
			if encrypted {
				if lease == nil {
					return
				}
				var operation *files.PreparedMarkdownOperation
				if err := lease.UseKey(func(key []byte) error {
					catalog, loadErr := noteAccess.catalogForKey(key)
					if loadErr != nil {
						return loadErr
					}
					var prepareErr error
					operation, _, prepareErr = repository.PrepareMarkdownCreateWithCatalog(input.Path, catalog, key, noteAccess.store, noteAccess.journal)
					return prepareErr
				}); err != nil {
					lease.Close()
					writeProtectedNoteError(w, err)
					return
				}
				err := lease.Commit(func() error {
					return operation.Commit(func(files.Markdown) error {
						w.Header().Set("Cache-Control", "no-store, private")
						writeJSON(w, http.StatusCreated, map[string]string{"path": input.Path, "type": input.Type})
						return nil
					})
				})
				if err != nil {
					operation.Abort()
					noteAccess.needsRecovery()
					writeProtectedNoteError(w, err)
				}
				return
			}
		}
		if err := repository.Create(input.Path, input.Type); err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"path": input.Path, "type": input.Type})
	})
	mux.HandleFunc("POST /api/repository/move", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Source           string `json:"source"`
			Target           string `json:"target"`
			RewriteToken     string `json:"rewriteToken"`
			ProtectionChoice string `json:"protectionChoice"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var preview files.MoveLinkPreview
			var bufferedKey []byte
			if err := lease.UseKey(func(key []byte) error {
				bufferedKey = append([]byte(nil), key...)
				return nil
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			defer clear(bufferedKey)
			err := lease.Commit(func() error {
				catalog, loadErr := noteAccess.catalogForKey(bufferedKey)
				if loadErr != nil {
					return loadErr
				}
				var moveErr error
				preview, moveErr = repository.MoveWithCatalog(input.Source, input.Target, input.RewriteToken, input.ProtectionChoice, catalog, bufferedKey, noteAccess.store, noteAccess.journal)
				if moveErr != nil {
					if errors.Is(moveErr, encryption.ErrTransactionNeedsRecovery) {
						noteAccess.needsRecovery()
					}
					return moveErr
				}
				if err := presentationStore.move(currentNotebookID(), input.Source, input.Target); err != nil {
					logger.Warn("move image presentation metadata failed", "error", err)
				}
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, map[string]any{"path": input.Target, "rewrites": preview.Rewrites})
				return nil
			})
			if err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		if input.RewriteToken == "" {
			preview, err := repository.PreviewMoveLinks(input.Source, input.Target)
			if err != nil {
				writeRepositoryError(w, err)
				return
			}
			if len(preview.Rewrites) > 0 {
				writeRepositoryError(w, files.ErrLinkPreviewRequired)
				return
			}
			if err := repository.Move(input.Source, input.Target); err != nil {
				writeRepositoryError(w, err)
				return
			}
			if err := presentationStore.move(currentNotebookID(), input.Source, input.Target); err != nil {
				logger.Warn("move image presentation metadata failed", "error", err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"path": input.Target, "rewrites": []files.LinkRewrite{}})
			return
		}
		preview, err := repository.MoveWithLinkUpdates(input.Source, input.Target, input.RewriteToken)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		if err := presentationStore.move(currentNotebookID(), input.Source, input.Target); err != nil {
			logger.Warn("move image presentation metadata failed", "error", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": input.Target, "rewrites": preview.Rewrites})
	})
	mux.HandleFunc("POST /api/repository/move/preview", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Source string `json:"source"`
			Target string `json:"target"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var preview files.MoveLinkPreview
			err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var previewErr error
				preview, previewErr = repository.PreviewMoveLinksWithCatalog(input.Source, input.Target, catalog, key)
				return previewErr
			})
			if err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, preview)
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		preview, err := repository.PreviewMoveLinks(input.Source, input.Target)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, preview)
	})
	mux.HandleFunc("GET /api/repository/links", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var links []files.NoteLink
			err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var linksErr error
				links, linksErr = repository.NoteLinksWithCatalog(r.URL.Query().Get("path"), catalog, key)
				return linksErr
			})
			if err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, map[string]any{"links": links})
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		links, err := repository.NoteLinks(r.URL.Query().Get("path"))
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"links": links})
	})
	mux.HandleFunc("DELETE /api/repository/entry", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var item files.TrashItem
			err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var moveErr error
				item, moveErr = repository.MoveToTrashWithCatalog(r.URL.Query().Get("path"), catalog, key, noteAccess.store, noteAccess.journal)
				return moveErr
			})
			if err != nil {
				lease.Close()
				if errors.Is(err, encryption.ErrTransactionNeedsRecovery) {
					noteAccess.needsRecovery()
				}
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				if err := presentationStore.deletePath(currentNotebookID(), item.OriginalPath); err != nil {
					logger.Warn("delete image presentation metadata failed", "error", err)
				}
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, item)
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		item, err := currentRepository().MoveToTrash(r.URL.Query().Get("path"))
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		if err := presentationStore.deletePath(currentNotebookID(), item.OriginalPath); err != nil {
			logger.Warn("delete image presentation metadata failed", "error", err)
		}
		writeJSON(w, http.StatusOK, item)
	})
	mux.HandleFunc("GET /api/repository/trash", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var items []files.TrashItem
			err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var listErr error
				items, listErr = repository.TrashItemsWithCatalog(catalog, key)
				return listErr
			})
			if err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, map[string]any{"items": items})
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		items, err := currentRepository().TrashItems()
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	})
	mux.HandleFunc("POST /api/repository/trash/{trashID}/restore", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ProtectionChoice string `json:"protectionChoice"`
		}
		if r.ContentLength != 0 && !decodeJSON(w, r, &input) {
			return
		}
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var item files.TrashItem
			err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var restoreErr error
				item, restoreErr = repository.RestoreTrashItemWithCatalog(r.PathValue("trashID"), input.ProtectionChoice, catalog, key, noteAccess.store, noteAccess.journal)
				return restoreErr
			})
			if err != nil {
				lease.Close()
				if errors.Is(err, encryption.ErrTransactionNeedsRecovery) {
					noteAccess.needsRecovery()
				}
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, item)
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		item, err := currentRepository().RestoreTrashItem(r.PathValue("trashID"))
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
	mux.HandleFunc("DELETE /api/repository/trash/{trashID}", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted {
			if lease == nil {
				return
			}
			var item files.TrashItem
			err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var deleteErr error
				item, deleteErr = repository.DeleteTrashItemWithCatalog(r.PathValue("trashID"), catalog, key, noteAccess.store, noteAccess.journal)
				return deleteErr
			})
			if err != nil {
				lease.Close()
				if errors.Is(err, encryption.ErrTransactionNeedsRecovery) {
					noteAccess.needsRecovery()
				}
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, item)
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		item, err := currentRepository().DeleteTrashItem(r.PathValue("trashID"))
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
	mux.HandleFunc("POST /api/repository/assets", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted && lease == nil {
			return
		}
		content, err := readAssetUpload(w, r)
		if err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.Is(err, files.ErrAssetTooLarge) || errors.As(err, &maxBytesError) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "image upload is too large"})
			} else {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid image upload"})
			}
			return
		}
		defer clear(content)
		note := r.URL.Query().Get("note")
		if encrypted {
			var operation *files.PreparedAssetOperation
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var prepareErr error
				operation, _, prepareErr = repository.PrepareAssetUploadWithCatalog(note, content, catalog, key, noteAccess.store, noteAccess.journal)
				return prepareErr
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			err := lease.Commit(func() error {
				return operation.Commit(func(asset files.Asset) error {
					writeJSON(w, http.StatusCreated, map[string]string{"path": asset.RelativePath})
					return nil
				})
			})
			if err != nil {
				operation.Abort()
				noteAccess.needsRecovery()
				writeProtectedNoteError(w, err)
			}
			return
		}
		asset, err := repository.SaveAsset(note, bytes.NewReader(content))
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"path": asset.RelativePath})
	})
	mux.HandleFunc("GET /api/repository/asset", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted && lease == nil {
			return
		}
		writeAsset := func(asset files.Asset, cacheControl string) {
			defer clear(asset.Content)
			w.Header().Set("Content-Type", asset.ContentType)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", cacheControl)
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(asset.Content)
			}
		}
		if encrypted {
			var asset files.Asset
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				var readErr error
				asset, _, readErr = repository.ReadAssetWithCatalog(r.URL.Query().Get("note"), r.URL.Query().Get("path"), catalog, key)
				return readErr
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				writeAsset(asset, "no-store, private")
				return nil
			}); err != nil {
				clear(asset.Content)
				writeProtectedNoteError(w, err)
			}
			return
		}
		asset, err := repository.ReadAsset(r.URL.Query().Get("note"), r.URL.Query().Get("path"))
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeAsset(asset, "no-store")
	})
	mux.HandleFunc("GET /api/repository/image-presentations", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		note := r.URL.Query().Get("note")
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted && lease == nil {
			return
		}
		load := func() (map[string]imagePresentationSize, error) {
			presentations, err := presentationStore.list(currentNotebookID(), note)
			if err != nil {
				return nil, err
			}
			return presentations, nil
		}
		if encrypted {
			var presentations map[string]imagePresentationSize
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				if _, _, readErr := repository.ReadMarkdownWithCatalog(note, catalog, key); readErr != nil {
					return readErr
				}
				var listErr error
				presentations, listErr = load()
				return listErr
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				w.Header().Set("Cache-Control", "no-store, private")
				writeJSON(w, http.StatusOK, map[string]any{"presentations": presentations})
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		if _, err := repository.ReadMarkdown(note); err != nil {
			writeRepositoryError(w, err)
			return
		}
		presentations, err := load()
		if err != nil {
			logger.Warn("read image presentation metadata failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image presentation settings are unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"presentations": presentations})
	})
	mux.HandleFunc("PUT /api/repository/image-presentation", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Note          string                `json:"note"`
			Image         string                `json:"image"`
			Size          imagePresentationSize `json:"size"`
			PreviousImage string                `json:"previousImage"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted && lease == nil {
			return
		}
		if !input.Size.valid() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "image size must be small, medium, large, or full"})
			return
		}
		set := func() error {
			return presentationStore.set(currentNotebookID(), input.Note, input.Image, input.Size, input.PreviousImage)
		}
		if encrypted {
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				asset, _, readErr := repository.ReadAssetWithCatalog(input.Note, input.Image, catalog, key)
				clear(asset.Content)
				if readErr != nil {
					return readErr
				}
				if input.PreviousImage != "" {
					previous, _, previousErr := repository.ReadAssetWithCatalog(input.Note, input.PreviousImage, catalog, key)
					clear(previous.Content)
					if previousErr != nil {
						return previousErr
					}
				}
				return nil
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				if err := set(); err != nil {
					logger.Warn("write image presentation metadata failed", "error", err)
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image presentation setting could not be saved"})
					return nil
				}
				writeJSON(w, http.StatusOK, map[string]string{"image": input.Image, "size": string(input.Size)})
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		if _, err := repository.ReadAsset(input.Note, input.Image); err != nil {
			writeRepositoryError(w, err)
			return
		}
		if input.PreviousImage != "" {
			if _, err := repository.ReadAsset(input.Note, input.PreviousImage); err != nil {
				writeRepositoryError(w, err)
				return
			}
		}
		if err := set(); err != nil {
			logger.Warn("write image presentation metadata failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image presentation setting could not be saved"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"image": input.Image, "size": string(input.Size)})
	})
	mux.HandleFunc("DELETE /api/repository/image-presentation", func(w http.ResponseWriter, r *http.Request) {
		repository, noteAccess := currentContentContext()
		lease, encrypted := beginProtectedNoteLease(w, r, noteAccess, sessions, keyManager)
		if encrypted && lease == nil {
			return
		}
		note, image := r.URL.Query().Get("note"), r.URL.Query().Get("image")
		remove := func() error { return presentationStore.deleteImage(currentNotebookID(), note, image) }
		if encrypted {
			if err := lease.UseKey(func(key []byte) error {
				catalog, loadErr := noteAccess.catalogForKey(key)
				if loadErr != nil {
					return loadErr
				}
				asset, _, readErr := repository.ReadAssetWithCatalog(note, image, catalog, key)
				clear(asset.Content)
				if readErr != nil {
					return readErr
				}
				return nil
			}); err != nil {
				lease.Close()
				writeProtectedNoteError(w, err)
				return
			}
			if err := lease.Commit(func() error {
				if err := remove(); err != nil {
					logger.Warn("delete image presentation metadata failed", "error", err)
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image presentation setting could not be removed"})
					return nil
				}
				w.WriteHeader(http.StatusNoContent)
				return nil
			}); err != nil {
				writeProtectedNoteError(w, err)
			}
			return
		}
		if _, err := repository.ReadAsset(note, image); err != nil {
			writeRepositoryError(w, err)
			return
		}
		if err := remove(); err != nil {
			logger.Warn("delete image presentation metadata failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "image presentation setting could not be removed"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/repository/assets/unreferenced", func(w http.ResponseWriter, _ *http.Request) {
		if _, noteAccess := currentContentContext(); rejectUnimplementedProtectedOperation(w, noteAccess) {
			return
		}
		assets, err := currentRepository().UnreferencedAssets()
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
	})
	mux.HandleFunc("POST /api/repository/assets/cleanup", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Paths []string `json:"paths"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		if _, noteAccess := currentContentContext(); rejectUnimplementedProtectedOperation(w, noteAccess) {
			return
		}
		result, err := currentRepository().DeleteUnreferencedAssets(input.Paths)
		if err != nil {
			writeRepositoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/notebooks/local", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		if input.Name == "" || len(input.Name) > 100 || strings.ContainsAny(input.Name, "\r\n\x00") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a valid notebook name is required"})
			return
		}
		notebookSwitchMu.Lock()
		defer notebookSwitchMu.Unlock()
		notebookBase := strings.TrimSpace(notebooksDirectory)
		if notebookBase == "" || metadataPath == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault creation is not configured on this server"})
			return
		}
		id, localPath, err := createNotebookDirectory(notebookBase)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "vault folder could not be created"})
			return
		}
		record := notebookRecord{ID: id, Name: input.Name, LocalPath: localPath}
		createdRepository, err := files.NewRepository(localPath)
		if err != nil {
			removeManagedNotebookDirectory(notebookBase, id, localPath)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "vault folder could not be opened"})
			return
		}
		createdNoteAccess := newProtectedNoteAccess(createdRepository, metadataPath, record)
		if err := registerActiveNotebook(metadataPath, record); err != nil {
			removeManagedNotebookDirectory(notebookBase, id, localPath)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "vault could not be registered"})
			return
		}
		activeMu.Lock()
		repository = createdRepository
		activeNoteAccess = createdNoteAccess
		activeRecord = record
		activeNotebookName = record.Name
		activeMu.Unlock()
		writeJSON(w, http.StatusCreated, record)
	})
	mux.HandleFunc("POST /api/notebooks/adopt", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name   string `json:"name"`
			Folder string `json:"folder"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		input.Folder = strings.TrimSpace(input.Folder)
		if input.Name == "" || len(input.Name) > 100 || strings.ContainsAny(input.Name, "\r\n\x00") || input.Folder == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a valid vault name and folder are required"})
			return
		}
		notebookSwitchMu.Lock()
		defer notebookSwitchMu.Unlock()
		notebookBase := strings.TrimSpace(notebooksDirectory)
		if notebookBase == "" || metadataPath == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault adoption is not configured on this server"})
			return
		}
		localPath, err := adoptNotebookDirectory(notebookBase, input.Folder)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose an existing direct child folder inside the configured vault directory"})
			return
		}
		registry, err := loadNotebookRegistry(metadataPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault registry is unavailable"})
			return
		}
		for _, existing := range registry.Entries {
			if filepath.Clean(existing.LocalPath) == filepath.Clean(localPath) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "this folder is already registered as a vault"})
				return
			}
		}
		id, err := newNotebookID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "vault could not be registered"})
			return
		}
		repositoryForVault, err := files.NewRepository(localPath)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the selected folder cannot be opened as a vault"})
			return
		}
		record := notebookRecord{ID: id, Name: input.Name, LocalPath: localPath}
		vaultNoteAccess := newProtectedNoteAccess(repositoryForVault, metadataPath, record)
		if err := registerActiveNotebook(metadataPath, record); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "vault could not be registered"})
			return
		}
		activeMu.Lock()
		repository = repositoryForVault
		activeNoteAccess = vaultNoteAccess
		activeRecord = record
		activeNotebookName = record.Name
		activeMu.Unlock()
		writeJSON(w, http.StatusCreated, record)
	})
	assets, err := fs.Sub(frontend.Files, "dist")
	if err != nil {
		panic(err)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "API endpoint not found"})
	})
	mux.Handle("/", spaHandler(assets))

	handler := http.Handler(mux)
	if sessions != nil {
		handler = sessions.LoadAndSave(authenticationBoundary(authService, sessions, csrfProtection(authService, sessions, requestIdentity, handler)))
	}
	return requestLogger(logger, securityHeaders(sameOriginProtection(requestIdentity, trustedOrigins, handler))), nil
}

const maxRequestBodySize = (10 << 20) + (64 << 10)
const maxSearchRequestBodySize = 8 << 10
const maxAssetRequestBodySize = (10 << 20) + (1 << 20)
const maxAuthRequestBodySize = 4 << 10

func readAssetUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAssetRequestBodySize)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, err
	}
	part, err := reader.NextPart()
	if err != nil {
		return nil, err
	}
	if part.FormName() != "file" || part.FileName() == "" {
		_ = part.Close()
		return nil, errors.New("image file is required")
	}
	content, readErr := io.ReadAll(io.LimitReader(part, files.MaxAssetSize+1))
	closeErr := part.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		clear(content)
		return nil, err
	}
	if int64(len(content)) > files.MaxAssetSize {
		clear(content)
		return nil, files.ErrAssetTooLarge
	}
	extra, err := reader.NextPart()
	if err == nil {
		_ = extra.Close()
		clear(content)
		return nil, errors.New("unexpected multipart field")
	}
	if !errors.Is(err, io.EOF) {
		clear(content)
		return nil, err
	}
	return content, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeJSONWithLimit(w, r, target, maxRequestBodySize)
}

func decodeJSONWithLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return false
	}
	return true
}

func writeRepositoryError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "repository operation failed"
	switch {
	case errors.Is(err, files.ErrNotConfigured):
		status, message = http.StatusServiceUnavailable, "repository is not configured"
	case errors.Is(err, files.ErrInvalidPath):
		status, message = http.StatusBadRequest, "invalid repository path"
	case errors.Is(err, files.ErrNotMarkdown):
		status, message = http.StatusBadRequest, "note path must end in .md"
	case errors.Is(err, files.ErrInvalidType):
		status, message = http.StatusBadRequest, "entry type must be file or directory"
	case errors.Is(err, files.ErrAlreadyExists):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, files.ErrRestoreCollision):
		status, message = http.StatusConflict, "The original location is already in use. Rename or move the existing item before restoring."
	case errors.Is(err, files.ErrTrashReferencedAsset):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, files.ErrInvalidTrashID):
		status, message = http.StatusBadRequest, "invalid trash item"
	case errors.Is(err, files.ErrLinkPreviewRequired):
		status, message = http.StatusConflict, "Review the internal note-link updates before moving this item."
	case errors.Is(err, files.ErrMoveProtectionChoiceRequired):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "move_protection_choice_required"})
		return
	case errors.Is(err, files.ErrMoveDecryptIntoProtected), errors.Is(err, files.ErrInvalidMoveProtectionChoice):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, files.ErrMoveRollbackFailed):
		status, message = http.StatusServiceUnavailable, files.ErrMoveRollbackFailed.Error()
	case errors.Is(err, files.ErrFileTooLarge):
		status, message = http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, files.ErrSearchQueryTooLong):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, files.ErrAssetTooLarge):
		status, message = http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, files.ErrUnsupportedMedia):
		status, message = http.StatusUnsupportedMediaType, err.Error()
	case errors.Is(err, files.ErrConflict):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, os.ErrNotExist):
		status, message = http.StatusNotFound, "Markdown file not found"
	case errors.Is(err, os.ErrPermission):
		status, message = http.StatusForbidden, "repository path is not readable"
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func spaHandler(files fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested == "." {
			requested = "index.html"
		}
		if info, err := fs.Stat(files, requested); err == nil && !info.IsDir() {
			if contentType := mime.TypeByExtension(path.Ext(requested)); contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data: blob: https: http:; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=()")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") && w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func authenticationBoundary(authService *auth.Service, sessions *auth.Sessions, next http.Handler) http.Handler {
	if authService == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readOnlyMethod := r.Method == http.MethodGet || r.Method == http.MethodHead
		public := !strings.HasPrefix(r.URL.Path, "/api/") ||
			(readOnlyMethod && r.URL.Path == "/api/health") ||
			(readOnlyMethod && r.URL.Path == "/api/auth/status") ||
			(r.Method == http.MethodPost && r.URL.Path == "/api/auth/setup") ||
			(r.Method == http.MethodPost && (r.URL.Path == "/api/auth/recovery/prepare" || r.URL.Path == "/api/auth/recovery/confirm")) ||
			(r.Method == http.MethodPost && (r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/login/mfa"))
		if public {
			next.ServeHTTP(w, r)
			return
		}
		state, err := authService.State(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication state is unavailable"})
			return
		}
		if state.Mode == auth.ModeLocal && !state.SetupCompleted {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner setup is required", "code": "setup_required"})
			return
		}
		if state.Mode == auth.ModeLocal && !sessions.Authenticated(r.Context()) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required", "code": "authentication_required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func csrfProtection(authService *auth.Service, sessions *auth.Sessions, identity *auth.RequestIdentity, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		state, err := authService.State(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication state is unavailable"})
			return
		}
		if state.Mode == auth.ModeDisabled {
			next.ServeHTTP(w, r)
			return
		}
		if (r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/login/mfa" || r.URL.Path == "/api/auth/setup" || r.URL.Path == "/api/auth/recovery/prepare" || r.URL.Path == "/api/auth/recovery/confirm") && !sessions.Authenticated(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		if !sessions.ValidCSRFToken(r.Context(), r.Header.Get("X-CSRF-Token")) {
			_ = authService.RecordSecurityEvent(r.Context(), "csrf", "rejected", "client="+auth.ClientReference(identity.ClientIP(r)))
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed", "code": "csrf_invalid"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOriginProtection(identity *auth.RequestIdentity, configuredOrigins string, next http.Handler) http.Handler {
	trusted := make(map[string]bool)
	for _, value := range strings.Split(configuredOrigins, ",") {
		if origin := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "/")); origin != "" {
			trusted[origin] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		origin := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.Header.Get("Origin")), "/"))
		if origin == "" {
			referer := strings.TrimSpace(r.Header.Get("Referer"))
			if referer != "" {
				parsed, err := url.Parse(referer)
				if err == nil {
					origin = strings.ToLower(parsed.Scheme + "://" + parsed.Host)
				}
			}
		}
		if origin != "" && !trusted[origin] {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != identity.Scheme(r) || !strings.EqualFold(parsed.Host, r.Host) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
				return
			}
		} else if origin == "" && r.Header.Get("Sec-Fetch-Site") != "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "request origin is required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.ReplaceAll(r.Method, "\n", "")
		method = strings.ReplaceAll(method, "\r", "")
		requestPath := strings.ReplaceAll(r.URL.Path, "\n", "")
		requestPath = strings.ReplaceAll(requestPath, "\r", "")
		logger.Info("http request", "method", sanitizeLogValue(method), "path", sanitizeLogValue(requestPath))
		next.ServeHTTP(w, r)
	})
}

// sanitizeLogValue keeps attacker-controlled request fields on one structured
// log record and removes terminal/control sequences. The limit also prevents a
// request target from creating disproportionate log volume.
func sanitizeLogValue(value string) string {
	const maxRunes = 1024
	clean := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || character == utf8.RuneError {
			return '�'
		}
		return character
	}, value)
	characters := []rune(clean)
	if len(characters) > maxRunes {
		return string(characters[:maxRunes]) + "…"
	}
	return clean
}

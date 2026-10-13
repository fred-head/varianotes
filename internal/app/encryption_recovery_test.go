package app

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

func TestRecoveryCodeActivationRequiresConfirmationAndStoresNoCode(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 2, true)
	service := openRecoveryTestService(t, metadataPath)
	defer service.Close()
	session := sha256.Sum256([]byte("recovery setup session"))

	challenge, code, err := service.BeginEncryptionRecoveryCode(t.Context(), "original vault password", "", session[:])
	if err != nil {
		t.Fatal(err)
	}
	assertNoPersistedRecoveryCode(t, filepath.Dir(metadataPath), code)
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		if state.hasRecoveryEnvelope {
			t.Fatalf("recovery envelope for %s became active before confirmation", entry.ID)
		}
	}
	if err := service.ConfirmEncryptionRecoveryCode(t.Context(), challenge, code, false, session[:]); !errors.Is(err, auth.ErrRecoveryNotConfirmed) {
		t.Fatalf("unconfirmed code error = %v", err)
	}
	otherSession := sha256.Sum256([]byte("another recovery setup session"))
	if err := service.ConfirmEncryptionRecoveryCode(t.Context(), challenge, code, true, otherSession[:]); !errors.Is(err, auth.ErrRecoveryChallengeExpired) {
		t.Fatalf("wrong-session confirmation error = %v", err)
	}
	wrongCode := code[:len(code)-1] + "0"
	if wrongCode == code {
		wrongCode = code[:len(code)-1] + "1"
	}
	if err := service.ConfirmEncryptionRecoveryCode(t.Context(), challenge, wrongCode, true, session[:]); !errors.Is(err, auth.ErrInvalidRecoveryCode) {
		t.Fatalf("wrong recovery code error = %v", err)
	}
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil || state.hasRecoveryEnvelope {
			t.Fatalf("wrong confirmation changed %s: state=%#v err=%v", entry.ID, state, err)
		}
	}
	if err := service.ConfirmEncryptionRecoveryCode(t.Context(), challenge, code, true, session[:]); err != nil {
		t.Fatal(err)
	}
	assertRecoveryCodeUnlocksVaults(t, entries, metadataPath, code)
	assertNoPersistedRecoveryCode(t, filepath.Dir(metadataPath), code)
}

func TestRecoveryCodeReplacesPasswordAndCodeAcrossVaults(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 2, true)
	service := openRecoveryTestService(t, metadataPath)
	defer service.Close()
	oldCode := activateRecoveryCode(t, service)

	newPassword := "replacement owner password"
	wrongOldCode := oldCode[:len(oldCode)-1] + "0"
	if wrongOldCode == oldCode {
		wrongOldCode = oldCode[:len(oldCode)-1] + "1"
	}
	if _, _, err := service.BeginPasswordRecovery(t.Context(), wrongOldCode, newPassword); !errors.Is(err, auth.ErrInvalidRecoveryCode) {
		t.Fatalf("wrong original code error = %v", err)
	}
	if err := service.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatalf("wrong original code changed login password: %v", err)
	}
	assertRecoveryCodeUnlocksVaults(t, entries, metadataPath, oldCode)
	challenge, newCode, err := service.BeginPasswordRecovery(t.Context(), oldCode, newPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatalf("password changed before new-code confirmation: %v", err)
	}
	assertRecoveryCodeUnlocksVaults(t, entries, metadataPath, oldCode)
	wrongCode := newCode[:len(newCode)-1] + "0"
	if wrongCode == newCode {
		wrongCode = newCode[:len(newCode)-1] + "1"
	}
	if err := service.ConfirmPasswordRecovery(t.Context(), challenge, newPassword, wrongCode, true); !errors.Is(err, auth.ErrInvalidRecoveryCode) {
		t.Fatalf("wrong replacement code error = %v", err)
	}
	if err := service.ConfirmPasswordRecovery(t.Context(), challenge, newPassword, newCode, false); !errors.Is(err, auth.ErrRecoveryNotConfirmed) {
		t.Fatalf("unconfirmed recovery error = %v", err)
	}
	if err := service.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatalf("unconfirmed recovery changed login password: %v", err)
	}

	insertLiveAuthSession(t, filepath.Join(filepath.Dir(metadataPath), "auth.db"))
	if err := service.ConfirmPasswordRecovery(t.Context(), challenge, newPassword, newCode, true); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyPassword(t.Context(), newPassword); err != nil {
		t.Fatalf("new password was not activated: %v", err)
	}
	if err := service.VerifyPassword(t.Context(), "original vault password"); !errors.Is(err, auth.ErrAuthentication) {
		t.Fatalf("old password remained active: %v", err)
	}
	separate, err := service.EncryptionPasswordSeparate(t.Context())
	if err != nil || separate {
		t.Fatalf("recovery did not bind vault password to login password: separate=%v err=%v", separate, err)
	}
	assertVaultPasswords(t, entries, metadataPath, newPassword)
	assertRecoveryCodeUnlocksVaults(t, entries, metadataPath, newCode)
	assertRecoveryCodeRejected(t, entries, metadataPath, oldCode)
	assertAuthSessionsRevoked(t, filepath.Join(filepath.Dir(metadataPath), "auth.db"))
	assertNoPersistedRecoveryCode(t, filepath.Dir(metadataPath), newCode)
}

func TestRecoveryCodeSetupUsesSeparateEncryptionPassword(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 1, true)
	service := openRecoveryTestService(t, metadataPath)
	defer service.Close()
	if err := service.RecoverPassword(t.Context(), "separate local login password"); err != nil {
		t.Fatal(err)
	}
	missingSession := sha256.Sum256([]byte("missing separate password"))
	if _, _, err := service.BeginEncryptionRecoveryCode(t.Context(), "separate local login password", "", missingSession[:]); !errors.Is(err, auth.ErrAuthentication) {
		t.Fatalf("missing separate encryption password error = %v", err)
	}
	session := sha256.Sum256([]byte("separate recovery setup session"))
	challenge, code, err := service.BeginEncryptionRecoveryCode(t.Context(), "separate local login password", "original vault password", session[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmEncryptionRecoveryCode(t.Context(), challenge, code, true, session[:]); err != nil {
		t.Fatal(err)
	}
	assertRecoveryCodeUnlocksVaults(t, entries, metadataPath, code)
}

func TestInterruptedPasswordRecoveryRestoresOldCodeBeforeServing(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 2, true)
	authPath := filepath.Join(filepath.Dir(metadataPath), "auth.db")
	service := openRecoveryTestService(t, metadataPath)
	oldCode := activateRecoveryCode(t, service)
	coordinator := NewRecoveryCodeCoordinator(metadataPath)
	newPassword := "interrupted replacement password"
	newCode := "VNRC1-" + strings.Repeat("A1B2C3D4-", 7) + "A1B2C3D4"
	planBytes, err := coordinator.PreparePasswordRecovery(t.Context(), oldCode, newPassword, newCode)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := decodeVaultRecoveryPlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	first := plan.Updates[0]
	firstState, err := inspectVaultProtection(entries[0], metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyRecoveryEnvelope(firstState.envelopePath, first.OldPasswordEnvelope, first.NewPasswordEnvelope, true, true, true); err != nil {
		t.Fatal(err)
	}
	if err := applyRecoveryEnvelope(firstState.recoveryEnvelopePath, first.OldRecoveryEnvelope, first.NewRecoveryEnvelope, first.OldRecoveryPresent, true, true); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"operation": "password", "plan": planBytes,
		"credential": map[string]any{
			"AlgorithmVersion": argon2.Version,
			"Parameters":       map[string]any{"MemoryKiB": 65536, "Iterations": 3, "Parallelism": 2, "SaltLength": 16, "KeyLength": 32},
			"Salt":             bytes.Repeat([]byte{3}, 16), "Hash": bytes.Repeat([]byte{4}, 32),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", authPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO auth_encryption_recovery_journal(id,phase,payload,created_at) VALUES(1,'prepared',?,?)`, payload, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	service = openRecoveryTestService(t, metadataPath)
	defer service.Close()
	if err := service.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatalf("prepared interruption changed the login credential: %v", err)
	}
	assertVaultPasswords(t, entries, metadataPath, "original vault password")
	assertRecoveryCodeUnlocksVaults(t, entries, metadataPath, oldCode)
	assertRecoveryCodeRejected(t, entries, metadataPath, newCode)
}

func TestInterruptedRecoveryCodeSetupRemovesUnconfirmedEnvelopes(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 2, true)
	authPath := filepath.Join(filepath.Dir(metadataPath), "auth.db")
	service := openRecoveryTestService(t, metadataPath)
	coordinator := NewRecoveryCodeCoordinator(metadataPath)
	code := "VNRC1-" + strings.Repeat("A1B2C3D4-", 7) + "A1B2C3D4"
	planBytes, err := coordinator.PrepareRecoveryCode(t.Context(), "original vault password", code)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := decodeVaultRecoveryPlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	first := plan.Updates[0]
	firstState, err := inspectVaultProtection(entries[0], metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyRecoveryEnvelope(firstState.recoveryEnvelopePath, nil, first.NewRecoveryEnvelope, false, true, true); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"operation": "code", "plan": planBytes})
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", authPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO auth_encryption_recovery_journal(id,phase,payload,created_at) VALUES(1,'prepared',?,?)`, payload, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	service = openRecoveryTestService(t, metadataPath)
	defer service.Close()
	if err := service.VerifyPassword(t.Context(), "original vault password"); err != nil {
		t.Fatalf("unconfirmed setup changed the password: %v", err)
	}
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil || state.hasRecoveryEnvelope {
			t.Fatalf("unconfirmed setup retained a recovery envelope for %s: state=%#v err=%v", entry.ID, state, err)
		}
	}
}

func TestPasswordRecoveryHTTPRevokesSessionAndLocksVaultKeys(t *testing.T) {
	metadataPath, entries, _ := passwordChangeFixture(t, 1, true)
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", metadataPath)
	service := openRecoveryTestService(t, metadataPath)
	defer service.Close()
	oldCode := activateRecoveryCode(t, service)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := auth.NewSessions(service, auth.DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	manager := encryption.NewKeyManager()
	handler, err := newHandlerWithKeyManager(logger, entries[0].LocalPath, service, sessions, manager)
	if err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"original vault password"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", login.Code, login.Body.String())
	}
	cookie, csrf := authRequestContextFromResponse(t, login)
	if cookie == nil || csrf == "" {
		t.Fatal("login did not establish a session and CSRF token")
	}
	sessionHash := sha256.Sum256([]byte(cookie.Value))
	if err := manager.Unlock(entries[0].ID, sessionHash[:], bytes.Repeat([]byte{8}, encryption.VaultKeySize)); err != nil {
		t.Fatal(err)
	}

	newPassword := "http recovered owner password"
	prepareBody, _ := json.Marshal(map[string]string{"recoveryCode": oldCode, "newPassword": newPassword})
	prepare := httptest.NewRecorder()
	handler.ServeHTTP(prepare, httptest.NewRequest(http.MethodPost, "/api/auth/recovery/prepare", bytes.NewReader(prepareBody)))
	if prepare.Code != http.StatusOK {
		t.Fatalf("password recovery prepare failed: %d %s", prepare.Code, prepare.Body.String())
	}
	var challenge struct {
		ChallengeID  string `json:"challengeId"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if err := json.Unmarshal(prepare.Body.Bytes(), &challenge); err != nil || challenge.ChallengeID == "" || challenge.RecoveryCode == "" {
		t.Fatalf("recovery prepare response is incomplete: %#v err=%v", challenge, err)
	}
	confirmBody, _ := json.Marshal(map[string]any{
		"challengeId": challenge.ChallengeID, "newPassword": newPassword,
		"recoveryCode": challenge.RecoveryCode, "confirmed": true,
	})
	confirm := httptest.NewRecorder()
	handler.ServeHTTP(confirm, httptest.NewRequest(http.MethodPost, "/api/auth/recovery/confirm", bytes.NewReader(confirmBody)))
	if confirm.Code != http.StatusOK || !strings.Contains(confirm.Body.String(), `"authenticated":false`) {
		t.Fatalf("password recovery confirmation failed: %d %s", confirm.Code, confirm.Body.String())
	}
	if manager.IsUnlocked(entries[0].ID, sessionHash[:]) {
		t.Fatal("password recovery left a vault key unlocked")
	}
	protected := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/repository/tree", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(protected, request)
	if protected.Code != http.StatusUnauthorized {
		t.Fatalf("password recovery left the old session active: %d %s", protected.Code, protected.Body.String())
	}
	if err := service.VerifyPassword(t.Context(), newPassword); err != nil {
		t.Fatalf("recovery endpoint did not replace the login password: %v", err)
	}
}

func openRecoveryTestService(t *testing.T, metadataPath string) *auth.Service {
	t.Helper()
	passwordCoordinator := NewPasswordChangeCoordinator(metadataPath)
	service, err := auth.Open(t.Context(), auth.Config{
		Mode: auth.ModeLocal, MetadataPath: filepath.Join(filepath.Dir(metadataPath), "auth.db"),
		PasswordCoordinator: passwordCoordinator, RecoveryCoordinator: NewRecoveryCodeCoordinator(metadataPath),
	}, passwordChangeLogger())
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !state.SetupCompleted {
		token, err := service.CreateBootstrapToken(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := service.CompleteSetup(t.Context(), token.Value, "original vault password"); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

func activateRecoveryCode(t *testing.T, service *auth.Service) string {
	t.Helper()
	session := sha256.Sum256([]byte("recovery setup session"))
	challenge, code, err := service.BeginEncryptionRecoveryCode(t.Context(), "original vault password", "", session[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmEncryptionRecoveryCode(t.Context(), challenge, code, true, session[:]); err != nil {
		t.Fatal(err)
	}
	return code
}

func assertRecoveryCodeUnlocksVaults(t *testing.T, entries []notebookRecord, metadataPath, code string) {
	t.Helper()
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		_, envelope, err := readRecoveryEnvelope(state.recoveryEnvelopePath)
		if err != nil {
			t.Fatalf("load %s recovery envelope: %v", entry.ID, err)
		}
		key, err := encryption.DecryptVaultKey(code, envelope)
		if err != nil {
			t.Fatalf("recovery code did not unlock %s: %v", entry.ID, err)
		}
		catalog, err := encryption.NewCatalogStore(state.catalogPath)
		if err != nil {
			t.Fatal(err)
		}
		catalogID, err := catalog.ReadVaultID()
		if err == nil {
			_, err = catalog.Load(key, catalogID)
		}
		clear(key)
		if err != nil {
			t.Fatalf("recovery code could not verify %s catalog: %v", entry.ID, err)
		}
	}
}

func assertRecoveryCodeRejected(t *testing.T, entries []notebookRecord, metadataPath, code string) {
	t.Helper()
	for _, entry := range entries {
		state, err := inspectVaultProtection(entry, metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		_, envelope, err := readRecoveryEnvelope(state.recoveryEnvelopePath)
		if err != nil {
			t.Fatal(err)
		}
		key, err := encryption.DecryptVaultKey(code, envelope)
		if err == nil {
			clear(key)
			t.Fatalf("outdated recovery code still unlocks %s", entry.ID)
		}
	}
}

func assertNoPersistedRecoveryCode(t *testing.T, root, code string) {
	t.Helper()
	err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(code)) {
			return fmt.Errorf("plaintext recovery code persisted in %s", filename)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertLiveAuthSession(t *testing.T, authPath string) {
	t.Helper()
	database, err := sql.Open("sqlite", authPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	hash := sha256.Sum256([]byte("existing authenticated session"))
	_, err = database.Exec(`INSERT INTO auth_sessions(session_id_hash,created_at,last_activity_at,idle_expires_at,absolute_expires_at,revoked_at,client_description,session_data) VALUES(?,?,?,?,?,NULL,'test',X'')`, hash[:], now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
}

func assertAuthSessionsRevoked(t *testing.T, authPath string) {
	t.Helper()
	database, err := sql.Open("sqlite", authPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var revoked sql.NullString
	if err := database.QueryRow(`SELECT revoked_at FROM auth_sessions WHERE client_description='test'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked.Valid {
		t.Fatal("password recovery left an existing session active")
	}
}

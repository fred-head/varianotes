package app

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
)

const exportVaultPassword = "current export vault password"
const exportNoteSentinel = "protected-export-sentinel-f184"

func TestProtectedNotebookExportReauthenticatesAndBindsOneUseGrant(t *testing.T) {
	harness := newProtectedExportHarness(t)
	requestJSON := func(method, target string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, target, bytes.NewReader(encoded))
		addAuthRequestContext(request, harness.cookie, harness.csrf)
		response := httptest.NewRecorder()
		harness.handler.ServeHTTP(response, request)
		return response
	}
	request := func(method, target string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, nil)
		addAuthRequestContext(request, harness.cookie, harness.csrf)
		response := httptest.NewRecorder()
		harness.handler.ServeHTTP(response, request)
		return response
	}
	reauth := func(password string) *httptest.ResponseRecorder {
		t.Helper()
		return requestJSON(http.MethodPost, "/api/notebooks/"+harness.vaultID+"/export/reauth", map[string]string{"password": password})
	}
	export := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		return requestJSON(http.MethodPost, "/api/notebooks/"+harness.vaultID+"/export", map[string]string{"token": token})
	}
	parseToken := func(response *httptest.ResponseRecorder) string {
		t.Helper()
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["token"] == "" {
			t.Fatalf("export grant missing: %d %s (err=%v)", response.Code, response.Body.String(), err)
		}
		return body["token"]
	}

	preflight := request(http.MethodGet, "/api/notebooks/"+harness.vaultID+"/export")
	if preflight.Code != http.StatusPreconditionRequired ||
		!strings.Contains(preflight.Body.String(), "\"code\":\"export_reauthentication_required\"") ||
		strings.Contains(preflight.Body.String(), exportNoteSentinel) ||
		preflight.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("protected export did not require fresh authentication safely: %d %s", preflight.Code, preflight.Body.String())
	}
	wrongPassword := reauth("wrong vault password")
	if wrongPassword.Code != http.StatusUnauthorized || strings.Contains(wrongPassword.Body.String(), "wrong vault password") {
		t.Fatalf("wrong export password response=%d %s", wrongPassword.Code, wrongPassword.Body.String())
	}

	issued := reauth(exportVaultPassword)
	if issued.Code != http.StatusOK || issued.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("export reauthentication failed: %d %s", issued.Code, issued.Body.String())
	}
	changedToken := parseToken(issued)
	if err := os.WriteFile(filepath.Join(harness.root, "Public.md"), []byte("changed after password prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := export(changedToken)
	if changed.Code != http.StatusConflict || strings.Contains(changed.Body.String(), exportNoteSentinel) {
		t.Fatalf("changed export snapshot was not rejected: %d %s", changed.Code, changed.Body.String())
	}
	if replay := export(changedToken); replay.Code != http.StatusUnauthorized {
		t.Fatalf("one-use export grant replay returned %d: %s", replay.Code, replay.Body.String())
	}

	issued = reauth(exportVaultPassword)
	lockedToken := parseToken(issued)
	if err := harness.manager.LockVault(harness.vaultID, encryption.LockReasonManual); err != nil {
		t.Fatal(err)
	}
	if revoked := export(lockedToken); revoked.Code != http.StatusUnauthorized {
		t.Fatalf("grant survived a vault lock: %d %s", revoked.Code, revoked.Body.String())
	}
	if err := harness.manager.Unlock(harness.vaultID, harness.sessionHash[:], harness.key); err != nil {
		t.Fatal(err)
	}

	issued = reauth(exportVaultPassword)
	validToken := parseToken(issued)
	archive := export(validToken)
	if archive.Code != http.StatusOK || archive.Header().Get("Content-Type") != "application/zip" || archive.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("protected export failed: %d %s", archive.Code, archive.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Body.Bytes()), int64(archive.Body.Len()))
	if err != nil {
		t.Fatalf("export response is not a ZIP: %v", err)
	}
	items := make(map[string][]byte)
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			t.Fatal(err)
		}
		items[entry.Name] = data
	}
	if string(items["private/Secret.md"]) != "# Secret\n"+exportNoteSentinel ||
		string(items["Public.md"]) != "changed after password prompt" ||
		!bytes.Equal(items["private/Secret.assets/secret.png"], harness.asset) {
		t.Fatalf("export did not preserve logical plaintext contents: %#v", items)
	}
	for name := range items {
		if strings.HasSuffix(name, ".age") || strings.HasSuffix(name, ".json") {
			t.Fatalf("archive exposed ciphertext or application metadata: %q", name)
		}
	}
	for _, sentinel := range []string{exportVaultPassword, exportNoteSentinel} {
		if strings.Contains(harness.logs.String(), sentinel) {
			t.Fatalf("export password or note sentinel appeared in logs: %q", sentinel)
		}
	}
	for _, directory := range []string{harness.root, harness.metadataRoot} {
		err := filepath.WalkDir(directory, func(filename string, entry os.DirEntry, walkErr error) error {
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
			if bytes.Contains(data, []byte(exportVaultPassword)) || bytes.Contains(data, []byte(exportNoteSentinel)) {
				t.Errorf("export plaintext or password persisted in %s", filename)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	issued = reauth(exportVaultPassword)
	logoutToken := parseToken(issued)
	logout := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	addAuthRequestContext(logoutRequest, harness.cookie, harness.csrf)
	harness.handler.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout failed: %d %s", logout.Code, logout.Body.String())
	}
	if afterLogout := export(logoutToken); afterLogout.Code != http.StatusUnauthorized {
		t.Fatalf("grant survived logout: %d %s", afterLogout.Code, afterLogout.Body.String())
	}
}

func TestNotebookExportDoesNotPromptWhenCatalogHasNoProtectedContent(t *testing.T) {
	harness := newExportHarness(t, false)
	request := httptest.NewRequest(http.MethodGet, "/api/notebooks/"+harness.vaultID+"/export", nil)
	addAuthRequestContext(request, harness.cookie, harness.csrf)
	response := httptest.NewRecorder()
	harness.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("unprotected catalog export asked for reauthentication: %d %s", response.Code, response.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatalf("unprotected catalog export is not a ZIP: %v", err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != "Public.md" {
		t.Fatalf("unprotected catalog export entries = %#v", reader.File)
	}
}

type protectedExportHarness struct {
	handler      http.Handler
	manager      *encryption.KeyManager
	root         string
	metadataRoot string
	vaultID      string
	cookie       *http.Cookie
	csrf         string
	key          []byte
	asset        []byte
	sessionHash  [32]byte
	logs         *bytes.Buffer
}

func newProtectedExportHarness(t *testing.T) protectedExportHarness {
	return newExportHarness(t, true)
}

func newExportHarness(t *testing.T, includeProtected bool) protectedExportHarness {
	t.Helper()
	root := t.TempDir()
	metadataRoot := t.TempDir()
	registryPath := filepath.Join(metadataRoot, "app", "notebooks.json")
	vaultID := "22222222222222222222222222222222"
	if includeProtected {
		if err := os.MkdirAll(filepath.Join(root, "private", "Secret.assets"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Public.md"), []byte("public before prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNotebookRegistry(registryPath, notebookRegistry{
		ActiveID: vaultID,
		Entries:  []notebookRecord{{ID: vaultID, Name: "Protected", LocalPath: root}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VARIANOTES_NOTEBOOK_METADATA", registryPath)

	key := bytes.Repeat([]byte{0x71}, encryption.VaultKeySize)
	catalog, err := encryption.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var asset []byte
	if includeProtected {
		noteID, err := encryption.NewCatalogObjectID()
		if err != nil {
			t.Fatal(err)
		}
		noteBytes := []byte("# Secret\n" + exportNoteSentinel)
		noteCiphertext, noteEnvelope, err := encryption.EncryptContent(key, noteID, noteBytes)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Objects = append(catalog.Objects, encryption.CatalogObject{
			ObjectID: noteID, Kind: encryption.CatalogObjectNote, LogicalPath: "private/Secret.md",
			PhysicalPath: "private/Secret.md.age", CiphertextHash: encryption.CiphertextSHA256(noteCiphertext),
			ContentKeyEnvelope: &noteEnvelope, Protected: true,
		})
		asset = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x26}, 32)...)
		assetID, err := encryption.NewCatalogObjectID()
		if err != nil {
			t.Fatal(err)
		}
		assetCiphertext, assetEnvelope, err := encryption.EncryptContent(key, assetID, asset)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Objects = append(catalog.Objects, encryption.CatalogObject{
			ObjectID: assetID, Kind: encryption.CatalogObjectAsset, LogicalPath: "private/Secret.assets/secret.png",
			PhysicalPath: "private/Secret.assets/secret.png.age", CiphertextHash: encryption.CiphertextSHA256(assetCiphertext),
			ContentKeyEnvelope: &assetEnvelope, Protected: true,
		})
		if err := os.WriteFile(filepath.Join(root, "private", "Secret.md.age"), noteCiphertext, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "private", "Secret.assets", "secret.png.age"), assetCiphertext, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	metadataDirectory := vaultEncryptionDirectory(registryPath, vaultID)
	catalogStore, err := encryption.NewCatalogStore(filepath.Join(metadataDirectory, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalogStore.Save(catalog, key); err != nil {
		t.Fatal(err)
	}
	envelope, err := encryption.EncryptVaultKey(exportVaultPassword, key)
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

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	authService, err := auth.Open(t.Context(), auth.Config{Mode: auth.ModeLocal, MetadataPath: filepath.Join(metadataRoot, "auth.db")}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authService.Close() })
	sessions, err := auth.NewSessions(authService, auth.DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	manager := encryption.NewKeyManager()
	t.Cleanup(func() { _ = manager.Close() })
	handler, err := newHandlerWithKeyManager(logger, "", authService, sessions, manager)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := authService.CreateBootstrapToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	setupBody, err := json.Marshal(map[string]string{"bootstrapToken": bootstrap.Value, "password": "a sufficiently long owner password"})
	if err != nil {
		t.Fatal(err)
	}
	setup := httptest.NewRecorder()
	handler.ServeHTTP(setup, httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(setupBody)))
	if setup.Code != http.StatusOK {
		t.Fatalf("auth setup failed: %d %s", setup.Code, setup.Body.String())
	}
	cookie, csrf := authRequestContextFromResponse(t, setup)
	if cookie == nil {
		t.Fatal("auth setup did not create a session")
	}
	sessionHash := sha256.Sum256([]byte(cookie.Value))
	if err := manager.Unlock(vaultID, sessionHash[:], key); err != nil {
		t.Fatal(err)
	}
	return protectedExportHarness{
		handler: handler, manager: manager, root: root, metadataRoot: metadataRoot, vaultID: vaultID,
		cookie: cookie, csrf: csrf, key: key, asset: asset, sessionHash: sessionHash, logs: logs,
	}
}

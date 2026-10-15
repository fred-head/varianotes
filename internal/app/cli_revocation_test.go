package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/fred-head/varianotes/internal/files"
)

func TestCLIMFAResetRevokesInFlightUnlockAndProtectedPublication(t *testing.T) {
	t.Run("unlock after session check", func(t *testing.T) {
		paused, resume := make(chan struct{}), make(chan struct{})
		var release sync.Once
		harness := newExportHarnessWithHooks(t, true, nil, func() {
			close(paused)
			<-resume
		})
		t.Cleanup(func() { release.Do(func() { close(resume) }) })
		if err := harness.manager.LockVault(harness.vaultID, encryption.LockReasonManual); err != nil {
			t.Fatal(err)
		}
		request := func() *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodPost, "/api/encryption/unlock", bytes.NewBufferString(`{"password":"`+exportVaultPassword+`"}`))
			addAuthRequestContext(r, harness.cookie, harness.csrf)
			w := httptest.NewRecorder()
			harness.handler.ServeHTTP(w, r)
			return w
		}

		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- request() }()
		waitForCLIResetPause(t, paused)
		runMFAResetCLI(t, filepath.Join(harness.metadataRoot, "auth.db"))
		release.Do(func() { close(resume) })
		response := awaitCLIResetRequest(t, done)
		if response.Code == http.StatusOK {
			t.Fatalf("HTTP unlock succeeded after CLI reset-mfa completed: %d %s", response.Code, response.Body.String())
		}
		if harness.manager.IsUnlocked(harness.vaultID, harness.sessionHash[:]) {
			t.Fatal("CLI reset-mfa left a grant for the revoked session")
		}
	})

	t.Run("protected move before durable publication", func(t *testing.T) {
		paused, resume := make(chan struct{}), make(chan struct{})
		var release sync.Once
		harness := newExportHarnessWithMutationHook(t, true, func() {
			close(paused)
			<-resume
		})
		t.Cleanup(func() { release.Do(func() { close(resume) }) })
		r := httptest.NewRequest(http.MethodPost, "/api/repository/move", bytes.NewBufferString(`{"source":"private/Secret.md","target":"private/Moved.md","protectionChoice":"keep"}`))
		addAuthRequestContext(r, harness.cookie, harness.csrf)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			harness.handler.ServeHTTP(w, r)
			done <- w
		}()
		waitForCLIResetPause(t, paused)
		runMFAResetCLI(t, filepath.Join(harness.metadataRoot, "auth.db"))
		release.Do(func() { close(resume) })
		response := awaitCLIResetRequest(t, done)
		if response.Code == http.StatusOK {
			t.Fatalf("protected move published after CLI reset-mfa completed: %d %s", response.Code, response.Body.String())
		}
		if _, err := os.Stat(filepath.Join(harness.root, "private", "Secret.md.age")); err != nil {
			t.Fatalf("revoked protected move removed its source: %v", err)
		}
		if _, err := os.Stat(filepath.Join(harness.root, "private", "Moved.md.age")); !os.IsNotExist(err) {
			t.Fatalf("revoked protected move published its target: %v", err)
		}
	})

	t.Run("protected save before publication", func(t *testing.T) {
		paused, resume := make(chan struct{}), make(chan struct{})
		var release sync.Once
		enabled := false
		harness := newExportHarnessWithPublicationHook(t, true, nil, nil, func() {
			if enabled {
				close(paused)
				<-resume
			}
		})
		t.Cleanup(func() { release.Do(func() { close(resume) }) })
		request := func(method, target, body string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(method, target, bytes.NewBufferString(body))
			addAuthRequestContext(r, harness.cookie, harness.csrf)
			w := httptest.NewRecorder()
			harness.handler.ServeHTTP(w, r)
			return w
		}
		current := request(http.MethodGet, "/api/repository/file?path=private%2FSecret.md", "")
		if current.Code != http.StatusOK {
			t.Fatalf("read protected note before save: %d %s", current.Code, current.Body.String())
		}
		var note struct {
			Content string `json:"content"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(current.Body.Bytes(), &note); err != nil || note.Version == "" {
			t.Fatalf("protected note version missing: %#v %v", note, err)
		}
		newContent := "# Must Not Publish After MFA Reset\n"
		body, err := json.Marshal(map[string]string{"content": newContent, "version": note.Version})
		if err != nil {
			t.Fatal(err)
		}
		enabled = true
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			done <- request(http.MethodPut, "/api/repository/file?path=private%2FSecret.md", string(body))
		}()
		waitForCLIResetPause(t, paused)
		runMFAResetCLI(t, filepath.Join(harness.metadataRoot, "auth.db"))
		release.Do(func() { close(resume) })
		response := awaitCLIResetRequest(t, done)
		if response.Code == http.StatusOK {
			t.Fatalf("protected save published after CLI reset-mfa completed: %d %s", response.Code, response.Body.String())
		}
		repository, err := files.NewRepository(harness.root)
		if err != nil {
			t.Fatal(err)
		}
		registry := filepath.Join(harness.metadataRoot, "app", "notebooks.json")
		access := newProtectedNoteAccess(repository, registry, notebookRecord{ID: harness.vaultID, LocalPath: harness.root})
		catalog, err := access.catalogForKey(harness.key)
		if err != nil {
			t.Fatal(err)
		}
		stored, protected, err := repository.ReadMarkdownWithCatalog("private/Secret.md", catalog, harness.key)
		if err != nil || !protected || stored.Content != note.Content {
			t.Fatalf("revoked protected save changed note content: protected=%v content=%q error=%v", protected, stored.Content, err)
		}
	})
}

func TestCLIMFAResetWaitsForCancelledProtectedWork(t *testing.T) {
	cliPath := buildMFAResetCLI(t)

	t.Run("protected save", func(t *testing.T) {
		entered, resume := make(chan struct{}), make(chan struct{})
		var release sync.Once
		enabled := false
		harness := newExportHarnessWithSessionHook(t, true, nil, nil, nil, func() {
			if enabled {
				close(entered)
				<-resume
			}
		})
		t.Cleanup(func() { release.Do(func() { close(resume) }) })
		request := func(ctx context.Context, method, target, body string) *httptest.ResponseRecorder {
			r := httptest.NewRequestWithContext(ctx, method, target, bytes.NewBufferString(body))
			addAuthRequestContext(r, harness.cookie, harness.csrf)
			w := httptest.NewRecorder()
			harness.handler.ServeHTTP(w, r)
			return w
		}
		current := request(t.Context(), http.MethodGet, "/api/repository/file?path=private%2FSecret.md", "")
		if current.Code != http.StatusOK {
			t.Fatalf("read protected note before save: %d %s", current.Code, current.Body.String())
		}
		var note struct {
			Content string `json:"content"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(current.Body.Bytes(), &note); err != nil || note.Version == "" {
			t.Fatalf("protected note version missing: %#v %v", note, err)
		}
		newContent := "# Publication remains serialized with MFA reset\n"
		body, err := json.Marshal(map[string]string{"content": newContent, "version": note.Version})
		if err != nil {
			t.Fatal(err)
		}
		enabled = true
		requestCtx, cancelRequest := context.WithCancel(t.Context())
		requestDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			requestDone <- request(requestCtx, http.MethodPut, "/api/repository/file?path=private%2FSecret.md", string(body))
		}()
		waitForCLIResetPause(t, entered)
		cancelRequest()
		resetDone := startMFAResetCLI(t, cliPath, filepath.Join(harness.metadataRoot, "auth.db"))
		assertMFAResetStillRunning(t, resetDone)
		release.Do(func() { close(resume) })
		response := awaitCLIResetRequest(t, requestDone)
		if response.Code != http.StatusOK {
			t.Fatalf("already serialized protected save did not finish before reset: %d %s", response.Code, response.Body.String())
		}
		awaitMFAResetCLI(t, resetDone)

		repository, err := files.NewRepository(harness.root)
		if err != nil {
			t.Fatal(err)
		}
		registry := filepath.Join(harness.metadataRoot, "app", "notebooks.json")
		access := newProtectedNoteAccess(repository, registry, notebookRecord{ID: harness.vaultID, LocalPath: harness.root})
		catalog, err := access.catalogForKey(harness.key)
		if err != nil {
			t.Fatal(err)
		}
		stored, protected, err := repository.ReadMarkdownWithCatalog("private/Secret.md", catalog, harness.key)
		if err != nil || !protected || stored.Content != newContent {
			t.Fatalf("protected save was not fully published before reset returned: protected=%v content=%q error=%v", protected, stored.Content, err)
		}
	})

	t.Run("unlock", func(t *testing.T) {
		entered, resume := make(chan struct{}), make(chan struct{})
		var release sync.Once
		harness := newExportHarnessWithSessionHook(t, true, nil, nil, nil, func() {
			close(entered)
			<-resume
		})
		t.Cleanup(func() { release.Do(func() { close(resume) }) })
		if err := harness.manager.LockVault(harness.vaultID, encryption.LockReasonManual); err != nil {
			t.Fatal(err)
		}
		requestCtx, cancelRequest := context.WithCancel(t.Context())
		r := httptest.NewRequestWithContext(requestCtx, http.MethodPost, "/api/encryption/unlock", bytes.NewBufferString(`{"password":"`+exportVaultPassword+`"}`))
		addAuthRequestContext(r, harness.cookie, harness.csrf)
		requestDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			w := httptest.NewRecorder()
			harness.handler.ServeHTTP(w, r)
			requestDone <- w
		}()
		waitForCLIResetPause(t, entered)
		cancelRequest()
		resetDone := startMFAResetCLI(t, cliPath, filepath.Join(harness.metadataRoot, "auth.db"))
		assertMFAResetStillRunning(t, resetDone)
		release.Do(func() { close(resume) })
		response := awaitCLIResetRequest(t, requestDone)
		if response.Code != http.StatusOK {
			t.Fatalf("already serialized unlock did not finish before reset: %d %s", response.Code, response.Body.String())
		}
		awaitMFAResetCLI(t, resetDone)

		statusRequest := httptest.NewRequest(http.MethodGet, "/api/encryption/status", nil)
		addAuthRequestContext(statusRequest, harness.cookie, harness.csrf)
		statusResponse := httptest.NewRecorder()
		harness.handler.ServeHTTP(statusResponse, statusRequest)
		if statusResponse.Code != http.StatusUnauthorized {
			t.Fatalf("unlock session remained usable after CLI reset-mfa: %d %s", statusResponse.Code, statusResponse.Body.String())
		}
	})
}

type mfaResetCLIResult struct {
	output string
	err    error
}

func buildMFAResetCLI(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "varianotes")
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/varianotes")
	command.Dir = filepath.Join("..", "..")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build auth reset-mfa CLI: %v\n%s", err, output)
	}
	return binary
}

func startMFAResetCLI(t *testing.T, binary, authMetadataPath string) <-chan mfaResetCLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, "auth", "reset-mfa")
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "VARIANOTES_AUTH_MODE=local", "VARIANOTES_AUTH_METADATA="+authMetadataPath)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start actual auth reset-mfa CLI: %v", err)
	}
	done := make(chan mfaResetCLIResult, 1)
	go func() {
		err := command.Wait()
		done <- mfaResetCLIResult{output: stdout.String() + stderr.String(), err: err}
	}()
	return done
}

func assertMFAResetStillRunning(t *testing.T, done <-chan mfaResetCLIResult) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("CLI reset-mfa returned while protected work was still paused: error=%v output=%s", result.err, result.output)
	case <-time.After(7 * time.Second):
	}
}

func awaitMFAResetCLI(t *testing.T, done <-chan mfaResetCLIResult) {
	t.Helper()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("run actual auth reset-mfa CLI: %v\n%s", result.err, result.output)
		}
		if !strings.Contains(result.output, "Owner MFA reset. All browser sessions were revoked") {
			t.Fatalf("auth reset-mfa CLI did not confirm the reset: %s", result.output)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("auth reset-mfa CLI did not finish after protected work released")
	}
}

func waitForCLIResetPause(t *testing.T, paused <-chan struct{}) {
	t.Helper()
	select {
	case <-paused:
	case <-time.After(30 * time.Second):
		t.Fatal("HTTP request did not reach the deterministic pre-publication pause")
	}
}

func awaitCLIResetRequest(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-done:
		return response
	case <-time.After(30 * time.Second):
		t.Fatal("HTTP request did not resume after CLI reset-mfa")
		return nil
	}
}

func runMFAResetCLI(t *testing.T, authMetadataPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", "./cmd/varianotes", "auth", "reset-mfa")
	command.Dir = filepath.Join("..", "..")
	command.Env = append(os.Environ(), "VARIANOTES_AUTH_METADATA="+authMetadataPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run actual auth reset-mfa CLI: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Owner MFA reset. All browser sessions were revoked") {
		t.Fatalf("auth reset-mfa CLI did not confirm the reset: %s", output)
	}
}

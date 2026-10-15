package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fred-head/varianotes/internal/encryption"
)

func TestSQLiteSessionStoreHashesOpaqueTokenAndHonorsRevocation(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	store := &sqliteSessionStore{db: service.db}
	token := "plaintext-session-token-must-not-be-persisted"
	data := []byte("server-side-session-data")
	if err := store.CommitCtx(t.Context(), token, data, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var storedHash, storedData []byte
	if err := service.db.QueryRowContext(t.Context(), `SELECT session_id_hash, session_data FROM auth_sessions`).Scan(&storedHash, &storedData); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(storedHash, []byte(token)) || bytes.Equal(storedHash, []byte(token)) {
		t.Fatal("plaintext session token was persisted")
	}
	if !bytes.Equal(storedData, data) {
		t.Fatal("server-side session data was not persisted")
	}
	if found, ok, err := store.FindCtx(t.Context(), token); err != nil || !ok || !bytes.Equal(found, data) {
		t.Fatalf("stored session was not found: ok=%v err=%v", ok, err)
	}
	if err := store.DeleteCtx(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.FindCtx(t.Context(), token); err != nil || ok {
		t.Fatalf("revoked session remained active: ok=%v err=%v", ok, err)
	}
}

func TestSessionCookieSecurityOptions(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	sessions, err := NewSessions(service, DefaultSessionOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	if !sessions.manager.Cookie.Secure || !sessions.manager.Cookie.HttpOnly || sessions.manager.Cookie.Path != "/api" || sessions.manager.Cookie.Name != "__Secure-varianotes_session" {
		t.Fatalf("unsafe production cookie settings: %#v", sessions.manager.Cookie)
	}
	localSessions, err := NewSessions(service, DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	if localSessions.manager.Cookie.Secure || !localSessions.manager.Cookie.HttpOnly || localSessions.manager.Cookie.Path != "/api" || localSessions.manager.Cookie.Name != "varianotes_session" {
		t.Fatalf("unsafe local-development cookie settings: %#v", localSessions.manager.Cookie)
	}
}

func TestActiveSessionRevalidatesAdmissionWithoutRefreshingExpiry(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	sessions, err := NewSessions(service, DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	store := &sqliteSessionStore{db: service.db, codec: sessions.manager.Codec, defaultIdle: time.Hour}
	token := "active-revalidation-test-token"
	data, err := sessions.manager.Codec.Encode(time.Now().Add(time.Hour), map[string]any{sessionPrincipalKey: OwnerPrincipal})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"live", "revoked", "expired", "malformed"} {
		t.Run(mutation, func(t *testing.T) {
			if _, err := service.db.Exec(`DELETE FROM auth_sessions`); err != nil {
				t.Fatal(err)
			}
			if err := store.CommitCtx(t.Context(), token, data, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			ctx, err := sessions.manager.Load(t.Context(), token)
			if err != nil {
				t.Fatal(err)
			}
			if !sessions.Authenticated(ctx) {
				t.Fatal("test admission principal missing")
			}
			var activityBefore, expiryBefore string
			if err := service.db.QueryRow(`SELECT last_activity_at,idle_expires_at FROM auth_sessions`).Scan(&activityBefore, &expiryBefore); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "revoked":
				err = store.DeleteCtx(t.Context(), token)
			case "expired":
				_, err = service.db.Exec(`UPDATE auth_sessions SET absolute_expires_at=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano))
			case "malformed":
				_, err = service.db.Exec(`UPDATE auth_sessions SET absolute_expires_at='not-a-date'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			active, err := sessions.Active(ctx)
			if err != nil || active != (mutation == "live") {
				t.Fatalf("active=%v error=%v after %s", active, err, mutation)
			}
			if !sessions.Authenticated(ctx) {
				t.Fatal("admission principal unexpectedly changed")
			}
			var activityAfter, expiryAfter string
			if err := service.db.QueryRow(`SELECT last_activity_at,idle_expires_at FROM auth_sessions`).Scan(&activityAfter, &expiryAfter); err != nil {
				t.Fatal(err)
			}
			if activityAfter != activityBefore || expiryAfter != expiryBefore {
				t.Fatal("liveness read refreshed session activity")
			}
		})
	}
}

func TestSessionPublicationBarrierSerializesMFAReset(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	guardDone := make(chan error, 1)
	var resetDone chan error
	guardStarted, resetStarted := false, false
	guardFinished, resetFinished := false, false
	metadataPath := filepath.Join(t.TempDir(), "auth.db")
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: metadataPath}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	resetService, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: metadataPath}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		release.Do(func() { close(resume) })
		if guardStarted && !guardFinished {
			select {
			case <-guardDone:
			case <-time.After(5 * time.Second):
			}
		}
		if resetStarted && !resetFinished {
			select {
			case <-resetDone:
			case <-time.After(5 * time.Second):
			}
		}
		_ = resetService.Close()
	}()
	sessions, err := NewSessions(service, DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	store := &sqliteSessionStore{db: service.db, codec: sessions.manager.Codec, defaultIdle: time.Hour}
	token := "session-publication-barrier-test-token"
	data, err := sessions.manager.Codec.Encode(time.Now().Add(time.Hour), map[string]any{sessionPrincipalKey: OwnerPrincipal})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCtx(t.Context(), token, data, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ctx, err := sessions.manager.Load(t.Context(), token)
	if err != nil || !sessions.Authenticated(ctx) {
		t.Fatalf("load authenticated test session: authenticated=%v error=%v", sessions.Authenticated(ctx), err)
	}

	guardStarted = true
	go func() {
		guardDone <- sessions.WithActiveSession(ctx, func() error {
			close(entered)
			<-resume
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active-session publication barrier did not enter")
	}

	resetDone = make(chan error, 1)
	resetStarted = true
	go func() { resetDone <- resetService.ResetMFA(t.Context()) }()
	deadline := time.Now().Add(5 * time.Second)
	for resetService.db.Stats().InUse == 0 && time.Now().Before(deadline) {
		select {
		case err := <-resetDone:
			resetFinished = true
			t.Fatalf("MFA reset completed inside the active publication barrier: %v", err)
		case <-time.After(time.Millisecond):
		}
	}
	if resetService.db.Stats().InUse == 0 {
		t.Fatal("MFA reset did not start its database transaction")
	}
	select {
	case err := <-resetDone:
		resetFinished = true
		t.Fatalf("MFA reset bypassed the active publication barrier: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release.Do(func() { close(resume) })
	select {
	case err := <-guardDone:
		guardFinished = true
		if err != nil {
			t.Fatalf("active publication barrier failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active publication barrier did not release")
	}
	select {
	case err := <-resetDone:
		resetFinished = true
		if err != nil {
			t.Fatalf("MFA reset failed after publication released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MFA reset did not finish after publication released")
	}

	called := false
	if err := sessions.WithActiveSession(ctx, func() error { called = true; return nil }); !errors.Is(err, ErrAuthentication) || called {
		t.Fatalf("revoked session passed publication barrier: called=%v err=%v", called, err)
	}
}

func TestSessionPublicationBarrierRejectsCancellationAtCallbackStart(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	sessions, err := NewSessions(service, DefaultSessionOptions(false))
	if err != nil {
		t.Fatal(err)
	}
	store := &sqliteSessionStore{db: service.db, codec: sessions.manager.Codec, defaultIdle: time.Hour}
	token := "session-callback-start-cancellation-test-token"
	data, err := sessions.manager.Codec.Encode(time.Now().Add(time.Hour), map[string]any{sessionPrincipalKey: OwnerPrincipal})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCtx(t.Context(), token, data, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sessionCtx, err := sessions.manager.Load(t.Context(), token)
	if err != nil || !sessions.Authenticated(sessionCtx) {
		t.Fatalf("load authenticated test session: authenticated=%v error=%v", sessions.Authenticated(sessionCtx), err)
	}

	requestCtx, cancelRequest := context.WithCancel(sessionCtx)
	request := httptest.NewRequestWithContext(requestCtx, http.MethodPost, "/api/protected-publication", nil)
	paused, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	releaseWork := func() { release.Do(func() { close(resume) }) }
	defer releaseWork()
	markerPath := filepath.Join(t.TempDir(), "published")
	manager := encryption.NewKeyManager()
	t.Cleanup(func() { _ = manager.Close() })
	hash := sessionHash(token)
	var callbackStarted bool
	publicationDone := make(chan error, 1)
	finished := false
	defer func() {
		cancelRequest()
		releaseWork()
		if !finished {
			select {
			case <-publicationDone:
			case <-time.After(5 * time.Second):
			}
		}
	}()
	go func() {
		publicationDone <- sessions.withActiveSessionBeforeStart(request.Context(), func() {
			close(paused)
			<-resume
		}, func() error {
			callbackStarted = true
			if err := os.WriteFile(markerPath, []byte("published"), 0o600); err != nil {
				return err
			}
			return manager.Unlock("33333333333333333333333333333333", hash[:], bytes.Repeat([]byte{0x31}, encryption.VaultKeySize))
		})
	}()
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("publication barrier did not pause immediately before callback admission")
	}
	cancelRequest()
	releaseWork()
	var barrierErr error
	select {
	case barrierErr = <-publicationDone:
		finished = true
	case <-time.After(5 * time.Second):
		t.Fatal("publication barrier did not return after cancellation")
	}
	_, fileErr := os.Stat(markerPath)
	filePublished := fileErr == nil
	grantPublished := manager.IsUnlocked("33333333333333333333333333333333", hash[:])
	if callbackStarted || filePublished || grantPublished || !errors.Is(barrierErr, context.Canceled) || !errors.Is(barrierErr, ErrSessionBarrier) {
		t.Fatalf("canceled callback admission: started=%v filePublished=%v grantPublished=%v error=%v (file stat error=%v)", callbackStarted, filePublished, grantPublished, barrierErr, fileErr)
	}
	if err := service.ResetMFA(t.Context()); err != nil {
		t.Fatalf("MFA reset remained blocked after canceled callback admission: %v", err)
	}
}

func TestSQLiteSessionStoreRejectsExpiredSessions(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	store := &sqliteSessionStore{db: service.db}
	token := "expired-opaque-session"
	if err := store.CommitCtx(t.Context(), token, []byte("session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(t.Context(), `UPDATE auth_sessions SET idle_expires_at=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.FindCtx(t.Context(), token); err != nil || ok {
		t.Fatalf("idle-expired session remained usable: ok=%v err=%v", ok, err)
	}

	token = "absolute-expired-opaque-session"
	if err := store.CommitCtx(t.Context(), token, []byte("session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.ExecContext(t.Context(), `UPDATE auth_sessions SET absolute_expires_at=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.FindCtx(t.Context(), token); err != nil || ok {
		t.Fatalf("absolute-expired session remained usable: ok=%v err=%v", ok, err)
	}
}

func TestSessionListingHidesInactiveRowsAndSessionWritesPruneThem(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	store := &sqliteSessionStore{db: service.db}

	if err := store.CommitCtx(t.Context(), "expired-session", []byte("expired"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCtx(t.Context(), "revoked-session", []byte("revoked"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCtx(t.Context(), "revoked-session"); err != nil {
		t.Fatal(err)
	}
	expiredHash := sessionHash("expired-session")
	if _, err := service.db.ExecContext(t.Context(), `UPDATE auth_sessions SET idle_expires_at=?, absolute_expires_at=? WHERE session_id_hash=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), expiredHash[:]); err != nil {
		t.Fatal(err)
	}

	records, err := service.Sessions(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("inactive sessions were listed: %#v", records)
	}
	var beforeCleanup int
	if err := service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_sessions`).Scan(&beforeCleanup); err != nil {
		t.Fatal(err)
	}
	if beforeCleanup != 2 {
		t.Fatalf("session listing mutated inactive rows: count=%d", beforeCleanup)
	}

	if err := store.CommitCtx(t.Context(), "fresh-session", []byte("fresh"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_sessions`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("session write did not prune inactive rows: count=%d", remaining)
	}
	records, err = service.Sessions(t.Context(), nil)
	if err != nil || len(records) != 1 {
		t.Fatalf("fresh session was not listed after cleanup: records=%#v err=%v", records, err)
	}
}

func TestSQLiteSessionStoreDoesNotResurrectRevokedSession(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	store := &sqliteSessionStore{db: service.db, defaultIdle: time.Hour}
	const token = "revoked-session-token"
	if err := store.CommitCtx(t.Context(), token, []byte("before"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCtx(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCtx(t.Context(), token, []byte("stale-request"), time.Now().Add(time.Hour)); !errors.Is(err, errSessionInactive) {
		t.Fatalf("stale request resurrected revoked session: %v", err)
	}
	if _, found, err := store.FindCtx(t.Context(), token); err != nil || found {
		t.Fatalf("revoked session became active: found=%v err=%v", found, err)
	}
}

func TestSQLiteSessionStoreRefreshesIdleActivityWithoutExtendingAbsoluteExpiry(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	store := &sqliteSessionStore{db: service.db, defaultIdle: time.Hour}
	const token = "active-session-token"
	absolute := time.Now().Add(30 * time.Minute).UTC()
	if err := store.CommitCtx(t.Context(), token, []byte("session"), absolute); err != nil {
		t.Fatal(err)
	}
	oldActivity := time.Now().Add(-10 * time.Minute).UTC()
	if _, err := service.db.ExecContext(t.Context(), `UPDATE auth_sessions SET last_activity_at=?, idle_expires_at=?`, oldActivity.Format(time.RFC3339Nano), time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindCtx(t.Context(), token); err != nil || !found {
		t.Fatalf("active session was not found: found=%v err=%v", found, err)
	}
	var activityText, idleText, absoluteText string
	if err := service.db.QueryRowContext(t.Context(), `SELECT last_activity_at, idle_expires_at, absolute_expires_at FROM auth_sessions`).Scan(&activityText, &idleText, &absoluteText); err != nil {
		t.Fatal(err)
	}
	activity, _ := time.Parse(time.RFC3339Nano, activityText)
	idleExpiry, _ := time.Parse(time.RFC3339Nano, idleText)
	storedAbsolute, _ := time.Parse(time.RFC3339Nano, absoluteText)
	if !activity.After(oldActivity) || idleExpiry.After(storedAbsolute) || storedAbsolute.Sub(absolute) > time.Second || absolute.Sub(storedAbsolute) > time.Second {
		t.Fatalf("unexpected activity refresh: activity=%v idle=%v absolute=%v", activity, idleExpiry, storedAbsolute)
	}
}

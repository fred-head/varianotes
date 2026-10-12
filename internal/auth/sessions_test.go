package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
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

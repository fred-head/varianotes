package auth

import (
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

func TestLoginThrottleUsesProgressiveTemporaryDelays(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	clock := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	throttle := NewLoginThrottle(service)
	throttle.now = func() time.Time { return clock }
	client := netip.MustParseAddr("203.0.113.9")
	for attempt := 1; attempt <= 3; attempt++ {
		if delay, err := throttle.Failure(t.Context(), client); err != nil || delay != 0 {
			t.Fatalf("attempt %d delayed unexpectedly: %v %v", attempt, delay, err)
		}
	}
	if delay, err := throttle.Failure(t.Context(), client); err != nil || delay != time.Second {
		t.Fatalf("fourth attempt delay = %v, err=%v", delay, err)
	}
	if remaining, err := throttle.Check(t.Context(), client); err != nil || remaining != time.Second {
		t.Fatalf("active throttle = %v, err=%v", remaining, err)
	}
	clock = clock.Add(2 * time.Second)
	if remaining, err := throttle.Check(t.Context(), client); err != nil || remaining != 0 {
		t.Fatalf("temporary throttle did not expire: %v, err=%v", remaining, err)
	}
	if err := throttle.Success(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	if remaining, err := throttle.Check(t.Context(), client); err != nil || remaining != 0 {
		t.Fatalf("successful login did not clear throttle: %v, err=%v", remaining, err)
	}
}

func TestLoginThrottleHasBoundedDelay(t *testing.T) {
	if delay := progressiveDelay(100, 3); delay != 30*time.Second {
		t.Fatalf("unbounded delay: %v", delay)
	}
}

func TestLoginThrottleBoundsConcurrentPasswordWork(t *testing.T) {
	throttle := &LoginThrottle{active: make(chan struct{}, 1)}
	if !throttle.Begin() {
		t.Fatal("first login work was rejected")
	}
	if throttle.Begin() {
		t.Fatal("concurrent login work was accepted")
	}
	throttle.End()
	if !throttle.Begin() {
		t.Fatal("login work remained locked")
	}
	throttle.End()
}

func TestSensitiveCredentialThrottleIsIndependentAndValidated(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	throttle := NewLoginThrottle(service)
	client := netip.MustParseAddr("203.0.113.10")
	for range 4 {
		if _, err := throttle.FailureOperation(t.Context(), ThrottleOperationSensitive, client); err != nil {
			t.Fatal(err)
		}
	}
	if delay, err := throttle.CheckOperation(t.Context(), ThrottleOperationSensitive, client); err != nil || delay <= 0 {
		t.Fatalf("sensitive throttle was not activated: delay=%v err=%v", delay, err)
	}
	if delay, err := throttle.CheckOperation(t.Context(), ThrottleOperationLogin, client); err != nil || delay != 0 {
		t.Fatalf("sensitive attempts unexpectedly blocked normal login: delay=%v err=%v", delay, err)
	}
	if _, err := throttle.CheckOperation(t.Context(), "user-controlled", client); err == nil {
		t.Fatal("invalid throttle operation was accepted")
	}
}

func TestSecurityEventsAreCoalescedRetainedAndBounded(t *testing.T) {
	service, err := Open(t.Context(), Config{Mode: ModeLocal, MetadataPath: filepath.Join(t.TempDir(), "auth.db")}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for range 3 {
		if err := service.RecordSecurityEvent(t.Context(), "login", "throttled", "client=stable"); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_security_events WHERE details='client=stable'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate security events were not coalesced: count=%d err=%v", count, err)
	}

	tx, err := service.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-securityEventRetention - time.Hour).UTC().Format(time.RFC3339Nano)
	for index := 0; index < maximumStoredSecurityEvents+10; index++ {
		occurredAt := time.Now().UTC().Format(time.RFC3339Nano)
		if index == 0 {
			occurredAt = old
		}
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO auth_security_events(event_type,occurred_at,outcome,details) VALUES('test',?,'failure',?)`, occurredAt, fmt.Sprintf("event-%d", index)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordSecurityEvent(t.Context(), "maintenance", "success", "retention"); err != nil {
		t.Fatal(err)
	}
	if err := service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_security_events`).Scan(&count); err != nil || count > maximumStoredSecurityEvents {
		t.Fatalf("security event retention is unbounded: count=%d err=%v", count, err)
	}
	if err := service.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM auth_security_events WHERE occurred_at=?`, old).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired security event was retained: count=%d err=%v", count, err)
	}
}

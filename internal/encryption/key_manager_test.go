package encryption

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestKeyManagerStartsLockedAndRequiresPerSessionUnlock(t *testing.T) {
	manager := newKeyManager(time.Minute)
	defer manager.Close()
	firstSession := bytes.Repeat([]byte{1}, 32)
	secondSession := bytes.Repeat([]byte{2}, 32)
	vaultKey := bytes.Repeat([]byte{3}, VaultKeySize)

	if manager.IsUnlocked("vault-a", firstSession) {
		t.Fatal("new key manager unexpectedly started unlocked")
	}
	firstEvents, cancelFirst, err := manager.Subscribe("vault-a", firstSession)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelFirst()
	secondEvents, cancelSecond, err := manager.Subscribe("vault-a", secondSession)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelSecond()
	assertLockEvent(t, firstEvents, true, LockReasonInitial)
	assertLockEvent(t, secondEvents, true, LockReasonInitial)

	if err := manager.Unlock("vault-a", firstSession, vaultKey); err != nil {
		t.Fatal(err)
	}
	unlocked, generation, err := manager.SessionStatus("vault-a", firstSession)
	if err != nil || !unlocked || generation == 0 {
		t.Fatalf("session status after unlock = (%v, %d, %v)", unlocked, generation, err)
	}
	assertLockEvent(t, firstEvents, false, LockReasonUnlocked)
	if manager.IsUnlocked("vault-a", secondSession) {
		t.Fatal("unlocking one session granted another session access")
	}
	select {
	case event := <-secondEvents:
		t.Fatalf("another session received an unlock notification: %#v", event)
	default:
	}

	wrongKey := bytes.Repeat([]byte{4}, VaultKeySize)
	if err := manager.Unlock("vault-a", secondSession, wrongKey); !errors.Is(err, ErrVaultKeyMismatch) {
		t.Fatalf("wrong key for an unlocked vault = %v, want mismatch", err)
	}
	if manager.IsUnlocked("vault-a", secondSession) {
		t.Fatal("failed unlock granted the second session access")
	}
	if err := manager.Unlock("vault-a", secondSession, vaultKey); err != nil {
		t.Fatal(err)
	}
	assertLockEvent(t, secondEvents, false, LockReasonUnlocked)
	if !manager.IsUnlocked("vault-a", firstSession) || !manager.IsUnlocked("vault-a", secondSession) {
		t.Fatal("each session should have a grant after its own successful unlock")
	}

	lease, err := manager.BeginUse("vault-a", firstSession)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	var usedKey []byte
	if err := lease.UseKey(func(key []byte) error {
		usedKey = append([]byte(nil), key...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(usedKey, vaultKey) {
		t.Fatal("key lease returned the wrong vault key")
	}
	clear(usedKey)

	if err := manager.LockAll(LockReasonManual); err != nil {
		t.Fatal(err)
	}
	assertLockEvent(t, firstEvents, true, LockReasonManual)
	assertLockEvent(t, secondEvents, true, LockReasonManual)
	if manager.IsUnlocked("vault-a", firstSession) || manager.IsUnlocked("vault-a", secondSession) {
		t.Fatal("manual lock retained a session grant")
	}
	published := false
	if err := lease.Commit(func() error { published = true; return nil }); !errors.Is(err, ErrVaultLocked) {
		t.Fatalf("stale response commit = %v, want locked", err)
	}
	if published {
		t.Fatal("a response from an earlier lock generation was published")
	}
}

func TestKeyManagerIdleTimerLocksWithoutBackgroundRequestsExtendingIt(t *testing.T) {
	manager := newKeyManager(120 * time.Millisecond)
	defer manager.Close()
	session := bytes.Repeat([]byte{5}, 32)
	vaultKey := bytes.Repeat([]byte{6}, VaultKeySize)
	events, cancel, err := manager.Subscribe("vault-b", session)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	assertLockEvent(t, events, true, LockReasonInitial)
	if err := manager.Unlock("vault-b", session, vaultKey); err != nil {
		t.Fatal(err)
	}
	assertLockEvent(t, events, false, LockReasonUnlocked)

	manager.mu.Lock()
	initialActivity := manager.vaults["vault-b"].lastActivity
	manager.mu.Unlock()
	for range 3 {
		if _, _, err := manager.SessionStatus("vault-b", session); err != nil {
			t.Fatalf("status poll failed: %v", err)
		}
	}
	lease, err := manager.BeginUse("vault-b", session)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if !manager.IsUnlocked("vault-b", session) {
		t.Fatal("background key use unexpectedly locked the vault")
	}
	manager.mu.Lock()
	activityAfterReads := manager.vaults["vault-b"].lastActivity
	manager.mu.Unlock()
	if !activityAfterReads.Equal(initialActivity) {
		t.Fatal("status polling or background reads extended the inactivity deadline")
	}

	select {
	case event := <-events:
		if !event.Locked || event.Reason != LockReasonIdle {
			t.Fatalf("idle timeout event = %#v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("key manager did not lock after the idle timeout")
	}
	if manager.IsUnlocked("vault-b", session) {
		t.Fatal("idle timeout retained a session grant")
	}
}

func TestKeyManagerForegroundTouchExtendsIdleDeadline(t *testing.T) {
	manager := newKeyManager(200 * time.Millisecond)
	defer manager.Close()
	session := bytes.Repeat([]byte{7}, 32)
	if err := manager.Unlock("vault-c", session, bytes.Repeat([]byte{8}, VaultKeySize)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := manager.Touch("vault-c", session); err != nil {
		t.Fatal(err)
	}
	time.Sleep(130 * time.Millisecond)
	if !manager.IsUnlocked("vault-c", session) {
		t.Fatal("foreground activity did not extend the idle deadline")
	}
	deadline := time.After(time.Second)
	for manager.IsUnlocked("vault-c", session) {
		select {
		case <-deadline:
			t.Fatal("key manager did not relock after the refreshed deadline")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestKeyManagerRevocationAndRestartClearSessionGrants(t *testing.T) {
	firstSession := bytes.Repeat([]byte{9}, 32)
	secondSession := bytes.Repeat([]byte{10}, 32)
	key := bytes.Repeat([]byte{11}, VaultKeySize)
	manager := newKeyManager(time.Minute)
	if err := manager.Unlock("vault-d", firstSession, key); err != nil {
		t.Fatal(err)
	}
	if err := manager.Unlock("vault-d", secondSession, key); err != nil {
		t.Fatal(err)
	}
	if err := manager.RevokeSession("vault-d", firstSession); err != nil {
		t.Fatal(err)
	}
	if manager.IsUnlocked("vault-d", firstSession) || !manager.IsUnlocked("vault-d", secondSession) {
		t.Fatal("revoking one session changed the wrong grants")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if manager.IsUnlocked("vault-d", secondSession) {
		t.Fatal("manager close retained a grant")
	}
	if err := manager.Unlock("vault-d", secondSession, key); !errors.Is(err, ErrKeyManagerClosed) {
		t.Fatalf("unlock after close = %v, want closed manager error", err)
	}

	restarted := newKeyManager(time.Minute)
	defer restarted.Close()
	if restarted.IsUnlocked("vault-d", secondSession) {
		t.Fatal("new manager loaded a prior process key")
	}
}

func TestKeyManagerRejectsInvalidIDsAndSessionHashes(t *testing.T) {
	manager := newKeyManager(time.Minute)
	defer manager.Close()
	if err := manager.Unlock("", bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, VaultKeySize)); !errors.Is(err, ErrInvalidVaultID) {
		t.Fatalf("empty vault ID = %v", err)
	}
	if err := manager.Unlock("vault", []byte("cookie-token"), bytes.Repeat([]byte{2}, VaultKeySize)); !errors.Is(err, ErrSessionRequired) {
		t.Fatalf("raw session token = %v", err)
	}
	if err := manager.Unlock("vault", bytes.Repeat([]byte{1}, 32), []byte("short")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short vault key = %v", err)
	}
}

func assertLockEvent(t *testing.T, events <-chan LockEvent, locked bool, reason LockReason) {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("lock event subscription closed unexpectedly")
		}
		if event.Locked != locked || event.Reason != reason {
			t.Fatalf("lock event = %#v, want locked=%t reason=%q", event, locked, reason)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lock event")
	}
}

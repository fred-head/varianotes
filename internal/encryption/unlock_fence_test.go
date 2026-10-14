package encryption

import (
	"bytes"
	"errors"
	"testing"
)

func TestUnlockAttemptFencesEveryRevocationBeforeFirstGrant(t *testing.T) {
	for _, action := range []string{"manual", "all", "session", "everywhere", "except", "close"} {
		t.Run(action, func(t *testing.T) {
			manager := NewKeyManager()
			defer manager.Close()
			session := bytes.Repeat([]byte{1}, 32)
			attempt, err := manager.BeginUnlock("vault", session)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "manual":
				err = manager.LockVault("vault", LockReasonManual)
			case "all":
				err = manager.LockAll(LockReasonLogout)
			case "session":
				err = manager.RevokeSession("vault", session)
			case "everywhere":
				err = manager.RevokeSessionEverywhere(session)
			case "except":
				err = manager.RevokeSessionsExcept(bytes.Repeat([]byte{2}, 32))
			case "close":
				err = manager.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			err = manager.CompleteUnlock(attempt, bytes.Repeat([]byte{3}, VaultKeySize))
			if !errors.Is(err, ErrVaultLocked) && !errors.Is(err, ErrKeyManagerClosed) {
				t.Fatalf("stale grant accepted: %v", err)
			}
			if manager.IsUnlocked("vault", session) || len(manager.vaults["vault"].key) != 0 {
				t.Fatal("stale attempt installed key/grant")
			}
		})
	}
}

func TestUnlockAttemptIsBoundToManagerAndUsedOnce(t *testing.T) {
	manager, other := NewKeyManager(), NewKeyManager()
	defer manager.Close()
	defer other.Close()
	session, key := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{3}, VaultKeySize)
	attempt, err := manager.BeginUnlock("vault", session)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.CompleteUnlock(attempt, key); !errors.Is(err, ErrVaultLocked) {
		t.Fatalf("foreign manager accepted attempt: %v", err)
	}
	if err := manager.CompleteUnlock(attempt, key); err != nil {
		t.Fatal(err)
	}
	if err := manager.CompleteUnlock(attempt, key); !errors.Is(err, ErrVaultLocked) {
		t.Fatalf("attempt reused: %v", err)
	}
	// No tombstones or pending-request map grow with failed admissions.
	for range 1000 {
		if _, err := manager.BeginUnlock("vault", session); err != nil {
			t.Fatal(err)
		}
	}
	if len(manager.vaults) != 1 || len(manager.vaults["vault"].grants) != 1 {
		t.Fatal("pending unlocks retained manager-side state")
	}
}

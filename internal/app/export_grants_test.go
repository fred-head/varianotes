package app

import (
	"crypto/sha256"
	"strconv"
	"testing"
	"time"
)

func TestExportGrantStoreIsOneUseReplacesSessionGrantAndStaysBounded(t *testing.T) {
	store := newExportGrantStore()
	now := time.Now()
	firstSession := sha256.Sum256([]byte("session-zero"))
	oldToken, err := store.Issue("vault", firstSession[:], 1, "snapshot-one", now)
	if err != nil {
		t.Fatal(err)
	}
	newToken, err := store.Issue("vault", firstSession[:], 1, "snapshot-two", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(oldToken, "vault", firstSession[:], now); err == nil {
		t.Fatal("superseded export grant remained usable")
	}
	grant, err := store.Consume(newToken, "vault", firstSession[:], now)
	if err != nil || grant.fingerprint != "snapshot-two" {
		t.Fatalf("consume current grant=%#v err=%v", grant, err)
	}
	if _, err := store.Consume(newToken, "vault", firstSession[:], now); err == nil {
		t.Fatal("export grant was reusable")
	}

	for index := 0; index < maxExportGrants; index++ {
		session := sha256.Sum256([]byte("session-" + strconv.Itoa(index)))
		if index == 0 {
			session = firstSession
		}
		if _, err := store.Issue("vault", session[:], 1, "snapshot", now); err != nil {
			t.Fatalf("issue bounded grant %d: %v", index, err)
		}
	}
	tooManySession := sha256.Sum256([]byte("session-over-capacity"))
	if _, err := store.Issue("vault", tooManySession[:], 1, "snapshot", now); err != errExportGrantCapacity {
		t.Fatalf("grant capacity error=%v, want %v", err, errExportGrantCapacity)
	}
	if _, err := store.Issue("vault", firstSession[:], 1, "replacement", now); err != nil {
		t.Fatalf("same session could not replace its old grant at capacity: %v", err)
	}
}

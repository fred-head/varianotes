package encryption

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRewrapVaultKeyEnvelopeChangesOnlyPasswordEnvelope(t *testing.T) {
	oldPassword := "original encryption password"
	newPassword := "separate replacement encryption password"
	vaultKey, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(vaultKey)
	original, err := EncryptVaultKey(oldPassword, vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	rewrapped, err := RewrapVaultKeyEnvelope(oldPassword, newPassword, original)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(original.Ciphertext, rewrapped.Ciphertext) {
		t.Fatal("password change reused the old vault-key envelope")
	}
	decrypted, err := DecryptVaultKey(newPassword, rewrapped)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(decrypted)
	if !bytes.Equal(decrypted, vaultKey) {
		t.Fatal("rewrapping changed the vault key")
	}
}

func TestVaultKeyEnvelopeStoreIsAtomicPrivateAndRejectsSymlinks(t *testing.T) {
	directory := t.TempDir()
	filename := filepath.Join(directory, "key-envelope.json")
	store, err := NewVaultKeyEnvelopeStore(filename)
	if err != nil {
		t.Fatal(err)
	}
	vaultKey, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(vaultKey)
	envelope, err := EncryptVaultKey("a sufficiently long encryption passphrase", vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(envelope); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != envelope.Version || !bytes.Equal(loaded.Ciphertext, envelope.Ciphertext) {
		t.Fatal("stored key envelope did not round trip")
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key-envelope permissions = %o, want 600", info.Mode().Perm())
	}

	target := filepath.Join(directory, "outside.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"age_ciphertext":"AQ=="}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filename); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(envelope); !errors.Is(err, ErrInvalidKeyEnvelopeStore) {
		t.Fatalf("symlink target was accepted: %v", err)
	}
}

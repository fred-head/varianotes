package encryption

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVaultKeyPasswordEnvelopeRoundTripAndBounds(t *testing.T) {
	vaultKey, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(vaultKey) != VaultKeySize {
		t.Fatalf("vault key has %d bytes, want %d", len(vaultKey), VaultKeySize)
	}

	envelope, err := EncryptVaultKey("correct horse battery staple", vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Version != FormatVersion {
		t.Fatalf("vault key envelope version = %d, want %d", envelope.Version, FormatVersion)
	}

	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded VaultKeyEnvelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	plaintext, err := DecryptVaultKey("correct horse battery staple", decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, vaultKey) {
		t.Fatal("vault key envelope round trip changed the key")
	}

	if plaintext, err := DecryptVaultKey("incorrect password", decoded); err == nil || plaintext != nil {
		t.Fatalf("wrong password returned plaintext or no error: key length=%d, err=%v", len(plaintext), err)
	}

	unsupported := decoded
	unsupported.Version++
	if plaintext, err := DecryptVaultKey("correct horse battery staple", unsupported); !errors.Is(err, ErrUnsupportedFormat) || plaintext != nil {
		t.Fatalf("unsupported version was not rejected without plaintext: key length=%d, err=%v", len(plaintext), err)
	}

	tampered := decoded
	tampered.Ciphertext = append([]byte(nil), decoded.Ciphertext...)
	tampered.Ciphertext[len(tampered.Ciphertext)/2] ^= 0x01
	if plaintext, err := DecryptVaultKey("correct horse battery staple", tampered); err == nil || plaintext != nil {
		t.Fatalf("tampered envelope returned plaintext or no error: key length=%d, err=%v", len(plaintext), err)
	}

	if _, err := EncryptVaultKey("", vaultKey); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("empty password was accepted: %v", err)
	}
	if _, err := EncryptVaultKey(string(bytes.Repeat([]byte("p"), maxPasswordBytes+1)), vaultKey); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("oversized password was accepted: %v", err)
	}
	if _, err := EncryptVaultKey("valid password", vaultKey[:VaultKeySize-1]); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("wrong-sized vault key was accepted: %v", err)
	}
}

func TestEnvelopeJSONRejectsUnknownAndUnsupportedFormats(t *testing.T) {
	vault := []byte(`{"version":2,"age_ciphertext":"AQ=="}`)
	var vaultEnvelope VaultKeyEnvelope
	if err := json.Unmarshal(vault, &vaultEnvelope); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("unsupported vault-key envelope version was accepted: %v", err)
	}
	unknown := []byte(`{"version":1,"age_ciphertext":"AQ==","extra":true}`)
	if err := json.Unmarshal(unknown, &vaultEnvelope); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("unknown vault-key envelope field was accepted: %v", err)
	}
	trailing := []byte(`{"version":1,"age_ciphertext":"AQ=="} {}`)
	if err := json.Unmarshal(trailing, &vaultEnvelope); err == nil {
		t.Fatalf("trailing JSON value was accepted: %v", err)
	}
	duplicate := []byte(`{"version":1,"version":2,"age_ciphertext":"AQ=="}`)
	if err := json.Unmarshal(duplicate, &vaultEnvelope); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("duplicate envelope field was accepted: %v", err)
	}
}

func TestContentRoundTripsTextBinaryEmptyAndLargeFiles(t *testing.T) {
	vaultKey, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	large := make([]byte, 4<<20)
	for i := range large {
		large[i] = byte((i*31 + i/251) % 256)
	}
	cases := []struct {
		name string
		id   string
		data []byte
	}{
		{name: "text", id: "object-text", data: []byte("# Notiz\nGrüße, 世界\n")},
		{name: "binary", id: "object-binary", data: []byte{0, 0xff, 1, 0x80, '\n'}},
		{name: "empty", id: "object-empty", data: []byte{}},
		{name: "large", id: "object-large", data: large},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ciphertext, envelope, err := EncryptContent(vaultKey, tc.id, tc.data)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(ciphertext, tc.data) && len(tc.data) > 0 {
				t.Fatal("ciphertext contains the full plaintext")
			}
			plaintext, err := DecryptContent(vaultKey, tc.id, ciphertext, envelope)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(plaintext, tc.data) {
				t.Fatalf("round trip changed content: got %d bytes, want %d", len(plaintext), len(tc.data))
			}
		})
	}
}

func TestContentRejectsTamperingTruncationAndSwappedEnvelopes(t *testing.T) {
	vaultKey, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, envelope, err := EncryptContent(vaultKey, "object-original", []byte("sensitive plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext := func(name string, data []byte, env ContentKeyEnvelope, id string, key []byte, want error) {
		t.Helper()
		plaintext, err := DecryptContent(key, id, data, env)
		if err == nil || plaintext != nil {
			t.Errorf("%s returned plaintext or no error: plaintext=%q err=%v", name, plaintext, err)
			return
		}
		if want != nil && !errors.Is(err, want) {
			t.Errorf("%s error = %v, want %v", name, err, want)
		}
	}

	tamperedCiphertext := append([]byte(nil), ciphertext...)
	tamperedCiphertext[len(tamperedCiphertext)-1] ^= 0x80
	assertNoPlaintext("tampered age ciphertext", tamperedCiphertext, envelope, "object-original", vaultKey, ErrAuthentication)
	assertNoPlaintext("truncated age ciphertext", ciphertext[:len(ciphertext)/2], envelope, "object-original", vaultKey, ErrAuthentication)
	assertNoPlaintext("wrong vault key", ciphertext, envelope, "object-original", bytes.Repeat([]byte{0x42}, VaultKeySize), ErrAuthentication)
	assertNoPlaintext("swapped object", ciphertext, envelope, "object-other", vaultKey, ErrInvalidEnvelope)

	tamperedEnvelope := envelope
	tamperedEnvelope.Ciphertext = append([]byte(nil), envelope.Ciphertext...)
	tamperedEnvelope.Ciphertext[len(tamperedEnvelope.Ciphertext)-1] ^= 0x01
	assertNoPlaintext("tampered content-key envelope", ciphertext, tamperedEnvelope, "object-original", vaultKey, ErrAuthentication)

	unsupported := envelope
	unsupported.Version++
	assertNoPlaintext("unsupported key-envelope version", ciphertext, unsupported, "object-original", vaultKey, ErrUnsupportedFormat)
}

func TestContentEnvelopeJSONRoundTripAndStrictness(t *testing.T) {
	vaultKey, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	_, envelope, err := EncryptContent(vaultKey, "object-json", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ContentKeyEnvelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ObjectID != envelope.ObjectID || !bytes.Equal(decoded.Nonce, envelope.Nonce) || !bytes.Equal(decoded.Ciphertext, envelope.Ciphertext) {
		t.Fatal("content-key envelope JSON round trip changed its fields")
	}

	unknown := bytes.Replace(encoded, []byte(`"wrapped_key"`), []byte(`"unknown_key"`), 1)
	if err := json.Unmarshal(unknown, &decoded); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("unknown content-key envelope field was accepted: %v", err)
	}
	unsupported := append([]byte(nil), encoded...)
	unsupported = bytes.Replace(unsupported, []byte(`"version":1`), []byte(`"version":9`), 1)
	if err := json.Unmarshal(unsupported, &decoded); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("unsupported content-key envelope version was accepted: %v", err)
	}
}

func TestOfficialAgeCLIDecryptsContent(t *testing.T) {
	ageCLI, err := exec.LookPath("age")
	if err != nil {
		t.Skip("official age CLI is installed in the CI interoperability job")
	}

	vaultKey, err := NewVaultKey()
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("decryptable with the official age command-line tool\n")
	ciphertext, envelope, err := EncryptContent(vaultKey, "cli-test-object", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := UnwrapContentKey(vaultKey, "cli-test-object", envelope)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(privateKey)

	directory := t.TempDir()
	identityPath := filepath.Join(directory, "identity.txt")
	ciphertextPath := filepath.Join(directory, "content.age")
	plaintextPath := filepath.Join(directory, "decrypted.bin")
	if err := os.WriteFile(identityPath, append(privateKey, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ciphertextPath, ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(ageCLI, "--decrypt", "--identity", identityPath, "--output", plaintextPath, ciphertextPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("official age CLI failed to decrypt content: %v\n%s", err, output)
	}
	actual, err := os.ReadFile(plaintextPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, plaintext) {
		t.Fatalf("official age CLI returned %q, want %q", actual, plaintext)
	}
}

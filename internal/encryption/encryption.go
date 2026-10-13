// Package encryption implements M31's versioned cryptographic primitives,
// authenticated catalog, and volatile lock state. Key material and catalogs
// remain disconnected from vault content APIs until the M31 activation gate
// passes; only lock, activity, and lock-event operations are exposed meanwhile.
package encryption

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"filippo.io/age"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// FormatVersion is the version for the VariaNotes key-envelope metadata.
	// Age ciphertexts keep their own interoperable age-encryption.org version.
	FormatVersion uint8 = 1

	VaultKeySize        = 32
	scryptWorkFactor    = 18
	maxPasswordBytes    = 1024
	maxEnvelopeBytes    = 64 << 10
	maxObjectIDBytes    = 128
	maxContentKeyBytes  = 256
	contentKeyAADPrefix = "varianotes/content-key/v1\x00"
)

var (
	ErrInvalidKey        = errors.New("invalid encryption key")
	ErrInvalidPassword   = errors.New("invalid encryption password")
	ErrInvalidEnvelope   = errors.New("invalid encryption envelope")
	ErrUnsupportedFormat = errors.New("unsupported encryption format version")
	ErrAuthentication    = errors.New("encrypted content authentication failed")
)

// VaultKeyEnvelope stores a vault key encrypted as a standard age file using
// age's scrypt password recipient. The age ciphertext is binary and remains
// decryptable by the official age CLI with passphrase mode.
type VaultKeyEnvelope struct {
	Version    uint8  `json:"version"`
	Ciphertext []byte `json:"age_ciphertext"`
}

// ContentKeyEnvelope stores the per-file age identity, encrypted with the
// random vault key. ObjectID is authenticated as associated data so moving an
// envelope to another object does not silently make it usable there.
type ContentKeyEnvelope struct {
	Version    uint8  `json:"version"`
	ObjectID   string `json:"object_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"wrapped_key"`
}

// NewVaultKey returns a cryptographically random key for a vault.
func NewVaultKey() ([]byte, error) {
	key := make([]byte, VaultKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate vault key: %w", err)
	}
	return key, nil
}

// EncryptVaultKey protects a vault key with a password using the age scrypt
// recipient at a fixed work factor. The work factor is age's standard default;
// DecryptVaultKey caps untrusted envelopes at the same factor.
func EncryptVaultKey(password string, vaultKey []byte) (VaultKeyEnvelope, error) {
	if len(vaultKey) != VaultKeySize {
		return VaultKeyEnvelope{}, ErrInvalidKey
	}
	if err := validatePassword(password); err != nil {
		return VaultKeyEnvelope{}, err
	}

	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return VaultKeyEnvelope{}, fmt.Errorf("create age password recipient: %w", err)
	}
	recipient.SetWorkFactor(scryptWorkFactor)

	var ciphertext bytes.Buffer
	w, err := age.Encrypt(&ciphertext, recipient)
	if err != nil {
		return VaultKeyEnvelope{}, fmt.Errorf("create vault key envelope: %w", err)
	}
	if _, err := w.Write(vaultKey); err != nil {
		return VaultKeyEnvelope{}, fmt.Errorf("encrypt vault key: %w", err)
	}
	if err := w.Close(); err != nil {
		return VaultKeyEnvelope{}, fmt.Errorf("finish vault key envelope: %w", err)
	}
	return VaultKeyEnvelope{Version: FormatVersion, Ciphertext: ciphertext.Bytes()}, nil
}

// DecryptVaultKey authenticates and decrypts a password-protected vault key.
// On every failure it returns a nil key, including failures discovered at the
// end of the age stream.
func DecryptVaultKey(password string, envelope VaultKeyEnvelope) ([]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if envelope.Version != FormatVersion {
		return nil, ErrUnsupportedFormat
	}
	if len(envelope.Ciphertext) == 0 || len(envelope.Ciphertext) > maxEnvelopeBytes {
		return nil, ErrInvalidEnvelope
	}

	identity, err := age.NewScryptIdentity(password)
	if err != nil {
		return nil, fmt.Errorf("create age password identity: %w", err)
	}
	identity.SetMaxWorkFactor(scryptWorkFactor)
	reader, err := age.Decrypt(bytes.NewReader(envelope.Ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	plaintext, err := io.ReadAll(io.LimitReader(reader, VaultKeySize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	if len(plaintext) != VaultKeySize {
		return nil, ErrInvalidEnvelope
	}
	return plaintext, nil
}

// RewrapVaultKeyEnvelope changes only the password-protected envelope. The
// vault key and every encrypted content object remain byte-for-byte unchanged.
// Callers that have an authenticated catalog should also load it with the
// returned key before committing the new envelope.
func RewrapVaultKeyEnvelope(currentPassword, newPassword string, envelope VaultKeyEnvelope) (VaultKeyEnvelope, error) {
	vaultKey, err := DecryptVaultKey(currentPassword, envelope)
	if err != nil {
		return VaultKeyEnvelope{}, err
	}
	defer clear(vaultKey)
	return EncryptVaultKey(newPassword, vaultKey)
}

// EncryptContent produces a standard age-encrypted file and a separate key
// envelope. The returned ciphertext can be decrypted by the official age CLI
// with the unwrapped per-file identity.
func EncryptContent(vaultKey []byte, objectID string, plaintext []byte) ([]byte, ContentKeyEnvelope, error) {
	if len(vaultKey) != VaultKeySize {
		return nil, ContentKeyEnvelope{}, ErrInvalidKey
	}
	if err := validateObjectID(objectID); err != nil {
		return nil, ContentKeyEnvelope{}, err
	}

	fileIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, ContentKeyEnvelope{}, fmt.Errorf("generate per-file age key: %w", err)
	}
	wrapped, err := wrapContentKey(vaultKey, objectID, []byte(fileIdentity.String()))
	if err != nil {
		return nil, ContentKeyEnvelope{}, err
	}

	var ciphertext bytes.Buffer
	w, err := age.Encrypt(&ciphertext, fileIdentity.Recipient())
	if err != nil {
		return nil, ContentKeyEnvelope{}, fmt.Errorf("create age content stream: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, ContentKeyEnvelope{}, fmt.Errorf("encrypt content: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, ContentKeyEnvelope{}, fmt.Errorf("finish age content stream: %w", err)
	}
	return ciphertext.Bytes(), wrapped, nil
}

// UnwrapContentKey returns the private age identity bytes needed to decrypt a
// file. Callers should keep the returned bytes only as long as needed and
// clear them when practical; Go and the age library do not guarantee erasure
// of all copies from memory.
func UnwrapContentKey(vaultKey []byte, objectID string, envelope ContentKeyEnvelope) ([]byte, error) {
	if len(vaultKey) != VaultKeySize {
		return nil, ErrInvalidKey
	}
	if err := validateObjectID(objectID); err != nil {
		return nil, err
	}
	if envelope.Version != FormatVersion {
		return nil, ErrUnsupportedFormat
	}
	if envelope.ObjectID != objectID || len(envelope.Nonce) != chacha20poly1305.NonceSizeX {
		return nil, ErrInvalidEnvelope
	}
	if len(envelope.Ciphertext) == 0 || len(envelope.Ciphertext) > maxContentKeyBytes {
		return nil, ErrInvalidEnvelope
	}

	aead, err := chacha20poly1305.NewX(vaultKey)
	if err != nil {
		return nil, ErrInvalidKey
	}
	privateKey, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, contentKeyAAD(objectID))
	if err != nil {
		return nil, ErrAuthentication
	}
	if len(privateKey) == 0 || len(privateKey) > maxContentKeyBytes {
		return nil, ErrInvalidEnvelope
	}
	if _, err := age.ParseX25519Identity(string(privateKey)); err != nil {
		return nil, ErrInvalidEnvelope
	}
	return privateKey, nil
}

// DecryptContent returns authenticated plaintext. It buffers the result until
// age verifies the final stream tag, and returns no plaintext if verification,
// key-envelope validation, or format validation fails.
func DecryptContent(vaultKey []byte, objectID string, ciphertext []byte, envelope ContentKeyEnvelope) ([]byte, error) {
	privateKey, err := UnwrapContentKey(vaultKey, objectID, envelope)
	if err != nil {
		return nil, err
	}
	defer clear(privateKey)
	identity, err := age.ParseX25519Identity(string(privateKey))
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	plaintext, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	return plaintext, nil
}

// VerifyEncryptedContent authenticates an age ciphertext and its object-bound
// key envelope without retaining the plaintext. It is useful before durable
// staging, where the caller needs to prove the bytes are encrypted content.
func VerifyEncryptedContent(vaultKey []byte, objectID string, ciphertext []byte, envelope ContentKeyEnvelope) error {
	privateKey, err := UnwrapContentKey(vaultKey, objectID, envelope)
	if err != nil {
		return err
	}
	defer clear(privateKey)
	identity, err := age.ParseX25519Identity(string(privateKey))
	if err != nil {
		return ErrInvalidEnvelope
	}
	reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthentication, err)
	}
	return nil
}

func wrapContentKey(vaultKey []byte, objectID string, privateKey []byte) (ContentKeyEnvelope, error) {
	aead, err := chacha20poly1305.NewX(vaultKey)
	if err != nil {
		return ContentKeyEnvelope{}, ErrInvalidKey
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return ContentKeyEnvelope{}, fmt.Errorf("generate content-key nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, privateKey, contentKeyAAD(objectID))
	return ContentKeyEnvelope{
		Version:    FormatVersion,
		ObjectID:   objectID,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

func contentKeyAAD(objectID string) []byte {
	return []byte(contentKeyAADPrefix + objectID)
}

func validatePassword(password string) error {
	if len(password) == 0 || len(password) > maxPasswordBytes || !utf8.ValidString(password) {
		return ErrInvalidPassword
	}
	return nil
}

func validateObjectID(objectID string) error {
	if len(objectID) == 0 || len(objectID) > maxObjectIDBytes || !utf8.ValidString(objectID) || strings.IndexByte(objectID, 0) >= 0 {
		return ErrInvalidEnvelope
	}
	return nil
}

func (e VaultKeyEnvelope) MarshalJSON() ([]byte, error) {
	if e.Version != FormatVersion {
		return nil, ErrUnsupportedFormat
	}
	if len(e.Ciphertext) == 0 || len(e.Ciphertext) > maxEnvelopeBytes {
		return nil, ErrInvalidEnvelope
	}
	type wire VaultKeyEnvelope
	return json.Marshal(wire(e))
}

func (e *VaultKeyEnvelope) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrInvalidEnvelope
	}
	var parsed struct {
		Version    uint8  `json:"version"`
		Ciphertext []byte `json:"age_ciphertext"`
	}
	if err := decodeStrictJSON(data, &parsed); err != nil {
		return err
	}
	if parsed.Version != FormatVersion {
		return ErrUnsupportedFormat
	}
	if len(parsed.Ciphertext) == 0 || len(parsed.Ciphertext) > maxEnvelopeBytes {
		return ErrInvalidEnvelope
	}
	e.Version, e.Ciphertext = parsed.Version, parsed.Ciphertext
	return nil
}

func (e ContentKeyEnvelope) MarshalJSON() ([]byte, error) {
	if e.Version != FormatVersion {
		return nil, ErrUnsupportedFormat
	}
	if err := validateObjectID(e.ObjectID); err != nil {
		return nil, err
	}
	if len(e.Nonce) != chacha20poly1305.NonceSizeX || len(e.Ciphertext) == 0 || len(e.Ciphertext) > maxContentKeyBytes {
		return nil, ErrInvalidEnvelope
	}
	type wire ContentKeyEnvelope
	return json.Marshal(wire(e))
}

func (e *ContentKeyEnvelope) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrInvalidEnvelope
	}
	var parsed struct {
		Version    uint8  `json:"version"`
		ObjectID   string `json:"object_id"`
		Nonce      []byte `json:"nonce"`
		Ciphertext []byte `json:"wrapped_key"`
	}
	if err := decodeStrictJSON(data, &parsed); err != nil {
		return err
	}
	if parsed.Version != FormatVersion {
		return ErrUnsupportedFormat
	}
	if err := validateObjectID(parsed.ObjectID); err != nil {
		return err
	}
	if len(parsed.Nonce) != chacha20poly1305.NonceSizeX || len(parsed.Ciphertext) == 0 || len(parsed.Ciphertext) > maxContentKeyBytes {
		return ErrInvalidEnvelope
	}
	e.Version, e.ObjectID, e.Nonce, e.Ciphertext = parsed.Version, parsed.ObjectID, parsed.Nonce, parsed.Ciphertext
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxEnvelopeBytes {
		return ErrInvalidEnvelope
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidEnvelope
	}
	return nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return ErrInvalidEnvelope
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return ErrInvalidEnvelope
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalidEnvelope
		}
		name, ok := token.(string)
		if !ok {
			return ErrInvalidEnvelope
		}
		if _, exists := seen[name]; exists {
			return ErrInvalidEnvelope
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return ErrInvalidEnvelope
		}
	}
	if _, err := decoder.Token(); err != nil {
		return ErrInvalidEnvelope
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidEnvelope
	}
	return nil
}

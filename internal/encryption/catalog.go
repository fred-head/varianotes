package encryption

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// CatalogFormatVersion versions the authenticated catalog independently of
	// age ciphertext and the key-envelope format.
	CatalogFormatVersion uint8 = 1
	maxCatalogBytes            = 16 << 20
	maxCatalogObjects          = 100_000
	maxCatalogRules            = 100_000
	maxCatalogPathBytes        = 4096
	catalogMACDomain           = "varianotes/catalog-authentication/v1\x00"
)

var (
	ErrInvalidCatalog         = errors.New("invalid encryption catalog")
	ErrCatalogAuthentication  = errors.New("encryption catalog authentication failed")
	ErrCatalogMissing         = errors.New("encryption catalog is missing")
	ErrCatalogTooLarge        = errors.New("encryption catalog is too large")
	ErrCatalogPathCollision   = errors.New("encryption catalog paths collide")
	ErrInvalidCatalogStore    = errors.New("invalid encryption catalog store")
	ErrCatalogCiphertextCheck = errors.New("encrypted object does not match its catalog entry")
)

type CatalogObjectKind string

const (
	CatalogObjectNote  CatalogObjectKind = "note"
	CatalogObjectAsset CatalogObjectKind = "asset"
)

// Catalog contains metadata only. It deliberately has no fields for note text,
// previews, or plaintext digests. Every known file has an explicit protection
// state so a recursive rule cannot silently leave a plaintext exception.
type Catalog struct {
	Version     uint8                  `json:"version"`
	VaultID     string                 `json:"vault_id"`
	Objects     []CatalogObject        `json:"objects"`
	FolderRules []FolderProtectionRule `json:"folder_rules"`
}

// CatalogObject binds one logical file to its physical representation. Plain
// objects have identical logical and physical paths and no digest. Protected
// objects use a .age physical path and carry a digest of ciphertext only.
type CatalogObject struct {
	ObjectID           string              `json:"object_id"`
	Kind               CatalogObjectKind   `json:"kind"`
	LogicalPath        string              `json:"logical_path"`
	PhysicalPath       string              `json:"physical_path"`
	CiphertextHash     string              `json:"ciphertext_sha256,omitempty"`
	ContentKeyEnvelope *ContentKeyEnvelope `json:"content_key_envelope,omitempty"`
	Protected          bool                `json:"protected"`
}

// FolderProtectionRule protects every current and future descendant of Path.
// The path remains visible; only its descendants' file contents are protected.
type FolderProtectionRule struct {
	Path string `json:"path"`
}

type catalogPayload struct {
	Version     uint8                  `json:"version"`
	VaultID     string                 `json:"vault_id"`
	Objects     []CatalogObject        `json:"objects"`
	FolderRules []FolderProtectionRule `json:"folder_rules"`
}

type authenticatedCatalog struct {
	Version     uint8                  `json:"version"`
	VaultID     string                 `json:"vault_id"`
	Objects     []CatalogObject        `json:"objects"`
	FolderRules []FolderProtectionRule `json:"folder_rules"`
	MAC         string                 `json:"authentication"`
}

// NewCatalog returns an empty catalog with a new random vault identity.
func NewCatalog() (Catalog, error) {
	vaultID, err := newCatalogID()
	if err != nil {
		return Catalog{}, err
	}
	return Catalog{Version: CatalogFormatVersion, VaultID: vaultID, Objects: []CatalogObject{}, FolderRules: []FolderProtectionRule{}}, nil
}

// NewCatalogObjectID returns an opaque, random 128-bit object identity.
func NewCatalogObjectID() (string, error) {
	return newCatalogID()
}

func newCatalogID() (string, error) {
	identifier := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, identifier); err != nil {
		return "", fmt.Errorf("generate catalog identifier: %w", err)
	}
	return hex.EncodeToString(identifier), nil
}

// MarshalCatalog validates and authenticates the catalog with a key derived
// from the random vault key. Entries and rules are sorted before encoding, so
// equivalent catalogs have a stable authenticated representation.
func MarshalCatalog(catalog Catalog, vaultKey []byte) ([]byte, error) {
	if len(vaultKey) != VaultKeySize {
		return nil, ErrInvalidKey
	}
	canonical, err := canonicalCatalog(catalog)
	if err != nil {
		return nil, err
	}
	payload := payloadForCatalog(canonical)
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode encryption catalog: %w", err)
	}
	mac := catalogMAC(vaultKey, encodedPayload)
	result, err := json.Marshal(authenticatedCatalog{
		Version: canonical.Version, VaultID: canonical.VaultID,
		Objects: canonical.Objects, FolderRules: canonical.FolderRules,
		MAC: hex.EncodeToString(mac),
	})
	if err != nil {
		return nil, fmt.Errorf("encode authenticated encryption catalog: %w", err)
	}
	if len(result) > maxCatalogBytes {
		return nil, ErrCatalogTooLarge
	}
	return result, nil
}

// ParseCatalog strictly decodes, authenticates, and validates a catalog. The
// expected vault ID prevents a valid catalog from being loaded for another
// registered vault. On every failure no catalog is returned.
func ParseCatalog(data, vaultKey []byte, expectedVaultID string) (Catalog, error) {
	if len(vaultKey) != VaultKeySize {
		return Catalog{}, ErrInvalidKey
	}
	if len(data) == 0 {
		return Catalog{}, ErrInvalidCatalog
	}
	if len(data) > maxCatalogBytes {
		return Catalog{}, ErrCatalogTooLarge
	}
	var parsed authenticatedCatalog
	if err := decodeCatalogJSON(data, &parsed); err != nil {
		return Catalog{}, err
	}
	if parsed.Version != CatalogFormatVersion {
		return Catalog{}, ErrUnsupportedFormat
	}
	if parsed.VaultID != expectedVaultID {
		return Catalog{}, ErrCatalogAuthentication
	}
	catalog := Catalog{Version: parsed.Version, VaultID: parsed.VaultID, Objects: parsed.Objects, FolderRules: parsed.FolderRules}
	canonical, err := normalizeCatalog(catalog)
	if err != nil {
		return Catalog{}, err
	}
	payload, err := json.Marshal(payloadForCatalog(canonical))
	if err != nil {
		return Catalog{}, fmt.Errorf("encode encryption catalog: %w", err)
	}
	providedMAC, err := hex.DecodeString(parsed.MAC)
	if err != nil || len(providedMAC) != sha256.Size {
		return Catalog{}, ErrCatalogAuthentication
	}
	if !hmac.Equal(providedMAC, catalogMAC(vaultKey, payload)) {
		return Catalog{}, ErrCatalogAuthentication
	}
	if err := validateCatalog(canonical); err != nil {
		return Catalog{}, err
	}
	return canonical, nil
}

// CiphertextSHA256 returns the digest stored for a protected object. It must
// only be called with ciphertext, never plaintext.
func CiphertextSHA256(ciphertext []byte) string {
	digest := sha256.Sum256(ciphertext)
	return hex.EncodeToString(digest[:])
}

// VerifyCatalogCiphertext confirms that ciphertext belongs to the cataloged
// protected object. This detects missing, swapped, and modified ciphertext
// before a later decryption layer can return content.
func VerifyCatalogCiphertext(object CatalogObject, ciphertext []byte) error {
	if !object.Protected || !isSHA256Hex(object.CiphertextHash) || CiphertextSHA256(ciphertext) != object.CiphertextHash {
		return ErrCatalogCiphertextCheck
	}
	return nil
}

// IsPathProtected applies explicit object protection and recursive folder
// rules. It also answers for paths that have no current object, which lets a
// later content writer protect a newly created descendant from its first byte.
func (catalog Catalog) IsPathProtected(logicalPath string) (bool, error) {
	if !isCatalogPath(logicalPath) {
		return false, ErrInvalidCatalog
	}
	canonical, err := canonicalCatalog(catalog)
	if err != nil {
		return false, err
	}
	for _, object := range canonical.Objects {
		if object.LogicalPath == logicalPath {
			return object.Protected, nil
		}
	}
	for _, rule := range canonical.FolderRules {
		if logicalPath == rule.Path || pathWithin(logicalPath, rule.Path) {
			return true, nil
		}
	}
	return false, nil
}

func catalogMAC(vaultKey, payload []byte) []byte {
	// HMAC with a domain label derives a catalog-only authentication key from
	// the vault key without reusing the content-encryption key directly.
	keyDeriver := hmac.New(sha256.New, vaultKey)
	_, _ = keyDeriver.Write([]byte(catalogMACDomain))
	catalogKey := keyDeriver.Sum(nil)
	authenticator := hmac.New(sha256.New, catalogKey)
	_, _ = authenticator.Write(payload)
	return authenticator.Sum(nil)
}

func payloadForCatalog(catalog Catalog) catalogPayload {
	return catalogPayload{Version: catalog.Version, VaultID: catalog.VaultID, Objects: catalog.Objects, FolderRules: catalog.FolderRules}
}

func canonicalCatalog(catalog Catalog) (Catalog, error) {
	canonical, err := normalizeCatalog(catalog)
	if err != nil {
		return Catalog{}, err
	}
	if err := validateCatalog(canonical); err != nil {
		return Catalog{}, err
	}
	return canonical, nil
}

func normalizeCatalog(catalog Catalog) (Catalog, error) {
	if catalog.Version != CatalogFormatVersion {
		return Catalog{}, ErrUnsupportedFormat
	}
	if !isCatalogID(catalog.VaultID) || len(catalog.Objects) > maxCatalogObjects || len(catalog.FolderRules) > maxCatalogRules {
		return Catalog{}, ErrInvalidCatalog
	}
	result := Catalog{
		Version: catalog.Version, VaultID: catalog.VaultID,
		Objects:     append([]CatalogObject(nil), catalog.Objects...),
		FolderRules: append([]FolderProtectionRule(nil), catalog.FolderRules...),
	}
	if result.Objects == nil {
		result.Objects = []CatalogObject{}
	}
	if result.FolderRules == nil {
		result.FolderRules = []FolderProtectionRule{}
	}
	sort.Slice(result.Objects, func(i, j int) bool { return result.Objects[i].ObjectID < result.Objects[j].ObjectID })
	sort.Slice(result.FolderRules, func(i, j int) bool { return result.FolderRules[i].Path < result.FolderRules[j].Path })
	return result, nil
}

func validateCatalog(catalog Catalog) error {
	objectIDs := make(map[string]struct{}, len(catalog.Objects))
	logicalPaths := make(map[string]struct{}, len(catalog.Objects))
	physicalPaths := make(map[string]struct{}, len(catalog.Objects))
	for _, object := range catalog.Objects {
		if !isCatalogID(object.ObjectID) || !isCatalogPath(object.LogicalPath) || !isCatalogPath(object.PhysicalPath) {
			return ErrInvalidCatalog
		}
		if object.Kind != CatalogObjectNote && object.Kind != CatalogObjectAsset {
			return ErrInvalidCatalog
		}
		if _, exists := objectIDs[object.ObjectID]; exists {
			return ErrInvalidCatalog
		}
		if _, exists := logicalPaths[object.LogicalPath]; exists {
			return ErrCatalogPathCollision
		}
		if _, exists := physicalPaths[object.PhysicalPath]; exists {
			return ErrCatalogPathCollision
		}
		objectIDs[object.ObjectID] = struct{}{}
		logicalPaths[object.LogicalPath] = struct{}{}
		physicalPaths[object.PhysicalPath] = struct{}{}
		if object.Protected {
			if object.PhysicalPath != object.LogicalPath+".age" || !isSHA256Hex(object.CiphertextHash) || !validCatalogContentKeyEnvelope(object.ObjectID, object.ContentKeyEnvelope) {
				return ErrInvalidCatalog
			}
		} else if object.PhysicalPath != object.LogicalPath || object.CiphertextHash != "" || object.ContentKeyEnvelope != nil {
			return ErrInvalidCatalog
		}
	}
	if hasFileAncestor(logicalPaths) || hasFileAncestor(physicalPaths) {
		return ErrCatalogPathCollision
	}
	rulePaths := make(map[string]struct{}, len(catalog.FolderRules))
	for _, rule := range catalog.FolderRules {
		if !isCatalogPath(rule.Path) {
			return ErrInvalidCatalog
		}
		if _, exists := rulePaths[rule.Path]; exists {
			return ErrInvalidCatalog
		}
		if _, exists := logicalPaths[rule.Path]; exists {
			return ErrCatalogPathCollision
		}
		for ancestor := path.Dir(rule.Path); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, exists := logicalPaths[ancestor]; exists {
				return ErrCatalogPathCollision
			}
		}
		rulePaths[rule.Path] = struct{}{}
	}
	for _, object := range catalog.Objects {
		if object.Protected {
			continue
		}
		for ancestor := path.Dir(object.LogicalPath); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, exists := rulePaths[ancestor]; exists {
				return ErrInvalidCatalog
			}
		}
	}
	return nil
}

func validCatalogContentKeyEnvelope(objectID string, envelope *ContentKeyEnvelope) bool {
	return envelope != nil && envelope.Version == FormatVersion && envelope.ObjectID == objectID &&
		len(envelope.Nonce) == chacha20poly1305.NonceSizeX && len(envelope.Ciphertext) > 0 && len(envelope.Ciphertext) <= maxContentKeyBytes
}

func hasFileAncestor(paths map[string]struct{}) bool {
	for value := range paths {
		for ancestor := path.Dir(value); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, exists := paths[ancestor]; exists {
				return true
			}
		}
	}
	return false
}

func isCatalogPath(value string) bool {
	if len(value) == 0 || len(value) > maxCatalogPathBytes || !utf8.ValidString(value) || hasControlCharacter(value) || strings.Contains(value, `\`) || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func hasControlCharacter(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func pathWithin(candidate, folder string) bool {
	return strings.HasPrefix(candidate, folder+"/")
}

func isCatalogID(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func decodeCatalogJSON(data []byte, target any) error {
	if err := rejectDuplicateJSONFieldsRecursive(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCatalog, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidCatalog
	}
	return nil
}

func rejectDuplicateJSONFieldsRecursive(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return ErrInvalidCatalog
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidCatalog
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			fieldToken, err := decoder.Token()
			if err != nil {
				return err
			}
			field, ok := fieldToken.(string)
			if !ok {
				return ErrInvalidCatalog
			}
			if _, exists := seen[field]; exists {
				return ErrInvalidCatalog
			}
			seen[field] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return ErrInvalidCatalog
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return ErrInvalidCatalog
		}
	default:
		return ErrInvalidCatalog
	}
	return nil
}

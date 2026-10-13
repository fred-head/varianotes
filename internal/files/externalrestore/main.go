// Command externalrestore is a small interoperability fixture used by the
// M31 P17 test. It deliberately imports no VariaNotes package: it reads the
// documented age files, XChaCha20-Poly1305 content-key envelopes, and
// authenticated catalog directly, then delegates content decryption to age.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	catalogMACDomain = "varianotes/catalog-authentication/v1\x00"
	contentKeyAAD    = "varianotes/content-key/v1\x00"
	maxCatalogBytes  = 16 << 20
	maxEnvelopeBytes = 64 << 10
	maxVaultKeySize  = 32
	scryptWorkFactor = 18
)

type vaultKeyEnvelope struct {
	Version       uint8  `json:"version"`
	AgeCiphertext []byte `json:"age_ciphertext"`
}

type contentKeyEnvelope struct {
	Version    uint8  `json:"version"`
	ObjectID   string `json:"object_id"`
	Nonce      []byte `json:"nonce"`
	WrappedKey []byte `json:"wrapped_key"`
}

type catalogObject struct {
	ObjectID           string              `json:"object_id"`
	Kind               string              `json:"kind"`
	LogicalPath        string              `json:"logical_path"`
	PhysicalPath       string              `json:"physical_path"`
	CiphertextHash     string              `json:"ciphertext_sha256,omitempty"`
	ContentKeyEnvelope *contentKeyEnvelope `json:"content_key_envelope,omitempty"`
	Protected          bool                `json:"protected"`
}

type folderRule struct {
	Path string `json:"path"`
}

type catalog struct {
	Version     uint8           `json:"version"`
	VaultID     string          `json:"vault_id"`
	Objects     []catalogObject `json:"objects"`
	FolderRules []folderRule    `json:"folder_rules"`
}

type authenticatedCatalog struct {
	Version     uint8           `json:"version"`
	VaultID     string          `json:"vault_id"`
	Objects     []catalogObject `json:"objects"`
	FolderRules []folderRule    `json:"folder_rules"`
	MAC         string          `json:"authentication"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	vaultPath := flag.String("vault", "", "path to the restored vault directory")
	metadataPath := flag.String("encryption-metadata", "", "path to this vault's encryption metadata directory")
	password := flag.String("password", "", "password for the age-wrapped vault key")
	outputPath := flag.String("output", "", "destination for the recovered vault tree")
	flag.Parse()
	if *vaultPath == "" || *metadataPath == "" || *password == "" || *outputPath == "" {
		return errors.New("vault, encryption metadata, password, and output are required")
	}
	if _, err := os.Stat(*vaultPath); err != nil {
		return fmt.Errorf("inspect backup vault: %w", err)
	}
	if err := os.MkdirAll(*outputPath, 0o700); err != nil {
		return fmt.Errorf("create recovered vault directory: %w", err)
	}

	keyEnvelope, err := readJSON[vaultKeyEnvelope](filepath.Join(*metadataPath, "key-envelope.json"), maxEnvelopeBytes*2)
	if err != nil {
		return fmt.Errorf("read password envelope: %w", err)
	}
	if keyEnvelope.Version != 1 || len(keyEnvelope.AgeCiphertext) == 0 || len(keyEnvelope.AgeCiphertext) > maxEnvelopeBytes {
		return errors.New("unsupported or invalid password envelope")
	}
	identity, err := age.NewScryptIdentity(*password)
	if err != nil {
		return errors.New("create age password identity")
	}
	identity.SetMaxWorkFactor(scryptWorkFactor)
	keyReader, err := age.Decrypt(bytes.NewReader(keyEnvelope.AgeCiphertext), identity)
	if err != nil {
		return errors.New("password did not decrypt the vault key envelope")
	}
	vaultKey, err := io.ReadAll(io.LimitReader(keyReader, maxVaultKeySize+1))
	if err != nil || len(vaultKey) != maxVaultKeySize {
		clear(vaultKey)
		return errors.New("vault key envelope did not contain a 256-bit key")
	}
	defer clear(vaultKey)

	authenticated, err := readJSON[authenticatedCatalog](filepath.Join(*metadataPath, "catalog.json"), maxCatalogBytes)
	if err != nil {
		return fmt.Errorf("read authenticated catalog: %w", err)
	}
	cat, err := verifyCatalog(authenticated, vaultKey)
	if err != nil {
		return err
	}
	return restoreTree(*vaultPath, *outputPath, cat, vaultKey)
}

func readJSON[T any](filename string, limit int64) (T, error) {
	var value T
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return value, errors.New("metadata file is missing, unsafe, or too large")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return value, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return value, errors.New("metadata contains trailing data")
	}
	return value, nil
}

func verifyCatalog(encoded authenticatedCatalog, key []byte) (catalog, error) {
	if encoded.Version != 1 || len(encoded.VaultID) != 32 {
		return catalog{}, errors.New("unsupported or invalid catalog")
	}
	if decoded, err := hex.DecodeString(encoded.VaultID); err != nil || len(decoded) != 16 {
		return catalog{}, errors.New("invalid catalog vault identity")
	}
	cat := catalog{Version: encoded.Version, VaultID: encoded.VaultID, Objects: encoded.Objects, FolderRules: encoded.FolderRules}
	sort.Slice(cat.Objects, func(i, j int) bool { return cat.Objects[i].ObjectID < cat.Objects[j].ObjectID })
	sort.Slice(cat.FolderRules, func(i, j int) bool { return cat.FolderRules[i].Path < cat.FolderRules[j].Path })
	payload, err := json.Marshal(cat)
	if err != nil {
		return catalog{}, errors.New("encode catalog authentication payload")
	}
	provided, err := hex.DecodeString(encoded.MAC)
	if err != nil || len(provided) != sha256.Size {
		return catalog{}, errors.New("invalid catalog authentication tag")
	}
	derive := hmac.New(sha256.New, key)
	_, _ = derive.Write([]byte(catalogMACDomain))
	authKey := derive.Sum(nil)
	check := hmac.New(sha256.New, authKey)
	_, _ = check.Write(payload)
	if !hmac.Equal(provided, check.Sum(nil)) {
		return catalog{}, errors.New("catalog authentication failed")
	}
	if err := validateCatalogPaths(cat); err != nil {
		return catalog{}, err
	}
	return cat, nil
}

func validateCatalogPaths(cat catalog) error {
	ids := make(map[string]bool, len(cat.Objects))
	logical := make(map[string]bool, len(cat.Objects))
	physical := make(map[string]bool, len(cat.Objects))
	for _, object := range cat.Objects {
		if object.ObjectID == "" || ids[object.ObjectID] || logical[object.LogicalPath] || physical[object.PhysicalPath] {
			return errors.New("catalog contains duplicate or empty object identities")
		}
		ids[object.ObjectID], logical[object.LogicalPath], physical[object.PhysicalPath] = true, true, true
		if _, err := safePath(".", object.LogicalPath); err != nil {
			return errors.New("catalog contains an unsafe logical path")
		}
		if _, err := safePath(".", object.PhysicalPath); err != nil {
			return errors.New("catalog contains an unsafe physical path")
		}
		if object.Protected {
			if object.PhysicalPath != object.LogicalPath+".age" || object.ContentKeyEnvelope == nil || object.ContentKeyEnvelope.ObjectID != object.ObjectID || object.ContentKeyEnvelope.Version != 1 {
				return errors.New("catalog protected object metadata is inconsistent")
			}
			if digest, err := hex.DecodeString(object.CiphertextHash); err != nil || len(digest) != sha256.Size {
				return errors.New("catalog protected object has an invalid ciphertext digest")
			}
		} else if object.PhysicalPath != object.LogicalPath || object.CiphertextHash != "" || object.ContentKeyEnvelope != nil {
			return errors.New("catalog plaintext object metadata is inconsistent")
		}
	}
	for _, rule := range cat.FolderRules {
		if _, err := safePath(".", rule.Path); err != nil {
			return errors.New("catalog contains an unsafe folder rule")
		}
	}
	return nil
}

func restoreTree(vaultRoot, outputRoot string, cat catalog, vaultKey []byte) error {
	knownCiphertext := make(map[string]catalogObject)
	protectedLogical := make(map[string]bool)
	for _, object := range cat.Objects {
		if object.Protected {
			knownCiphertext[filepath.FromSlash(object.PhysicalPath)] = object
			protectedLogical[filepath.FromSlash(object.LogicalPath)] = true
		}
	}
	for _, rule := range cat.FolderRules {
		directory, err := safePath(outputRoot, rule.Path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create protected folder: %w", err)
		}
	}
	err := filepath.WalkDir(vaultRoot, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filename == vaultRoot {
			return nil
		}
		relative, err := filepath.Rel(vaultRoot, filename)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("backup vault contains a symbolic link")
		}
		if entry.IsDir() {
			out, err := safePath(outputRoot, filepath.ToSlash(relative))
			if err != nil {
				return err
			}
			return os.MkdirAll(out, 0o700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("backup vault contains a non-regular file")
		}
		if strings.HasPrefix(entry.Name(), ".vnotes-txn-") || strings.HasSuffix(entry.Name(), ".transaction.json") {
			return errors.New("backup contains an unfinished transaction stage")
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".age") {
			_, ok := knownCiphertext[relative]
			if !ok {
				return errors.New("backup contains untracked age ciphertext")
			}
			return nil
		}
		if protectedLogical[relative] {
			return errors.New("backup contains plaintext beside its protected ciphertext")
		}
		source, err := os.Open(filename)
		if err != nil {
			return err
		}
		out, err := safePath(outputRoot, filepath.ToSlash(relative))
		if err != nil {
			_ = source.Close()
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			_ = source.Close()
			return err
		}
		destination, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = source.Close()
			return err
		}
		_, copyErr := io.Copy(destination, source)
		return errors.Join(copyErr, destination.Close(), source.Close())
	})
	if err != nil {
		return fmt.Errorf("copy ordinary vault files: %w", err)
	}

	temporary, err := os.MkdirTemp("", "varianotes-external-identities-")
	if err != nil {
		return fmt.Errorf("create temporary identity directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	for index, object := range cat.Objects {
		if !object.Protected {
			continue
		}
		ciphertextPath, err := safePath(vaultRoot, object.PhysicalPath)
		if err != nil {
			return err
		}
		ciphertext, err := os.ReadFile(ciphertextPath)
		if err != nil {
			return fmt.Errorf("read protected object %q: %w", object.LogicalPath, err)
		}
		digest := sha256.Sum256(ciphertext)
		if hex.EncodeToString(digest[:]) != object.CiphertextHash {
			return fmt.Errorf("ciphertext digest mismatch for %q", object.LogicalPath)
		}
		envelope := object.ContentKeyEnvelope
		if len(envelope.Nonce) != chacha20poly1305.NonceSizeX || len(envelope.WrappedKey) == 0 || len(envelope.WrappedKey) > 256 {
			return fmt.Errorf("content key envelope for %q is invalid", object.LogicalPath)
		}
		aead, err := chacha20poly1305.NewX(vaultKey)
		if err != nil {
			return errors.New("create content-key authenticator")
		}
		privateIdentity, err := aead.Open(nil, envelope.Nonce, envelope.WrappedKey, []byte(contentKeyAAD+object.ObjectID))
		if err != nil {
			return fmt.Errorf("authenticate content key for %q", object.LogicalPath)
		}
		if _, err := age.ParseX25519Identity(string(privateIdentity)); err != nil {
			clear(privateIdentity)
			return fmt.Errorf("parse content identity for %q", object.LogicalPath)
		}
		identityPath := filepath.Join(temporary, fmt.Sprintf("identity-%d.txt", index))
		if err := os.WriteFile(identityPath, append(privateIdentity, '\n'), 0o600); err != nil {
			clear(privateIdentity)
			return fmt.Errorf("write temporary content identity: %w", err)
		}
		clear(privateIdentity)
		output, err := safePath(outputRoot, object.LogicalPath)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
			return err
		}
		if _, err := os.Lstat(output); err == nil || !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recovered path %q already exists", object.LogicalPath)
		}
		command := exec.Command("age", "--decrypt", "--identity", identityPath, "--output", output, ciphertextPath)
		outputBytes, err := command.CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(outputBytes))
			if message == "" {
				return fmt.Errorf("official age CLI could not decrypt %q: %w", object.LogicalPath, err)
			}
			return fmt.Errorf("official age CLI could not decrypt %q: %s", object.LogicalPath, message)
		}
	}
	return nil
}

func safePath(root, relative string) (string, error) {
	if relative == "" || strings.Contains(relative, "\\") || strings.HasPrefix(relative, "/") || path.Clean(relative) != relative {
		return "", errors.New("unsafe backup path")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("unsafe backup path")
		}
	}
	return filepath.Join(root, filepath.FromSlash(relative)), nil
}

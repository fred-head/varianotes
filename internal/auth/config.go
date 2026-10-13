package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
)

type Mode string

const (
	ModeLocal    Mode = "local"
	ModeDisabled Mode = "disabled"

	OwnerPrincipal = "owner"
)

type Config struct {
	Mode                Mode
	ModeExplicit        bool
	MetadataPath        string
	EncryptionKeyPath   string
	CookieSecure        bool
	TrustedProxies      []netip.Prefix
	AllowMFAKeyRecovery bool
	PasswordCoordinator PasswordChangeCoordinator
}

// PasswordChangeCoordinator prepares and applies password-encrypted vault-key
// envelopes. The auth service journals the returned plan beside the
// credential transition and calls Apply or Rollback during recovery.
type PasswordChangeCoordinator interface {
	PreparePasswordChange(ctx context.Context, currentPassword, newPassword string) ([]byte, error)
	ApplyPasswordChange(ctx context.Context, plan []byte) error
	RollbackPasswordChange(ctx context.Context, plan []byte) error
	HasProtectedContent(ctx context.Context) (bool, error)
}

type EnvironmentLookup func(string) (string, bool)

// ConfigFromEnvironment selects local authentication unless the operator
// explicitly configures disabled mode.
func ConfigFromEnvironment(lookup EnvironmentLookup) (Config, error) {
	if lookup == nil {
		return Config{}, errors.New("authentication environment lookup is required")
	}

	rawMode, explicit := lookup("VARIANOTES_AUTH_MODE")
	if strings.TrimSpace(rawMode) == "" {
		rawMode = string(ModeLocal)
		explicit = false
	}
	mode, err := ParseMode(rawMode)
	if err != nil {
		return Config{}, err
	}

	metadataPath, _ := lookup("VARIANOTES_AUTH_METADATA")
	metadataPath = strings.TrimSpace(metadataPath)
	if metadataPath == "" {
		notebookMetadata, ok := lookup("VARIANOTES_NOTEBOOK_METADATA")
		if ok && strings.TrimSpace(notebookMetadata) != "" {
			metadataPath = filepath.Join(filepath.Dir(strings.TrimSpace(notebookMetadata)), "auth.db")
		} else {
			metadataPath = "/data/app/auth.db"
		}
	}
	if !filepath.IsAbs(metadataPath) {
		return Config{}, errors.New("authentication metadata path must be absolute")
	}
	encryptionKeyPath, _ := lookup("VARIANOTES_AUTH_ENCRYPTION_KEY_FILE")
	encryptionKeyPath = strings.TrimSpace(encryptionKeyPath)
	if encryptionKeyPath == "" {
		encryptionKeyPath = filepath.Join(filepath.Dir(metadataPath), "auth.key")
	}
	if !filepath.IsAbs(encryptionKeyPath) {
		return Config{}, errors.New("authentication encryption key path must be absolute")
	}

	cookieSecure := true
	if raw, ok := lookup("VARIANOTES_SESSION_COOKIE_SECURE"); ok && strings.TrimSpace(raw) != "" {
		cookieSecure, err = strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, errors.New("VARIANOTES_SESSION_COOKIE_SECURE must be true or false")
		}
	}
	trustedProxies, err := parseTrustedProxies(lookup)
	if err != nil {
		return Config{}, err
	}
	return Config{Mode: mode, ModeExplicit: explicit, MetadataPath: filepath.Clean(metadataPath), EncryptionKeyPath: filepath.Clean(encryptionKeyPath), CookieSecure: cookieSecure, TrustedProxies: trustedProxies}, nil
}

func parseTrustedProxies(lookup EnvironmentLookup) ([]netip.Prefix, error) {
	raw, _ := lookup("VARIANOTES_TRUSTED_PROXIES")
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			return nil, errors.New("VARIANOTES_TRUSTED_PROXIES contains an empty entry")
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", value)
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case ModeLocal, ModeDisabled:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported authentication mode %q; expected local or disabled", value)
	}
}

package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

const exportGrantLifetime = 2 * time.Minute
const maxExportGrants = 256

var errInvalidExportGrant = errors.New("export reauthentication is required")
var errExportGrantCapacity = errors.New("too many pending export grants")

type exportGrant struct {
	vaultID     string
	sessionHash [32]byte
	generation  uint64
	fingerprint string
	expiresAt   time.Time
}

// exportGrantStore keeps only a short-lived, one-use authorization binding.
// Passwords and vault keys are never retained in the grant.
type exportGrantStore struct {
	mu     sync.Mutex
	grants map[string]exportGrant
}

func newExportGrantStore() *exportGrantStore {
	return &exportGrantStore{grants: make(map[string]exportGrant)}
}

func (s *exportGrantStore) Issue(vaultID string, sessionHash []byte, generation uint64, fingerprint string, now time.Time) (string, error) {
	if s == nil || vaultID == "" || len(sessionHash) != sha256.Size || generation == 0 || fingerprint == "" {
		return "", errInvalidExportGrant
	}
	var session [32]byte
	copy(session[:], sessionHash)
	s.mu.Lock()
	defer s.mu.Unlock()
	for existing, grant := range s.grants {
		if !grant.expiresAt.After(now) {
			delete(s.grants, existing)
		} else if grant.vaultID == vaultID && subtle.ConstantTimeCompare(grant.sessionHash[:], session[:]) == 1 {
			delete(s.grants, existing)
		}
	}
	if len(s.grants) >= maxExportGrants {
		return "", errExportGrantCapacity
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes[:])
	key := hashExportToken(token)
	s.grants[key] = exportGrant{
		vaultID: vaultID, sessionHash: session, generation: generation,
		fingerprint: fingerprint, expiresAt: now.Add(exportGrantLifetime),
	}
	return token, nil
}

func (s *exportGrantStore) Consume(token, vaultID string, sessionHash []byte, now time.Time) (exportGrant, error) {
	if s == nil || len(sessionHash) != sha256.Size || len(token) != 64 {
		return exportGrant{}, errInvalidExportGrant
	}
	if _, err := hex.DecodeString(token); err != nil {
		return exportGrant{}, errInvalidExportGrant
	}
	key := hashExportToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[key]
	if !ok || !grant.expiresAt.After(now) || grant.vaultID != vaultID ||
		subtle.ConstantTimeCompare(grant.sessionHash[:], sessionHash) != 1 {
		return exportGrant{}, errInvalidExportGrant
	}
	delete(s.grants, key)
	return grant, nil
}

func hashExportToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

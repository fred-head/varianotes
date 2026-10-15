package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/fred-head/varianotes/internal/files"
)

type protectedNoteAccess struct {
	enabled bool
	vaultID string
	store   *encryption.CatalogStore
	journal *encryption.TransactionJournal
	err     error
}

func newProtectedNoteAccess(repository *files.Repository, registryPath string, record notebookRecord) *protectedNoteAccess {
	if repository == nil || !repository.Configured() {
		return nil
	}
	access := &protectedNoteAccess{}
	if registryPath == "" || record.ID == "" {
		hasAge, ageErr := repository.HasAgeFiles()
		if ageErr != nil {
			access.enabled, access.err = true, ageErr
			return access
		}
		if hasAge {
			access.enabled = true
			access.err = errors.New("encrypted files exist without registered encryption metadata")
			return access
		}
		return nil
	}
	state, err := inspectVaultProtection(record, registryPath)
	if err != nil {
		access.enabled, access.err = true, err
		return access
	}
	if !state.protected {
		hasAge, ageErr := repository.HasAgeFiles()
		if ageErr != nil {
			access.enabled, access.err = true, ageErr
			return access
		}
		if hasAge {
			access.enabled = true
			access.err = errors.New("encrypted files exist without valid encryption metadata")
			return access
		}
		return nil
	}
	access.enabled = true
	access.vaultID = record.ID
	access.store, err = encryption.NewCatalogStore(state.catalogPath)
	if err != nil {
		access.err = err
		return access
	}
	access.journal, err = encryption.NewTransactionJournal(record.LocalPath, access.store)
	if err != nil {
		access.err = err
	}
	return access
}

func (access *protectedNoteAccess) catalogForKey(vaultKey []byte) (encryption.Catalog, error) {
	if access == nil || !access.enabled || access.err != nil || access.store == nil || access.journal == nil {
		if access != nil && access.err != nil {
			return encryption.Catalog{}, access.err
		}
		return encryption.Catalog{}, errors.New("protected note storage is unavailable")
	}
	expectedID := registeredCatalogID(access.vaultID)
	var catalog encryption.Catalog
	err := access.journal.WithRecoveredCatalog(vaultKey, expectedID, func(current encryption.Catalog) error {
		catalog = current
		return nil
	})
	if err != nil {
		return encryption.Catalog{}, fmt.Errorf("recover encrypted note storage: %w", err)
	}
	return catalog, nil
}

func beginProtectedNoteLease(w http.ResponseWriter, r *http.Request, access *protectedNoteAccess, sessions *auth.Sessions, manager *encryption.KeyManager) (*encryption.KeyLease, bool) {
	if access == nil || !access.enabled {
		return nil, false
	}
	if access.err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encrypted note storage is unavailable"})
		return nil, true
	}
	if sessions == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "an authenticated session is required to open encrypted notes"})
		return nil, true
	}
	lease, err := manager.BeginUse(access.vaultID, sessions.CurrentHash(r.Context()))
	switch {
	case err == nil:
		return lease, true
	case errors.Is(err, encryption.ErrVaultLocked):
		writeJSON(w, http.StatusLocked, map[string]string{"error": "vault is locked", "code": "encryption_locked"})
	case errors.Is(err, encryption.ErrSessionRequired):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "an authenticated session is required"})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "encrypted note access is unavailable"})
	}
	return nil, true
}

func withProtectedSession(ctx context.Context, sessions *auth.Sessions, manager *encryption.KeyManager, work func() error) error {
	err := sessions.WithActiveSession(ctx, work)
	if errors.Is(err, auth.ErrAuthentication) || errors.Is(err, auth.ErrSessionBarrier) {
		if manager != nil && sessions != nil {
			err = errors.Join(err, manager.RevokeSessionEverywhere(sessions.CurrentHash(ctx)))
		}
	}
	return err
}

func commitProtectedLease(ctx context.Context, sessions *auth.Sessions, manager *encryption.KeyManager, lease *encryption.KeyLease, publish func() error) error {
	err := withProtectedSession(ctx, sessions, manager, func() error {
		return lease.Commit(publish)
	})
	if err != nil && lease != nil {
		lease.Close()
	}
	return err
}

type protectedSessionWriter struct {
	ctx         context.Context
	sessions    *auth.Sessions
	manager     *encryption.KeyManager
	lease       *encryption.KeyLease
	destination io.Writer
}

func (writer *protectedSessionWriter) Write(content []byte) (int, error) {
	written := 0
	err := withProtectedSession(writer.ctx, writer.sessions, writer.manager, func() error {
		return writer.lease.GuardCommit(func() error {
			var err error
			written, err = writer.destination.Write(content)
			if err == nil && written != len(content) {
				return io.ErrShortWrite
			}
			return err
		})
	})
	return written, err
}

func writeProtectedNoteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrAuthentication):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
	case errors.Is(err, auth.ErrSessionBarrier):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "authentication state is unavailable"})
	case errors.Is(err, encryption.ErrVaultLocked):
		writeJSON(w, http.StatusLocked, map[string]string{"error": "vault is locked", "code": "encryption_locked"})
	case errors.Is(err, encryption.ErrSessionRequired):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "an authenticated session is required"})
	case errors.Is(err, encryption.ErrInvalidVaultID):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid vault ID"})
	default:
		writeRepositoryError(w, err)
	}
}

func rejectUnimplementedProtectedOperation(w http.ResponseWriter, access *protectedNoteAccess) bool {
	if access == nil || !access.enabled {
		return false
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "this operation is unavailable for encrypted vaults",
		"code":  "encrypted_operation_unavailable",
	})
	return true
}

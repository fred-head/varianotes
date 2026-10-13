package app

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fred-head/varianotes/internal/auth"
	"github.com/fred-head/varianotes/internal/encryption"
	"github.com/fred-head/varianotes/internal/files"
)

func handleNotebookExport(w http.ResponseWriter, r *http.Request, logger *slog.Logger, metadataPath string, sessions *auth.Sessions, manager *encryption.KeyManager, grants *exportGrantStore, reauthenticated bool) {
	setExportNoStore(w)
	record, err := findNotebook(metadataPath, r.PathValue("notebookID"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "vault not found"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault could not be exported"})
		}
		return
	}
	repository, err := files.NewRepository(record.LocalPath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault files are unavailable"})
		return
	}
	access := newProtectedNoteAccess(repository, metadataPath, record)
	if access == nil || !access.enabled {
		if reauthenticated {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "export authorization is invalid", "code": "export_reauthentication_required"})
			return
		}
		writeOrdinaryNotebookExport(w, logger, repository, record)
		return
	}
	if access.err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "protected vault export is unavailable"})
		return
	}
	if !reauthenticated {
		lease, _ := beginProtectedNoteLease(w, r, access, sessions, manager)
		if lease == nil {
			return
		}
		defer lease.Close()
		var catalog encryption.Catalog
		var snapshot files.ArchiveSnapshot
		err := lease.UseKey(func(key []byte) error {
			var loadErr error
			catalog, loadErr = access.catalogForKey(key)
			if loadErr != nil {
				return loadErr
			}
			snapshot, loadErr = repository.InspectArchiveWithCatalog(catalog, key)
			return loadErr
		})
		if err != nil {
			writeExportFailure(w, err)
			return
		}
		if snapshot.ContainsProtected {
			writeJSON(w, http.StatusPreconditionRequired, map[string]string{
				"error": "protected content requires the current vault password",
				"code":  "export_reauthentication_required",
			})
			return
		}
		if err := lease.CheckCurrent(); err != nil {
			writeExportFailure(w, err)
			return
		}
		setNotebookExportHeaders(w, record)
		err = lease.UseKey(func(key []byte) error {
			return repository.WriteArchiveWithCatalog(w, catalog, key, snapshot.Fingerprint, lease.CheckCurrent)
		})
		if err != nil {
			logger.Warn("notebook export stream stopped", "notebook", record.ID)
		}
		return
	}

	if sessions == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated session is required"})
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
		return
	}
	sessionHash := sessions.CurrentHash(r.Context())
	grant, err := grants.Consume(input.Token, record.ID, sessionHash, time.Now())
	if err != nil || !manager.GenerationCurrent(record.ID, sessionHash, grant.generation) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "export authorization expired or was revoked", "code": "export_reauthentication_required"})
		return
	}
	lease, err := manager.BeginUse(record.ID, sessionHash)
	if err != nil || lease.Generation() != grant.generation {
		if lease != nil {
			lease.Close()
		}
		writeExportFailure(w, encryption.ErrVaultLocked)
		return
	}
	defer lease.Close()
	var catalog encryption.Catalog
	var snapshot files.ArchiveSnapshot
	err = lease.UseKey(func(key []byte) error {
		var loadErr error
		catalog, loadErr = access.catalogForKey(key)
		if loadErr != nil {
			return loadErr
		}
		snapshot, loadErr = repository.InspectArchiveWithCatalog(catalog, key)
		return loadErr
	})
	if err != nil {
		writeExportFailure(w, err)
		return
	}
	if !snapshot.ContainsProtected || snapshot.Fingerprint != grant.fingerprint {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "vault contents changed; start the export again", "code": "export_snapshot_changed"})
		return
	}
	if err := lease.CheckCurrent(); err != nil {
		writeExportFailure(w, err)
		return
	}
	setNotebookExportHeaders(w, record)
	err = lease.UseKey(func(key []byte) error {
		return repository.WriteArchiveWithCatalog(w, catalog, key, grant.fingerprint, lease.CheckCurrent)
	})
	if err != nil {
		logger.Warn("reauthenticated notebook export stream stopped", "notebook", record.ID)
	}
}

func handleNotebookExportReauthentication(w http.ResponseWriter, r *http.Request, metadataPath string, sessions *auth.Sessions, manager *encryption.KeyManager, grants *exportGrantStore) {
	setExportNoStore(w)
	if sessions == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated session is required"})
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSONWithLimit(w, r, &input, maxAuthRequestBodySize) {
		return
	}
	if input.Password == "" || len(input.Password) > 1024 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vault password is required"})
		return
	}
	record, err := findNotebook(metadataPath, r.PathValue("notebookID"))
	if err != nil {
		writeExportFailure(w, err)
		return
	}
	repository, err := files.NewRepository(record.LocalPath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault files are unavailable"})
		return
	}
	access := newProtectedNoteAccess(repository, metadataPath, record)
	if access == nil || !access.enabled {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "vault has no protected content", "code": "export_not_protected"})
		return
	}
	if access.err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "protected vault export is unavailable"})
		return
	}
	state, err := inspectVaultProtection(record, metadataPath)
	if err != nil || !state.hasEnvelope {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault password envelope is unavailable"})
		return
	}
	envelopeStore, err := encryption.NewVaultKeyEnvelopeStore(state.envelopePath)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault password envelope is unavailable"})
		return
	}
	envelope, err := envelopeStore.Load()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault password envelope is unavailable"})
		return
	}
	lease, _ := beginProtectedNoteLease(w, r, access, sessions, manager)
	if lease == nil {
		return
	}
	defer lease.Close()
	invalidPassword := false
	var issuedToken string
	err = lease.UseKey(func(activeKey []byte) error {
		vaultKey, decryptErr := encryption.DecryptVaultKey(input.Password, envelope)
		if decryptErr != nil || subtle.ConstantTimeCompare(vaultKey, activeKey) != 1 {
			clear(vaultKey)
			invalidPassword = true
			return nil
		}
		defer clear(vaultKey)
		catalog, catalogErr := access.catalogForKey(vaultKey)
		if catalogErr != nil {
			return catalogErr
		}
		snapshot, snapshotErr := repository.InspectArchiveWithCatalog(catalog, vaultKey)
		if snapshotErr != nil {
			return snapshotErr
		}
		if snapshot.ContainsProtected {
			issuedToken, snapshotErr = grants.Issue(record.ID, sessions.CurrentHash(r.Context()), lease.Generation(), snapshot.Fingerprint, time.Now())
		}
		return snapshotErr
	})
	input.Password = ""
	if invalidPassword {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "vault password is incorrect", "code": "invalid_credentials"})
		return
	}
	if err != nil {
		writeExportFailure(w, err)
		return
	}
	if issuedToken == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "vault has no protected content", "code": "export_not_protected"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": issuedToken})
}

func writeOrdinaryNotebookExport(w http.ResponseWriter, logger *slog.Logger, repository *files.Repository, record notebookRecord) {
	if err := repository.ValidateArchive(); err != nil {
		writeExportFailure(w, err)
		return
	}
	setNotebookExportHeaders(w, record)
	if err := repository.WriteArchive(w); err != nil {
		logger.Warn("notebook export stream stopped", "notebook", record.ID)
	}
}

func setNotebookExportHeaders(w http.ResponseWriter, record notebookRecord) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": strings.TrimSpace(record.Name) + ".zip"}))
	setExportNoStore(w)
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func setExportNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	if w.Header().Get("Vary") == "" {
		w.Header().Set("Vary", "Cookie")
	}
}

func writeExportFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, encryption.ErrVaultLocked):
		writeJSON(w, http.StatusLocked, map[string]string{"error": "vault is locked", "code": "encryption_locked"})
	case errors.Is(err, encryption.ErrSessionRequired):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authenticated session is required"})
	case errors.Is(err, files.ErrExportTooLarge), errors.Is(err, files.ErrExportTooManyEntries):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "vault is too large to export in one archive"})
	case errors.Is(err, files.ErrExportSnapshotChanged):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "vault contents changed; start the export again", "code": "export_snapshot_changed"})
	case errors.Is(err, files.ErrProtectedContentCorrupt), errors.Is(err, files.ErrProtectedObjectMissing):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "protected vault content is unavailable"})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "vault could not be exported"})
	}
}

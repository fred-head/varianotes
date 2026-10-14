package encryption

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	transactionFormatVersion   uint8 = 1
	transactionMACDomain             = "varianotes/transaction-journal/v1\x00"
	maxTransactionJournalBytes       = 16 << 20
	maxTransactionChanges            = 100_000
	maxTransactionFileBytes          = 64 << 20
	maxTransactionTotalBytes   int64 = 2 << 30
)

var (
	ErrInvalidTransaction        = errors.New("invalid encryption transaction")
	ErrTransactionJournalCorrupt = errors.New("encryption transaction journal is corrupt")
	ErrTransactionPending        = errors.New("encryption transaction is already pending")
	ErrTransactionNeedsRecovery  = errors.New("encryption transaction requires recovery")
	ErrTransactionConflict       = errors.New("encryption transaction target changed before commit")
)

// CiphertextChange describes one encrypted vault file to replace or remove.
// Callers must provide complete, authenticated age ciphertext in Ciphertext;
// plaintext is never accepted as transaction staging data. Delete leaves the
// current file untouched until the durable commit point.
type CiphertextChange struct {
	Path               string
	ObjectID           string
	Ciphertext         []byte
	ContentKeyEnvelope ContentKeyEnvelope
	Delete             bool
	// ExpectedSHA256 binds a delete to the bytes read during conversion. It is
	// checked in memory while preparing; only a vault-keyed fingerprint is
	// persisted for forward recovery.
	ExpectedSHA256 string
	// RejectHardlinks makes deletion fail closed if another path still shares
	// the source inode. It is used when converting plaintext originals.
	RejectHardlinks bool
	// PublishPlaintext is only for an explicitly approved decrypt operation or
	// ordinary backlink update. Staging remains encrypted until durable commit.
	PublishPlaintext bool
}

// TransactionJournal coordinates ciphertext replacements and an authenticated
// catalog replacement. Staging files for vault content sit beside their final
// paths so rename remains atomic on the vault filesystem. The journal and the
// staged catalog live beside the catalog in server-owned application metadata.
// Only one backend writer per vault is supported.
type TransactionJournal struct {
	vaultRoot string
	catalog   *CatalogStore
	name      string
	mu        sync.Mutex
	fault     func(string) error
	parent    *TransactionJournal
	guard     func(func() error) error
}

type transactionRecord struct {
	Version           uint8               `json:"version"`
	ID                string              `json:"id"`
	VaultID           string              `json:"vault_id"`
	State             string              `json:"state"`
	Changes           []transactionChange `json:"changes"`
	CatalogStage      string              `json:"catalog_stage"`
	CatalogSHA256     string              `json:"catalog_sha256"`
	MAC               string              `json:"authentication"`
	CreateDirectories []string            `json:"create_directories,omitempty"`
	RemoveDirectories []string            `json:"remove_directories,omitempty"`
}

type transactionChange struct {
	Path                 string              `json:"path"`
	ObjectID             string              `json:"object_id,omitempty"`
	Stage                string              `json:"stage,omitempty"`
	SHA256               string              `json:"sha256,omitempty"`
	Delete               bool                `json:"delete,omitempty"`
	ExpectedFingerprint  string              `json:"expected_fingerprint,omitempty"`
	RejectHardlinks      bool                `json:"reject_hardlinks,omitempty"`
	PlaintextFingerprint string              `json:"plaintext_fingerprint,omitempty"`
	ContentKeyEnvelope   *ContentKeyEnvelope `json:"content_key_envelope,omitempty"`
	PriorFingerprint     string              `json:"prior_fingerprint,omitempty"`
}

type transactionPayload struct {
	Version           uint8               `json:"version"`
	ID                string              `json:"id"`
	VaultID           string              `json:"vault_id"`
	State             string              `json:"state"`
	Changes           []transactionChange `json:"changes"`
	CatalogStage      string              `json:"catalog_stage"`
	CatalogSHA256     string              `json:"catalog_sha256"`
	CreateDirectories []string            `json:"create_directories,omitempty"`
	RemoveDirectories []string            `json:"remove_directories,omitempty"`
}

// PreparedTransaction is a durable prepare record plus ciphertext staging.
// The original target fingerprints are held only in memory; a restart before
// Commit discards the prepared transaction and leaves originals untouched.
type PreparedTransaction struct {
	journal      *TransactionJournal
	record       transactionRecord
	key          []byte
	prior        map[string]string
	priorCatalog string
	used         bool
	guard        func(func() error) error
}

// WithCommitGuard scopes durable decisions to a caller's live lease without
// modifying the shared journal or taking repository locks inside the guard.
func (j *TransactionJournal) WithCommitGuard(guard func(func() error) error) *TransactionJournal {
	if j == nil || guard == nil {
		return nil
	}
	return &TransactionJournal{parent: j, guard: guard}
}

// GuardMutation also covers ordinary Trash paths in an encrypted vault. Those
// callers must acquire all filesystem locks before entering this leaf guard.
func (j *TransactionJournal) GuardMutation(work func() error) error {
	if work == nil {
		return ErrInvalidTransaction
	}
	if j != nil && j.guard != nil {
		return j.guard(work)
	}
	return work()
}

// NewTransactionJournal binds a journal to one vault root and its catalog.
func NewTransactionJournal(vaultDirectory string, catalog *CatalogStore) (*TransactionJournal, error) {
	if catalog == nil || vaultDirectory == "" {
		return nil, ErrInvalidTransaction
	}
	absolute, err := filepath.Abs(vaultDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve transaction vault root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect transaction vault root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidTransaction
	}
	relativeMetadata, err := filepath.Rel(filepath.Clean(absolute), catalog.directory)
	if err != nil {
		return nil, ErrInvalidTransaction
	}
	if relativeMetadata == "." || (relativeMetadata != ".." && !strings.HasPrefix(relativeMetadata, ".."+string(filepath.Separator))) {
		return nil, ErrInvalidTransaction
	}
	return &TransactionJournal{
		vaultRoot: filepath.Clean(absolute),
		catalog:   catalog,
		name:      "." + catalog.name + ".transaction.json",
	}, nil
}

// Prepare validates and fingerprints current targets, writes a durable
// prepared journal, stages ciphertext and the next authenticated catalog, and
// verifies every staged byte. No live content or catalog path is changed.
func (j *TransactionJournal) Prepare(changes []CiphertextChange, catalog Catalog, vaultKey []byte) (*PreparedTransaction, error) {
	return j.PrepareContext(context.Background(), changes, catalog, vaultKey)
}

// PrepareContext stages a transaction and aborts it if the caller cancels
// before every ciphertext and the authenticated catalog have been verified.
func (j *TransactionJournal) PrepareContext(ctx context.Context, changes []CiphertextChange, catalog Catalog, vaultKey []byte) (*PreparedTransaction, error) {
	return j.prepareFilesystem(ctx, changes, catalog, vaultKey, nil, nil)
}

// PrepareFilesystem also journals directory creation/removal. Before commit
// only empty destination directories and ciphertext stages may be created.
// Prepared recovery removes them; committed recovery completes publication.
func (j *TransactionJournal) PrepareFilesystem(changes []CiphertextChange, catalog Catalog, vaultKey []byte, createDirectories, removeDirectories []string) (*PreparedTransaction, error) {
	return j.prepareFilesystem(context.Background(), changes, catalog, vaultKey, createDirectories, removeDirectories)
}

func (j *TransactionJournal) prepareFilesystem(ctx context.Context, changes []CiphertextChange, catalog Catalog, vaultKey []byte, createDirectories, removeDirectories []string) (*PreparedTransaction, error) {
	if j != nil && j.parent != nil {
		tx, err := j.parent.prepareFilesystem(ctx, changes, catalog, vaultKey, createDirectories, removeDirectories)
		if err == nil {
			tx.guard = j.guard
		}
		return tx, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if j == nil || len(vaultKey) != VaultKeySize || len(changes) > maxTransactionChanges {
		return nil, ErrInvalidTransaction
	}
	encodedCatalog, err := MarshalCatalog(catalog, vaultKey)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	priorCatalog, err := j.currentCatalogDigest()
	if err != nil {
		return nil, err
	}
	previousCatalog, err := j.catalog.Load(vaultKey, catalog.VaultID)
	if errors.Is(err, ErrCatalogMissing) {
		previousCatalog = Catalog{}
	} else if err != nil {
		return nil, err
	}
	if err := validateTransactionBindings(changes, catalog); err != nil {
		return nil, err
	}

	root, err := os.OpenRoot(j.vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("open transaction vault root: %w", err)
	}
	defer root.Close()
	createDirectories = sortedTransactionDirectories(createDirectories)
	removeDirectories = sortedTransactionDirectories(removeDirectories)
	if err := validatePlannedDirectories(root, createDirectories, removeDirectories); err != nil {
		return nil, err
	}
	prior := make(map[string]string, len(changes))
	previousObjects := make(map[string]CatalogObject, len(previousCatalog.Objects))
	for _, object := range previousCatalog.Objects {
		previousObjects[object.PhysicalPath] = object
	}
	seen := make(map[string]struct{}, len(changes))
	var totalBytes int64
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !isTransactionContentPath(change.Path) {
			return nil, ErrInvalidTransaction
		}
		if _, exists := seen[change.Path]; exists {
			return nil, ErrInvalidTransaction
		}
		seen[change.Path] = struct{}{}
		if change.Delete {
			if change.PublishPlaintext || len(change.Ciphertext) != 0 || change.ObjectID != "" || change.ContentKeyEnvelope.Version != 0 || len(change.ContentKeyEnvelope.Ciphertext) != 0 || (change.ExpectedSHA256 != "" && !isSHA256Hex(change.ExpectedSHA256)) {
				return nil, ErrInvalidTransaction
			}
		} else {
			if len(change.Ciphertext) == 0 || len(change.Ciphertext) > maxTransactionFileBytes || !isCatalogID(change.ObjectID) || change.ExpectedSHA256 != "" || change.RejectHardlinks {
				return nil, ErrInvalidTransaction
			}
			totalBytes += int64(len(change.Ciphertext))
			if totalBytes > maxTransactionTotalBytes {
				return nil, ErrInvalidTransaction
			}
			if err := VerifyEncryptedContent(vaultKey, change.ObjectID, change.Ciphertext, change.ContentKeyEnvelope); err != nil {
				return nil, errors.Join(ErrInvalidTransaction, err)
			}
		}
		if err := validatePlannedTransactionTarget(root, change.Path, createDirectories); err != nil {
			return nil, err
		}
		prior[change.Path], err = transactionTargetDigest(root, change.Path)
		if errors.Is(err, fs.ErrNotExist) && plannedTransactionParent(change.Path, createDirectories) {
			err = nil
			prior[change.Path] = ""
		}
		if err != nil {
			return nil, err
		}
		if change.ExpectedSHA256 != "" && prior[change.Path] != change.ExpectedSHA256 {
			return nil, ErrTransactionConflict
		}
		if change.RejectHardlinks {
			info, statErr := root.Lstat(filepath.FromSlash(change.Path))
			if statErr != nil || !hasSingleLink(info) {
				return nil, errors.Join(ErrInvalidTransaction, statErr)
			}
		}
		if previous, exists := previousObjects[change.Path]; exists && previous.Protected && prior[change.Path] != previous.CiphertextHash {
			return nil, ErrCatalogCiphertextCheck
		}
	}
	if err := j.ensureNoPendingJournal(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	id, err := newCatalogID()
	if err != nil {
		return nil, err
	}
	record := transactionRecord{
		Version: transactionFormatVersion, ID: id, VaultID: catalog.VaultID, State: "prepared",
		CatalogStage: transactionCatalogStageName(id), CatalogSHA256: transactionDigest(encodedCatalog),
		Changes:           make([]transactionChange, 0, len(changes)),
		CreateDirectories: createDirectories, RemoveDirectories: removeDirectories,
	}
	if len(createDirectories)+len(removeDirectories) > 0 {
		record.Version = 2
	}
	ordered := append([]CiphertextChange(nil), changes...)
	sort.Slice(ordered, func(a, b int) bool { return ordered[a].Path < ordered[b].Path })
	for index, change := range ordered {
		entry := transactionChange{Path: change.Path, ObjectID: change.ObjectID, Delete: change.Delete, RejectHardlinks: change.RejectHardlinks}
		if change.ExpectedSHA256 != "" {
			entry.ExpectedFingerprint = transactionSourceFingerprint(vaultKey, change.ExpectedSHA256)
		}
		if !change.Delete {
			entry.Stage = transactionContentStageName(id, index)
			entry.SHA256 = transactionDigest(change.Ciphertext)
			if change.PublishPlaintext {
				record.Version = 2
				plaintext, err := DecryptContent(vaultKey, change.ObjectID, change.Ciphertext, change.ContentKeyEnvelope)
				if err != nil {
					return nil, err
				}
				entry.PlaintextFingerprint = transactionSourceFingerprint(vaultKey, transactionDigest(plaintext))
				clear(plaintext)
				envelope := change.ContentKeyEnvelope
				entry.ContentKeyEnvelope = &envelope
			}
		}
		record.Changes = append(record.Changes, entry)
	}
	if record.Version == 2 {
		for index := range record.Changes {
			entry := &record.Changes[index]
			if !entry.Delete {
				entry.PriorFingerprint = "missing"
				if digest := prior[entry.Path]; digest != "" {
					entry.PriorFingerprint = transactionSourceFingerprint(vaultKey, digest)
				}
			}
		}
	}
	if err := validateTransactionRecord(record); err != nil {
		return nil, err
	}
	if err := j.saveRecord(record, vaultKey); err != nil {
		return nil, fmt.Errorf("persist prepared encryption transaction: %w", err)
	}
	tx := &PreparedTransaction{
		journal: j, record: record, key: append([]byte(nil), vaultKey...), prior: prior, priorCatalog: priorCatalog,
	}
	if err := j.createTransactionDirectories(root, record); err != nil {
		return nil, j.abortPrepared(tx, err)
	}
	for index, change := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, j.abortPrepared(tx, err)
		}
		entry := record.Changes[index]
		if change.Delete {
			continue
		}
		if err := j.writeVaultStage(root, entry.Path, entry.Stage, change.Ciphertext); err != nil {
			return nil, j.abortPrepared(tx, err)
		}
	}
	metadataRoot, err := os.OpenRoot(j.catalog.directory)
	if err != nil {
		return nil, j.abortPrepared(tx, fmt.Errorf("open catalog metadata for staging: %w", err))
	}
	stageErr := j.writeMetadataStage(metadataRoot, record.CatalogStage, encodedCatalog)
	closeErr := metadataRoot.Close()
	if err := errors.Join(stageErr, closeErr); err != nil {
		return nil, j.abortPrepared(tx, err)
	}
	if err := j.verifyPreparedStages(root, record, encodedCatalog); err != nil {
		return nil, j.abortPrepared(tx, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, j.abortPrepared(tx, err)
	}
	return tx, nil
}

// Commit checks that original targets still match their pre-prepare state,
// durably records the forward-only decision, and applies it. Any error after
// that decision leaves the journal in place and returns ErrTransactionNeedsRecovery.
func (tx *PreparedTransaction) Commit() error {
	return tx.CommitContext(context.Background())
}

// CommitContext observes cancellation until the durable forward commit point.
// Once that point is written, recovery always finishes the transaction.
func (tx *PreparedTransaction) CommitContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx == nil || tx.journal == nil || tx.used {
		return ErrInvalidTransaction
	}
	if tx.guard != nil {
		guard := tx.guard
		tx.guard = nil
		entered := false
		err := guard(func() error { entered = true; return tx.CommitContext(ctx) })
		if !entered {
			err = errors.Join(err, tx.Abort())
		}
		return err
	}
	tx.used = true
	defer clear(tx.key)
	j := tx.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	root, err := os.OpenRoot(j.vaultRoot)
	if err != nil {
		return fmt.Errorf("open transaction vault root: %w", err)
	}
	defer root.Close()
	record, err := j.loadRecord(tx.key)
	if err != nil {
		return err
	}
	if record.ID != tx.record.ID || record.State != "prepared" {
		return ErrTransactionJournalCorrupt
	}
	if err := ctx.Err(); err != nil {
		return j.abortPrepared(tx, err)
	}
	for _, entry := range record.Changes {
		current, digestErr := transactionTargetDigest(root, entry.Path)
		if digestErr != nil {
			return digestErr
		}
		if current != tx.prior[entry.Path] {
			return j.abortPrepared(tx, ErrTransactionConflict)
		}
		if entry.RejectHardlinks {
			info, statErr := root.Lstat(filepath.FromSlash(entry.Path))
			if statErr != nil || !hasSingleLink(info) {
				return j.abortPrepared(tx, errors.Join(ErrInvalidTransaction, statErr))
			}
		}
	}
	currentCatalog, err := j.currentCatalogDigest()
	if err != nil {
		return err
	}
	if currentCatalog != tx.priorCatalog {
		return j.abortPrepared(tx, ErrTransactionConflict)
	}
	if err := ctx.Err(); err != nil {
		return j.abortPrepared(tx, err)
	}
	if err := j.fail("transaction.commit.before"); err != nil {
		return j.abortPrepared(tx, err)
	}
	record.State = "committed"
	if err := j.saveRecord(record, tx.key); err != nil {
		persisted, loadErr := j.loadRecord(tx.key)
		if loadErr == nil && persisted.ID == record.ID && persisted.State == "prepared" {
			return j.abortPrepared(tx, err)
		}
		return errors.Join(ErrTransactionNeedsRecovery, err, loadErr)
	}
	tx.record = record
	if err := j.fail("transaction.commit.after"); err != nil {
		return errors.Join(ErrTransactionNeedsRecovery, err)
	}
	if err := j.applyCommitted(root, record, tx.key); err != nil {
		return errors.Join(ErrTransactionNeedsRecovery, err)
	}
	return nil
}

// Abort discards staged state while the transaction is still prepared. A
// committed transaction cannot be rolled back and must be recovered forward.
func (tx *PreparedTransaction) Abort() error {
	if tx == nil || tx.journal == nil || tx.used {
		return ErrInvalidTransaction
	}
	tx.used = true
	defer clear(tx.key)
	j := tx.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.loadRecord(tx.key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.ID != tx.record.ID || record.State != "prepared" {
		return ErrTransactionNeedsRecovery
	}
	return tx.journal.cleanupRecord(record)
}

// Recover discards a prepared transaction because its live targets are still
// the old state. A committed transaction is verified and completed forward.
// Callers must run it before serving affected content after a restart.
func (j *TransactionJournal) Recover(vaultKey []byte) error {
	return j.recoverForVault(vaultKey, "")
}

// RecoverForVault also checks the independently registered vault identity
// before discarding stages or publishing a committed journal. Applications
// must use this when a journal belongs to a registered vault.
func (j *TransactionJournal) RecoverForVault(vaultKey []byte, expectedVaultID string) error {
	if !isCatalogID(expectedVaultID) {
		return ErrInvalidVaultID
	}
	return j.recoverForVault(vaultKey, expectedVaultID)
}

func (j *TransactionJournal) recoverForVault(vaultKey []byte, expectedVaultID string) error {
	if j == nil || len(vaultKey) != VaultKeySize {
		return ErrInvalidTransaction
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.loadRecord(vaultKey)
	if errors.Is(err, fs.ErrNotExist) {
		return j.cleanupOrphanJournalStages()
	}
	if err != nil {
		return err
	}
	if expectedVaultID != "" && record.VaultID != expectedVaultID {
		return ErrCatalogAuthentication
	}
	if record.State == "prepared" {
		if err := j.cleanupRecord(record); err != nil {
			return errors.Join(ErrTransactionNeedsRecovery, err)
		}
		return nil
	}
	root, err := os.OpenRoot(j.vaultRoot)
	if err != nil {
		return errors.Join(ErrTransactionNeedsRecovery, fmt.Errorf("open transaction vault root: %w", err))
	}
	defer root.Close()
	if err := j.applyCommitted(root, record, vaultKey); err != nil {
		return errors.Join(ErrTransactionNeedsRecovery, err)
	}
	return nil
}

func (j *TransactionJournal) ensureNoPendingJournal() error {
	root, err := os.OpenRoot(j.catalog.directory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open transaction metadata directory: %w", err)
	}
	defer root.Close()
	_, err = root.Lstat(j.name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect pending transaction journal: %w", err)
	}
	return ErrTransactionPending
}

func (j *TransactionJournal) currentCatalogDigest() (string, error) {
	root, err := os.OpenRoot(j.catalog.directory)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("open transaction catalog directory: %w", err)
	}
	defer root.Close()
	if err := validateCatalogStoreTarget(root, j.catalog.name); err != nil {
		return "", err
	}
	digest, err := transactionFileDigest(root, j.catalog.name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return digest, err
}

func (j *TransactionJournal) abortPrepared(tx *PreparedTransaction, cause error) error {
	if err := j.cleanupRecord(tx.record); err != nil {
		return errors.Join(ErrTransactionNeedsRecovery, cause, err)
	}
	return cause
}

func (j *TransactionJournal) writeVaultStage(root *os.Root, target, stage string, content []byte) error {
	if err := validateTransactionTarget(root, target); err != nil {
		return err
	}
	stagePath := path.Join(path.Dir(target), stage)
	if err := j.fail("transaction.content_stage.write"); err != nil {
		return err
	}
	file, err := root.OpenFile(filepath.FromSlash(stagePath), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create ciphertext transaction stage: %w", err)
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = j.fail("transaction.content_stage.file_sync")
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write ciphertext transaction stage: %w", err)
	}
	if err := j.syncVaultDirectory(root, path.Dir(target), "transaction.content_stage.directory_sync"); err != nil {
		return err
	}
	return nil
}

func (j *TransactionJournal) writeMetadataStage(root *os.Root, name string, content []byte) error {
	if err := j.fail("transaction.catalog_stage.write"); err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create catalog transaction stage: %w", err)
	}
	written, writeErr := file.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = j.fail("transaction.catalog_stage.file_sync")
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write catalog transaction stage: %w", err)
	}
	return syncTransactionDirectory(root, ".", j.fail, "transaction.catalog_stage.directory_sync")
}

func (j *TransactionJournal) verifyPreparedStages(root *os.Root, record transactionRecord, catalogBytes []byte) error {
	for _, entry := range record.Changes {
		if entry.Delete {
			continue
		}
		stagePath := path.Join(path.Dir(entry.Path), entry.Stage)
		actual, err := transactionFileDigest(root, stagePath)
		if err != nil {
			return fmt.Errorf("verify ciphertext stage for %q: %w", entry.Path, err)
		}
		if actual != entry.SHA256 {
			return ErrTransactionJournalCorrupt
		}
	}
	if transactionDigest(catalogBytes) != record.CatalogSHA256 {
		return ErrTransactionJournalCorrupt
	}
	metadataRoot, err := os.OpenRoot(j.catalog.directory)
	if err != nil {
		return fmt.Errorf("open catalog metadata for verification: %w", err)
	}
	defer metadataRoot.Close()
	actual, err := transactionFileDigest(metadataRoot, record.CatalogStage)
	if err != nil {
		return fmt.Errorf("verify catalog transaction stage: %w", err)
	}
	if actual != record.CatalogSHA256 {
		return ErrTransactionJournalCorrupt
	}
	return nil
}

func (j *TransactionJournal) applyCommitted(root *os.Root, record transactionRecord, vaultKey []byte) error {
	// Verify every remaining stage and the catalog before applying more files.
	// A missing stage is valid only when its final target already has the new
	// digest, which makes recovery idempotent after a partial prior attempt.
	for _, entry := range record.Changes {
		if err := validateTransactionTarget(root, entry.Path); err != nil {
			// Directory cleanup can already have removed a deleted source's
			// parents before a later journal cleanup failure. Absence completes
			// that delete; symlinks and other validation failures still block.
			if entry.Delete && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		if entry.Delete {
			continue
		}
		current, err := transactionTargetDigest(root, entry.Path)
		if err != nil {
			return err
		}
		if transactionChangeInstalled(entry, current, vaultKey) {
			continue
		}
		if entry.PriorFingerprint != "" && !transactionPriorMatches(entry, current, vaultKey) {
			return ErrTransactionConflict
		}
		stagePath := path.Join(path.Dir(entry.Path), entry.Stage)
		stageDigest, err := transactionFileDigest(root, stagePath)
		if err != nil || stageDigest != entry.SHA256 {
			return errors.Join(ErrTransactionJournalCorrupt, err)
		}
	}
	metadataRoot, err := os.OpenRoot(j.catalog.directory)
	if err != nil {
		return fmt.Errorf("open catalog metadata for recovery: %w", err)
	}
	defer metadataRoot.Close()
	if err := validateCatalogStoreTarget(metadataRoot, j.catalog.name); err != nil {
		return err
	}
	catalogDigest, err := transactionFileDigest(metadataRoot, j.catalog.name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var catalogBytes []byte
	if catalogDigest == record.CatalogSHA256 {
		catalogBytes, err = readTransactionFile(metadataRoot, j.catalog.name, maxCatalogBytes)
	} else {
		stageDigest, stageErr := transactionFileDigest(metadataRoot, record.CatalogStage)
		if stageErr != nil || stageDigest != record.CatalogSHA256 {
			return errors.Join(ErrTransactionJournalCorrupt, stageErr)
		}
		catalogBytes, err = readTransactionFile(metadataRoot, record.CatalogStage, maxCatalogBytes)
	}
	if err != nil {
		return err
	}
	nextCatalog, err := ParseCatalog(catalogBytes, vaultKey, record.VaultID)
	if err != nil {
		return errors.Join(ErrTransactionJournalCorrupt, err)
	}
	if err := validateTransactionRecordBindings(record, nextCatalog); err != nil {
		return errors.Join(ErrTransactionJournalCorrupt, err)
	}

	for _, entry := range record.Changes {
		if entry.Delete {
			if err := validateTransactionTarget(root, entry.Path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return err
			}
			if info, err := root.Lstat(filepath.FromSlash(entry.Path)); err == nil {
				if entry.ExpectedFingerprint != "" {
					actual, digestErr := transactionTargetDigest(root, entry.Path)
					if digestErr != nil || transactionSourceFingerprint(vaultKey, actual) != entry.ExpectedFingerprint {
						return errors.Join(ErrTransactionNeedsRecovery, ErrTransactionConflict, digestErr)
					}
				}
				if entry.RejectHardlinks && !hasSingleLink(info) {
					return errors.Join(ErrTransactionNeedsRecovery, ErrInvalidTransaction)
				}
				if err := j.fail("transaction.content.delete"); err != nil {
					return err
				}
				if err := root.Remove(filepath.FromSlash(entry.Path)); err != nil {
					return fmt.Errorf("remove transaction target %q: %w", entry.Path, err)
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := j.syncVaultDirectory(root, path.Dir(entry.Path), "transaction.content.directory_sync"); err != nil {
				return err
			}
			continue
		}
		current, err := transactionTargetDigest(root, entry.Path)
		if err != nil {
			return err
		}
		if transactionChangeInstalled(entry, current, vaultKey) {
			continue
		}
		if entry.PlaintextFingerprint != "" {
			if err := j.publishTransactionPlaintext(root, entry, vaultKey); err != nil {
				return err
			}
			continue
		}
		stagePath := path.Join(path.Dir(entry.Path), entry.Stage)
		if err := j.fail("transaction.content.rename"); err != nil {
			return err
		}
		if err := root.Rename(filepath.FromSlash(stagePath), filepath.FromSlash(entry.Path)); err != nil {
			return fmt.Errorf("install transaction content %q: %w", entry.Path, err)
		}
		if err := j.syncVaultDirectory(root, path.Dir(entry.Path), "transaction.content.directory_sync"); err != nil {
			return err
		}
	}

	catalogDigest, err = transactionFileDigest(metadataRoot, j.catalog.name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if catalogDigest != record.CatalogSHA256 {
		if err := j.fail("transaction.catalog.rename"); err != nil {
			return err
		}
		if err := metadataRoot.Rename(record.CatalogStage, j.catalog.name); err != nil {
			return fmt.Errorf("install transaction catalog: %w", err)
		}
		if err := syncTransactionDirectory(metadataRoot, ".", j.fail, "transaction.catalog.directory_sync"); err != nil {
			return err
		}
	}
	if err := j.removeTransactionDirectories(root, record.RemoveDirectories); err != nil {
		return err
	}
	return j.cleanupRecord(record)
}

func (j *TransactionJournal) saveRecord(record transactionRecord, vaultKey []byte) error {
	encoded, err := marshalTransactionRecord(record, vaultKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(j.catalog.directory, 0o700); err != nil {
		return fmt.Errorf("create transaction metadata directory: %w", err)
	}
	root, err := os.OpenRoot(j.catalog.directory)
	if err != nil {
		return fmt.Errorf("open transaction metadata directory: %w", err)
	}
	defer root.Close()
	if err := validateTransactionJournalTarget(root, j.name); err != nil {
		return err
	}
	var temporary string
	for range 8 {
		id, idErr := newCatalogID()
		if idErr != nil {
			return idErr
		}
		temporary = ".vnotes-journal-" + id + ".tmp"
		file, openErr := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(openErr, fs.ErrExist) {
			continue
		}
		if openErr != nil {
			return fmt.Errorf("create transaction journal stage: %w", openErr)
		}
		phase := "transaction.journal." + record.State
		writeErr := j.fail(phase + ".write")
		if writeErr == nil {
			written, fileWriteErr := file.Write(encoded)
			if fileWriteErr == nil && written != len(encoded) {
				fileWriteErr = io.ErrShortWrite
			}
			writeErr = fileWriteErr
		}
		if writeErr == nil {
			writeErr = j.fail(phase + ".file_sync")
		}
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_ = root.Remove(temporary)
			return fmt.Errorf("write transaction journal: %w", err)
		}
		if err := j.fail(phase + ".rename"); err != nil {
			_ = root.Remove(temporary)
			return err
		}
		if err := root.Rename(temporary, j.name); err != nil {
			_ = root.Remove(temporary)
			return fmt.Errorf("replace transaction journal: %w", err)
		}
		if err := syncTransactionDirectory(root, ".", j.fail, phase+".directory_sync"); err != nil {
			return err
		}
		return nil
	}
	return errors.New("could not allocate a transaction journal stage")
}

func (j *TransactionJournal) loadRecord(vaultKey []byte) (transactionRecord, error) {
	root, err := os.OpenRoot(j.catalog.directory)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return transactionRecord{}, fs.ErrNotExist
		}
		return transactionRecord{}, fmt.Errorf("open transaction metadata directory: %w", err)
	}
	defer root.Close()
	info, err := root.Lstat(j.name)
	if err != nil {
		return transactionRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxTransactionJournalBytes {
		return transactionRecord{}, ErrTransactionJournalCorrupt
	}
	data, err := readTransactionFile(root, j.name, maxTransactionJournalBytes)
	if err != nil {
		return transactionRecord{}, errors.Join(ErrTransactionJournalCorrupt, err)
	}
	record, err := parseTransactionRecord(data, vaultKey)
	if err != nil {
		return transactionRecord{}, err
	}
	return record, nil
}

func (j *TransactionJournal) cleanupRecord(record transactionRecord) error {
	var result error
	root, err := os.OpenRoot(j.vaultRoot)
	if err != nil {
		return fmt.Errorf("open vault to clean transaction stages: %w", err)
	}
	for _, entry := range record.Changes {
		if entry.Delete {
			continue
		}
		stagePath := path.Join(path.Dir(entry.Path), entry.Stage)
		if removeErr := removeTransactionFile(root, stagePath); removeErr != nil {
			result = errors.Join(result, removeErr)
		} else if _, statErr := root.Lstat(filepath.FromSlash(path.Dir(entry.Path))); errors.Is(statErr, fs.ErrNotExist) {
			continue
		} else if syncErr := j.syncVaultDirectory(root, path.Dir(entry.Path), "transaction.cleanup.content_sync"); syncErr != nil {
			result = errors.Join(result, syncErr)
		}
		if entry.PlaintextFingerprint != "" {
			result = errors.Join(result, removeTransactionFile(root, transactionPlainTemporary(entry)))
		}
	}
	if record.State == "prepared" {
		result = errors.Join(result, j.removeTransactionDirectories(root, record.CreateDirectories))
	}
	result = errors.Join(result, root.Close())
	metadataRoot, err := os.OpenRoot(j.catalog.directory)
	if err != nil {
		return errors.Join(result, fmt.Errorf("open metadata to clean transaction stages: %w", err))
	}
	if removeErr := removeTransactionFile(metadataRoot, record.CatalogStage); removeErr != nil {
		result = errors.Join(result, removeErr)
	}
	if result == nil {
		if syncErr := syncTransactionDirectory(metadataRoot, ".", j.fail, "transaction.cleanup.catalog_sync"); syncErr != nil {
			result = errors.Join(result, syncErr)
		}
	}
	if result == nil {
		if removeErr := removeTransactionFile(metadataRoot, j.name); removeErr != nil {
			result = errors.Join(result, removeErr)
		}
	}
	if result == nil {
		result = errors.Join(result, syncTransactionDirectory(metadataRoot, ".", j.fail, "transaction.cleanup.journal_sync"))
	}
	if result == nil {
		result = errors.Join(result, cleanupOrphanJournalStages(metadataRoot))
	}
	result = errors.Join(result, metadataRoot.Close())
	return result
}

func (j *TransactionJournal) cleanupOrphanJournalStages() error {
	root, err := os.OpenRoot(j.catalog.directory)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open metadata to clean transaction journal stages: %w", err)
	}
	defer root.Close()
	return cleanupOrphanJournalStages(root)
}

func cleanupOrphanJournalStages(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	changed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".vnotes-journal-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		identifier := strings.TrimSuffix(strings.TrimPrefix(name, ".vnotes-journal-"), ".tmp")
		if !isCatalogID(identifier) {
			continue
		}
		info, statErr := root.Lstat(name)
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() {
			return ErrTransactionJournalCorrupt
		}
		if err := root.Remove(name); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return syncTransactionDirectory(root, ".", nil, "")
	}
	return nil
}

func (j *TransactionJournal) syncVaultDirectory(root *os.Root, relative string, phase string) error {
	return syncTransactionDirectory(root, relative, j.fail, phase)
}

func (j *TransactionJournal) fail(phase string) error {
	if j.fault != nil {
		return j.fault(phase)
	}
	return nil
}

func validateTransactionTarget(root *os.Root, relative string) error {
	if !isTransactionContentPath(relative) {
		return ErrInvalidTransaction
	}
	components := strings.Split(relative, "/")
	for index := range components {
		current := strings.Join(components[:index+1], "/")
		info, err := root.Lstat(filepath.FromSlash(current))
		if errors.Is(err, fs.ErrNotExist) {
			if index != len(components)-1 {
				return fmt.Errorf("transaction parent %q does not exist: %w", current, fs.ErrNotExist)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect transaction path %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidTransaction
		}
		if index == len(components)-1 {
			if !info.Mode().IsRegular() {
				return ErrInvalidTransaction
			}
		} else if !info.IsDir() {
			return ErrInvalidTransaction
		}
	}
	return nil
}

func transactionTargetDigest(root *os.Root, relative string) (string, error) {
	if err := validateTransactionTarget(root, relative); err != nil {
		return "", err
	}
	_, err := root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return transactionFileDigest(root, relative)
}

func transactionFileDigest(root *os.Root, relative string) (string, error) {
	info, err := root.Lstat(filepath.FromSlash(relative))
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxTransactionFileBytes {
		return "", ErrInvalidTransaction
	}
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return "", err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return "", errors.Join(ErrInvalidTransaction, statErr)
	}
	hasher := sha256.New()
	readBytes, readErr := io.Copy(hasher, io.LimitReader(file, maxTransactionFileBytes+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "", err
	}
	if openedInfo.Size() > maxTransactionFileBytes || readBytes > maxTransactionFileBytes {
		return "", ErrInvalidTransaction
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func readTransactionFile(root *os.Root, relative string, maximum int64) ([]byte, error) {
	info, err := root.Lstat(filepath.FromSlash(relative))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, ErrTransactionJournalCorrupt
	}
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, errors.Join(ErrTransactionJournalCorrupt, statErr)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, ErrTransactionJournalCorrupt
	}
	return data, nil
}

func removeTransactionFile(root *os.Root, relative string) error {
	info, err := root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrTransactionJournalCorrupt
	}
	return root.Remove(filepath.FromSlash(relative))
}

func syncTransactionDirectory(root *os.Root, relative string, fail func(string) error, phase string) error {
	if fail != nil {
		if err := fail(phase); err != nil {
			return err
		}
	}
	if relative == "" {
		relative = "."
	}
	directory, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return fmt.Errorf("open transaction directory %q for sync: %w", relative, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func validateTransactionJournalTarget(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrTransactionJournalCorrupt
	}
	return nil
}

func marshalTransactionRecord(record transactionRecord, vaultKey []byte) ([]byte, error) {
	if len(vaultKey) != VaultKeySize || validateTransactionRecord(record) != nil {
		return nil, ErrInvalidTransaction
	}
	payload := transactionPayloadFromRecord(record)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	keyDeriver := hmac.New(sha256.New, vaultKey)
	_, _ = keyDeriver.Write([]byte(transactionMACDomain))
	authKey := keyDeriver.Sum(nil)
	authenticator := hmac.New(sha256.New, authKey)
	_, _ = authenticator.Write(encoded)
	record.MAC = hex.EncodeToString(authenticator.Sum(nil))
	encoded, err = json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxTransactionJournalBytes {
		return nil, ErrInvalidTransaction
	}
	return encoded, nil
}

func parseTransactionRecord(data, vaultKey []byte) (transactionRecord, error) {
	if len(data) == 0 || len(data) > maxTransactionJournalBytes || len(vaultKey) != VaultKeySize {
		return transactionRecord{}, ErrTransactionJournalCorrupt
	}
	if err := rejectDuplicateJSONFieldsRecursive(data); err != nil {
		return transactionRecord{}, ErrTransactionJournalCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record transactionRecord
	if err := decoder.Decode(&record); err != nil {
		return transactionRecord{}, errors.Join(ErrTransactionJournalCorrupt, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return transactionRecord{}, ErrTransactionJournalCorrupt
	}
	providedMAC, err := hex.DecodeString(record.MAC)
	if err != nil || len(providedMAC) != sha256.Size {
		return transactionRecord{}, ErrTransactionJournalCorrupt
	}
	payload, err := json.Marshal(transactionPayloadFromRecord(record))
	if err != nil {
		return transactionRecord{}, err
	}
	keyDeriver := hmac.New(sha256.New, vaultKey)
	_, _ = keyDeriver.Write([]byte(transactionMACDomain))
	authKey := keyDeriver.Sum(nil)
	authenticator := hmac.New(sha256.New, authKey)
	_, _ = authenticator.Write(payload)
	if !hmac.Equal(providedMAC, authenticator.Sum(nil)) || validateTransactionRecord(record) != nil {
		return transactionRecord{}, ErrTransactionJournalCorrupt
	}
	return record, nil
}

func transactionPayloadFromRecord(record transactionRecord) transactionPayload {
	return transactionPayload{
		Version: record.Version, ID: record.ID, VaultID: record.VaultID, State: record.State,
		Changes: record.Changes, CatalogStage: record.CatalogStage, CatalogSHA256: record.CatalogSHA256,
		CreateDirectories: record.CreateDirectories, RemoveDirectories: record.RemoveDirectories,
	}
}

func validateTransactionRecord(record transactionRecord) error {
	if (record.Version != transactionFormatVersion && record.Version != 2) || !isCatalogID(record.ID) || !isCatalogID(record.VaultID) ||
		(record.State != "prepared" && record.State != "committed") ||
		len(record.Changes) > maxTransactionChanges || record.CatalogStage != transactionCatalogStageName(record.ID) ||
		!isSHA256Hex(record.CatalogSHA256) {
		return ErrInvalidTransaction
	}
	if err := validateTransactionDirectoryRecord(record); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(record.Changes))
	for index, entry := range record.Changes {
		if !isTransactionContentPath(entry.Path) {
			return ErrInvalidTransaction
		}
		if _, exists := seen[entry.Path]; exists {
			return ErrInvalidTransaction
		}
		seen[entry.Path] = struct{}{}
		if entry.Delete {
			if entry.PlaintextFingerprint != "" || entry.ContentKeyEnvelope != nil {
				return ErrInvalidTransaction
			}
			if entry.PriorFingerprint != "" {
				return ErrInvalidTransaction
			}
			if entry.ObjectID != "" || entry.Stage != "" || entry.SHA256 != "" || (entry.ExpectedFingerprint != "" && !isSHA256Hex(entry.ExpectedFingerprint)) {
				return ErrInvalidTransaction
			}
		} else if !isCatalogID(entry.ObjectID) || entry.Stage != transactionContentStageName(record.ID, index) || !isSHA256Hex(entry.SHA256) || entry.ExpectedFingerprint != "" || entry.RejectHardlinks {
			return ErrInvalidTransaction
		}
		if entry.PlaintextFingerprint != "" {
			if record.Version != 2 || !isSHA256Hex(entry.PlaintextFingerprint) || !validCatalogContentKeyEnvelope(entry.ObjectID, entry.ContentKeyEnvelope) {
				return ErrInvalidTransaction
			}
		} else if entry.ContentKeyEnvelope != nil {
			return ErrInvalidTransaction
		}
		if record.Version == 2 && !entry.Delete {
			if entry.PriorFingerprint != "missing" && !isSHA256Hex(entry.PriorFingerprint) {
				return ErrInvalidTransaction
			}
		} else if entry.PriorFingerprint != "" {
			return ErrInvalidTransaction
		}
		if index > 0 && record.Changes[index-1].Path >= entry.Path {
			return ErrInvalidTransaction
		}
	}
	return nil
}

func validateTransactionBindings(changes []CiphertextChange, catalog Catalog) error {
	objects := make(map[string]CatalogObject, len(catalog.Objects))
	for _, object := range catalog.Objects {
		objects[object.PhysicalPath] = object
	}
	for _, change := range changes {
		object, exists := objects[change.Path]
		if change.Delete {
			if exists {
				return ErrInvalidTransaction
			}
			continue
		}
		if change.PublishPlaintext {
			if err := validatePlaintextPublication(change.Path, catalog); err != nil {
				return err
			}
			continue
		}
		if !exists || !object.Protected || object.ObjectID != change.ObjectID || object.CiphertextHash != transactionDigest(change.Ciphertext) || !equalContentKeyEnvelope(object.ContentKeyEnvelope, &change.ContentKeyEnvelope) {
			return ErrInvalidTransaction
		}
	}
	return nil
}

func equalContentKeyEnvelope(first, second *ContentKeyEnvelope) bool {
	if first == nil || second == nil || first.Version != second.Version || first.ObjectID != second.ObjectID {
		return false
	}
	return bytes.Equal(first.Nonce, second.Nonce) && bytes.Equal(first.Ciphertext, second.Ciphertext)
}

func isTransactionContentPath(value string) bool {
	if !isCatalogPath(value) {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if strings.HasPrefix(component, ".vnotes-txn-") {
			return false
		}
	}
	return true
}

func validateTransactionRecordBindings(record transactionRecord, catalog Catalog) error {
	objects := make(map[string]CatalogObject, len(catalog.Objects))
	for _, object := range catalog.Objects {
		objects[object.PhysicalPath] = object
	}
	for _, change := range record.Changes {
		object, exists := objects[change.Path]
		if change.Delete {
			if exists {
				return ErrInvalidTransaction
			}
			continue
		}
		if change.PlaintextFingerprint != "" {
			if err := validatePlaintextPublication(change.Path, catalog); err != nil {
				return err
			}
			continue
		}
		if !exists || !object.Protected || object.ObjectID != change.ObjectID || object.CiphertextHash != change.SHA256 {
			return ErrInvalidTransaction
		}
	}
	return nil
}

func transactionContentStageName(id string, index int) string {
	return fmt.Sprintf(".vnotes-txn-%s-%06d.stage", id, index)
}

func transactionCatalogStageName(id string) string {
	return ".vnotes-txn-" + id + "-catalog.stage"
}

func transactionDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func transactionSourceFingerprint(vaultKey []byte, digest string) string {
	authenticator := hmac.New(sha256.New, vaultKey)
	_, _ = authenticator.Write([]byte("varianotes/transaction-source-fingerprint/v1\x00"))
	_, _ = authenticator.Write([]byte(digest))
	return hex.EncodeToString(authenticator.Sum(nil))
}

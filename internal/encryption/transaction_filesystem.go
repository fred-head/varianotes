package encryption

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func sortedTransactionDirectories(paths []string) []string {
	result := append([]string(nil), paths...)
	sort.Strings(result)
	return result
}

func validateTransactionDirectoryRecord(record transactionRecord) error {
	if len(record.CreateDirectories)+len(record.RemoveDirectories) > maxTransactionChanges {
		return ErrInvalidTransaction
	}
	if record.Version == 1 && len(record.CreateDirectories)+len(record.RemoveDirectories) != 0 {
		return ErrInvalidTransaction
	}
	seen := make(map[string]bool)
	for _, directories := range [][]string{record.CreateDirectories, record.RemoveDirectories} {
		for i, relative := range directories {
			if !isTransactionContentPath(relative) || seen[relative] || i > 0 && directories[i-1] >= relative {
				return ErrInvalidTransaction
			}
			seen[relative] = true
		}
	}
	return nil
}

func plannedTransactionParent(relative string, directories []string) bool {
	parent := path.Dir(relative)
	for _, directory := range directories {
		if directory == parent {
			return true
		}
	}
	return false
}

func validatePlannedTransactionTarget(root *os.Root, relative string, directories []string) error {
	if err := validateTransactionTarget(root, relative); err != nil {
		if errors.Is(err, fs.ErrNotExist) && plannedTransactionParent(relative, directories) {
			return nil
		}
		return err
	}
	return nil
}

func validatePlannedDirectories(root *os.Root, create, remove []string) error {
	if err := validateTransactionDirectoryRecord(transactionRecord{Version: 2, CreateDirectories: create, RemoveDirectories: remove}); err != nil {
		return err
	}
	for _, relative := range create {
		if _, err := root.Lstat(filepath.FromSlash(relative)); err == nil {
			return ErrTransactionConflict
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		parent := path.Dir(relative)
		if plannedTransactionParent(relative, create) {
			continue
		}
		if err := validateTransactionDirectory(root, parent); err != nil {
			return err
		}
	}
	for _, relative := range remove {
		if err := validateTransactionDirectory(root, relative); err != nil {
			return err
		}
	}
	return nil
}

func validateTransactionDirectory(root *os.Root, relative string) error {
	current := "."
	for _, part := range strings.Split(relative, "/") {
		current = path.Join(current, part)
		info, err := root.Lstat(filepath.FromSlash(current))
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidTransaction
		}
	}
	return nil
}

func (j *TransactionJournal) createTransactionDirectories(root *os.Root, record transactionRecord) error {
	for _, relative := range record.CreateDirectories {
		if err := validateTransactionDirectory(root, path.Dir(relative)); err != nil {
			return err
		}
		if err := j.fail("transaction.directory.create"); err != nil {
			return err
		}
		if err := root.Mkdir(filepath.FromSlash(relative), 0o755); err != nil {
			return err
		}
		if err := j.syncVaultDirectory(root, path.Dir(relative), "transaction.directory.sync"); err != nil {
			return err
		}
	}
	return nil
}

func (j *TransactionJournal) removeTransactionDirectories(root *os.Root, directories []string) error {
	for i := len(directories) - 1; i >= 0; i-- {
		relative := directories[i]
		if _, err := root.Lstat(filepath.FromSlash(relative)); errors.Is(err, fs.ErrNotExist) {
			if err := j.syncExistingVaultAncestor(root, path.Dir(relative)); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		if err := validateTransactionDirectory(root, relative); err != nil {
			return err
		}
		if err := j.fail("transaction.directory.remove"); err != nil {
			return err
		}
		// Remove only empty directories. Unexpected external files are retained.
		if err := root.Remove(filepath.FromSlash(relative)); err != nil {
			return errors.Join(ErrTransactionConflict, err)
		}
		if err := j.syncVaultDirectory(root, path.Dir(relative), "transaction.directory.sync"); err != nil {
			return err
		}
	}
	return nil
}

// Retry an interrupted delete even when its former parent is already gone.
// Sync the nearest surviving ancestor before clearing the recovery record.
func (j *TransactionJournal) syncExistingVaultAncestor(root *os.Root, relative string) error {
	for {
		if err := validateTransactionDirectory(root, relative); err == nil {
			return j.syncVaultDirectory(root, relative, "transaction.directory.sync")
		} else if !errors.Is(err, fs.ErrNotExist) || relative == "." {
			return err
		}
		relative = path.Dir(relative)
	}
}

func validatePlaintextPublication(relative string, catalog Catalog) error {
	protected, err := catalog.IsPathProtected(relative)
	if err != nil || protected || strings.EqualFold(path.Ext(relative), ".age") {
		return errors.Join(ErrInvalidTransaction, err)
	}
	for _, object := range catalog.Objects {
		if object.Protected && (object.PhysicalPath == relative || object.LogicalPath == relative) {
			return ErrInvalidTransaction
		}
	}
	return nil
}

func transactionChangeInstalled(entry transactionChange, digest string, key []byte) bool {
	if entry.PlaintextFingerprint != "" {
		return digest != "" && transactionSourceFingerprint(key, digest) == entry.PlaintextFingerprint
	}
	return digest == entry.SHA256
}

func transactionPriorMatches(entry transactionChange, digest string, key []byte) bool {
	if digest == "" {
		return entry.PriorFingerprint == "missing"
	}
	return transactionSourceFingerprint(key, digest) == entry.PriorFingerprint
}

func transactionPlainTemporary(entry transactionChange) string {
	return path.Join(path.Dir(entry.Path), entry.Stage+".plain")
}

func (j *TransactionJournal) publishTransactionPlaintext(root *os.Root, entry transactionChange, key []byte) error {
	stagePath := path.Join(path.Dir(entry.Path), entry.Stage)
	ciphertext, err := readTransactionFile(root, stagePath, maxTransactionFileBytes)
	if err != nil {
		return err
	}
	plaintext, err := DecryptContent(key, entry.ObjectID, ciphertext, *entry.ContentKeyEnvelope)
	clear(ciphertext)
	if err != nil {
		return errors.Join(ErrTransactionJournalCorrupt, err)
	}
	defer clear(plaintext)
	if transactionSourceFingerprint(key, transactionDigest(plaintext)) != entry.PlaintextFingerprint {
		return ErrTransactionJournalCorrupt
	}
	if err := j.fail("transaction.plaintext.publish.before"); err != nil {
		return err
	}
	temporary := transactionPlainTemporary(entry)
	// A committed restart may leave a partial plaintext temporary from an
	// explicitly approved decrypt. Its name is authenticated and reserved.
	if info, err := root.Lstat(filepath.FromSlash(temporary)); err == nil {
		if !info.Mode().IsRegular() || !hasSingleLink(info) {
			return ErrInvalidTransaction
		}
		if err := root.Remove(filepath.FromSlash(temporary)); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	file, err := root.OpenFile(filepath.FromSlash(temporary), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := file.Write(plaintext)
	if writeErr == nil && written != len(plaintext) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	err = errors.Join(writeErr, file.Close())
	if err != nil {
		return err
	}
	if err := j.fail("transaction.plaintext.publish.after_write"); err != nil {
		return err
	}
	if err := validateTransactionTarget(root, entry.Path); err != nil {
		return err
	}
	if err := root.Rename(filepath.FromSlash(temporary), filepath.FromSlash(entry.Path)); err != nil {
		return err
	}
	return j.syncVaultDirectory(root, path.Dir(entry.Path), "transaction.content.directory_sync")
}

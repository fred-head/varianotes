package files

import "github.com/fred-head/varianotes/internal/encryption"

func (r *Repository) guardOrdinaryTrash(journal *encryption.TransactionJournal, work func() (TrashItem, error)) (TrashItem, error) {
	r.trashMu.Lock()
	defer r.trashMu.Unlock()
	r.content.mu.Lock()
	defer r.content.mu.Unlock()
	var result TrashItem
	err := journal.GuardMutation(func() error { var err error; result, err = work(); return err })
	return result, err
}

func (r *Repository) moveToTrashGuarded(relative string, journal *encryption.TransactionJournal) (TrashItem, error) {
	return r.guardOrdinaryTrash(journal, func() (TrashItem, error) { return r.moveToTrashLocked(relative) })
}

func (r *Repository) restoreTrashItemGuarded(id string, journal *encryption.TransactionJournal) (TrashItem, error) {
	return r.guardOrdinaryTrash(journal, func() (TrashItem, error) { return r.restoreTrashItemLocked(id) })
}

func (r *Repository) deleteTrashItemGuarded(id string, journal *encryption.TransactionJournal) (TrashItem, error) {
	return r.guardOrdinaryTrash(journal, func() (TrashItem, error) { return r.deleteTrashItemLocked(id) })
}

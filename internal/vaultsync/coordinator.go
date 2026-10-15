// Package vaultsync owns process-lifetime synchronization for canonical vault
// directories. A second Repository, export request or journal must join the
// same coordinator, rather than create another independent writer/recovery.
package vaultsync

import "sync"

type Coordinator struct {
	Content sync.RWMutex
	Trash   sync.Mutex
	Move    sync.Mutex
	Journal sync.Mutex
	// ActiveTransaction is protected by Journal. Ownership lasts from a
	// successful Prepare through Commit/Abort, including the gap between calls.
	// No keys, file contents or catalog snapshots are retained here.
	ActiveTransaction string
}

var coordinators sync.Map

// ForRoot requires a cleaned, absolute, symlink-resolved directory. Entries
// live for the process lifetime so switching/reopening a vault cannot split
// synchronization. The supported deployment has one backend writer per vault.
func ForRoot(canonicalRoot string) *Coordinator {
	value, _ := coordinators.LoadOrStore(canonicalRoot, &Coordinator{})
	return value.(*Coordinator)
}

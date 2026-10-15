# M31 C1: ownership of live auth journals

This follow-up addresses C1 only. It does not grant P17 sign-off, change its
criteria, authorize encryption activation, or replace a fresh overall review.
PR #24 remains open pending that review.

## Reproduction on main

The test-only commit `03c91c0` is based on main
`6815833df11521e35263d83089d5e82d7b2633c0`. Its
`TestPasswordChangeJournalCannotBeRecoveredByConcurrentMFAResetCLI` failed
against the existing implementation before the fix:

1. A real password change pauses after the first durable envelope write in a
   two-vault fixture, with its journal still `prepared`.
2. A separately built CLI runs `auth reset-mfa` using the same auth database and
   notebook registry. Its actual `auth.Open` initialization rolls back and
   deletes the live journal, then reports success while the writer is paused.
3. The writer resumes and fails because its journal disappeared. The old login
   remains valid while an envelope has changed to the replacement password.

The test checks that the CLI waits while the owner is paused, then checks both
command results, journal completion, rejection of the old login, and use of the
new password for both vault envelopes. The original failure included the CLI
returning during the live transaction, password-change recovery errors, a
rejected new login, an accepted old login, and an envelope decryption failure.

## Coordination

A separate empty SQLite database at the resolved auth database path plus
`.journal-lock` provides process and instance ownership. `BEGIN IMMEDIATE`
is held from before initialization/recovery or transition preflight through
journal completion or error cleanup. Existing local credential mutexes alone
could not protect a second service or CLI process. Locking the auth database
itself for this whole interval would prevent the owner's own journal writes.

Both password and recovery-code envelope transactions participate. A canceled
waiter fails without replaying or clearing a journal. An owner's canceled
request does not release the lock underneath its callback. No persisted owner
flag or PID cleanup is needed: connection close and actual process death
release the SQLite lock. Failed rollback/replay retains the journal; another
credential transition must not replace its pending recovery state.

The file is mode `0600` in the private auth metadata directory and is never
unlinked on release. Unusable lock files fail closed. Existing database and
parent symlink aliases resolve to the same lock. As with SQLite WAL storage,
hard-link database aliases and storage without reliable SQLite file locking
are unsupported. All participating processes must run the fixed version;
stop older binaries before upgrade. The existing single backend writer per
vault constraint remains in force.

## Regression coverage

- Real concurrent CLI initialization, controlled at a durable envelope boundary,
  followed by consistent login and envelope use.
- Actual child-process exit without defers at a partial prepared change and
  after the credential/journal commit; a fresh CLI recovers backward or forward.
- Unavailable vault during post-crash CLI initialization: envelope and complete
  journal remain unchanged, followed by successful retry when storage returns.
- Request cancellation after an envelope write: the journal survives until
  startup recovery restores the old credential/envelope combination.
- Lock-wait cancellation, canceled-owner lifetime, path alias contention,
  unusable coordination files, failed rollback/retry, blocked credential
  entrypoints, and refusal to reset over an unfinished journal.

All fixtures use disposable storage. They neither enable product encryption nor
operate on an operator's auth database or notebooks.

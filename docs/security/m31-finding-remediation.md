# M31 security finding remediation

This records the follow-up to the full P01–P17 AI review. Passing regression
tests are implementation evidence, not independent security approval. P17 and
PR #24 remain open; encryption activation remains disabled; P18 is unchanged.

## F1 — New plaintext attachment reference on protected save

Confirmed: the original save accepted a reference to an ordinary shared image
without converting its bytes. The regression failed against the original code.
Protected saves now reject existing unregistered/plaintext attachments before
staging. Already protected attachments are authenticated before accepting the
reference. The current note and catalog remain unchanged on rejection; the
browser can retain the draft. Ordinary attachments must be converted with their
source note first, or uploaded through a protected note. Automatic attachment
conversion during a protected save is not implemented.

Regression: `TestProtectedSaveRejectsPlaintextSharedAttachment` checks rejection
and byte-identical current ciphertext; the shared-upload regression continues
to cover one encrypted object serving ordinary and protected references.

## F2 — Skipped unsupported local attachments

Confirmed for existing PDF references, local images outside `.assets`, and
percent-encoded paths. All three regressions failed against the original code.
Protection inventory now resolves local destinations independently of the
upload convention, then rejects unsupported existing attachments before
staging. It still protects supported shared images. Remote links, anchors, and
Markdown links do not select local attachments. Unsupported attachment formats
are blocked, not silently left unprotected.

Regression: `TestConversionRejectsUnsupportedLocalReferences` verifies original
notes remain present and no encrypted note is staged on unsafe preflight.

## F7 — Plaintext descendants in protected export

Confirmed for ordinary notes, images, and other files written externally under
a recursive protected folder. The original ZIP writer emitted each fixture.
Archive preflight now applies folder policy to every ordinary regular file and
rejects unregistered protected descendants before the first ZIP byte.

Regression: `TestExportRejectsUntrackedProtectedDescendantsBeforeFirstByte`.

## Validation

- Serial uncached `internal/files` suite passed after the fixes.
- Serial uncached `go test -p 1 -count=1 ./...` and `go vet -p 1 ./internal/files` passed. CI is recorded in the follow-up PR.
- Persistent Go caches and disk-backed temporary directories; two Go workers,
  one package at a time, and a 1536 MiB Go memory target. No load stress tests.

## F10 — Registered vault identity

Confirmed: swapping a complete foreign ciphertext/catalog/password-envelope
package between two registrations with the same owner password was accepted
after restart. The regression failed against the original implementation.
Application catalog checks now use a domain-separated identity derived from
the independently configured notebook registration, including password-change
and recovery verification. Journal recovery checks that identity before any
cleanup or publication.

Regressions: `TestRegisteredVaultRejectsCompleteForeignPackageAfterRestart`
checks content access, password change and recovery setup;
`TestRegisteredTransactionRecoveryRejectsForeignIdentityBeforeMutation` checks
prepared and committed journals, preserved bytes, and valid recovery.

Compatibility: activation has never shipped. Old unbound M31 fixtures are
rejected without rewriting keys, catalogs or ciphertext. They remain
decryptable externally with the original envelope. There is deliberately no
automatic enrollment of an existing identity; a future trusted import would
need explicit operator verification. Replacing the registration itself or
rolling back a complete snapshot of the same registration is outside this
cross-vault binding; it requires independently trusted anti-rollback state.

## Still open

F9 awaits code verification and remediation. A fresh full M31
review is required after all confirmed findings are fixed. No merge or security
approval for P17 is claimed here.

## F3 — Durable move, backlinks and decrypt restore

Confirmed by control-flow inspection: live renames, ordinary backlink writes,
and decrypted restore files previously preceded `journal.Prepare`. In-process
rollback could not cover a process crash. Move now stages destination content
and source deletions together; ordinary backlinks and explicit decrypt targets
are staged as ciphertext. Restore uses the same plaintext-publication primitive.
No content mutation or plaintext publication precedes durable commit. Directory
creation/removal participates in prepared rollback and committed forward
recovery. Untracked protected descendants and unsafe/hardlinked entries block
the move. Unexpected recovery target files are retained and block publication.

Version 2 journal records authenticate plaintext publication envelopes, keyed
plaintext/previous-target fingerprints and directory plans. Version 1 journals
remain readable. Plaintext temporaries are created only after a durable,
explicitly authorized decrypt/ordinary-file commit and are covered by recovery
cleanup; before commit every stage is ciphertext. Recover committed operations
before serving affected content; do not roll back a durable forward decision.

Regressions:
- `TestMovePreparedRestartPreservesSourceAndOrdinaryBacklink` (keep/decrypt)
- `TestDecryptRestorePreparedRestartKeepsOnlyTrashCiphertext`
- `TestPlaintextPublicationAndDirectoriesRecoverAtEveryBoundary` (eleven fault
  phases; idempotent restart, source/catalog retention before commit, exact
  plaintext only after commit, no residual stages, including cleanup failures
  after source directories are already removed)
- `TestPlaintextPublicationRejectsRecursiveProtectionBeforeStaging`
- `TestCommittedPlaintextPublicationRetainsExternalCollision`

Validation: serial uncached `go test -p 1 -count=1 ./...` and focused filesystem
and journal cases passed; `go vet -p 1 ./internal/encryption ./internal/files`
passed. F5/F6 still require lease guarding and lock-order corrections at the
application boundary. Fresh independent review of the complete format and
integration remains required.

## F4 — Content save during envelope transition

Confirmed by failing regressions: a second unlocked session saving after the
first envelope update made both prepared rollback and committed forward
password-journal replay fail permanently on startup. Password recovery had the
same failure after its last plan verification.

Apply/rollback now depend on the exact authenticated plan's old/new envelopes
and independent registration identity, not a mutable whole-catalog checksum.
Password change and recovery keep the same data key, so legitimate saves are
compatible with both envelopes. Key/catalog authentication still occurs during
plan preparation; stale recovery confirmation is still rejected before
journaling; unknown envelope replacements and foreign registrations fail
closed. Recovery never rewrites content or the catalog. No data-key mutation
was introduced and no new content quiescing is necessary for rewrapping that
unchanged key.

Regressions: `TestPasswordJournalRestartAllowsConcurrentCatalogSave` and
`TestRecoveryCodeJournalRestartAllowsConcurrentCatalogSave` cover prepared and
committed restarts across two vaults, an unlocked second writer after first
envelope update, and byte-identical saved ciphertext after recovery.
`TestInterruptedPasswordRecoveryRestoresOldCodeBeforeServing` now saves after
last verification/first update and checks old password and code restoration.
Serial uncached app/auth/command suites and app vet passed; CI remains the gate.

## F5/F6 — Mutation generation and lock order

Confirmed by code trace: Trash mutated inside `UseKey` before checking the lease
again; Move held the manager mutex while acquiring repository locks, whereas
Save acquired them in reverse order. Tree publication had the same inverse
acquisition and was corrected too.

Move and every Trash path now validate the reusable lease at each durable
commit. Failed guards abort prepared stages. Ordinary Trash operations in an
encrypted vault use the same guard after acquiring filesystem locks. Responses
are separately checked. All repository locks precede the publication barrier;
callbacks never hold the manager state mutex. Revocation/manual/idle lock waits
for an already-entered publication boundary without holding that mutex. Expired
grants cannot start another commit. Preparation can finish after a successful
lock, but its durable decision fails and cannot publish plaintext or delete data.
Already committed transactions remain forward-only for restart recovery.

Regressions:
- `TestHTTPTrashMutationRejectsLockAfterFinalPreparation`: real CSRF/session
  routes for Trash, decrypt restore and permanent deletion; pause after final
  prepare, complete HTTP manual lock, resume, require 423 and original bytes.
- `TestPreparedTrashMutationCannotCommitAfterSuccessfulLock`: no plaintext or
  stages after a rejected prepared mutation.
- `TestOrdinaryTrashPathsInEncryptedVaultRejectRevokedLease`
- `TestSaveMoveAndManualLockHaveNoLockOrderCycle`
- `TestPublicationDoesNotHoldStateMutexAndLockWaitsForBoundary`
- `TestIdleExpiryRejectsPreparedGuardAndCompletesWithoutStateMutexCycle`

Validation: serial uncached full Go suite, focused route cases, affected-package
vet and focused race checks. Publication callbacks must stay leaf operations.
Lock completion waits for an active durable commit or response write; existing
server write timeouts still bound network writes. Physical key erasure and
already delivered plaintext revocation are not claimed. Fresh independent
review must verify this ordering across the complete M31 scope.

## F8 — Derived dialogs and late content responses after lock

Confirmed: the original UI left conflict plaintext visible after a lock and
installed a delayed conflict or note response after lock/unlock. These three
regressions failed against the original code; backlink previews had the same
unguarded control flow. Lock events, manual lock, failed protection status,
session changes and notebook switches now discard derived dialogs and advance
an in-memory view epoch. Content responses require the same registration,
lock generation and epoch. Conflict, note, save and move flows check that fence
after asynchronous boundaries; conflict and backlink dialogs also require an
unlocked view when rendered. A late save acknowledgement cannot mark the hidden
draft saved. The unsaved editor draft remains only in tab memory and is shown
again after unlock; discarded comparisons can be fetched again on retry.

Regression file: `frontend/src/App.lock-security.test.tsx` covers eight cases:
visible/late conflicts, visible/late backlink previews, delayed note loading,
late conflict resolution, late save acknowledgement and switching away/back to
the same registration. The existing real-editor tests still cover manual lock,
status failure, lock events, draft retention and browser-storage exclusion.
Validation: serial full frontend suite and focused additional regressions;
frontend lint/build. Browser memory erasure or revocation of previously viewed
plaintext is not claimed; the requirement is to hide protected content and
reject stale responses while preserving the unsaved draft.

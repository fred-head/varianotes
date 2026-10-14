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

F3–F6, F8, and F9 await code verification and remediation. A fresh full M31
review is required after all confirmed findings are fixed. No merge or security
approval for P17 is claimed here.

# M31 B1/B2 vault coordination candidate

> **Historical, candidate-bound remediation record.** The B1/B2 findings and
> correction evidence below describe the candidate based on `dc588fe`; the
> original review provenance remains in the linked report. This document does
> not assess later commits. P17 has since completed following the documented
> fresh AI review, and PR #24 is merged. P18 remains open, so encryption
> activation stays disabled.

Scope: findings B1 and B2 from the immutable
[follow-up AI source review](m31-r-followup-ai-review.md). The correction starts
from `dc588fe`; the report's original provenance remains unchanged.
At the time of this candidate, B3 (online CLI revocation) and B4 (first-upload
parent fsync) remained open. The B1/B2 correction later merged in PR #38; the
B3 and B4 corrections later merged in PRs #39 and #40. Encryption activation
stayed disabled and P18 was still open. This implementation evidence does not
by itself satisfy the Alpha 3 P17 sign-off rule; P17 has since completed after
the documented fresh AI review.

## Reproduction before the correction

Regression commit `43b863d` adds only the failing tests and fault seams.
Both findings were reproduced before production behavior was changed.

- `TestRecoveryCannotDiscardActiveSave` pauses an independent journal after
  reading `prepared`, commits the writer until the new ciphertext is installed,
  then lets stale recovery clean up. The Save fails at catalog rename because
  the stage is gone; the installed ciphertext does not match the old catalog
  or its envelope, and reopening/recovering cannot repair it.
- `TestHTTPExportRecoveryDuringPreparedSave` runs Export GET, password reauth
  and authorized Export POST while a real repository Save remains prepared.
  All three routes discard its journal/stages before the original fix, causing
  the subsequent Save commit to fail. Abort cases also verify recovery deferral.
- `TestTrashReconciliationWithStaleCatalogPreservesRestoreMapping` gives B
  the catalog from before A, then interrupts A after source deletion before
  catalog rename, or before metadata rename after commit. In both cases stale
  reconciliation deletes pending `metadata.json`; journal recovery retains
  ciphertext, but listing/Restore fails because `OriginalPath` is lost.

Initial focused runs of these regressions failed with those exact outcomes.

## Correction

One process-lifetime coordinator is retained per canonical, symlink-resolved
vault directory. All repositories and journals, including those constructed
by exports and vault switches, share its content, Trash, Move and journal
locks. Successful Prepare retains a live transaction identity across the gap
before Commit/Abort. Recovery authenticates the journal but rejects live work
with `ErrTransactionPending`; HTTP exports return 503 and can be retried.
Commit/Abort release ownership even when storage fails, leaving any orphaned
journal available for recovery. Aborted Markdown/assets and interrupted
Move/Restore preparation now actually abort their prepared transaction.

Recovery, registration authentication, authoritative catalog loading and
Trash reconciliation execute under the same content/journal locks. The old
snapshot contributes the expected vault identity, never the commit decision.
No access-instance recovery cache can hide another instance's partial commit.
Repeated recovery/finalization failures leave pending mappings untouched.
Unexpected payload without a matching catalog entry is retained together with
its mapping and requires recovery rather than speculative cleanup.

The lock order is Trash/Move (where needed), Content, Journal, catalog store.
Recovery's early Journal ownership check releases that lock before acquiring
Content, then rechecks ownership. Existing lease commit guards remain leaf
publication/authorization barriers.

## Validation and limits

The focused tests also cover HTTP Move/Restore concurrent with Export GET,
manual-lock abort and successful retry. Actual subprocess exits cover prepared
Save abandonment, installed ciphertext before its sync/catalog rename, and
Trash directory/metadata preparation, source deletion before catalog rename,
content commit, and both sides of metadata rename. Restart listing deliberately
uses the pre-operation catalog, then restores original paths, exact ciphertext
and note content. Failed Abort cleanup is recovered by another journal instance.

All Go work uses the persistent caches and disk-backed temporary directory
from AGENTS.md. The full serial `go test -count=1 -p=1 ./...` and `go vet ./...`
passed, as did the full encryption/files race suites and the focused app race
run matching `TestHTTPExportRecoveryDuringPrepared` and
`TestHTTPTrashMutationRejectsLockAfterFinalPreparation`. Initial concurrent full
test/race attempts with `GOMAXPROCS=2` and `GOMEMLIMIT=1536MiB` timed out in
password KDF work; their dumps did not show blocked vault locks. The normal
full suite was then rerun successfully in serial with default runtime limits.
At the time of this candidate, the complete race gate remained CI work.

These tests exercise process death and injected I/O failures;
they do not simulate physical power loss. At the time of this candidate, a
full-scope review of the updated M31 candidate remained required under the
[Alpha 3 P17 rule](m31-p17-ai-review-signoff.md); that review could serve as the
selected sign-off if it reported no release blockers and all required technical
checks passed on the reviewed commit. P17 has since completed following the
documented fresh AI review. This record does not assess later commits; P18
remains open and activation remains disabled.

# M31 R1–R5 remediation candidates

Source findings: [fresh AI report](m31-fresh-ai-review.md). These follow-up
changes are candidates, not a release or merge approval. Before any R-fix PR
merges, a fresh AI session must review the complete pre-P01-to-P17 candidate
and assess release blockers. The AI review does not replace P17's independent
reviewer sign-off. PR #24 remains open, activation disabled, P18 unchanged.

Tests run serially with disk-backed caches/temporary data, `GOMAXPROCS=2` and
`GOMEMLIMIT=1536MiB`; focused race checks use the same limits. Abrupt subprocess
exit tests exercise real process death/restart. Fault injection models failed
durability barriers; no physical power-cut or block-device test is claimed.

## R1 — transaction commit recovery

**Confirmed.** Before the fix, new tests failed: a visible committed marker
whose parent sync failed was replayed without re-establishing the barrier;
recovery removed `Note.md`. Already installed content/catalog also skipped
their publication syncs. An abruptly exited child at commit-directory sync
was followed by a new process that never encountered that required barrier.

**Correction.** Recovery rewrites and syncs the authenticated commit decision
before any live mutation. Retry resyncs installed destinations and catalogs.
All publications precede source deletion; replay retains the authenticated
path order on disk. Already absent directory deletions sync the nearest
surviving ancestor before journal removal. Barrier errors preserve recovery
state and stop forward work.

**Regression evidence.** `TestRecoveryProcessBoundary` uses three abruptly
terminated subprocesses and final recovery, retaining the source until the
destination barrier and checking exact ciphertext/catalog afterwards.
`TestRecoveryRequiresDurableCommitAndInstalledContent`,
`TestRecoveryRequiresDurableInstalledCatalog`, and
`TestRecoveryResyncsAlreadyRemovedDirectory` cover repeated fsync failures
and journal retention. Full encryption/files package tests and focused
encryption race tests passed. CI and fresh full-scope AI review remain gates.

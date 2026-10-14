# M31 R1–R5 remediation candidates

Source findings: [fresh AI report](m31-fresh-ai-review.md). These follow-up
changes are candidates, not a release or merge approval. Before any R-fix PR
merges, a fresh AI session must review the complete pre-P01-to-P17 candidate
and assess release blockers. The AI review does not replace P17's independent
reviewer sign-off. PR #24 remains open, activation disabled, P18 unchanged.

**Fresh follow-up outcome: blocked.** The completed
[full-scope AI source review](m31-r-followup-ai-review.md) reports three high
blockers (B1–B3) and a medium durability finding (B4). R2 remains open at stale
catalog reconciliation and R4 at documented online CLI revocation. The serial
R1/R2 barriers, R3 replay and R5 display fences are correction candidates,
not overall recovery/safety guarantees. No correction PR has merged.

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

## R2 — protected Trash durability

**Confirmed.** `TestProtectedTrashDurabilityBeforeSourceDelete` failed against
the previous path: the restore mapping's directory and its parent had not
been synced at the commit boundary. Destination directories were absent from
the authenticated filesystem plan. The shared cause with R1 is treating
visible namespace entries as durable.

**Correction.** The complete destination/empty-directory chain uses
`PrepareFilesystem`. Pending restore metadata and its parent are explicitly
synced before the journal's forward decision; the Trash-root barrier is
retried and errors propagate. Metadata finalization syncs destination, source,
and Trash parents in that order; retry repeats the barriers even when the
rename is already visible. An unexpected pending payload is retained.

**Regression evidence.** `TestProtectedTrashProcessRestart` abruptly exits
children after pending mkdir, metadata file write, prepared directories,
content commit and metadata rename. Restart recovers/discards the journal,
preserves exact note content/protection and restores committed items.
`TestProtectedTrashBarrierFailuresPreserveRestoreMapping` injects repeated
pending/final-parent sync failures. The commit-boundary regression checks
durable metadata and the full directory plan. Full files tests and focused
Trash/lock/concurrency race tests passed. These exercise process interruption
and failed barriers, not actual storage power loss. CI/full-scope AI review
remain outstanding.

**Remaining R2 boundary:** B2 in the follow-up AI review identifies pending
restore-mapping deletion based on a stale/partially committed catalog. The
durability tests above do not cover that competing GET/commit interleaving.

## R3 — matching envelope replay

**Confirmed.** New rollback/forward regressions failed because matching
visible envelopes were skipped. A prepared SQLite password-change journal
survived two child processes that each synced a temporary old envelope,
renamed it and abruptly exited before the parent-directory barrier. Auth
restart removed the journal while leaving that visible inode untouched.

**Correction.** Both password-change replay and existing recovery-envelope
replay persist the exact target bytes via the existing atomic, file/parent-
synced envelope writer even on equality. Errors propagate before auth clears
its journal. Independent registered Vault-ID and exact old/new-envelope
checks remain; replay still permits legitimate concurrent content saves.

**Regression evidence.** `TestEnvelopeReplayRewritesMatchingTarget` covers
matching targets in both directions for both replay paths.
`TestEnvelopeRollbackProcessRestart` constructs the precise filesystem
interruption boundary in two real child processes, then runs real auth
startup recovery, checks a durable rewrite before journal completion, verifies
the original login/vault passwords, and verifies unchanged content bytes.
The child performs the writer's rename sequence explicitly; it is not a full
backend process or a physical power-cut test. Focused password/recovery app
tests and race checks (including concurrent catalog-save replay) passed.
The broader uncached auth/route gate and CI/full-scope AI review remain gates.

## R4 — pending Unlock against lock/revocation

**Confirmed.** Deterministically paused HTTP unlocks restored grants after
manual lock (including an already locked vault), logout, current/all-session
revocation, revocation before first grant, and another session's revocation.
After logout the auth middleware could reject response/session persistence,
but the manager still retained the stale grant.

**Correction.** A one-use, manager/vault/session-bound attempt captures the
vault generation and a bounded revocation epoch before credential work.
Grant installation checks it atomically under the existing commit gate.
All in-process manager revocations invalidate pending work, including sessions
with no grant;
no tombstone/request map accumulates. Auth liveness is read again from SQLite
after slow work. Logout/current/all-session deletion finish with a second
key-manager barrier after durable auth revocation, covering the interval
between their initial lock and auth commit. No manager-state mutex spans
filesystem/auth callbacks; existing mutation-lock ordering is retained.

**Regression evidence.** `TestPendingHTTPUnlockCannotOvertakeLockOrRevocation`
covers seven transitions at a paused HTTP grant boundary.
`TestUnlockAttemptFencesEveryRevocationBeforeFirstGrant` and
`TestUnlockAttemptIsBoundToManagerAndUsedOnce` cover all manager revocations,
foreign/reused attempts and bounded retained state.
`TestActiveSessionRevalidatesAdmissionWithoutRefreshingExpiry` covers live,
revoked, expired and malformed durable session timestamps.
Focused encryption/files/app race tests (including parallel write/manual/idle
lock regressions) passed, as did the complete uncached auth/app/CLI gate.
Revocation conservatively cancels unrelated pending unlocks too; existing
active grants of unaffected sessions remain intact. CI/full-scope AI review
remain required before merge.

**Remaining R4 boundary:** B3 in the follow-up AI review identifies online CLI
revocation through a second Auth service/database connection. It does not
advance the running server's manager epoch, and a session read before grant
installation is not an atomic cross-process publication barrier. The seven
HTTP regressions above do not cover that path. R4 is not marked complete.

## R5 — failed lock-event channel and browser display lifetime

**Confirmed.** Before the fix, five new browser regressions kept protected
plaintext visible after SSE failure/offline or after fifteen minutes with a
silent channel and stalled status requests (editor, conflict and search).
The existing editor read-only timer did not hide the rendered content.

**Correction.** SSE errors/malformed events, offline and health failures hide
content immediately and invalidate pending content/unlock responses. Channel
reopening and unlock events require a fresh session/generation status before
display. Status, unlock and confirmed activity return a consistent remaining
server idle duration without status polling renewing activity. A monotonic
browser timer enforces that duration, bounded to fifteen minutes and reduced
by request latency; status polling cannot extend the confirmed deadline.
Only confirmed activity or a new grant can renew it. Abort/epoch checks reject
stale status and activity replies. Unknown status with protected tree entries
also hides content. Unsaved drafts remain hidden in tab memory; derived
search/conflict/preview data is discarded, with no browser-storage copy.

**Regression evidence.** `App.lock-security.test.tsx` covers channel failure,
offline, silent-channel deadlines for all three surfaces, successful polling
without deadline extension, response latency, reconnect confirmation, late
unlock/search replies and hidden draft retention. All 125 frontend tests,
lint and production build passed (existing large-chunk warning remains).
`TestSessionIdleTimeIsBoundToGrantAndPollingDoesNotRenew` and the session-scoped
HTTP test cover deadline/grant binding, activity and no-store responses.
Focused encryption/app race checks including pending unlock passed. These
are component tests with a simulated event channel/timer, not a real proxy
outage or suspended-browser process test. CI/full-scope AI review remain gates.

## Combined validation and candidate provenance

- Complete candidate `f29ecf3d4d5a3788565a5216aa8ff8e2eda44f3b` assembles
  P01–P17, F1–F10 and R1–R5 without merging PR #24. Its baseline is
  `060e6367a7905f6306bac7273f19e1681df41f39` (before P01).
- The complete uncached `go test -p 1 -count=1 ./...` and `go vet -p 1 ./...`
  passed on that candidate, including P17 and official age external recovery.
- Candidate `7d0e68f7a8caed682ee2dd8861a0050a9798d5d7` adds only three lines
  that wait for the first note's tab before opening the second tab in an
  existing frontend test. All 125 frontend tests, lint and build passed with
  this addition; production sources are identical to `f29ecf3`.
- CI exposed F9's five-second completion budget under real scrypt/race
  instrumentation. Its open branch now allows sixty seconds and joins the
  released request before database cleanup. Real credential work and all
  security assertions remain enabled; the focused normal/race test passed.
  The correction was merged into each open R branch without rewriting history.
- Fresh AI source-review session `01a13a59-9c7c-7073-9792-1f97f8818894`
  inspected the complete baseline-to-candidate difference in the unchanged
  isolated `f29ecf3` checkout and the three-line test addition. It executed no
  tests; implementer validation is not counted as the reviewer's own evidence.
  It is an AI review, not P17's independent expert sign-off.

## CI at the correction commits (2026-10-14)

All four CI workflow jobs passed: backend tests/security/race/vulnerability,
frontend lint/tests/build/audit, complete-history secret scan, and hardened
container build/scan/persistence. These results do not close the AI blockers
or stand in for protected-main CodeQL/up-to-date/review requirements.

| PR | Correction head | Passing CI run |
| --- | --- | --- |
| #30 F8 | `7dded6c` | `38552629909` (CodeQL also passed) |
| #31 F9 | `10c010f` | `38561259995` |
| #32 R1 | `66a5bd6` | `38561291819` |
| #33 R2 | `6f77489` | `38561315066` |
| #34 R3 | `39e874e` | `38561317712` |
| #35 R4 | `be85c80` | `38561321991` |
| #36 R5 | `919fee5` | `38562055107` |

The later documentation-only follow-up has its own pending CI; no inherited
green result is asserted for its new commit. All PRs are held open. Locally,
temporary data and caches stayed on disk; sampled swap during the resumed
validation stayed around 4.0 GiB, with approximately 10–11 GiB RAM available.

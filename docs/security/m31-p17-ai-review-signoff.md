# M31 P17 AI review sign-off for Alpha 3

## Decision and limits

No independent security expert is available for the Alpha 3 M31 review. For
this milestone, the project selects a fresh, separate, read-only AI session as
the P17 sign-off, subject to the requirements below. This decision does not
make an AI review an expert audit or a general security certification.

The review session must not modify the candidate checkout or its Git state.
It may inspect source, tests, documentation, and prior reports, and must state
the exact commit it reviewed. A report about an older commit does not sign off
a changed candidate.

## Required review scope and report

The session must review the complete M31 candidate through P17, including all
P01–P17 implementation and integration changes in the candidate, against the
pre-P01 baseline. It must include P17's end-to-end fault matrix, recovery and
external-decryption paths, and the relevant existing security boundaries. The
report must:

- identify the exact reviewed commit and comparison baseline;
- verify the disposition of every previously reported finding against the
  current code and tests, citing concrete evidence;
- identify each concrete remaining release blocker, or explicitly report that
  no release blockers remain; and
- record meaningful scope limits and any remaining non-blocking findings,
  explaining why they do not weaken the agreed security boundary.

The report is a bounded source and evidence review. It must not claim expert
audit status or certify the system as generally secure.

## P17 completion gate

P17 may be marked complete only when both conditions hold on the same exact
candidate commit:

1. A fresh, separate review satisfying this rule reports no open release
   blockers.
2. All P01–P17 required technical checks, including the acceptance tests and
   applicable repository CI/release gates, have passed on that commit.

If the candidate changes after review, repeat the full-scope review and the
required technical checks on the changed commit before P17 can be completed.
P18 and production activation remain blocked until P17 passes this gate.
Passing P17 does not bypass P18: production activation remains disabled until
the separate P18 release gate passes.

# Security Maintenance and Vulnerability Response

This document defines how VariaNotes evaluates dependency updates, scanner
findings, published artifacts, and privately reported vulnerabilities. It is a
maintenance procedure, not a claim that automation can find every defect.

## Continuous signals

Before the first VariaNotes release, complete the independent GitHub repository
and GHCR setup in [new repository setup](docs/development/new-repository-setup.md).

VariaNotes uses independent signals so a source commit is not required to detect
a newly disclosed vulnerability:

- Dependabot alerts and reviewed update pull requests for Go, npm, Docker, and
  GitHub Actions;
- CodeQL Extended analysis for Go, JavaScript/TypeScript, and workflow files;
- `govulncheck`, `gosec`, `npm audit`, static analysis, and complete-history
  secret scans;
- daily Trivy builds of the current source against fresh base images;
- daily rescans of both the newest immutable Alpha image and the moving Alpha
  channel tag;
- tag-gated construction of one immutable source-SHA candidate, exact-digest
  multi-architecture validation, SBOM generation, and signed provenance
  attestation before version/channel promotion.

Scheduled Trivy JSON reports are retained as workflow artifacts for 30 days.
Missing reports, scanner failures, and advisory-database failures are failures,
not clean results. GitHub workflow notifications are the initial maintainer
signal; the run summary identifies the reference or digest, component,
advisory, severity, installed version, and fixed version where known.

The new repository variable `VARIANOTES_LATEST_ALPHA_VERSION` identifies the
newest immutable image tag for scheduled rescanning. Configure it after the
first immutable Alpha release completes successfully. Until then, the scheduled
workflow skips the immutable-image rescan while continuing source and fresh
image checks.

## Dependency pull-request policy

Automated dependency updates are untrusted code changes. They never merge or
publish automatically.

For every dependency pull request:

1. Identify whether the dependency affects development only, production,
   authentication/session/MFA, cryptography, SQLite, vault filesystem access,
   the editor, PWA/service-worker behavior, proxy trust, or the container
   runtime.
2. Read upstream release notes and advisories. Check the maintainer and release
   status, license, changed transitive dependencies, and known breaking changes.
3. Keep major upgrades and security-sensitive updates separate from unrelated
   changes. Do not group authentication or cryptographic libraries.
4. Review the actual lockfile/module changes. `go.sum` and `package-lock.json`
   must remain reproducible, and CI must not create uncommitted dependency
   resolution.
5. Require the complete protected-branch checks: backend tests, race detection,
   `go vet`, `gosec`, `govulncheck`, frontend lint/tests/build, `npm audit`, secret scan,
   CodeQL, container build, Trivy, and runtime/persistence smoke tests.
6. For changes affecting authentication, session handling, MFA, cryptography,
   persistence, proxy trust, or PWA sessions, also run the dedicated
   Milestone 19 unit, integration, adversarial, expiry, recovery, and PWA tests.
7. Perform focused manual behavior testing when an update can change editor
   serialization, PWA caching, persistence, authentication, or browser
   compatibility.
8. Merge only after a human maintainer accepts the risk and the branch is
   current. A green CI result is necessary but not sufficient.

Patch and same-release-line updates are usually lower risk, but Semantic
Versioning does not guarantee unchanged defaults, timing, serialization, or
security behavior. Auto-merge remains disabled. Introducing it later requires
a separately reviewed policy naming an extremely narrow development-only class.

## Finding triage

Every Dependabot, CodeQL, Trivy, `gosec`, `govulncheck`, npm, secret-scanning, or private
report receives the following assessment:

1. Record the affected source revision, release tags/digests, component,
   advisory or rule, severity, fixed version, and detection time.
2. Confirm the finding with a current advisory database or authoritative
   upstream advisory when possible. Do not dismiss it merely because a second
   scanner does not report it.
3. Evaluate reachability, attacker prerequisites, exposed data, authentication
   boundary, and whether VariaNotes's actual build includes the affected code.
4. Classify the response as affected, not affected, mitigated, false positive,
   or under investigation. Any dismissal must contain a specific technical
   rationale in the scanner's audit trail.
5. Identify supported releases and whether credentials, notebook data, hosts,
   or deployment boundaries may be compromised.
6. Choose remediation, temporary mitigation, upgrade/migration needs, and a
   rollback plan before publishing a replacement image.

Critical authentication bypass, remote-code-execution, credential disclosure,
path traversal, destructive data loss, and container escape findings receive
immediate priority. High and Critical findings fail source/image gates unless a
time-limited exception is reviewed and documented. A scanner outage never
qualifies for an exception that marks a release clean.

### Static-analysis classification requirements

A CodeQL dismissal is permitted only after the complete reported source-to-sink
flow and all intervening checks have been reviewed. The audit comment must name
the concrete control that makes the flow safe. For VariaNotes filesystem flows,
that means confirming normalization, absolute/parent rejection, reserved-name
rejection where applicable, resolved-parent or resolved-target symlink checks,
root containment, expected file type, and a destructive-operation
revalidation. A shared validator's tests must cover the rejected attacker
inputs. Never dismiss a finding merely because it is noisy or because another
scanner is green.

## Time-limited exceptions

An exception is a last resort when a finding is demonstrably unreachable or
mitigated and no fixed dependency exists. It must be committed as reviewable
security documentation and include:

- advisory/rule and affected component;
- affected versions and image digests;
- reachability evidence and compensating controls;
- owner and approval date;
- expiry date no more than 30 days away;
- upstream tracking link and removal condition;
- explicit confirmation that the exception does not cover authentication
  bypass, RCE, credential disclosure, traversal, destructive loss, or container
  escape without an extraordinary documented response.

Do not add broad ignore files, severity downgrades, or scanner exclusions merely
to make CI green.

## Remediation and release

Security fixes use a focused branch and pull request. Tests should reproduce the
unsafe behavior without publishing real credentials, private note contents, or
an immediately exploitable public proof before users can update.

A fix is released as a new immutable version and image digest. Existing Git
tags and immutable container tags are never moved. The release record identifies
affected and fixed versions, digest, SBOM/provenance evidence, limitations,
migration steps, mitigations, and rollback instructions. The moving Alpha tag is
updated only after the immutable multi-architecture image passes all gates.

The release workflow must not rebuild an image after validation and call the
new bytes equivalent. It first publishes a source-SHA candidate, resolves its
manifest digest, pulls each supported architecture from that digest for Trivy
and runtime/persistence smoke tests, and only then attaches the immutable
version and moving Alpha aliases to the same manifest. A failed candidate is
never promoted. A rerun may reuse the same source-SHA candidate only after
resolving and revalidating its unchanged digest.

Base-image tags and Alpine package resolution intentionally remain fresh inputs
so supported security fixes can be incorporated by scheduled and release
builds. Rebuilding an old source commit is therefore not expected to be
byte-identical. The canonical release identity is the promoted OCI digest with
its SBOM and provenance; immutable version aliases must continue to resolve to
that digest. Dependabot reviews Docker tag changes, while daily fresh-image and
published-image scans detect newly disclosed runtime vulnerabilities.

If persistence, notebook metadata, authentication schemas, or sessions are
involved, validate the release with fresh storage and a representative copy of
application data. Back up `/data` before testing schema migration. Rollback must
restore compatible application metadata from the backup when a schema is not
backward-compatible; canonical Markdown vaults remain ordinary files.

## Credential and disclosure response

If a credential may have been exposed:

1. Revoke or rotate it immediately at its authority (Git provider, proxy,
   registry, or other service).
2. Replace affected VariaNotes-managed keys and verify trusted host identities.
3. Inspect Git history, workflow logs, artifacts, images, caches, mirrors, and
   forks. Removing a secret from the latest commit is insufficient.
4. Preserve evidence without copying secrets into issues or ordinary logs.

Private reports remain confidential until affected users have a practical fix
or mitigation and coordinated disclosure is ready. Public advisories should
state impact, affected/fixed versions, remediation, migration, rollback, and
credit where appropriate without exposing private notebook data.

## Periodic maintenance review

Before each Alpha release and at least quarterly while maintained:

- confirm scheduled workflows still run and scanners receive current databases;
- review open/dismissed Dependabot, CodeQL, secret, and image findings;
- verify action SHAs, base images, security libraries, and tool versions remain
  maintained;
- verify the immutable-image repository variable and moving channel resolve to
  the intended release;
- exercise a scanner-failure path, a vulnerable test fixture where safe, fresh
  installation, representative-data upgrade, and rollback;
- confirm supported-version and private-reporting documentation is accurate.

These controls detect known advisories and recognizable unsafe patterns. They
cannot guarantee detection of unknown vulnerabilities, application logic flaws,
unsafe deployment, compromised maintainers, or mistakes in authentication
design. Security-sensitive features still require threat modeling and human
review.

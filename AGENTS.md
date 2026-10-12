# VariaNotes

VariaNotes is a self-hosted browser interface for ordinary Markdown vaults.
Current behavior is documented in `README.md`; this file keeps only invariants
and a map to task-specific context.

## Invariants

- One vault is one provider-independent directory. Notes stay ordinary
  Markdown; assets stay ordinary files. Both must remain usable without
  VariaNotes, and core features must not depend on a proprietary cloud service.
- Application metadata may describe configuration or UI/security state, but it
  must never become the only copy of a note or asset.
- The server filesystem is the canonical vault. Online-only saves use version
  checks; a stale save must preserve the browser draft and current server file
  for an explicit decision.
- Operators own backup, retention, and restore. An export is a portable copy,
  not a backup service or an in-app history.
- Keep filesystem operations inside the notebook root and reject traversal or
  symlink escapes. Data loss and security risks outweigh UI convenience.
- Keep authentication single-owner and fail-closed (`local` by default;
  `disabled` only by explicit configuration). Only one active backend writer
  per vault directory is supported.

## Find the relevant context

Read only what the task needs; do not load every document up front.

- Code boundaries and data flow: [`docs/architecture.md`](docs/architecture.md)
- User-visible behavior, storage, and export: [`README.md`](README.md)
- Accepted constraints: [`KNOWN-LIMITATIONS.md`](KNOWN-LIMITATIONS.md)
- Authentication/session design: [`docs/security/authentication-architecture.md`](docs/security/authentication-architecture.md)
- Proxy and TLS trust: [`docs/security/reverse-proxy.md`](docs/security/reverse-proxy.md)
- Validation commands and scope: [`docs/development/testing.md`](docs/development/testing.md)
- Dependency/security maintenance: [`SECURITY-MAINTENANCE.md`](SECURITY-MAINTENANCE.md)
- Release, upgrade, and recovery: [`ALPHA-RELEASE.md`](ALPHA-RELEASE.md)
- Shipped changes and changelog expectations: [`CHANGELOG.md`](CHANGELOG.md), [`CONTRIBUTING.md`](CONTRIBUTING.md)
- Git-free reboot contract and phases: [`docs/roadmap/git-free-reboot.md`](docs/roadmap/git-free-reboot.md)
- Full Alpha 3 milestone roadmap: [`docs/roadmap/alpha3-candidates.md`](docs/roadmap/alpha3-candidates.md)
- Historical milestone specifications: [`docs/history/agent-plan-archive.md`](docs/history/agent-plan-archive.md)

Use the nearest relevant source and tests for implementation details. Avoid
routine reads of `frontend/node_modules/`, `frontend/dist/`,
`frontend/*.tsbuildinfo`, and `frontend/package-lock.json`; inspect the lockfile
for dependency work.

## Local Go caches

For Go development, tests, vet, and security scans, use the persistent caches
`GOCACHE=/home/fredmin/.cache/go-build`,
`GOMODCACHE=/home/fredmin/go/pkg/mod`, and
`GOTMPDIR=/home/fredmin/.cache/varianotes/go-tmp`. Do not put large Go caches in
RAM-backed `/tmp`.

## Validation

Choose the smallest check that covers the change, then expand for shared,
cross-component, filesystem/persistence, authentication/security, or
release risk. Documentation-only changes do not need application test suites.
See [`docs/development/testing.md`](docs/development/testing.md); CI remains the
merge gate.

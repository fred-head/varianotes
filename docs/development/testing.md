# Development and validation

Choose validation from the changed behavior and its failure risk. Start with
the narrowest relevant test or check, then expand when the change crosses
packages, changes shared behavior, or exposes an unexpected failure. CI runs
broader merge gates; that does not require repeating the full matrix locally
for every small change.

## Commands

The authoritative targets are in [`../../Makefile`](../../Makefile),
`frontend/package.json`, and [CI](../../.github/workflows/ci.yml).

| Change | Focused validation | Broader check when warranted |
| --- | --- | --- |
| Go package behavior | e.g. `go test ./internal/app` | `go test ./...`; `go vet ./...` |
| Note paths, writes, assets, trash, links, or cleanup | `go test ./internal/files` | `go test ./...` for cross-package effects |
| Auth/session/MFA or auth route boundary | `go test -count=1 ./internal/auth ./internal/app ./cmd/varianotes` | Follow security/release policy for broad changes |
| One frontend behavior | `cd frontend && npm test -- src/components/editor/MarkdownEditor.test.tsx` (substitute the affected test file) | `npm test` |
| Frontend code or production/PWA integration | `cd frontend && npm run lint` or `npm run build` as relevant | Run all frontend checks for cross-cutting changes |
| Dependency changes | Focused package/runtime checks | Follow [`../../SECURITY-MAINTENANCE.md`](../../SECURITY-MAINTENANCE.md) and require CI gates |
| Release candidate | — | Follow [`../../ALPHA-RELEASE.md`](../../ALPHA-RELEASE.md) and the tagged release workflow |

`make test`, `make vet`, and `make test-race` run repository-wide Go checks;
`make frontend` installs dependencies and runs the full frontend lint, test,
and build sequence; `make audit` runs broad Go and npm security checks. Use
these when their scope matches the change, not as a default for documentation
or narrowly scoped work.

## Risk triggers

Expand validation for filesystem/persistence, authentication/security,
cross-component behavior, dependency/configuration, or release changes.
Preserve specific security gates: CI runs uncached auth
and route-boundary tests, race detection, static analysis, vulnerability and
secret scanning, frontend checks, and hardened-container persistence checks.
Dependency and release procedures may require the complete gate even when a
local focused test passes.

For destructive or security-sensitive behavior, inspect existing negative and
adversarial tests in the affected package. Add or update focused coverage when
the behavior changes. Documentation-only and instruction-only changes normally
need link/command/diff review rather than application suites.

## Efficient output

Use normal, non-verbose test modes first. Go and Vitest report failures without
printing every successful test by default. Add verbose output or isolate a
single test only when diagnosing a failure; do not suppress exit status or
error output.

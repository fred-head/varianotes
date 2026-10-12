# Alpha 2 dependency and toolchain inventory

This is a historical Alpha 2 snapshot. References to RepoQuill describe the
product at the time of review and are preserved as part of that record; the
current product name is VariaNotes.

Initial snapshot: 2026-08-30

Final source review: 2026-09-01

This document began as the Milestone 24 Phase 1 review baseline. It records what
the repository declared and resolved, what the upstream package managers would
permit, and which newer releases required a controlled decision in Phase 2. The
accepted and deliberately retained Phase 2 decisions are recorded below without
rewriting the original comparison out of the audit trail.

## Accepted Phase 2 updates

On 2026-08-30 the first two controlled update blocks were applied:

- Node.js 24 LTS replaced Node.js 22 for the frontend Docker build and every
  CI, release, and scheduled-security workflow.
- Testing Library React moved to 16.3.3 and user-event to 14.6.6.
- React DOM types moved to 19.2.5 and typescript-eslint to 8.69.0.
- Vite moved to 8.2.2 and successfully builds the production/PWA assets with
  Rolldown. React plugin 5.2.0 remains because Vite documents it as compatible
  while plugin 6.1.1 currently produces an unresolved optional Babel/Rolldown
  peer conflict under npm's normal resolver.
- ESLint moved to 10.9.1 with `@eslint/js` 10.0.1, globals 17.12.0, and the
  React Refresh lint plugin 0.5.5.
- Vitest moved to 4.1.11 and jsdom to 30.0.1; the final suite contains 85
  frontend tests.
- TypeScript moved to 6.0.3, the newest stable line supported by
  typescript-eslint 8.69.0. TypeScript 7.0.2 is intentionally retained as a
  future candidate because both stable and canary typescript-eslint currently
  require TypeScript `<6.1.0`.

Milkdown kit/react 7.22.1 was adopted as a coordinated update. Its CommonMark
schema correctly changed inline code to a non-inclusive mark, matching the
upstream fix for text typed after a code span. That exposed an incompatibility
in RepoQuill's additional empty-cursor inline-code toggle: after the first
character ProseMirror no longer reported the cursor as inside the mark, so a
second toolbar click could start the mark again instead of ending it. RepoQuill
now distinguishes an explicitly cleared `storedMarks` list from the marked text
immediately before the cursor. This preserves both Milkdown's non-inclusive
boundary and RepoQuill's start/type/stop workflow. Keep the dedicated regression
test when updating Milkdown; remove this compatibility logic only if a future
Milkdown command natively supports toggling inline code at an empty cursor with
the same behavior.

The regenerated lockfile reports no npm audit vulnerabilities. Local
development now fails early with an explanatory message unless Node 24 LTS or
a compatible Node 26+ line is used. This matches Docker and CI instead of
silently testing with the code-server's old Node 20 installation.

Version currency is not a vulnerability verdict. The release candidate still
has to pass the npm, Go, CodeQL, secret, container, SBOM, provenance, and runtime
gates in Milestone 24 Phases 3-5.

## Authoritative inventory sources

- `frontend/package.json` declares 6 browser/build dependencies and 16 direct
  development dependencies. `frontend/package-lock.json` is the complete npm
  resolution with 778 non-root package records, including 76 marked optional.
  A clean install on the current Linux development platform audits 714 packages;
  platform-specific optional packages explain why that installed count differs.
- The root `go.mod` declares 5 direct and 10 explicit indirect modules. The
  resolved `go list -m all` graph contains 35 third-party modules. `go.sum` is
  the checksum set, not by itself a list of shipped modules.
- `frontend/go.mod` is an intentionally empty module boundary. It declares Go
  1.24 but has no dependencies and contributes no code to the shipped binary.
- `Dockerfile` is the source inventory for three build/runtime base images and
  the Alpine runtime packages.
- `.github/workflows/*.yml` is the inventory for tool versions and immutable
  GitHub Action commit pins.
- The exact OS package revisions and transitive contents depend on the base
  image digest and repository state at image-build time. The source currently
  uses floating tags and `apk upgrade`; therefore these versions cannot be
  truthfully inferred from Git alone. Phases 4 and 5 must record them from the
  exact release-candidate image and its generated SBOM.

The npm currency snapshot was produced with `npm outdated` and package metadata
from the [official npm registry](https://registry.npmjs.org/). Go module
currency was resolved through the [official Go module proxy](https://proxy.golang.org/).
Toolchain lifecycle data comes from the [Go downloads API](https://go.dev/dl/?mode=json),
[Node.js releases](https://nodejs.org/en/about/previous-releases), and
[Alpine release branches](https://alpinelinux.org/releases/). Action currency
uses each project's official GitHub release feed.

## Dependency classes

- **Browser runtime:** React, Milkdown, and their bundled transitive code execute
  in the user's browser. Tailwind's Vite integration is a build plugin; the
  generated CSS is shipped.
- **Server runtime:** the Go standard library and the direct Go modules are
  compiled into the static RepoQuill binary. The final image also contains
  Alpine, CA certificates, Git, and the OpenSSH client.
- **Build/test/development only:** Node, npm, Vite, TypeScript, ESLint, Vitest,
  jsdom, Testing Library, type packages, and most associated transitive modules
  do not exist as a Node runtime in the final container.
- **CI/release only:** GitHub Actions, govulncheck, Trivy, Gitleaks, QEMU, and
  Buildx execute in GitHub-hosted jobs and are not application dependencies.

## Direct npm dependencies

“Permitted” is the newest version allowed by the current manifest selector and
seen by npm on the snapshot date. Exact Milkdown pins deliberately do not float.

| Package | Class | Declared | Locked | Permitted | Latest | License | Phase 1 result |
| --- | --- | ---: | ---: | ---: | ---: | --- | --- |
| `@milkdown/kit` | Browser runtime/editor | `7.22.1` | 7.22.1 | 7.22.1 | 7.22.1 | MIT | Updated; empty-cursor compatibility covered by regression test |
| `@milkdown/react` | Browser runtime/editor | `7.22.1` | 7.22.1 | 7.22.1 | 7.22.1 | MIT | Updated in lockstep with kit |
| `@tailwindcss/vite` | Build/CSS | `^4.1.0` | 4.3.3 | 4.3.3 | 4.3.3 | MIT | Current |
| `react` | Browser runtime | `^19.2.0` | 19.2.8 | 19.2.8 | 19.2.8 | MIT | Current |
| `react-dom` | Browser runtime | `^19.2.0` | 19.2.8 | 19.2.8 | 19.2.8 | MIT | Current |
| `tailwindcss` | Build/CSS | `^4.1.0` | 4.3.3 | 4.3.3 | 4.3.3 | MIT | Current |
| `@eslint/js` | Development/lint | `^10.0.1` | 10.0.1 | 10.0.1 | 10.0.1 | MIT | Updated; current |
| `@testing-library/react` | Test | `^16.3.3` | 16.3.3 | 16.3.3 | 16.3.3 | MIT | Updated; current |
| `@testing-library/user-event` | Test | `^14.6.6` | 14.6.6 | 14.6.6 | 14.6.6 | MIT | Updated; current |
| `@types/react` | Development/types | `^19.2.0` | 19.2.18 | 19.2.18 | 19.2.18 | MIT | Current |
| `@types/react-dom` | Development/types | `^19.2.5` | 19.2.5 | 19.2.5 | 19.2.5 | MIT | Updated; current |
| `@vitejs/plugin-react` | Build | `^5.0.0` | 5.2.0 | 5.2.0 | 6.1.1 | MIT | Retained: Vite-8-compatible; plugin 6 peer graph does not resolve cleanly |
| `eslint` | Development/lint | `^10.9.1` | 10.9.1 | 10.9.1 | 10.9.1 | MIT | Updated from unsupported line; current |
| `eslint-plugin-react-hooks` | Development/lint | `^7.1.0` | 7.1.1 | 7.1.1 | 7.1.1 | MIT | Current |
| `eslint-plugin-react-refresh` | Development/lint | `^0.5.5` | 0.5.5 | 0.5.5 | 0.5.5 | MIT | Updated; current |
| `globals` | Development/lint | `^17.12.0` | 17.12.0 | 17.12.0 | 17.12.0 | MIT | Updated; current |
| `jsdom` | Test DOM | `^30.0.1` | 30.0.1 | 30.0.1 | 30.0.1 | MIT | Updated; current |
| `typescript` | Build/typecheck | `^6.0.3` | 6.0.3 | 6.0.3 | 7.0.2 | Apache-2.0 | Latest officially supported by typescript-eslint; TS 7 blocked |
| `typescript-eslint` | Development/lint | `^8.69.0` | 8.69.0 | 8.69.0 | 8.69.0 | MIT | Updated; current |
| `vite` | Build | `^8.2.2` | 8.2.2 | 8.2.2 | 8.2.2 | MIT | Updated; current |
| `vite-plugin-pwa` | Build/PWA | `^1.3.0` | 1.3.0 | 1.3.0 | 1.3.0 | MIT | Current |
| `vitest` | Test | `^4.1.11` | 4.1.11 | 4.1.11 | 4.1.11 | MIT | Updated; current |

The npm lockfile is the complete transitive inventory. High-impact families in
that graph include Milkdown/ProseMirror, Vite/Rollup/esbuild, Workbox, Tailwind,
Vitest/Vite Node API, jsdom, and Testing Library. Optional native packages are
locked for supported build platforms. They must be allowed to move only through
normal lockfile regeneration in Phase 2, not by hand-editing transitive entries.

The Node 24 clean install reports a deprecation notice for `glob` 11.1.0
through the current Workbox/PWA build chain. It is build-only transitive code
and npm reports zero known vulnerabilities. RepoQuill does not override it
independently of the current PWA plugin and Workbox graph.

## Go runtime dependencies

All five direct modules are on the latest version reported by the Go module
proxy on the snapshot date.

| Module | Runtime responsibility | Current | Latest | License | Maintenance result |
| --- | --- | ---: | ---: | --- | --- |
| `github.com/alexedwards/scs/v2` | Server-side session manager API | 2.9.0 | 2.9.0 | MIT | Maintained stable release; custom SQLite store remains RepoQuill-owned |
| `github.com/pquerna/otp` | TOTP generation and validation | 1.5.0 | 1.5.0 | Apache-2.0 | Maintained release; includes barcode/QR transitive code |
| `golang.org/x/crypto` | Argon2id password derivation | 0.55.0 | 0.55.0 | BSD-3-Clause | Current Go security module |
| `golang.org/x/term` | Safe interactive recovery CLI input | 0.45.0 | 0.45.0 | BSD-3-Clause | Current Go module |
| `modernc.org/sqlite` | Pure-Go auth/session metadata database | 1.57.0 | 1.57.0 | BSD-3-Clause | Current actively released SQLite driver |

The complete resolved graph has 35 third-party modules. The 10 explicit
indirect requirements are barcode, go-humanize, UUID, isatty, strftime,
bigfft, `x/sys`, modernc libc, mathutil, and memory. Other resolved modules are
transitive build/test graph nodes. Available newer transitive versions include
barcode 1.1.0, `x/mod` 0.40.0, `x/net` 0.58.0, `x/sync` 0.22.0,
`x/tools` 0.49.0, modernc libc 1.75.6, and modernc memory 1.12.1.
Phase 2 must prefer direct-module updates followed by `go mod tidy`; it must not
independently force transitive versions without a demonstrated reason.

### Alpha 2 authentication and security dependency review

- Password hashing uses `x/crypto/argon2`; random generation, AES-GCM,
  SHA-256, constant-time comparison, and secure token generation use Go's
  maintained standard cryptography packages.
- SCS supplies session lifecycle primitives, but RepoQuill stores only hashed
  opaque session identifiers in its own SQLite store. No general identity or
  user-management framework was added.
- `pquerna/otp` implements the standardized TOTP calculation. RepoQuill owns
  enrollment scoping, secret encryption, replay prevention, recovery codes,
  and rate limiting.
- `modernc.org/sqlite` keeps CGO out of the runtime image and stores only
  authentication/application metadata, never canonical note content.
- The direct licenses are permissive and already represented in
  `THIRD-PARTY-NOTICES.md`: MIT, Apache-2.0, and BSD-3-Clause. No copyleft or
  source-availability obligation was newly introduced by Alpha 2.
- No direct Alpha 2 security dependency appears abandoned or unsupported from
  the current release metadata. Vulnerability absence is established by the
  release scans, not inferred from maintenance activity.

## Toolchains, base images, and system packages

| Component | Use | Declared/current source | Latest maintained line | Phase 1 result |
| --- | --- | --- | --- | --- |
| Go language directive | Module compatibility | 1.25.0 | 1.27.0 | Retained language baseline; production builds use the supported Go 1.26 line |
| Go builder and CI | Build/test only | 1.26.7 / `1.26.x` | 1.27.0 | Retained supported previous line to avoid a late toolchain migration; revisit before 1.26 support ends |
| Node builder and CI | Build/test only | `node:24-alpine` / 24 | Node 24.20.0 LTS; Node 26.8.1 current | Updated to current LTS |
| Alpine runtime | Server runtime | `alpine:3.24` | 3.24.1 branch | Current branch; upstream support through 2028-06-01 |
| Node builder Alpine | Build only | floating `node:24-alpine` | image-dependent | Fresh input; exact released candidate is identified and validated by OCI digest |
| Go builder Alpine | Build only | `golang:1.26.7-alpine` | Go 1.27 line available | Retained supported build line; builder does not ship |
| Runtime packages | Server runtime | `ca-certificates`, `git`, `openssh-client` after `apk upgrade` | Alpine 3.24 repositories | Exact versions are build-time resolutions; final SBOM/Trivy gate required |

The final runtime has no Node.js, npm, Go compiler, shell-based application
runtime, or frontend source tree. Git and OpenSSH are intentional runtime tools
for provider-independent synchronization.

Floating base tags and build-time `apk upgrade` improve patch uptake but mean
the same source commit is not byte-for-byte reproducible indefinitely. This is
an explicit Phase 2 decision rather than an unnoticed gap: scheduled fresh-image
builds and Docker Dependabot remain the patch-discovery path. The release
workflow builds one immutable source-SHA candidate, records its manifest digest
and SBOM/provenance, validates both supported architectures from that exact
digest, and only then attaches version/channel aliases. It never rebuilds after
validation. The promoted OCI digest, rather than a later source rebuild, is the
canonical reproducible Alpha artifact.

## CI and release tooling

Every GitHub Action is referenced by an immutable full commit SHA. The comment
records its intended release or major family.

| Action/tool | Pinned annotation | Latest release | Result |
| --- | --- | --- | --- |
| `actions/checkout` | 7.0.1 | 7.0.1 | Current |
| `actions/setup-node` | 7.0.0 | 7.0.0 | Current |
| `actions/setup-go` | 7.0.0 | 7.0.0 | Current |
| `actions/upload-artifact` | 7.0.1 | 7.0.1 | Current |
| `actions/attest` | 4.2.2 SHA | 4.2.2 | Current; exact release annotation verified |
| `docker/setup-qemu-action` | 4.2.0 SHA | 4.2.0 | Current; exact release annotation verified |
| `docker/setup-buildx-action` | 4.3.0 SHA | 4.3.0 | Current; exact release annotation verified |
| `docker/build-push-action` | 7.3.0 SHA | 7.3.0 | Current; exact release annotation verified |
| `docker/login-action` | 4.6.0 SHA | 4.6.0 | Current; exact release annotation verified |
| `aquasecurity/trivy-action` | 0.36.0 | 0.36.0 | Current |
| `gitleaks/gitleaks-action` | 3.0.0 | 3.0.0 | Current |
| `golang.org/x/vuln/cmd/govulncheck` | 1.7.0 | 1.7.0 | Current pinned scanner |

Action SHAs remain immutable. Each Docker/attestation SHA was resolved through
the official GitHub API and matched its current patch-release tag before its
human-readable annotation was made exact.

## Phase 2 decision queue

Completed low-risk compatible updates:

- Testing Library React 16.3.3 and user-event 14.6.6,
- React DOM type definitions 19.2.5,
- typescript-eslint 8.69.0 and globals 17.12.0,
- Node 24 LTS for local, CI, and container frontend builds.

Remaining compatible maintenance candidates:

- direct Go modules only if a newer compatible release appears when Phase 2
  begins,
- no direct Go update was available at the Phase 1 snapshot.

Completed editor compatibility update:

- Milkdown kit/react moved together to 7.22.1. RepoQuill's empty-cursor
  inline-code extension was adapted to Milkdown's non-inclusive mark boundary;
  retain its regression test and reassess the compatibility code during the
  next Milkdown update.

Completed separate migration evaluations:

- Vite 8.2.2 with the officially compatible React plugin 5.2.0,
- ESLint 10 with `@eslint/js` 10, globals 17, and related plugins,
- Vitest 4 and jsdom 30,
- TypeScript 6.0.3 with the required Vite client type declaration.

Retained pending upstream compatibility:

- `@vitejs/plugin-react` 6.1.1 until its optional Babel/Rolldown peer graph
  resolves normally through npm,
- TypeScript 7 until typescript-eslint officially permits it,
- Go 1.27 until the next deliberate supported-toolchain migration. Go 1.26
  remains an upstream-supported release line for Alpha 2.

No installed direct dependency is now classified as unsupported or
unmaintained. The two remaining `npm outdated` entries are the deliberately
retained React plugin 6 and TypeScript 7 described above. That does not authorize
release: the exact selected Phase 2 graph and built
multi-architecture image still require all later M24 security and regression
gates.

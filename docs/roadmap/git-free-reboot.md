# Git-free notebook reboot

Status: active product direction; the current working branch implements the
filesystem vault lifecycle, Git removal, ZIP export, and documentation phases.
This document replaces Git as the assumed product foundation. The repository
remains the development host for the application.

Roadmap placement: insert this reboot after Alpha 3 Milestone 30 and before
Milestone 31. Alpha 3 scope and milestone dispositions are tracked in
[`alpha3-candidates.md`](alpha3-candidates.md). M27 direct OIDC authentication
has been moved to Alpha 4; its requirements remain there as the planning basis.
The reboot supersedes only the Git-specific milestones and sync-derived
behaviors listed in the Alpha 3 roadmap. M31 remains deferred, while shipped
M32 and other compatible features stay in the product.

## Product goal

Build a self-hosted, single-owner Markdown vault that feels natural in a browser
and installable PWA. The server's filesystem is the canonical vault. The PWA is
online-only for note content: every device sees the same server state, and
there is no local replica queue to reconcile later.

Markdown notes, folders, and ordinary asset files remain readable without the
application. VariaNotes does not provide remote backup or promise recovery from
operator backup loss. The operator owns backup creation, retention, and restore.
An on-demand archive export may provide a portable copy of a vault; it is not a
backup service.

## Product decisions

- **Canonical data:** ordinary files under the configured notebook directory.
- **Access:** one owner, existing fail-closed authentication, optional TOTP MFA,
  session controls, and the existing trusted-proxy/TLS boundary.
- **Connectivity:** online-only note reads and writes. Cache the application
  shell only; never queue edits or cache notebook API responses in the PWA.
- **Ownership model:** retain multiple named vaults on one server. Devices use
  the same hosted vault rather than syncing separate local copies.
- **Backups:** operator responsibility, documented against the complete
  persistent data volume. No automatic remote copy is implied.
- **History:** no Git history or in-app version archive. Recovery is through
  operator-managed backups. App-level save conflicts still preserve the open
  browser draft and current server version until the owner chooses a result.
- **Trash:** retain the explicit recoverable Trash workflow. Permanent deletion
  removes the item from the active vault; operator backups may retain older
  copies according to their own policy.
- **Export:** provide a user-triggered archive of the active vault's ordinary
  files. The archive excludes application/authentication metadata, Trash, and
  legacy `.git` internals. VariaNotes-only image presentation sizes remain in
  server metadata and are not included. V1 does not import archives; existing
  vault folders are adopted from the operator-configured notebook directory.
- **Git:** no runtime Git commands, remotes, SSH keys, Git-derived status,
  commits, Git history, or Git conflict workflow. A legacy `.git` directory is
  left untouched during adoption; VariaNotes neither reads nor deletes it.
- **M31:** optional encrypted notes and folders are deferred. Do not introduce
  encryption state or secret handling as part of the Git removal.
- **M32:** the document outline is already implemented. Keep it as part of the
  rebooted product and preserve its browser-local preference and responsive
  navigation behavior.
- **Product identity:** VariaNotes is the sole active product identity.

## Feature disposition

| Existing capability | Reboot disposition |
| --- | --- |
| Markdown/GFM editor, toolbar, code blocks, task lists, and document stats | Keep |
| Notes, folders, tabs, rename/move, internal links, link checks | Keep |
| Image paste/upload, note-owned assets, cleanup, lightbox, presentation sizes | Keep |
| Full-text search over names, folders, and Markdown | Keep |
| Recoverable Trash and explicit permanent deletion | Keep; clarify that operator backups may retain copies |
| Multiple notebooks and notebook switching | Keep as multiple local vault folders |
| Authentication, MFA, sessions, password recovery, proxy trust | Keep |
| Responsive UI, installable online-only PWA, theme and local UI preferences | Keep |
| Visible running application version and container hardening | Keep |
| Git remote onboarding, cloning, connection checks, branch settings | Remove; replace onboarding with local vault creation/adoption |
| Managed SSH keys, host trust, key rotation | Remove from the application runtime and deployment configuration |
| Scheduled/manual Git sync, commit/status indicators, received-change notices | Remove; saves write directly to the canonical server files |
| Git multi-file conflict review and Git recovery refs | Remove |
| Note History backed by Git commits | Remove; recovery is through operator-managed backups |
| GitHub/GitLab/Forgejo workflow documentation | Replace with vault setup, export, and operator backup guidance |
| Git-free data portability | Add on-demand vault export; keep files directly usable outside the app |
| M31 selective encryption | Defer until after the new storage and recovery model is established |
| M32 document outline | Keep; already implemented and covered by the reboot |

Save concurrency remains a filesystem concern. Version-checked atomic writes
must continue to reject stale browser tabs. The editor must preserve both the
browser draft and current server version for an explicit user decision, without
creating a hidden permanent history.

## Implementation phases

### Phase 0: lock the data and security contract

- Specify local vault creation and adoption for folders containing ordinary
  Markdown notes and assets. Preserve those files and do not invoke Git or SSH.
- Define archive format, path/symlink handling, and the included ordinary files.
  V1 exports a vault and adopts folders already under the configured vault root;
  it does not import uploaded archives.

### Phase 1: filesystem-only notebook lifecycle

- Keep notebook registration, activation, naming, health checks, and safe local
  deletion without remote/branch/SSH fields.
- Create new empty vault directories without initializing Git.
- Adopt vault directories by path within the operator-configured notebook root.
  The app ignores any `.git` directory in an adopted folder and leaves it
  untouched; exports omit it.
- Store only current vault registration fields in the application registry.

### Phase 2: remove Git from the application

- Remove Git-backed onboarding, sync scheduling, status and sync panels,
  remote/conflict/history APIs, SSH settings, and their application services.
- Preserve version-checked filesystem saves, explicit stale-save resolution,
  link and asset behavior, and recoverable Trash.
- Remove Git and OpenSSH from the production container unless an independently
  justified runtime feature still needs them.
- Define how the PWA reports an unreachable server and ensure it never presents
  an offline edit as saved.

### Phase 3: portability, operations, and release preparation

- Add a bounded, safe archive export for a selected vault; preserve relative
  paths and ordinary file bytes.
- Rewrite setup, deployment, environment-variable, backup, recovery,
  limitations, and release documentation for clean installations.
- Explain that backups may retain content after app-level permanent deletion.
- Prepare the independent VariaNotes GitHub repository and GHCR package. Start
  with a fresh initial commit and do not transfer prior history, branches, pull
  requests, or release tags. Configure the required GitHub settings before the
  first release; do not publish a release as part of source preparation.

### Phase 4: post-reboot feature backlog

- Carry M27 direct OIDC authentication into Alpha 4 using the specification in
  [`alpha3-candidates.md`](alpha3-candidates.md); retain the single-owner and
  fail-closed authentication constraints.
- Revisit M31 encryption only with an explicit threat model, external recovery
  format, key lifecycle, asset/search behavior, and backup-retention policy.
- Keep M32's already implemented document outline in the supported feature set;
  it is not waiting for a later milestone.

## Reboot completion criteria

- A clean installation creates and edits a filesystem vault without requiring
  Git or SSH binaries, repositories, credentials, or remotes.
- Existing ordinary Markdown notebooks can be adopted without changing their
  note or asset bytes; legacy Git history is neither silently deleted nor used
  by the application.
- Every retained feature has an explicit implementation or documented
  replacement in the feature disposition table.
- The browser clearly distinguishes saved server state from an offline or
  failed request. No edit queue or note cache is persisted by the PWA.
- Permanent deletion semantics, export contents, and operator backup duties
  are documented without claiming to erase external backups.
- The container, UI, API, configuration, dependency graph, and user docs no
  longer advertise or require Git synchronization.

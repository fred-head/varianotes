# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- M31 exports and repository instances share vault synchronization and preserve
  active Save/Move/Restore transactions through commit or abort. Protected
  Trash reconciliation recovers and reloads the current catalog under those
  locks, retaining restore mappings across interrupted commits/finalization.
  B3/B4 and further security review remain open; activation stays disabled.

- M31 recovery repeats commit, content, catalog and key-envelope durability
  barriers before completing interrupted work. Serial protected Trash paths journal their
  destination directories and preserves durable restore metadata across
  interrupted finalization. Further security review remains required.

- M31 pending unlock requests are fenced against HTTP manual lock, logout
  and session revocation. Online CLI revocation coordination remains open.

- M31 protected views hide plaintext on lock-channel or connection failures
  and enforce a local display deadline when lock events stop arriving.
  Reconnection requires confirmed session status; hidden drafts stay in tab
  memory. Productive encryption activation remains disabled.

- M31 export password reauthentication now shares the bounded credential slot
  and persistent failure throttle with vault unlock, including across restarts.

- M31 locks discard conflict and backlink comparisons and reject late content
  responses across lock generations and vault switches. Unsaved drafts stay
  hidden in tab memory and remain available after unlocking.

- M31 Move/Trash commits recheck the session and lock generation before changing
  live content. Manual and idle locks reject prepared late mutations; content
  preparation and publication now use a consistent lock order without holding
  the key-manager state mutex during filesystem or network work.

- M31 password and recovery journal replay permits concurrent content saves
  with the unchanged data key, preserving ciphertext and preventing legitimate
  catalog changes from permanently blocking authentication startup.

- M31 moves, ordinary backlink updates, and explicit decrypt restores stage
  encrypted data before the durable journal decision. Directory plans and
  plaintext publication now recover after interruption; unexpected target
  files are retained instead of overwritten during recovery.

- Bind M31 catalog and journal recovery to the independently registered vault
  identity. Foreign packages and unbound pre-release fixtures fail closed
  without rewriting or deleting encrypted data.

- M31 protection preflight rejects existing plaintext attachment references on
  protected saves and unsupported local attachments during conversion. ZIP
  export rejects unregistered plaintext descendants of protected folders before
  streaming. Encryption activation remains disabled pending the M31 gates.

### Added

- Added M31P16 protection status, per-session unlock and manual lock, recovery
  code setup and replacement, and password recovery that preserves encrypted
  content. Locked views hide protected notes while unsaved drafts stay only in
  tab memory. Moves and Trash restores require an explicit keep-protected or
  decrypt choice; attachment placeholders and plaintext export/backup warnings
  remain visible. Encryption activation stays gated.
- Added M31P15 reauthenticated plaintext ZIP export for protected vaults.
  Exports use logical paths, validate the full archive before streaming, bind
  a one-use grant to the session and lock generation, and reject changed
  snapshots. Protected exports warn that the ZIP and earlier backups may
  contain plaintext; no server-side plaintext ZIP is staged.
- Added M31P14 search over protected notes only while a valid session key lease
  is active. Search requests avoid putting query text in URLs, results use
  `no-store`, and the browser drops results and ignores late responses after a
  server lock. Search data is not indexed or persisted; M31 activation stays
  gated.
- Added M31P13 journaled Trash, restore, and permanent deletion for protected
  notes, folders, and attachments. Restores apply the same keep-protection or
  decrypt choice as moves; collisions preserve both copies, shared attachments
  stay available, and referenced or ambiguous Trash data fails closed. Pending
  and completed operations reconcile after restart. M31 activation stays gated.
- Added M31P12 policy-aware note and folder moves with protected link previews,
  automatic encryption when moving into a protected folder, and explicit
  keep-protected or decrypt choices when moving out. Catalog paths and
  encrypted backlink updates commit through the M31 journal; stale previews
  leave the source untouched. M31 activation stays gated.
- Added M31P11 preflight and journaled conversion of existing notes, folders,
  shared attachments, and associated Trash. Unsafe entries, hardlinks, and
  `.age` collisions fail before staging; plaintext originals remain until the
  commit point and committed conversions recover forward. M31 activation stays
  gated.
- Added M31P10 catalog-backed image attachments with stable object identities,
  encrypted first-write uploads, shared Markdown-relative references, and
  locked-image placeholders. Multipart uploads stay bounded in memory without
  plaintext temporary files; encrypted reads are session-lease checked and
  cached only as `no-store` responses. M31 activation remains gated.
- Added M31P09 policy-aware protected Markdown reads, first-write encrypted
  note creation, and journaled saves with ciphertext-backed conflict versions.
  The wrapped per-file age key is stored in the authenticated catalog. Locked
  and corrupt content fails closed; M31 activation remains gated.
- Added one-time, 256-bit vault-key recovery codes with confirmation before
  activation and journaled multi-vault password recovery. Successful recovery
  rotates the code and vault-key envelopes, changes the local password, revokes
  sessions, and leaves encryption locked.
- Added M31P07 password coordination for vault-key envelopes, with a durable
  auth database journal and restart rollback/forward recovery. Password reset
  leaves vault envelopes unchanged and marks encryption passwords separate for
  later login changes. Startup refuses to disable auth while registered vaults
  contain encryption state or cannot be inspected.
- Added the M31 transaction journal for authenticated ciphertext and catalog
  updates, with a durable forward commit point, restart recovery, and hidden
  ciphertext staging. Encryption remains inactive pending the M31 release gate.
- Added the volatile M31 key manager with per-session unlock grants, a
  15-minute inactivity lock, explicit activity tracking, generation-checked
  response leases, manual/logout locking, and session-scoped lock events.
  User-facing unlock and activation remain unavailable while M31 is gated.
- Added versioned, authenticated encryption catalogs with durable recursive
  folder rules, object-bound ciphertext digests, and fail-closed metadata
  loading. Encryption remains inactive pending the M31 release gate.
- Added filesystem vault creation, adoption of existing folders, and bounded
  ZIP export of ordinary vault files.
- Added explicit stale-save review that preserves the browser draft and current
  server file until the owner chooses a version or edits a combined result.
- Added an optional document outline as a collapsible right-edge heading tree
  that uses the available margin without reducing the note editor's width.
- Added portable fenced-code language selection and controlled syntax
  highlighting, with accessible in-block copy controls in Edit and Read only
  modes.
- Added a keyboard accessible desktop sidebar control that leaves a narrow
  restore strip and lets the editor use the released width; the local browser
  preference is remembered while the mobile navigation drawer stays available.
- Kept Markdown paste available when it contains external HTTP(S) images, with
  each image blocked until the user explicitly loads it.

### Changed

- Rebranded RepoQuill as VariaNotes across the Go module, CLI, frontend, PWA,
  configuration, Docker deployment, documentation, and release workflows.
  Only the `VARIANOTES_*` environment namespace and VariaNotes application
  identifiers are supported; old names have no configuration fallback.
- Prepared a clean-slate VariaNotes source and release setup for the independent
  `fred-head/varianotes` repository and `ghcr.io/fred-head/varianotes` image.
  RepoQuill's Git history, branches, pull requests, and release tags are not
  carried into the new project. First publication remains a separate operator
  action.
- Replaced Git-backed notebooks with ordinary filesystem vaults. The server
  filesystem is canonical, saves write directly to it with version checks, and
  note content is online-only in the PWA. Existing folders can be adopted
  without changing note or asset files; VariaNotes ignores and leaves any
  legacy `.git` directory untouched. ZIP export includes ordinary vault files
  but omits `.git` internals and application metadata. Operators own backup and
  restore.
- Redesigned the Markdown editor toolbar with compact SVG icon buttons and a
  grouped Insert menu, keeping common formatting actions directly accessible.
- Moved the running VariaNotes version into the Settings header, right-aligned
  beside the heading.
- Made the Paste as Markdown toolbar action more compact with a paste icon and
  visible MD label while retaining its full accessible name and hover tooltip.

### Removed

- Removed Git and OpenSSH from the application runtime and production
  container, along with Git repository initialization, remote and branch
  management, clone onboarding, SSH key administration, scheduled and manual
  synchronization, Git-derived status, commits, note history, Git conflict
  review, and Git recovery APIs. The UI now reports direct server-save status.
- Removed offline note replicas and queued edits. PWA API and asset requests
  are network-only and `no-store`; an unreachable server cannot make an edit
  appear saved.

### Fixed

- Aligned interactive task-list checkboxes with the first text line while
  preserving their touch-friendly target size.
- Hide expired or revoked browser sessions from Security settings and prune
  inactive session records on subsequent session writes.
- Calculate document status statistics from the parsed editor content so
  Markdown syntax, image metadata, and link destinations do not inflate visible
  word and character counts; Unicode graphemes and logical document lines are
  counted consistently in Edit and Read only modes.
- Align the document outline with the editor toolbar and keep the note
  scrollbar visible while the outline is open.

### Security

- Pin CI and security scans to Go 1.26.9, require that toolchain for local Go
  builds, and use Go 1.27.2 for container builds to include the standard
  library security fixes in both supported toolchain lines.

## [0.1.0-alpha.2.security.5] - 2026-10-06

### Security

- Updated vulnerable transitive npm dependencies to audit-clean versions,
  including `brace-expansion` 2.1.7/5.0.12, `dompurify` 3.4.16,
  `fast-uri` 3.1.8, and test-only `undici` 8.11.2. These updates address
  GHSA-6j4f-fj2g-mc7p, GHSA-q2hr-2g5m-vwhr, GHSA-qhr7-859c-m2p7,
  GHSA-p98j-92pf-mc4p, GHSA-6688-9rhm-gjv2, GHSA-hrr3-gc8f-f4qj,
  GHSA-3wwx-pv8p-q78v, GHSA-pmjh-fq2x-6v4x, GHSA-r53p-7pc4-xj5r,
  GHSA-rfgv-xxqx-mfg5, GHSA-3xpg-4rpp-hhhm, GHSA-2jfj-6hjv-fm6j,
  GHSA-2gqq-gqf2-x968, GHSA-w293-vg96-wgc3, GHSA-8436-99hf-9mmv,
  GHSA-vp8m-p9jh-q5pm, and GHSA-rx4f-c7p8-82vq.
- Pinned transitive `katex` to 0.18.4 for GHSA-238p-pmpm-9mq7 and updated
  `source-map-js` to 1.2.2 for GHSA-68fv-2mgg-jv7q.

## [0.1.0-alpha.2.security.4] - 2026-10-05

### Added

- Added an explicit, mobile-friendly Paste as Markdown workflow and automatic
  handling for unambiguous `text/markdown` clipboard content while preserving
  ordinary text, rich HTML, and pasted-image behavior.
- Added accessible GFM task controls that toggle `[ ]` and `[x]` with mouse,
  touch, or keyboard input while participating in Undo/Redo and autosave.

### Changed

- Made the received-changes notification transient. It now disappears
  automatically while the change list remains available in synchronization
  details.

### Fixed

- Internal-note suggestions now open only after an intentional `[[` trigger,
  so ordinary brackets and task-list syntax do not open the suggestion menu.

### Security

- Markdown paste conversion rejects raw HTML and active external or absolute
  image references, retaining the original clipboard text instead of fetching
  untrusted assets.
- Updated the test-only transitive `undici` dependency to 8.11.2 after the npm
  audit gate detected the WebSocket decompression denial-of-service advisory.
- Fixed npm audit advisories by updating `brace-expansion` 2.1.4/5.0.9 to
  2.1.7/5.0.12 (GHSA-6j4f-fj2g-mc7p, GHSA-q2hr-2g5m-vwhr,
  GHSA-qhr7-859c-m2p7), `dompurify` 3.4.14 to 3.4.16
  (GHSA-p98j-92pf-mc4p), and `fast-uri` 3.1.7 to 3.1.8
  (GHSA-hrr3-gc8f-f4qj).

## [0.1.0-alpha.2.security.3] - 2026-09-03

### Fixed

- Restored the connection-test and final-review content in notebook onboarding
  steps 4 and 5 by removing structural CSS selectors that accidentally hid the
  active step.
- Made wizard-step rendering explicit in React and added a CSS-aware regression
  test for the complete connection and review transition.

## [0.1.0-alpha.2.security.2] - 2026-09-03

### Fixed

- Kept the notebook onboarding navigation visible in the dialog footer so
  `Back`, `Continue`, and `Connect notebook` remain reachable on constrained
  desktop, mobile, and PWA viewports.

## [0.1.0-alpha.2.security.1] - 2026-09-03

### Security

- Updated the transitive build-time `fast-uri` dependency from 3.1.5 to 3.1.7
  to address newly published URL canonicalization and SSRF advisories detected
  by scheduled npm security surveillance.

## [0.1.0-alpha.2] - 2026-09-02

### Security

- Added fail-closed single-owner password authentication, persistent server-side
  sessions, session-bound CSRF protection, trusted-proxy handling, progressive
  throttling, operator-only recovery, and an explicit unauthenticated mode.
- Added optional password-first TOTP MFA with local QR generation, encrypted
  secrets, replay protection, atomically single-use recovery codes, safe factor
  replacement, and separate operator MFA reset.
- Added the Milestone 19 adversarial route/MFA/session test matrix and a current
  OWASP-oriented Alpha 2 authentication review and release gate.
- Conflict-resolution drafts containing combined note text now stay in
  tab-scoped `sessionStorage` and are removed when the owner logs out or the
  authenticated session expires.
- Alpha publishing now builds one immutable multi-architecture source-SHA
  candidate, scans and smoke-tests that exact digest on AMD64 and ARM64, and
  promotes only the verified digest to version and channel tags.

### Added

- Responsive setup, login, reauthentication, Security settings, active-session
  administration, MFA enrollment/recovery, and visible disabled-mode guidance
  for browser and installed PWA use.
- Recoverable Trash with deliberate restore, note-focused Git history with safe
  restore, and portable internal note links with broken-link detection and
  rename/move rewriting.
- Complete notebook management and beginner-oriented provider-independent SSH
  onboarding, including a guided GitHub deploy-key path.
- Guided conflict review that preserves both sides and handles Markdown,
  delete/modify, rename/move, images, and binary files without requiring normal
  users to repair Git manually.
- An original-asset image lightbox with accessible fit/actual-size inspection in
  Edit, Read only, desktop, mobile, and installed PWA contexts.
- Responsive Small, Medium, Large, and Full inline image presentation
  presets that persist outside Git without changing Markdown or original image
  assets, apply in Edit and Read only modes, and retain original-asset lightbox
  inspection.

### Changed

- Milkdown kit/react now use 7.22.1. RepoQuill's empty-cursor inline-code mode
  preserves its explicit start/type/stop behavior across Milkdown's safer
  non-inclusive code-mark boundary, with a dedicated regression test.
- Frontend builds, CI, release jobs, and local-development requirements now use
  Node.js 24 LTS, with compatible Testing Library, React type, and
  TypeScript-ESLint and globals patch/minor updates applied reproducibly.
- The maintained frontend toolchain now uses Vite 8, ESLint 10, Vitest 4,
  jsdom 30, and the latest TypeScript release officially supported by
  typescript-eslint.
- Human-readable synchronization details now distinguish locally saved content
  from remote synchronization, report received changes, and keep note switching
  responsive while Git work continues safely in the background.
- Notes can be opened in session-scoped tabs; tabs follow supported note moves
  and close safely when their files are deleted.
- Fresh installations begin with explicit notebook onboarding instead of a
  synthetic local notebook. Inactive legacy registrations can be removed
  without deleting their files.
- Alpha 2 deployment, authentication, onboarding, synchronization, conflict,
  image, upgrade, recovery, and limitation documentation now matches the
  implemented application.
- Release documentation now distinguishes the pre-release source-SHA candidate
  from immutable version and moving Alpha aliases and records that the released
  digest, SBOM, provenance, scan, and runtime evidence refer to one artifact.

### Fixed

- Hardened notebook search and asset-reference scanning against concurrent
  symlink replacement by using root-scoped file access, bounded stored
  authentication material before integer conversion, and added `gosec` as a
  required CI, scheduled-security, and release gate.

- The browser-provided PWA installation suggestion can be dismissed, and the
  choice remains dismissed in that browser.

## [0.1.0-alpha.1.security.1] - 2026-08-26

### Security

- Rebuilt the runtime from current Alpine packages to replace OpenSSL
  `3.5.7-r0`, flagged by CVE-2026-14456, with the fixed package line.
- Established a zero-open-alert source baseline across CodeQL, Dependabot, and
  secret scanning after reviewing every historical source-to-sink finding.

### Changed

- Request logging now strips control characters and limits attacker-controlled
  fields, while all user-facing filesystem paths reject control characters in
  addition to traversal, absolute paths, reserved directories, and symlinks.
- GitHub-owned setup/checkout actions and Gitleaks now use their Node 24-capable
  release lines, and every workflow action remains pinned to an immutable SHA.
- Security maintenance now includes daily source, dependency, secret, freshly
  built image, immutable release image, and moving-channel image surveillance.
- Alpha publishing verifies immutable image aliases before moving the channel
  tag and creates a prerelease record with digest, SBOM/provenance, migration,
  and rollback guidance.
- Dependency updates follow an explicit human-review policy with complete
  functional, runtime, and security gates and no automatic merge or deployment.

## [0.1.0-alpha.1] - 2026-08-22

### Added

- Public alpha contribution guidance, supported-version security reporting, and
  a consolidated known-limitations document.
- Apache License 2.0 project licensing and matching package/container metadata.
- Fail-closed release gates for race detection, Go/npm vulnerability auditing,
  Git-history secret scanning, image vulnerability scanning, hardened container
  startup, health verification, and persistent Markdown survival.
- Tag-gated GHCR publishing for signed, SBOM-attested `linux/amd64` and
  `linux/arm64` images with immutable version/SHA tags and the moving
  `0.1.0-alpha` channel.
- Milestone 10 alpha hardening with same-origin protection for mutations, restrictive browser security headers, strict API fallbacks, request limits, and release/security documentation.
- Strict notebook onboarding validation that permits SSH remotes only, rejects local paths and unsafe Git protocols, and validates branch names before invoking Git.
- Hardened container defaults: localhost-only published port, read-only root filesystem, dropped Linux capabilities, no-new-privileges, and a constrained temporary filesystem.
- Release and CI builds now use the supported Go 1.26 toolchain line.
- Milestone 8 mobile notebook drawer, browser-provided install action, standalone manifest metadata, safe-area support, and explicit online-first offline messaging.
- Application-shell-only service-worker precaching; API responses and note contents are never added to the offline cache.

- Milestone 9 contextual slash-command menu for headings, lists, tasks, quotes, code, links, images, tables, and horizontal rules using the existing portable Milkdown commands.
- Keyboard filtering, arrow-key navigation, Enter/Escape handling, and touch-friendly command selection without proprietary Markdown syntax.
- One-command local development launcher for preparing persistent test directories and running the Go backend plus Vite together.
- Milestone 7 notebook search across folder names, Markdown filenames, and note contents with line-aware, clickable sidebar results.
- Confined backend search that ignores Git metadata, dependency trees, note asset directories, symlinks, non-Markdown content, and oversized notes.
- Configurable scheduled Git sync, editing-inactivity sync, notebook-switch sync, and best-effort tab-close sync using the existing conflict-safe synchronization service.
- Conflict-reducing sync triggers for application startup, returning focus, and opening another note.
- Browser-local sync preferences with independent interval, inactivity, switch, and close controls in Settings.
- Request-detached backend background sync trigger so an accepted tab-close request can finish after the browser request ends.
- Compact, keyboard-accessible notebook switcher with active-state indication, direct Add Notebook action, and a lightweight Manage Notebooks view.
- Dedicated responsive notebook onboarding flow outside Settings, including safe reuse of existing unassigned managed SSH keys with fingerprints and creation dates.
- Multi-entry notebook registry listing and activation APIs that preserve registered notebooks and switch active filesystem/Git services without a page reload.
- Managed SSH key administration in Settings with public-key copy, creation time, notebook assignment, unused-key identification, and confirmed deletion.
- Backend assignment revalidation that blocks deletion of notebook keys, malformed IDs, unsafe key directories, and keys whose assignment status cannot be established safely.
- Interactive provider-independent SSH host trust: discover presented host keys, display SHA256 fingerprints, require explicit approval, persist the exact approved keys, and automatically retry connection testing.
- Opaque, expiring host-trust approval requests with a second key scan to prevent arbitrary `known_hosts` injection and discovery/approval races.
- High-severity changed-host detection that displays previous and presented fingerprints while refusing automatic trusted-key replacement.
- Correct SSH host/port trust separation plus touch-friendly fingerprint review and copy controls.
- Milestone 5.1 per-notebook RepoQuill-managed Ed25519 SSH keys with persistent private material, public-key-only browser enrollment, and an advanced existing-server SSH alternative.
- Pre-clone connection testing with explicit success, host-verification, authentication, repository-access, and network failure states.
- Strict managed-key selection using isolated identities, batch mode, and a configured `known_hosts` file without automatic first-use trust.
- Security tests for private/public key separation, restrictive permissions, key-ID confinement, credential URL rejection, connection classification, and API response safety.
- Milestone 5 provider-independent Git service using direct `git` process arguments for status, commit, fetch, rebase, push, and manual synchronization.
- Separate repository Git state in the document status bar plus an explicit Sync action that waits for local save completion.
- Conflict detection with affected-file reporting, non-destructive sync failure states, and sanitized authentication/network errors.
- Clone-and-add workflow for existing Git repositories with optional branch selection, server-allocated working trees, and persistent active-notebook metadata.
- Local bare-repository tests for cloning, clean/dirty status, commit and push, no-change sync, remote integration, conflicts, failed remotes, and credential sanitization.
- Milestone 4.2 Settings maintenance workflow for scanning, reviewing, selecting, confirming, and deleting unreferenced image assets.
- Conservative Markdown reference resolution for relative, angle-bracket, percent-encoded, spaced, and nested asset paths.
- Per-file cleanup revalidation, failure reporting, repository confinement, symlink rejection, and safe empty `.assets` directory removal.
- Backend and frontend cleanup tests covering reference safety, manipulated paths, selection, confirmation, race revalidation, and deletion results.
- Sticky contextual image toolbar with Alt text, Replace image, and Remove image actions.
- Conservative image replacement that uploads a new asset and updates only the selected Markdown reference.
- Visual 10×10 table-size picker with hover/focus preview and touch-friendly selection.
- Cursor-relative table controls for adding or deleting rows and columns, deleting the table, and undoing structural edits through editor history.
- Responsive Milkdown formatting toolbar with undo/redo, block types, marks, lists, quotes, code, links, images, GFM tables, and horizontal rules.
- Active mark/block/list feedback and contextual image alt-text actions with accessible pressed and disabled states.
- Compact document status bar with save state plus live word, character, and line counts, structured for future Git sync status.
- Explicit per-note Edit/Read only toggle that prevents editor mutations without changing Markdown or repository metadata.
- Responsive Settings dialog with a persisted Off/1/5/15/30-minute editor auto-lock preference.
- Document-activity-based auto-lock that waits for autosave and ignores scrolling, pointer movement, and text selection.
- Contextual image metadata dialog for editing or clearing Markdown alt text on a selected image.
- Persistent light/dark UI switch that follows the system preference on first use.
- Clipboard screenshot/image paste directly into the Milkdown editor at the current cursor position.
- Touch-friendly image picker for camera/gallery uploads on supported mobile browsers.
- Per-note asset uploads using portable `<note>.assets/<random-name>` paths in ordinary Markdown.
- Confined image delivery with MIME detection, ownership checks, symlink rejection, and a 10 MiB upload limit.
- Backend asset tests covering upload/read, invalid media, size limits, traversal, cross-note access, and symlink escapes.
- Selection-relative note and folder creation using filename-only prompts.
- Note and folder creation, including immediately visible empty directories.
- Secure backend rename and move operations with collision protection.
- Deliberate note and folder deletion with recursive folder handling.
- Companion `.assets` lifecycle handling for note moves, renames, and deletions.
- File-operation API tests covering collisions, traversal, symlinks, recursive deletion, and asset link updates.
- Local demo notebook with nested Lorem Ipsum notes and CommonMark formatting examples.
- Live WYSIWYG-style Markdown editing powered by Milkdown and CommonMark.
- Debounced autosave and explicit save controls with Saved, Unsaved, Saving, Failed, and Conflict states.
- Atomic Markdown writes that preserve file permissions and detect external changes with content-version hashes.
- Repository tree and Markdown file-reading APIs configured through `REPOQUILL_REPOSITORY`.
- Responsive, accessible repository browser with nested folders and Markdown source display.
- Repository confinement tests covering traversal attempts and symlink escapes.
- Go HTTP server with health endpoint, graceful shutdown, structured request logging, and embedded frontend delivery.
- React, TypeScript, Vite, and Tailwind CSS frontend with backend connectivity status.
- Online-first PWA manifest and service worker setup.
- Multi-stage Docker image containing one application binary plus Git, OpenSSH, and CA certificates.
- Docker Compose configuration with persistent `/data` volume and health check.
- GitHub Actions checks for Go, frontend, and container builds.
- Initial backend tests and project documentation.

### Changed

- Public project links and OCI image metadata now point to
  `github.com/fred-head/repoquill`.
- Automated Git commands disable repository hooks, HTTP responses include defense-in-depth security headers, and unsafe or trailing request data is rejected.
- Docker and CI frontend installs now use the reproducible npm lockfile through `npm ci`.

- Code blocks now use predictable Enter behavior: one Enter starts the next code line, two can create a blank line, and a third Enter at the block end exits into a normal paragraph.
- Code blocks use a compact code-specific line height, while Inline code can now be enabled at an empty cursor, typed into directly, and disabled again with the toolbar, slash command, or Escape.
- Polished editor controls with a true Blockquote toggle, explicit link creation/edit/removal, clearer active link state, accessible slash-command announcements, and compact paragraph spacing that distinguishes Enter from Shift+Enter.
- Code-block Enter handling now explicitly uses ProseMirror's single-newline command instead of browser-dependent contenteditable behavior.
- Note navigation now waits only for required local persistence; Git synchronization runs in the background with single-flight follow-up handling for edits made during an active sync.
- Recently synchronized clean notebooks suppress redundant note-switch syncs, and completed background syncs never reload or discard the active editor draft.
- Milestone 6 notebook navigation now syncs both the notebook being left and the selected notebook when switch synchronization is enabled, without conflating local save and remote sync failures.
- The sidebar header now exposes a single `Notebooks` switcher, while the active notebook name remains in the switcher menu, tree root, and creation context.
- Settings now focuses on configuration and maintenance; the duplicate notebook clone form moved to the primary notebook navigation workflow.
- Notebook switching completes local save first and clears note, selection, tree, and expanded-folder state before loading the newly active notebook.
- The primary note-taking UI now uses the configured notebook name in the sidebar title, tree root, creation target, and move picker instead of repository-oriented labels.
- Removing an image from the editor now removes only its Markdown node; stored asset files remain untouched for data safety.
- Contextual table controls now appear on the first table interaction and remain sticky beneath the main formatting toolbar while scrolling.
- GFM tables now scroll horizontally within the note on narrow screens, while table mutation controls remain hidden in Read only mode.
- The note formatting toolbar now stays visible directly below the top Edit/Read only and Save actions while scrolling.
- The Settings action now uses a crisp, platform-independent SVG gear icon instead of an emoji glyph.
- The compact document status bar is now anchored to the bottom of the editor viewport instead of leaving unused space beneath it.
- Long unbroken text now soft-wraps consistently in Edit and Read only modes without altering Markdown; code blocks retain horizontal overflow behavior.
- Auto-lock timers now invoke browser timer APIs through their global receiver, preventing an `Illegal invocation` blank screen when opening an editable note.
- Edit/Read only transitions now remount Milkdown with the current draft instead of mutating a live ProseMirror view, preventing a blank UI during auto-lock.
- Newly pasted or selected images now use empty Markdown alt text instead of the original upload filename.
- Note renames now rewrite CommonMark angle-bracket and percent-encoded asset paths when filenames contain spaces.
- Milkdown image nodes now use the inline image view so repository-relative asset paths are proxied for display without changing the stored Markdown.
- The image picker now inserts ProseMirror image nodes directly at the cursor instead of visible Markdown source text.
- Repository folders are collapsed by default.
- File operations now follow a selected-item explorer model with selection-relative global creation actions.
- Rename is inline, while Move uses a folder picker instead of repository-relative path input.
- Rename, Move, and Delete are contextual rather than permanently displayed on every tree row.
- Expanded folder state now survives tree refreshes, file operations, folder path changes, and browser reloads.

# Alpha 3 roadmap

This document records the Alpha 3 milestones and their current dispositions.
The Git-free notebook reboot is inserted after Milestone 30 and before
Milestone 31; it changes the product foundation without dropping compatible
Alpha 3 work. Milestone 27 has been moved to Alpha 4. Statuses below distinguish
shipped work from work that remains, is superseded by the reboot, or is carried
forward. Historical milestone requirements remain below so their decisions and
acceptance criteria are not lost.

This roadmap retains historical milestone decisions and acceptance criteria.
Its current product and runtime references use the VariaNotes identity.

---

# Alpha 3 implementation order

Alpha 3 continues the single-owner, self-hosted Markdown product. The approved
Git-free reboot replaces the Git-backed storage and synchronization foundation
between Milestones 30 and 31. See the [reboot roadbook](git-free-reboot.md) for
its product contract and implementation phases.

The original milestones and reboot have these current dispositions:

| Work item | Status |
| --- | --- |
| M25 behavior-preserving frontend decomposition | Incomplete; continue as a focused maintainability task |
| M26 expanded user documentation | Incomplete; complete as part of the reboot documentation phase |
| M27 direct OIDC authentication | Moved to Alpha 4; specification retained below |
| M28 local-only Git notebooks | Shipped before the reboot; superseded by filesystem vaults |
| M29 managed SSH key rotation | Shipped before the reboot; removed with Git and SSH runtime support |
| M30 visible application version | Shipped; keep |
| Git-free notebook reboot | Inserted here, after M30 and before M31 |
| M31 selective encryption | Planned for Alpha 3; activation remains gated by all M31 steps and security review |
| M32 document outline | Shipped; keep |
| M33 code block language and copy UX | Shipped; keep |
| M34 collapsible notebook sidebar | Shipped; keep |
| M35 accurate document statistics | Shipped; keep |
| M36 refresh after external synchronization | Shipped for Git synchronization; obsolete after the reboot removes external sync |
| M37 sticky editor toolbar | Shipped; keep |
| M38 Markdown-aware paste UX | Shipped; keep |
| M39 unambiguous internal-note link trigger | Shipped; keep |
| M40 transient received-changes notification | Shipped for Git synchronization; obsolete after the reboot removes external sync |
| M41 interactive task-list checkboxes | Shipped; keep |
| M42 expired browser session cleanup | Shipped; keep |

The reboot preserves all compatible shipped Alpha 3 features and keeps the
remaining Alpha 3 work in the roadmap. M27's requirements remain below as the
planning basis for Alpha 4, not as Alpha 3 scope. Git-only milestones and
sync-derived notifications are retained here as historical decisions, with
their supersession stated explicitly. M31 remains part of Alpha 3. Its
implementation and activation are governed by the checklist and release gate
below; a UI or API must not enable encryption before the complete gate passes.

## Milestone 25 - Behavior-preserving frontend decomposition

Split the oversized `App.tsx` into focused components and hooks while retaining
the existing React state and API architecture where practical. Re-scope
remaining extractions to the Git-free vault product; workflows removed by the
reboot are not extraction targets.

Initial extraction candidates include:

- vault creation/adoption and Manage Vaults,
- version-checked save-conflict resolution,
- Trash,
- Settings and authentication settings,
- search and navigation state where a small extraction reduces `App.tsx`
  coupling.

Requirements:

- preserve all existing UX, accessibility, responsive behavior, and API calls,
- make small reviewable extractions rather than one wholesale rewrite,
- keep shared state ownership explicit and avoid introducing a new global state
  framework without a demonstrated need,
- add or retain regression coverage around every extracted workflow,
- do not combine the refactor with unrelated feature or design changes.

Completion criteria:

- `App.tsx` primarily composes focused application areas instead of containing
  their complete implementations,
- extracted areas remain independently understandable and testable,
- existing frontend, end-to-end, mobile/PWA, and authentication behavior remains
  unchanged.

## Milestone 26 - Expanded user documentation

Keep the README as a compact project introduction and quick-start guide. Build
expanded documentation for users and self-hosting operators covering:

- installation and upgrades,
- first-time setup and everyday use,
- local authentication, MFA, recovery, and disabled-auth mode,
- vault creation, folder adoption, and migration from a Git-backed release,
- version-checked save conflicts and their recovery choices,
- backup, restore, and disaster recovery,
- portable vault export and its distinction from a backup service,
- PWA installation and limitations,
- reverse proxy and troubleshooting guidance,
- OIDC setup and behavior after Alpha 4 Milestone 27 is implemented.

Prefer a version-controlled `docs/` source in the main repository so changes are
reviewed, versioned, searchable, and shipped with the matching release. A GitHub
Wiki may provide an additional presentation layer, but must not become the only
maintained copy of essential operational or recovery documentation.

Completion criteria:

- a new user can deploy, secure, use, back up, and troubleshoot
  VariaNotes without reading milestone specifications,
- documentation distinguishes saved server state from unsaved browser text,
- security-sensitive guidance matches the exact released behavior,
- README links clearly to the expanded documentation.

## Milestone 27 - Direct OIDC authentication (Alpha 4)

Add standards-based OIDC authentication for providers such as Authentik,
Authelia, Keycloak, and other compatible identity providers.

The IdP authenticates the owner. After a successful callback, VariaNotes creates
and manages its own normal application session. Do not implement this as classic
forward-auth in front of every browser or API request.

Requirements:

- retain `local` password authentication and explicit `disabled` mode,
- use a focused, maintained OIDC library and Authorization Code flow with PKCE,
- validate issuer, discovery metadata, state, nonce, signature, audience, and
  redirect URI according to the OIDC specification,
- explicitly bind access to the configured single owner; successful login at
  the IdP must not become open registration or multi-user access,
- keep client secrets and tokens out of frontend storage, URLs, logs, notebook
  repositories, and diagnostics,
- issue the same hardened VariaNotes application-session boundary used by local
  authentication after successful OIDC authentication,
- make MFA, password recovery, and identity lifecycle the IdP's responsibility
  in OIDC mode,
- provide actionable setup and failure diagnostics without leaking secrets,
- preserve safe browser/PWA session-expiry and logout behavior,
- document reverse-proxy, TLS, callback URL, and provider configuration.

Do not add account registration, roles, organizations, invitations, or a custom
OAuth/OIDC implementation.

Completion criteria:

- representative Authentik, Authelia, and Keycloak configurations are tested,
- an unbound IdP identity cannot become the VariaNotes owner,
- replay, callback tampering, invalid issuer/audience, expired tokens, and login
  CSRF are rejected by focused tests,
- local and disabled modes continue to work unchanged,
- OIDC sessions work reliably in browser and installed PWA usage.

## Milestone 28 - Local-only Git notebooks

Allow creation of a notebook without a remote service. The notebook must still
be an ordinary local Git repository, but initially has no `origin` remote.

Requirements:

- initialize a normal repository and branch inside the managed notebook root,
- keep notes and assets fully portable and visible as regular Git content,
- create local commits automatically using the notebook's configured sync
  triggers, without making a network connection,
- retain local commits, History, Trash/Restore, and recovery behavior,
- describe the notebook as local-only rather than reporting missing remote sync
  as a failure,
- hide or adapt remote-only synchronization actions and scheduling clearly,
- provide a deliberate later workflow for adding and validating an empty remote
  or one that shares Git history; reject unrelated histories without changing
  either repository,
- allow a connected notebook to return to local-only mode while preserving its
  local history and leaving the remote untouched,
- never require provider APIs or silently create a hosted repository,
- preserve all path-safety, concurrency, and data-safety rules.

Completion criteria:

- a local-only notebook can be created, edited, versioned, restored, restarted,
  backed up, and removed from VariaNotes safely,
- absence of `origin` never produces a misleading synchronization error,
- adding a compatible remote later preserves existing history and never force
  pushes or discards either side silently,
- switching to local-only removes the local remote configuration without
  deleting or changing the hosted repository.

## Milestone 29 - Managed SSH key rotation

Add a guided, non-destructive rotation workflow for managed notebook SSH keys.

Required sequence:

1. generate a new managed key without modifying the old key,
2. show the new public key for registration at the Git provider,
3. test host trust and repository access with the new key,
4. switch selected notebooks only after a successful test and confirmation,
5. leave the old key unassigned or assigned to notebooks not yet migrated,
6. instruct the operator to remove the old public key at the provider,
7. optionally delete the unassigned local private key through the existing
   deliberate key-management safeguards.

Requirements:

- never replace key material in place,
- never make the current key unusable before the replacement is verified,
- support rotating notebooks independently and show remaining assignments,
- prevent deletion of keys that are still assigned,
- avoid logging, exporting, or exposing private key material,
- preserve explicit SSH host fingerprint trust and repository connection tests,
- provide recovery guidance when a provider update or test fails midway.

Completion criteria:

- a key can be rotated without notebook downtime or loss of repository access,
- partial and failed rotations leave the previous working configuration intact,
- key assignment, cleanup, audit-safe diagnostics, and mobile/PWA workflows are
  covered by focused tests.

## Milestone 30 - Visible application version

Show the exact running VariaNotes version in a discoverable, unobtrusive place in
the application UI, such as an About area in Settings and a compact application
footer or equivalent mobile-safe location.

Requirements:

- use the backend-provided build version as the single source of truth,
- display the immutable release version, including alpha and maintenance
  suffixes, without inventing a separate frontend version,
- show `dev` clearly for unversioned local development builds,
- keep the version accessible on desktop and mobile/PWA without competing with
  note content or document save/synchronization state,
- provide a copyable version value for support and diagnostics,
- do not expose commit, host, path, or other deployment details unless they are
  deliberately added to a separate sanitized diagnostics view.

Completion criteria:

- the owner can identify the exact running release from the UI,
- container build metadata, backend API, UI, changelog, and Git tag agree for a
  release,
- version rendering and the `dev` fallback have focused tests.

## Milestone 31 - Selective encryption for notes and folders

M31 remains in Alpha 3. This checklist is the implementation plan. **P01–P10
are complete; P11–P18 remain open.** M31 is not complete until P01–P18 are
complete and the independent security review in P17 has passed. Encryption
activation must stay disabled in the UI, API, and any operator command until
the P18 release gate passes.

The size labels are relative estimates, not measured durations: S is small, M
is medium, and L is large or has substantial failure-recovery work. Each step
requires its listed tests; P17 adds end-to-end and adversarial coverage.

### Product decisions and security boundaries

- **Protection goal:** protect fully converted vault files and later server
  backups from reading without the password or key. The server decrypts content
  while unlocked. An attacker controlling the running, unlocked server can read
  plaintext and keys and may capture a password entered later. This is not
  end-to-end encryption.
- **Visible information:** filenames, folder layout, titles derived from
  filenames, file sizes, modification patterns, and some asset relationships
  remain visible. Earlier plaintext backups, snapshots, copies, and retained
  storage blocks are not retroactively protected or securely erased.
- **Keys and authentication:** a random vault key protects content keys; the
  password protects the vault key. The proposed age format creates a random key
  for each file. A regular password change rewraps the vault key without
  rewriting content. Local login initially supplies the unlock password. Later
  OIDC must use a separate encryption password; OIDC tokens are never keys.
  Encryption cannot be newly enabled without login. Login cannot be disabled
  while encrypted data remains unless it is decrypted first or a separate
  encryption password is established.
- **Recovery:** provide one high-entropy recovery code, shown once during setup
  or replacement and stored separately by the user. Recovery preserves
  ciphertext and permits password replacement. Old backups retain their old
  password/recovery envelopes; rewrapping current keys does not revoke old
  copies.
- **Files and policy:** encrypt note contents, images, and attachments; keep
  filenames and folder names visible. A protected folder recursively protects
  all current and future descendants, with no unprotected exceptions. A single
  note may be protected in an unprotected folder. A shared attachment is one
  encrypted object for every reference; never create a plaintext duplicate.
  While locked, an unprotected note referencing it shows “Geschützter Anhang”.
- **Moves and conversion:** moving unprotected content into a protected folder
  encrypts it. Moving protected content out requires an explicit choice to keep
  protection or decrypt it; for a protected folder the choice applies to the
  whole subtree. Keeping protection persists after the move. Protecting existing
  content also converts existing attachments. On failure, preserve originals
  and report a recoverable incomplete state; never present partial conversion
  as complete.
- **Lock and search:** keep the data key in server memory only while unlocked;
  lock after 15 minutes without user activity, logout, restart, or manual lock.
  Enforce state across tabs and sessions and reject late responses. Protected
  content is searchable only while unlocked. Do not persist a plaintext index;
  discard in-memory search data on lock.
- **Export:** every ZIP containing protected content asks again for the current
  unlock password and produces an unencrypted plaintext ZIP. An export without
  protected content does not ask. Warn that the ZIP is unencrypted and earlier
  backups remain plaintext.
- **Format and failures:** use a maintained authenticated encryption format
  that an external tool can decrypt without VariaNotes; age is the proposed
  baseline, subject to P03 validation. Authenticate catalog paths and object
  identities because file encryption alone does not prevent swaps or full
  rollback. Fail closed on corruption, unknown versions, missing keys, and
  invalid metadata; never fall back to plaintext.
- **Operational limits:** do not claim physical memory erasure or secure removal
  of prior SSD/filesystem copies. Account for swap, core dumps, proxy buffering,
  temporary files, browser memory, and external programs writing into protected
  folders. Unsafe or ambiguous filesystem entries block conversion.

### Ordered implementation steps

- [x] **P01 — Shared file access layer** *(dependencies: none; size: M)*
  **Goal/scope:** common content reader/writer and version-check boundary,
  initially preserving ordinary files; retain path confinement and filesystem
  coordination in this layer.
  **Acceptance/tests:** byte-identical ordinary reads/writes; stale versions
  conflict; traversal, symlink escape, non-regular files, and unsafe sizes are
  rejected; concurrent writes have deterministic outcomes.

- [x] **P02 — Route every content path through the shared layer**
  *(dependencies: P01; size: L)*
  **Goal/scope:** remove direct content access from note operations, link preview
  and rewrite, assets, search, conflicts, and ZIP export, including export's
  separately created repository.
  **Acceptance/tests:** inventory and exercise every content path; current
  plaintext behavior and conflict handling remain; no route bypasses the layer.

- [x] **P03 — Encryption format and key envelopes**
  *(dependencies: P01; size: M)*
  **Goal/scope:** integrate the selected maintained format, random vault key,
  per-file random content keys, password-protected vault-key envelope, format
  version, and bounded password derivation.
  **Acceptance/tests:** round-trip text, binary, empty, and large files; official
  age CLI decrypts test files; wrong password, tampering, truncation, and
  unsupported versions fail without returning plaintext. Implemented in
  `internal/encryption` with age v1.3.2, age scrypt at a fixed and capped work
  factor, per-file X25519 identities, and XChaCha20-Poly1305 content-key
  envelopes bound to object IDs. CI builds the pinned official age CLI for the
  interoperability test. These primitives are not connected to vault
  reads, writes, APIs, or operator commands; the M31 activation gate remains
  closed.

- [x] **P04 — Authenticated catalog and durable rules**
  *(dependencies: P01, P03; size: M)*
  **Goal/scope:** versioned catalog of vault/object IDs, logical/physical paths,
  ciphertext digests, explicit note protection, and recursive folder rules;
  authenticate metadata and bind entries to objects.
  **Acceptance/tests:** empty protected folders retain rules after restart;
  tampered, swapped, missing, or unsupported entries fail closed; `.age`
  collisions are detected; no note text, preview, or plaintext fingerprint is
  stored in the catalog. Implemented in `internal/encryption` with a
  versioned HMAC-authenticated catalog, random vault/object IDs, ciphertext-only
  SHA-256 digests, recursive rules, strict metadata/path validation, and an
  atomic owner-only catalog store in server application metadata. Catalog
  loading requires the expected vault ID and fails closed for missing,
  unsupported, corrupt, or unauthenticated state. These catalog primitives
  remain disconnected from vault operations until later M31 steps and the P18
  activation gate.

- [x] **P05 — In-memory key manager and server lock state**
  *(dependencies: P03, P04; size: M)*
  **Goal/scope:** central key lifetime, per-session grants, 15-minute inactivity
  lock, logout/manual/restart lock, tab notifications, and stale-response
  invalidation.
  **Acceptance/tests:** no protected API returns plaintext while locked; other
  sessions must unlock; background requests do not extend timeout; restart
  starts locked; logout/manual lock revoke grants; late responses stay hidden.
  Implemented in `internal/encryption` as a volatile per-vault key manager with
  grants bound to authenticated session hashes, a 15-minute idle timer, explicit
  foreground activity, generation-checked key leases, and session-scoped lock
  events. The lock, activity, and event routes do not provide an unlock path or
  protected content; startup remains locked and the M31 activation gate stays
  closed. Logout, session renewal/revocation, password change, and MFA disable
  lock all in-memory vault keys.

- [x] **P06 — Transaction journal and restart recovery**
  *(dependencies: P01, P03, P04; size: L)*
  **Goal/scope:** reusable prepare, ciphertext staging, verification, commit
  point, and forward recovery for changes spanning content and catalog files.
  **Acceptance/tests:** inject failures at writes, syncs, renames, and commit
  boundaries; before commit the old state survives, after commit restart
  completes forward; full disk and permission errors preserve originals and
  block affected content; no plaintext staging files remain.
  Implemented in `internal/encryption/transaction.go`. The prepared journal is
  HMAC-authenticated with the vault key; each replacement is verified as
  object-bound age ciphertext and must match the authenticated next catalog.
  Ciphertext stages live beside their vault targets, while the catalog stage
  and journal live in application metadata. Originals remain in place until a
  durable commit record selects forward recovery. A restart discards a prepared
  transaction or completes a committed one through `Recover`; callers must
  recover before serving affected content. Failures after the commit point
  return a recovery-required error and retain the journal. Staging names are
  hidden from ordinary file enumeration and direct content reads. These
  transaction primitives are not wired into vault operations; the M31
  activation gate remains closed.

- [x] **P07 — Password changes and authentication-mode rules**
  *(dependencies: P03–P06; size: L)*
  **Goal/scope:** coordinate login credential and vault-envelope changes;
  support later separate encryption password; guard login disablement in runtime
  and startup configuration.
  **Acceptance/tests:** password change rewrites envelopes but not content;
  interruption across vaults/auth database resumes safely; unavailable
  registered vault blocks partial change; password reset without old password
  preserves ciphertext/envelopes; disable rules include encrypted Trash; OIDC
  credentials are never keys. Implemented as a journaled auth/envelope
  coordinator: it prepares every registered vault before changing any envelope,
  commits the auth credential and journal phase atomically, then finishes the
  envelope updates. Startup rolls a prepared change back or completes a
  committed change before serving requests. Recovery without the old password
  changes auth state only and records a separate encryption-password binding,
  so later login changes leave those envelopes alone. Disabling auth scans every
  registered vault and blocks on encryption metadata, `.age` files including
  Trash, or an unavailable vault. The envelope rewrap primitive accepts a
  separate password; future OIDC credentials are not part of this API. No note
  or asset bytes are rewritten.

- [x] **P08 — One-time recovery code**
  *(dependencies: P03–P07; size: L)*
  **Goal/scope:** code creation, one-time display, user confirmation, password
  loss recovery, replacement, and multi-vault envelope update.
  **Acceptance/tests:** private code is not persisted on server; setup activates
  only after envelopes verify; wrong code changes nothing; interrupted recovery
  does not consume old code early; success changes password and code, revokes
  sessions, and locks vaults; documented external recovery works. Implemented
  as a random 256-bit, one-time displayed code whose age envelopes are staged
  in memory until confirmation. Confirmed setup and password recovery update
  every protected vault through an auth-database recovery journal; prepared
  operations roll back at startup and committed operations finish forward.
  Password recovery replaces both the local password and code, revokes all
  sessions, and locks in-memory vault keys. See the recovery details in the
  [authentication architecture](../security/authentication-architecture.md).
  The externally stored code restores access through VariaNotes from a matching
  complete backup; standalone content decryption remains in the P17/P18 gate.

- [x] **P09 — Encrypted note reads, saves, and conflicts**
  *(dependencies: P01–P06; size: M)*
  **Goal/scope:** route protected note reads, creates, saves, and conflict
  resolution through policy-aware encrypted storage.
  **Acceptance/tests:** protected saves and temporary files contain only
  ciphertext; creation in protected folders is encrypted from the first write;
  stale saves preserve draft and server version; corrupt data never opens as
  Markdown or gets overwritten. Implemented protected note reads, creates,
  version-checked saves, conflict responses, and logical tree entries through
  the authenticated catalog and per-session key leases. The per-file age key
  envelope is authenticated in the catalog; saves and creates use the M31
  transaction journal. Corrupt or untracked ciphertext fails closed, and
  ciphertext-backed versions avoid storing plaintext hashes. The protected
  content routes remain unavailable while locked, and no unlock or activation
  path is exposed before the M31 release gate.

- [x] **P10 — Shared images and attachments**
  *(dependencies: P01–P06, P09; size: L)*
  **Goal/scope:** stable attachment identity and safe reference resolution;
  encrypted upload/read and global protection when any protected rule applies.
  **Acceptance/tests:** shared bytes exist once and are encrypted; locked
  references show “Geschützter Anhang”; removing one reference does not decrypt
  or delete a still-used object; encoded/traversal paths and bounded large-file
  streams are covered; multipart upload leaves no plaintext spill files.
  Implemented catalog-backed attachment objects and journaled first-write
  encryption, with shared Markdown-relative path resolution and a fail-closed
  check for plaintext attachments referenced by protected notes. Multipart
  uploads stream through a bounded reader without temporary files. Locked image
  requests return no content and the editor shows “Geschützter Anhang”; shared
  cleanup tests retain an object until every reference is removed. M31
  activation remains gated by P11–P18 and the independent security review.

- [ ] **P11 — Convert existing notes, folders, and attachments**
  *(dependencies: P04, P06, P09, P10; size: L)*
  **Goal/scope:** preflight, inventory, stage, verify, and convert selected
  existing content, including recursive files and associated Trash.
  **Acceptance/tests:** detect collisions, symlinks, hardlinks, and unsupported
  entries before conversion; inject cancellation, disk errors, and restart at
  every phase; preserve originals before commit; resume after commit; never show
  partial conversion as complete.

- [ ] **P12 — Rename, move, and link updates**
  *(dependencies: P06, P09–P11; size: L)*
  **Goal/scope:** enforce keep-protected/decrypt choices on moves; preserve
  recursive folder policy and update links through encrypted writes.
  **Acceptance/tests:** unprotected-to-protected converts automatically;
  protected-to-unprotected requires a choice; protection survives “keep”;
  decrypting under a protected parent is rejected; stale previews and interrupted
  multi-note link updates do not corrupt files.

- [ ] **P13 — Trash and restore**
  *(dependencies: P06, P10–P12; size: M)*
  **Goal/scope:** retain encryption/policy across Trash, restore, and permanent
  delete; respect shared references.
  **Acceptance/tests:** protected Trash remains ciphertext; restore into a
  protected destination encrypts; restore out requires the same keep/decrypt
  decision as a move; collisions preserve both copies; abort/restart is
  recoverable; unknown references cause retention or blocked deletion, not data
  loss.

- [ ] **P14 — Search, caches, and plaintext leak checks**
  *(dependencies: P05, P09, P10; size: M)*
  **Goal/scope:** search protected content only while unlocked; review derived
  previews, API responses, logs, temporary files, browser storage, and service
  worker caching.
  **Acceptance/tests:** no persistent plaintext index; lock discards results;
  late responses stay hidden; sentinel content/passwords are absent from logs,
  metadata, temp files, persistent browser storage, and API caches.

- [ ] **P15 — Reauthenticated plaintext ZIP export**
  *(dependencies: P05, P06, P09–P14; size: L)*
  **Goal/scope:** snapshot export contents, re-prompt for current password if any
  protected note or shared attachment is included, then stream logical plaintext
  paths into an unencrypted ZIP.
  **Acceptance/tests:** protected export always prompts and unprotected export
  never does; concurrent mutations cannot bypass reauthentication; corrupt files
  fail before streaming; no server-side plaintext ZIP staging; keyfiles/journals
  are excluded; lock/logout invalidates the one-use grant.

- [ ] **P16 — Protection, lock, recovery, and export UI**
  *(dependencies: P05, P07–P15; size: M)*
  **Goal/scope:** expose status, protect/unprotect choices, unlock/lock, recovery,
  shared-attachment placeholder, and backup/export warnings.
  **Acceptance/tests:** wording and choices match product decisions; unsaved
  drafts remain browser-memory-only and hidden on lock; tabs follow server state;
  activation stays gated even if UI code merges earlier.

- [ ] **P17 — End-to-end fault matrix and independent security review**
  *(dependencies: P01–P16; size: L)*
  **Goal/scope:** exercise complete flows and obtain independent review of crypto
  integration, transaction recovery, auth coordination, and data leaks.
  **Acceptance/tests:** cover conversion/save/move/Trash/export aborts; restarts at
  journal boundaries; corrupt files/envelopes; password change and recovery
  interruption; multi-session lock races; shared assets; and byte-for-byte
  external recovery without VariaNotes. Resolve all blocking findings before
  release; document any remaining finding and confirm it does not weaken the
  agreed security boundary. Obtain reviewer sign-off.

- [ ] **P18 — Operations documentation and release gate**
  *(dependencies: P01–P17; size: S)*
  **Goal/scope:** document setup, recovery, external decryption, backups, visible
  metadata, memory limits, earlier plaintext copies, and plaintext ZIPs; enable
  the feature only after all mandatory steps and review pass.
  **Acceptance/tests:** follow external recovery from a complete test backup;
  verify no activation path works before the gate; pass new/adopted-vault flows;
  record P01–P18 and security review complete before marking M31 complete.

### M31 completion and activation gate

- P01–P18 are mandatory and ordered. Each step's tests must pass before it is
  marked complete.
- P17's independent security review and end-to-end external recovery are
  release conditions, not follow-up work.
- P16 may be developed behind a disabled gate, but no UI, API, or operator
  command may activate encryption before P18.
- If a data path bypasses the shared layer, a failure can expose or lose content,
  or external recovery cannot decrypt the full vault without VariaNotes, the
  activation gate remains closed.
- M31 is complete only after every checklist step and the security review pass.
  Earlier plaintext backups and physical remnants remain outside this claim.

## Milestone 32 - Document outline / table of contents

Add an optional, contextual document outline generated from the headings of the
currently open note. Treat this as navigation UI rather than persisted note
content.

Desktop interaction:

- add a clear Outline / Table of contents button to the note editor toolbar,
- open a collapsible panel on the right side of the editor,
- keep the panel visually subordinate to the note and allow it to be closed at
  any time,
- remember the local open/closed preference without writing UI state into the
  notebook repository.

Mobile/PWA interaction:

- use the same toolbar action,
- open the outline as a touch-friendly drawer or sheet instead of permanently
  reducing the editor width,
- close the drawer after the user chooses a heading and move focus safely back
  to the note.

Requirements:

- derive the hierarchy from ordinary Markdown Heading 1-6 nodes in the active
  Milkdown/ProseMirror document,
- update the outline while headings are added, renamed, removed, or reordered,
- clicking an entry scrolls to the exact heading without inserting generated
  anchors, `[TOC]` syntax, IDs, frontmatter, or other proprietary Markdown,
- handle duplicate heading text by document position rather than assuming that
  heading labels are unique,
- show the current document section while scrolling where this can be done
  reliably without disrupting editing,
- preserve indentation and heading-level relationships while remaining readable
  when levels are skipped,
- provide a compact empty state when the note contains no headings,
- work in both Edit and Read only modes,
- expose semantic navigation, keyboard operation, visible focus, accessible
  labels, and adequate touch targets,
- avoid expensive full-document reparsing on every keystroke and test behavior
  with long notes.

Completion criteria:

- the outline opens from the editor toolbar only when a note is active,
- desktop uses the collapsible right-side panel and mobile/PWA uses a drawer or
  equivalent narrow-layout presentation,
- heading selection navigates accurately for nested and duplicate headings,
- live editing updates the hierarchy without changing serialized Markdown,
- opening, closing, navigation, empty state, accessibility, Read only behavior,
  and responsive presentation have focused tests.

## Milestone 33 - Code block language and copy UX

Improve fenced code blocks while keeping standard Markdown as the canonical
representation.

Requirements:

- show a compact `Copy` action on code blocks in both Edit and Read only modes,
- copy only the literal code content without fence markers, language metadata,
  line numbers, or formatting,
- provide readable success and failure feedback that does not rely on color and
  does not trigger browser modal dialogs,
- expose a contextual language selector when a code block is selected in Edit
  mode,
- offer `Plain text` plus a focused initial set of common languages such as
  Shell/Bash, PowerShell, Python, JavaScript, TypeScript, JSON, YAML, Markdown,
  HTML, CSS, SQL, Go, and Dockerfile,
- serialize the selected language through the ordinary fenced-code info string,
  for example ` ```python ` or ` ```bash `,
- preserve valid unknown language identifiers already present in imported
  Markdown even when VariaNotes cannot highlight them,
- apply syntax highlighting as presentation only; it must never rewrite code,
  execute code, fetch language definitions from untrusted locations, or persist
  generated HTML into Markdown,
- use a maintained highlighting library with an explicitly controlled language
  set, safe token rendering, acceptable bundle size, and compatible licensing,
- keep unrecognized or malformed language metadata readable as unhighlighted
  plain code,
- make Copy and language selection keyboard-accessible and touch-friendly
  without obscuring code on narrow/mobile layouts,
- retain the existing Enter behavior for writing and leaving code blocks.

Completion criteria:

- newly selected languages round-trip through portable fenced Markdown,
- imported recognized and unknown language tags survive load/edit/save without
  silent loss,
- highlighting never changes copied or serialized code content,
- Copy works reliably in browser and installed PWA contexts with accessible
  feedback and a safe failure state,
- plain, highlighted, long, horizontally scrolling, duplicate, Read only, and
  mobile code blocks have focused tests.

## Milestone 34 - Collapsible notebook sidebar

Allow users to hide the left notebook and file-tree sidebar when they need more
horizontal space, especially on portrait-oriented or otherwise narrow desktop
monitors.

Requirements:

- provide a clear, keyboard-accessible control that collapses the complete
  notebook/file-tree sidebar without closing the active note,
- keep a compact and discoverable control available to restore the sidebar,
- preserve the active notebook, selected note, open note tabs, unsaved editor
  state, and expanded folder state while the sidebar is hidden,
- remember the user's local collapsed/expanded preference without storing UI
  state in a notebook repository,
- let the editor and its toolbars use the released horizontal space without
  introducing fixed-width gaps or horizontal page scrolling,
- retain the existing mobile drawer behavior instead of treating a hidden
  desktop sidebar as a replacement for mobile navigation,
- use accessible labels, visible focus, suitable touch targets, and state
  semantics such as `aria-expanded`,
- avoid hiding save, server-availability, authentication, or conflict status
  along with the navigation.

Completion criteria:

- the sidebar can be collapsed and restored without navigating away from or
  reloading the active note,
- editor width responds correctly on landscape, portrait, and narrow desktop
  layouts,
- the preference survives a browser reload on the same client,
- desktop keyboard interaction and the existing mobile/PWA drawer behavior have
  focused tests.

## Milestone 35 - Accurate document statistics

Correct the editor status-bar statistics so they describe the content a user
can actually read instead of counting the raw serialized Markdown source.

Current defect:

- `documentStats` operates directly on Markdown source text,
- serialized hard-break syntax such as `<br />` contributes several characters
  and the false word `br`,
- an empty new paragraph can therefore increase the word count,
- image alt text, generated asset names, and relative asset paths are counted
  even though the image is a single non-text document node.

Required behavior:

- derive statistics from parsed Markdown/editor document semantics rather than
  applying a word expression to the complete Markdown source,
- count visible prose, headings, list text, quotations, link labels, inline
  code, and code-block contents while excluding their Markdown delimiters,
- exclude link destinations and Markdown metadata from word and character
  counts,
- count every image node as zero words and zero characters regardless of its
  alt text, title, source filename, generated asset filename, or path,
- treat soft breaks, hard breaks, empty paragraphs, and line-ending encodings as
  zero words and zero characters; they may affect only the line count,
- count user-perceived Unicode characters consistently where browser support
  permits, rather than inflating composed characters or emoji sequences,
- define line count from logical document lines/blocks consistently in Edit and
  Read only modes without changing serialized Markdown,
- update statistics live without adding noticeable work to normal typing,
- keep the status-bar labels and responsive layout unchanged.

Completion criteria:

- Enter and Shift+Enter never create phantom words or characters,
- a blank line changes only the documented line statistic,
- inserting, replacing, resizing, or changing metadata for an image does not
  change word or character counts,
- Markdown syntax and asset/link destinations do not inflate visible-text
  statistics,
- prose, Unicode text, headings, lists, links, inline/code blocks, line endings,
  empty paragraphs, hard breaks, and images have focused unit tests,
- Edit and Read only modes report identical statistics for the same note.

## Milestone 36 - Refresh opened notes after external synchronization

Refresh the editor after a successful synchronization receives an external
change to the note currently being viewed, without weakening draft or conflict
protection.

Current defect:

- Git synchronization updates the notebook working tree and reloads the file
  tree but does not reload the active note,
- `receivedChanges` informs the user about the remote update while the editor
  continues showing its pre-sync `FileResponse`, content, and version,
- the note becomes current only after a full browser reload or after navigating
  away and opening it again,
- the existing synchronization UI test explicitly preserves this stale-view
  behavior and must be replaced with the intended safe-refresh behavior.

Required behavior:

- after a successful, conflict-free synchronization, inspect received `added`,
  `updated`, `moved`, and `deleted` changes against the active note,
- when the active note was updated externally and has no unsaved local editor
  changes, fetch its current server representation and atomically replace the
  displayed content, version, active draft baseline, and editor document,
- force Milkdown to consume the refreshed document without requiring browser
  reload, tab switching, or note switching,
- refresh identically after manual, scheduled, inactivity, startup, focus, and
  safe note-switch synchronization triggers,
- never replace editor content when typing or another unsaved local change
  occurred while synchronization was running,
- revalidate the active notebook, selected path, note version, and local-change
  generation before applying an asynchronous reload so a late response cannot
  replace a newly selected note,
- preserve the current optimistic version/conflict workflow whenever local and
  external edits overlap; never silently prefer the remote copy,
- handle external rename/move by updating the relevant open tab and selected
  path where the received-change metadata proves the identity safely,
- handle external deletion with a clear non-destructive state that preserves
  any local draft and lets the user deliberately close or recover it,
- keep unrelated open tabs available; inactive tabs must load the latest server
  content when activated,
- retain the received-changes notice as useful history, but do not require it as
  the mechanism for refreshing a safely reloadable active note.

Completion criteria:

- an externally modified active note updates immediately after successful sync,
- the refreshed version becomes the new save baseline and subsequent edits do
  not cause a false conflict,
- external changes to inactive tabs appear when those tabs are activated,
- edits made before or during synchronization are never discarded,
- update, rename/move, deletion, unrelated-note changes, multi-tab behavior,
  Read only mode, and every automatic sync trigger have focused tests,
- delayed fetch and note-switch race tests prove that stale asynchronous
  responses cannot replace the wrong editor document.

## Milestone 37 - Sticky editor toolbar in mobile/PWA layouts

Keep the Milkdown formatting toolbar available while scrolling through a long
note, including in the installed standalone PWA and narrow mobile layouts.

Current defect:

- the note-level Edit/Read only, Save, History, and Sync header is sticky,
- the Milkdown formatting and contextual toolbars remain ordinary content
  inside the note article and scroll out of view,
- `--editor-toolbar-top` is calculated from the app-header/tab layout but is not
  currently applied to the editor-toolbar container.

Required behavior:

- make the primary editor formatting toolbar sticky below the app header and
  optional note-tab row while a note is open,
- calculate the offset from the actual visible header/tab layout so the toolbar
  never hides underneath or overlaps those controls,
- keep contextual table, image, and internal-link toolbars directly below the
  primary formatting toolbar when their respective content is selected,
- allow toolbar actions to remain horizontally scrollable on narrow screens
  without causing the complete page or editor to scroll sideways,
- preserve the current document scroll position, editor selection, focus, and
  software-keyboard interaction when using a toolbar action,
- account for installed-PWA safe areas and changing mobile visual viewport
  height without adding permanent empty space,
- avoid covering the selected text, document heading, slash-command menu,
  dialog, or contextual controls with stacked sticky elements,
- show the toolbar only where document editing context is meaningful and retain
  all existing Read only disabling rules,
- use the same behavior in browser and standalone display modes rather than
  maintaining a PWA-specific duplicate toolbar.

Completion criteria:

- the formatting toolbar remains visible after scrolling to the bottom of a
  long note in the installed PWA,
- layouts with and without note tabs use the correct sticky offset,
- contextual table and image toolbars remain reachable and do not overlap the
  primary toolbar,
- portrait phone, landscape phone, narrow desktop, standalone PWA, browser,
  and opened software-keyboard layouts have focused responsive tests,
- scrolling, formatting, editor focus, and touch interaction continue working
  without document jumps or hidden controls.

## Milestone 38 - Markdown-aware paste UX

Make pasted Markdown optionally become the corresponding rendered editor
structure instead of always inserting its syntax as ordinary text.

Current behavior and cause:

- Milkdown's CommonMark and GFM parsers are used when a note document is
  loaded, but VariaNotes does not currently pass pasted plain text through that
  Markdown parser,
- ProseMirror therefore preserves plain clipboard text literally; input rules
  that react while typing do not retrospectively parse a pasted block,
- clipboard HTML may retain schema-supported rich structure through
  ProseMirror's normal paste path, while the existing upload plugin separately
  handles pasted image files,
- raw Markdown copied from a source editor normally arrives as `text/plain`
  and is consequently shown as literal `#`, `-`, backtick, and table syntax.

Required behavior:

- support an explicit, discoverable **Paste as Markdown** path on desktop and
  mobile/PWA,
- parse `text/markdown` clipboard content as Markdown when the browser provides
  that unambiguous media type,
- preserve ordinary paste and paste-as-plain-text behavior so text containing
  incidental Markdown characters is not reformatted unexpectedly,
- only consider conservative smart detection for `text/plain` after ambiguous
  examples such as shell commands, source code, and prose punctuation are
  covered by tests; an explicit choice is preferable to destructive guessing,
- support portable CommonMark/GFM structures already understood by the editor,
  including headings, emphasis, lists, task lists, blockquotes, fenced code,
  links, tables, and horizontal rules,
- insert the parsed result at the current selection as one undoable editor
  operation and keep the existing autosave/status behavior,
- retain the current clipboard screenshot/image upload workflow and never
  download or upload arbitrary URLs merely because pasted Markdown references
  an image; preserve external HTTP(S) image references as Markdown and render
  them behind an explicit per-image load action,
- keep HTML handling schema-constrained and do not introduce unsafe raw-HTML
  insertion or `dangerouslySetInnerHTML`,
- keep Read only mode non-mutating and provide touch-accessible behavior that
  does not depend only on a desktop keyboard shortcut,
- define clear behavior for invalid or unsupported Markdown: preserve the
  original clipboard text and explain any explicit conversion failure without
  losing clipboard content.

Completion criteria:

- pasted explicit Markdown can become the expected Milkdown document nodes
  without requiring the user to retype it,
- normal plain-text, rich HTML, image, code, and Read only paste paths retain
  their intended behavior,
- relative links and image references remain portable Markdown and no external
  asset is fetched implicitly; external images load only after an explicit
  user action,
- conversion, selection replacement, undo/redo, autosave serialization,
  mobile/PWA access, and ambiguous clipboard inputs have focused tests.

Status: completed on 2026-09-29. VariaNotes now provides an explicit
touch-accessible Paste as Markdown dialog, handles unambiguous `text/markdown`
clipboard data, preserves ordinary paste paths, inserts conversions as one
undoable transaction, refuses active raw HTML without discarding its source,
and keeps external HTTP(S) image references as Markdown behind a blocked
placeholder until the user explicitly loads each image.

## Milestone 39 - Unambiguous internal-note link trigger

Prevent ordinary square brackets and Markdown task-list syntax from opening or
leaving behind the internal-note suggestion menu.

Current behavior and cause:

- the internal-note trigger currently accepts either `[[` or a single `[`,
  because Milkdown may normalize two typed opening brackets before the existing
  document listener observes them,
- this also treats ordinary bracketed text and task-list input such as
  `- [ ]` or `- [x]` as possible note-link queries,
- the suggestion menu has no independent outside-click, blur, or timeout
  cleanup, so a task-list transformation can leave `No matching notes` visible
  after the original trigger context is gone.

Required behavior:

- activate internal-note suggestions only after an intentional `[[` sequence,
  detected before editor input normalization loses that distinction,
- never open the note suggestions for a single bracket, ordinary bracketed
  prose, Markdown links, or unchecked and checked task-list syntax,
- keep the existing portable result: selecting a note must still serialize to
  an ordinary relative Markdown link rather than proprietary wiki-link syntax,
- close the menu when the trigger becomes invalid, including after `]`, Escape,
  focus loss, an outside interaction, selection movement, block conversion,
  note change, and Read only transition,
- keep keyboard navigation, touch selection, missing-note behavior, editor
  focus, and autosave behavior intact,
- use one implementation in browser and standalone PWA layouts.

Completion criteria:

- typing `[[` opens note suggestions and selecting a result produces the same
  portable relative Markdown link as today,
- typing `[ordinary text]`, `- [ ] Task`, and `- [x] Task` never opens or leaves
  behind the note-suggestion menu,
- empty results cannot remain visible after their trigger context disappears,
- trigger, dismissal, keyboard, pointer/touch, task-list, Read only, and note-
  switching behavior have focused regression tests.

## Milestone 40 - Transient received-changes notification

Keep users informed when synchronization receives external notebook changes
without leaving a permanent informational banner above the editor.

Current behavior and cause:

- successful synchronization stores received-change metadata in one
  `receivedChanges` state value,
- that same value controls both the persistent top-level notification and the
  useful `Recently received changes` list in synchronization details,
- the banner has a manual dismissal action but no automatic lifetime, and
  dismissing it also discards the detail list for the current session.

Required behavior:

- separate received-change history from the transient banner's visibility,
- automatically hide the informational banner after a short, documented
  interval such as 10-15 seconds while retaining the changes in synchronization
  details,
- restart the visibility interval when a later synchronization receives a new
  batch of external changes,
- pause or defer automatic dismissal while the banner or one of its controls is
  hovered, keyboard-focused, or actively touched,
- keep manual dismissal available and make it hide only the banner,
- opening a changed note from the banner must remain available and may dismiss
  the banner without removing the retained detail history,
- clear retained history at the appropriate notebook boundary so changes from
  one notebook are never shown for another,
- never auto-dismiss conflicts, failures, recovery drafts, destructive-action
  confirmations, or any state that requires a user decision,
- retain accessible status text and avoid communicating receipt or dismissal
  through animation or color alone.

Completion criteria:

- a successful sync with external changes shows a concise notification and it
  disappears automatically without a refresh or manual close,
- the same received changes remain available under synchronization details
  after automatic or manual banner dismissal,
- a newer received-change batch replaces or clearly updates the previous
  transient notification and receives a fresh display interval,
- timer cleanup, hover/focus/touch pausing, manual dismissal, changed-note
  opening, notebook switching, and non-dismissible error/conflict states have
  focused browser and PWA regression tests.

## Milestone 41 - Interactive and accessible task-list checkboxes

Fix task-list items so users can mark them complete directly inside the note
editor while retaining ordinary portable GFM Markdown.

Current behavior and cause:

- Milkdown's GFM schema already parses `- [ ]` and `- [x]` into a `checked`
  list-item attribute and serializes that attribute back to Markdown correctly,
- VariaNotes currently draws the visible checkbox only with a CSS `::before`
  pseudo-element,
- that pseudo-element is not a focusable or actionable control and no editor
  pointer, touch, or keyboard handler changes the `checked` attribute,
- the existing Task list toolbar action changes between a normal list item and
  a task item; it does not mark an existing task complete.

Required behavior:

- provide a real, clearly discoverable task-checkbox control for every GFM task
  item in Edit mode,
- toggle unchecked and checked state with mouse, touch, Space, and Enter,
- expose appropriate checkbox semantics and state to assistive technology and
  retain a visible focus indicator,
- use a touch-friendly target without making clicks on the task text itself
  toggle the checkbox unexpectedly,
- update the list item's `checked` attribute through one normal undoable
  ProseMirror transaction so existing autosave and document status behavior
  continue to apply,
- serialize only standard GFM task markers (`[ ]` and `[x]`) with an ordinary
  Markdown bullet marker; do not add custom syntax, application metadata, or
  backend state,
- show checked state clearly in Read only mode while preventing every mutation
  path there,
- preserve cursor placement, text selection, nested task lists, mixed ordinary
  and task lists, list indentation, toolbar actions, and slash commands,
- preserve existing task state across save, reload, external editors, Git
  synchronization, history, and conflict resolution,
- evaluate Milkdown's list-item block component where useful, but do not adopt
  it blindly if it changes ordinary-list behavior or fails VariaNotes's keyboard,
  accessibility, mobile, or Read only requirements,
- use the same implementation in browser and installed PWA layouts.

Completion criteria:

- clicking or tapping an unchecked task changes its GFM marker to `[x]`, and
  toggling it again restores `[ ]`,
- Space and Enter operate a focused task checkbox without moving or corrupting
  the note text,
- each toggle participates in Undo/Redo and the normal autosave state flow,
- regular bullet and numbered lists remain unchanged and their markers do not
  become interactive task controls,
- Read only mode cannot change task state through pointer, touch, or keyboard
  input,
- checked, unchecked, nested, mixed-list, reload, selection, Undo/Redo,
  autosave, desktop, mobile/PWA, and assistive-technology semantics have focused
  regression tests.

Status: completed on 2026-09-29. VariaNotes now renders GFM task items with a
real accessible checkbox control, toggles their standard Markdown state through
undoable editor transactions, retains state after serialization and reload, and
keeps normal and Read only list behavior intact. Focused regression coverage
includes pointer and keyboard activation, text-click isolation, Undo/Redo,
nested and mixed lists, reload, and non-mutating Read only behavior.

## Milestone 42 - Expired browser session cleanup

Keep expired and revoked browser sessions out of the Security settings list and
remove inactive session records during subsequent session writes. A session
that expires after the user returns must not leave a stale entry beside the
newly created session.

Requirements:

- list only sessions that have not been revoked and whose idle and absolute
  expiry times are still in the future,
- prune expired and revoked records opportunistically when a session is written
  so inactive metadata stays bounded without making the read-only listing
  mutate storage,
- preserve the current session row until its upsert is checked, so an expired
  or revoked token cannot be recreated by a stale request,
- retain the existing session revocation and expiry behavior.

Completion criteria:

- expired and revoked sessions no longer appear in Security settings,
- a later session write removes inactive records while preserving the new
  session,
- a stale request cannot resurrect an expired or revoked session,
- focused tests cover listing, cleanup, and non-resurrection behavior.

Status: implementation completed and merged into `main` in PR #79 on
2026-10-06. It is not included in `v0.1.0-alpha.2.security.5`; it is planned
for an Alpha 3 release.

---


# 48. Future Features After Alpha

Potential later features:

- raw Markdown/source mode,
- OIDC authentication,
- provider-specific repository creation,
- GitHub integration,
- GitLab integration,
- Forgejo/Gitea integration,
- custom slash snippets,
- templates,
- optional note metadata/frontmatter,
- configurable editor preferences,
- import helpers,
- export convenience tools,
- WebDAV-like access only if it remains non-invasive.

Every future feature must preserve the core portability principle.

---

# 49. Features Requiring Special Scrutiny

Before implementing any of the following, explicitly verify that they do not compromise plain-file portability:

- custom note properties,
- embedded databases,
- transclusion,
- block references,
- shared attachments,
- encrypted note bodies,
- generated metadata,
- custom Markdown extensions,
- collaborative editing,
- offline editing.

Do not let convenient application features quietly turn the repository into an opaque application database.

---

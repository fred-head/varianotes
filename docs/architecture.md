# VariaNotes architecture

VariaNotes serves a self-hosted, online-only Markdown vault through a browser
and installable PWA. The operator-configured filesystem is canonical. The app
does not keep a device replica, edit queue, Git repository, or in-app content
history. For behavior and deployment, see [README](../README.md).

## Data boundaries

- One vault is one ordinary directory. Markdown notes, folders, and assets
  remain application-independent files.
- The browser reads and writes vault content through authenticated APIs. API
  and asset responses are not cached. Saves use content versions and atomic
  filesystem writes; a stale version returns a conflict while preserving the
  browser draft and current server file for an explicit decision.
- `internal/files/content.go` is the shared boundary for ordinary file reads
  and version-checked atomic replacements. It enforces root confinement, size
  limits, and coordination with repository mutations. `internal/files/protected.go`
  adds policy-aware Markdown operations: protected notes use catalog logical
  paths, ciphertext-backed versions, and the transaction journal; ordinary
  notes retain atomic version checks. Protected responses are published through
  per-session key leases so a completed lock hides late results. Operations not
  yet implemented for encryption, including search, assets, moves, Trash, and
  ZIP export, fail closed for encrypted vaults.
- `internal/encryption/` contains the versioned age and key-envelope primitives
  from M31P03, the authenticated durable catalog from M31P04, the volatile
  per-vault key manager from M31P05, the transaction journal from M31P06, and
  the password and recovery-code envelope storage used by M31P07 and M31P08.
  Catalogs authenticate vault/object identities, logical and physical paths,
  ciphertext-only digests, explicit protection state, and recursive folder
  rules. The transaction journal stages verified ciphertext beside vault files,
  stores its authenticated journal and catalog stage in application metadata,
  and recovers committed changes forward after restart. Recovery needs the
  unlocked vault key and must run before affected content is served. The store
  keeps catalogs in server application metadata outside the vault and rejects
  missing, corrupt, or unauthenticated state. M31P07 coordinates auth password
  changes with the password-wrapped vault keys through a durable auth database
  journal; it never rewrites note or asset bytes. Key envelopes and catalogs
  live beside the notebook registry under `encryption/` application metadata.
  M31P08 adds per-vault recovery-code envelopes and an authenticated owner
  recovery flow. The code is displayed once and is never persisted;
  interrupted, confirmed multi-vault changes roll back before commit or finish
  forward after commit. Password recovery through the code rewraps every vault
  key, replaces the code, updates the login credential, revokes sessions, and
  locks in-memory keys. Operator password recovery without the previous
  password leaves envelopes untouched and marks the encryption password
  separate from later login changes. Disabling authentication checks all
  registered vaults and remains blocked while encryption metadata or
  ciphertext exists, including in Trash. Protected note reads, creates, saves,
  and conflict checks now use the key manager, authenticated catalog, and
  transaction journal. M31P11 adds a prepared conversion path for existing
  Markdown, supported attachments, recursively selected folders, and associated
  Trash; it fails closed on collisions, symlinks, hardlinks, and unsupported
  entries, then commits plaintext removal and ciphertext/catalog installation
  through forward recovery. The application still exposes no user-facing
  unlock or activation path; encryption remains gated until all M31 steps and
  the release review pass.
- Multiple named vaults on one server are registered in application metadata.
  The registry stores IDs, display names, and filesystem paths. It is not a
  copy of note or asset content.
- Authentication/session state is kept separately from vault files. MFA
  encryption material is operator data and must be backed up with auth state.
- Trash is recoverable application state represented by files under the vault.
  Permanent deletion removes the active copy; operator backups follow their
  own retention rules.
- ZIP export streams ordinary vault files with bounded entry count and total
  size. It excludes `.git`, Trash, symlinks, dependency directories, and app
  metadata.

## Code map

| Area | Responsibility | Start here |
| --- | --- | --- |
| `cmd/varianotes/` | Process startup, configuration, and operator auth commands | `main.go` |
| `internal/app/` | HTTP routes, authentication middleware, vault registry and export | `handler.go`, `notebooks.go` |
| `internal/files/` | Confined filesystem operations, Markdown, assets, links, search, Trash, export | `repository.go`, `root.go`, `export.go` |
| `internal/auth/` | Local/disabled mode, credentials, sessions, MFA, proxy identity | `config.go`, `service.go` |
| `frontend/src/` | React app, API client, editor, and browser state | `App.tsx`, `api.ts`, `components/editor/MarkdownEditor.tsx` |
| `web/` | Embeds the built frontend into the Go application | `embed.go` |

## Change tracing

- Note editing crosses `frontend/src/`, `/api/repository/file` in
  `internal/app/handler.go`, and `internal/files/repository.go`.
- Image insertion crosses the editor/API client and `internal/files/assets.go`;
  presentation sizes are separate application metadata.
- Path confinement, atomic writes, Trash, and export safeguards live in
  `internal/files/`; read its scoped `AGENTS.md` before changing those paths.
- Vault creation, adoption, activation, registry, and export routes are in
  `internal/app/handler.go` and `internal/app/notebooks.go`.
- Authentication behavior spans `internal/auth/` and the route boundary in
  `internal/app/handler.go`. See the security guides for session, TLS, and
  trusted-proxy constraints.

The Git-free product direction is described in the
[reboot roadbook](roadmap/git-free-reboot.md). When adopting a vault folder,
VariaNotes ignores any `.git` directory and leaves it untouched; export omits
that metadata.

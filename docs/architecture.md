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
  limits, and coordination with repository mutations. Markdown and asset reads
  and writes, note operations, link rewrites, search, Trash, and ZIP export all
  route through this layer. Later M31 steps will add encrypted storage there.
- `internal/encryption/` contains the versioned age and key-envelope primitives
  from M31P03, the authenticated durable catalog from M31P04, the volatile
  per-vault key manager from M31P05, the transaction journal from M31P06, and
  the password-envelope store used by M31P07.
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
  Startup rolls back a prepared password change or completes a committed one
  before serving requests. Password recovery without the previous password
  leaves envelopes untouched and marks the encryption password separate from
  later login changes. Disabling authentication checks all registered vaults and
  remains blocked while encryption metadata or ciphertext exists, including in
  Trash. The key manager's lock, activity, and lock-event routes do not
  unlock vaults or serve protected content. Key envelopes, catalog, key manager,
  and transaction journal are not wired into vault content operations;
  encryption stays inactive until the M31 release gate passes.
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

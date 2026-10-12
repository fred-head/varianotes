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

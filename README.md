# VariaNotes

VariaNotes is a self-hosted browser application for ordinary Markdown notes.
Your server filesystem is the canonical vault; devices use the same vault
online. VariaNotes keeps notes as plain Markdown and assets as ordinary files,
so they remain useful with other tools.

**Self-hosted Markdown notes with full control over your data.**

*Your notes. Your space.*

> **Alpha software:** use an immutable image version, keep operator-managed
> backups, and review [Known Limitations](KNOWN-LIMITATIONS.md) before storing
> important notes. The operator owns backup, retention, and recovery.

## Features

- CommonMark and GFM editing, document outline, code blocks, task lists, and
  document statistics
- Folders, note links, search, rename and move previews, tabs, and recoverable
  Trash
- Image paste and upload, note-owned assets, cleanup, and non-destructive image
  presentation sizes
- Multiple named vault folders on one server, with create, adopt, and switch
  workflows
- On-demand ZIP export of ordinary vault files
- Online-only note content with version-checked saves and explicit review when
  a browser draft conflicts with the current server file
- Responsive browser and installable PWA interface with local theme and layout
  preferences
- Fail-closed, single-owner password authentication with optional TOTP MFA,
  session controls, and trusted reverse-proxy configuration
- Hardened multi-architecture Docker images for `linux/amd64` and `linux/arm64`

## Install with Docker Compose

The source and deployment metadata are prepared for the independent repository
[`fred-head/varianotes`](https://github.com/fred-head/varianotes) and image
`ghcr.io/fred-head/varianotes`. The external repository and GHCR package must be
created and configured separately before the first published release. See the
[new repository setup guide](docs/development/new-repository-setup.md).

Clone the source and build a fresh local image:

```sh
git clone https://github.com/fred-head/varianotes.git
cd varianotes
docker compose up --build -d
```

Compose uses the `varianotes` project and a `varianotes-data` named volume
mounted at `/data`.
The container runs as the unprivileged `varianotes` user, with a read-only root
filesystem, dropped Linux capabilities, `no-new-privileges`, and a bounded
temporary filesystem. Do not use an authentication database from another
application build; clean installations initialize new authentication state.

For localhost-only HTTP testing, set `VARIANOTES_SESSION_COOKIE_SECURE=false`
in `.env`. Keep it `true` behind HTTPS. The default host bind is
`127.0.0.1:8080`; put an Internet-facing deployment behind a trusted TLS reverse
proxy and never expose the backend port directly to the public Internet.

Create the one-time account setup token in a trusted terminal, then enter it on
the setup screen with the owner password:

```sh
docker compose exec varianotes varianotes auth bootstrap-token
```

The token is shown once and expires after 15 minutes. Passwords require at
least 15 characters. Security settings let the owner change the password,
configure session lifetimes, inspect sessions, enable TOTP MFA, and revoke
other devices.

Create a vault or adopt an existing folder beneath `VARIANOTES_NOTEBOOKS_DIR`.
Adoption opens the ordinary Markdown files and assets in place. The application
does not use Git or SSH. Devices access the same server vault; VariaNotes does
not keep a local note replica, queue offline edits, or cache note API responses.

After the first release is published, deployments can replace `build: .` and
the local image tag in `compose.yaml` with an immutable tag such as
`ghcr.io/fred-head/varianotes:<release-version>`. Alpha releases do not publish
a `latest` tag.

## Configuration and storage

Compose persists the complete application data directory at `/data`. Back it
up as a unit, including all vaults, the vault registry, authentication database,
and MFA encryption key. The ZIP export is a portable copy initiated by the
owner; it is not a backup and excludes application/authentication metadata and
Trash.

| Variable | Purpose and default |
| --- | --- |
| `VARIANOTES_AUTH_MODE` | `local` by default; `disabled` removes built-in authentication. |
| `VARIANOTES_AUTH_METADATA` | Authentication database, normally `/data/app/auth.db`. |
| `VARIANOTES_AUTH_ENCRYPTION_KEY_FILE` | MFA key path, normally `/data/app/auth.key`. |
| `VARIANOTES_SESSION_COOKIE_SECURE` | Keep `true` behind HTTPS; use `false` only for localhost HTTP testing. |
| `VARIANOTES_TRUSTED_PROXIES` | Exact proxy IPs/CIDRs allowed to supply forwarding headers. |
| `VARIANOTES_TRUSTED_ORIGINS` | Exact public origins only when proxy rewriting requires them. |
| `VARIANOTES_NOTEBOOKS_DIR` | Vault storage root, normally `/data/notebooks`. |
| `VARIANOTES_NOTEBOOK_METADATA` | Vault registry, normally `/data/app/notebooks.json`. |
| `VARIANOTES_ADDR` | Internal HTTP listen address, normally `:8080`. |
| `VARIANOTES_PUBLISH_ADDR` | Compose host bind address, normally `127.0.0.1`. |
| `VARIANOTES_DEV_VAULT_PATH` | Development-only initial vault path used by `scripts/dev.sh`. |

Only the `VARIANOTES_*` environment namespace is supported. Each setting has a
single value and no legacy fallback. Persistent application data uses the
generic `/data` mount point; the default Compose volume is `varianotes-data`.

For the supported operator password recovery command:

```sh
docker compose exec -it varianotes varianotes auth reset-password
```

Reset MFA separately when required:

```sh
docker compose exec varianotes varianotes auth reset-mfa
```

These commands update authentication metadata only; they do not alter vault
files. Authentication databases from other application builds are not migrated
and must not be reused.

## Vault files, export, and deletion

A vault is a normal directory of notes, folders, and assets. Uploaded images
use relative Markdown links such as
`![](Network.assets/diagram.png)`. Files remain usable with Obsidian, VS Code,
and other Markdown applications. Image presentation sizes are application
metadata; they do not resize or duplicate the original asset.

Use **Manage Vaults → Export ZIP** to download a selected vault. Export
preserves relative paths and file bytes. It excludes `.git` internals, Trash,
symlinks, dependency directories, and application/authentication data. Archive
import is not supported; place a vault folder beneath the configured root and
adopt it in the app.

Moving an item to Trash is recoverable in the app. Emptying Trash or permanent
deletion removes the item from the active vault, but operator backups may keep
older copies. Unused-asset cleanup is a separate explicit operation.

When two browser sessions edit the same note, the server rejects a stale save.
The open browser draft and current server version remain available for an
explicit choice or a manually edited combined version. VariaNotes does not
create an in-app version history or retain a hidden save archive.

## Security

VariaNotes provides single-owner local authentication by default, optional TOTP
MFA, session expiry and revocation, and trusted-proxy controls. It does not
provide multiple users or roles, and it does not terminate TLS. See the
[reverse-proxy guide](docs/security/reverse-proxy.md) and
[local-authentication guide](docs/security/local-authentication-setup.md).
P17's fresh security review is complete. M31 encrypted-vault activation remains
disabled until the separate P18 release gate passes. See the
[encrypted-vault operations and recovery guide](docs/security/m31-operations.md)
for backup handling, standalone recovery, and the release conditions.
Report security issues through the private reporting instructions in
[`SECURITY.md`](SECURITY.md).

## Development

Read [development and testing](docs/development/testing.md) before changing
code. Start the development server with:

```sh
./scripts/dev.sh
```

The primary Go entry point is `cmd/varianotes`; its binary and CLI command are
both `varianotes`. The Go module is `github.com/fred-head/varianotes` and the
frontend package is `varianotes-frontend`.

## Architecture and operating guides

- [Architecture](docs/architecture.md)
- [Known limitations](KNOWN-LIMITATIONS.md)
- [Authentication architecture](docs/security/authentication-architecture.md)
- [Reverse proxy and TLS](docs/security/reverse-proxy.md)
- [Alpha release and recovery](ALPHA-RELEASE.md)
- [Security maintenance](SECURITY-MAINTENANCE.md)
- [Changelog](CHANGELOG.md)

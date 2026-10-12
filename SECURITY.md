# VariaNotes Security Policy

## Supported versions

VariaNotes is currently alpha software. Only the most recent published alpha is
eligible for security fixes. Older alpha builds are unsupported and should be
upgraded rather than exposed for continued use.

## Alpha security model

The current alpha provides fail-closed single-owner local authentication with
optional TOTP MFA. It has no registration, usernames, roles, email recovery,
multi-user authorization, or note encryption. `VARIANOTES_AUTH_MODE=disabled`
deliberately removes this boundary and is safe only when the operator
constrains access by a private network, VPN, or external protection.

Authentication does not replace transport security. HTTPS termination and
restricted backend reachability remain required for Internet-facing instances.
OIDC is deferred and is not an implemented Alpha 2 authentication mode.

The supplied Compose configuration publishes port 8080 on `127.0.0.1` by default. Set `VARIANOTES_PUBLISH_ADDR` only when deliberate network exposure is protected separately.

## Security boundaries

- Markdown files and assets remain inside the configured vault directory.
- Symlinks, traversal paths, absolute paths, `.git`, Trash, and dependency
  directories are rejected or excluded from note operations and export.
- Local-auth sessions use server-side state, confined secure cookies, and a
  session-bound CSRF token. Mutations also enforce exact request origins.
- Forwarded IP and scheme headers are ignored unless the direct peer matches an
  explicitly configured `VARIANOTES_TRUSTED_PROXIES` address or CIDR.
- Vault onboarding creates ordinary directories or adopts direct child folders
  under the operator-configured vault root. A legacy `.git` directory is left
  untouched and is never read by the app.
- Version-checked saves reject stale browser edits. The owner must explicitly
  choose how to resolve a stale save, and the browser draft remains available
  in the open page until that decision.
- Local password and MFA recovery revoke sessions and change only authentication
  metadata. They never rewrite, encrypt, or delete notebook content.
- The PWA caches the application shell, never API responses or note contents.
  Vault files are online-only; there is no offline replica or edit queue.
- Unsaved note content is not persisted to browser storage. Older versions'
  browser drafts are cleared when the app starts or authentication ends.

## Repository security controls

The public source repository uses layered controls for proposed changes:

- GitHub Actions may run only GitHub-owned actions and the explicitly allowed
  Docker, Aqua Security, and Gitleaks actions. Every action reference must use a
  full commit SHA.
- Pull requests targeting `main` must pass the backend, frontend, repository
  secret, and hardened container gates against the current branch state.
- CodeQL default setup scans Go, JavaScript/TypeScript, and GitHub Actions on
  pull requests, protected-branch changes, and its weekly schedule. The Extended
  query suite is enabled, and new High or Critical security findings block
  merging.
- Secret scanning, push protection, Dependabot alerts, and Dependabot security
  update proposals are enabled. Dependency pull requests are never merged or
  deployed automatically.
- Force-pushes and deletion of `main` are blocked. Review conversations must be
  resolved before merging, and the ruleset has no bypass actor.

Scanner findings are triaged with their data flow and existing validation in
view. Alerts are not bulk-suppressed merely to make a check pass; any dismissal
must retain a specific technical justification in GitHub's audit trail.

The complete dependency-review, exception, remediation, credential-rotation,
and coordinated-disclosure process is documented in
[SECURITY-MAINTENANCE.md](SECURITY-MAINTENANCE.md).

## Operator responsibilities

- Protect and back up the complete `/data` volume. It contains vaults, the vault
  registry, authentication state, and the separate MFA encryption key.
- Terminate TLS at the reverse proxy, keep the backend unreachable from
  untrusted networks, and configure only the actual proxy addresses as trusted.
- Keep the container image, reverse proxy, and host OS patched.
- Backups and their retention are the operator's responsibility. ZIP export is
  a portable copy and does not remove older copies from backups.
- On upgrade, old `.git` directories and managed SSH key files may remain in
  the persistent volume. VariaNotes no longer reads or removes them.
- Do not mount arbitrary host directories or a Docker socket into the container.
- Prefer note-owned local assets when opening a notebook from an untrusted
  source. Ordinary Markdown can contain external image URLs that contact their
  server when rendered.

## Reporting a vulnerability

Do not open a public issue containing credentials, private repository details,
note contents, or an exploitable proof of concept. Use
[GitHub private vulnerability reporting](https://github.com/fred-head/varianotes/security/advisories/new)
and include the affected version, impact, reproduction conditions, and suggested
mitigation if known. Remove real credentials and private note contents from the
report whenever they are not essential to reproduction.

If private vulnerability reporting is temporarily unavailable, do not publish
the report as an issue. Wait for the private channel to be restored or contact
the maintainer through a separately verified private channel.

This is alpha software. Security controls reduce known risk but are not a guarantee that defects do not exist.

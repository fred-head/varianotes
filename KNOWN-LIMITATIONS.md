# Known Alpha Limitations

VariaNotes is single-owner evaluation software. Review these limitations before
using it with important notes.

## Deployment and access

- There is no multi-user authorization, roles, or collaborative editing. The
  owner account has access to every registered vault.
- VariaNotes does not terminate TLS. Internet-facing deployments require an
  HTTPS reverse proxy and carefully scoped trusted-proxy configuration.
- OIDC and phishing-resistant authentication are not implemented. TOTP MFA is
  optional and does not protect a session that has already been stolen.
- The supported ownership model assumes one active backend writer per vault.
- Authentication metadata and the separate MFA encryption key are stored in
  the persistent data volume. Back them up together with all vault files.

## Online-only editing and recovery

- The app shell can open without a network connection, but note content is
  online-only. The PWA does not keep note or asset API responses, queue edits,
  or synchronize local device copies.
- If connectivity fails while a note is open, that editor remains in memory
  until it is closed or the page is reloaded. The app does not report unsaved
  text as saved. Closing the page can lose that draft.
- Concurrent edits to the same note are not merged automatically. Version
  checks reject stale writes and the owner must select the browser draft, the
  current server file, or an edited combined version.
- There is no Git history or in-app version archive. Operators own backup
  schedule, retention, off-site storage, and restore testing. An export is a
  portable copy, not an automatic backup.
- App-level permanent deletion does not erase copies held by operator backups.
- External tools can change vault files. VariaNotes does not poll the filesystem
  for external changes; a stale save is rejected when its version check detects
  the change.

## Portability and compatibility

- Notes are ordinary CommonMark/GFM Markdown, but rendered details may differ
  among Markdown applications.
- Uploaded image ownership follows the `<note>.assets/` convention. Assets
  outside this convention are not managed as note-owned uploads.
- Symbolic links are rejected for operations that could escape the vault and
  skipped during export.
- Export creates a bounded ZIP of ordinary vault files. It excludes `.git`,
  Trash, symlinks, dependency directories, and application metadata. Importing
  archives is not implemented; adopt a folder placed under the configured
  vault root.
- A legacy `.git` folder retained during upgrade remains on disk but is not
  read or modified by the app. Its old commits may contain deleted notes or
  secrets.
- Standard Markdown images can reference external HTTP(S) servers. Loading one
  can disclose the client's IP address to that server. VariaNotes suppresses
  the HTTP referrer but does not proxy the image.
- Image presentation sizes are VariaNotes metadata and do not appear in other
  Markdown applications. Restoring a trashed note resets those sizes.

## Release expectations

- Only the newest alpha is expected to receive fixes.
- Alpha configuration and UI behavior may change between prereleases.
- Test upgrades with a full backup or disposable copy before replacing an
  important installation.

Security issues must be reported privately as described in
[SECURITY.md](SECURITY.md).

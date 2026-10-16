# VariaNotes alpha release and recovery guide

This guide covers clean VariaNotes installations and releases. CI and the
release workflow are publication gates; do not promote an image that has not
passed the exact-digest and security checks.

## New repository setup

VariaNotes is prepared for the independent repository
[`fred-head/varianotes`](https://github.com/fred-head/varianotes). Before its
first release, an operator must create that repository, transfer the source
tree with a fresh initial commit, and configure the GitHub and GHCR settings in
the [new repository setup guide](docs/development/new-repository-setup.md).
Do not transfer prior Git history, branches, pull requests, or release tags.
This source preparation does not create the repository, publish an image, or
make a release.

## Clean installation

1. Use the independent VariaNotes source repository or a published immutable
   VariaNotes image.
2. Start the Compose service with a new `varianotes-data` volume mounted at
   `/data`. The container runs as the unprivileged `varianotes` account and
   keeps its root filesystem read-only.
3. Create the initial owner with the one-time token from
   `docker compose exec varianotes varianotes auth bootstrap-token`.
4. Create a vault or adopt a plain Markdown folder beneath the configured vault
   root. Confirm notes and assets open and save correctly.
5. Configure HTTPS at a trusted reverse proxy before exposing the service to
   the Internet. Keep `VARIANOTES_SESSION_COOKIE_SECURE=true`.

VariaNotes expects fresh authentication metadata and VariaNotes browser
identifiers. It does not migrate authentication databases, sessions, MFA
secrets, recovery codes, or browser state from another application build.
Ordinary Markdown files can still be opened as a vault by placing a folder
beneath the configured root and adopting it.

## Persistent data and recovery

Back up the complete `/data` volume, including all vault directories,
`/data/app/notebooks.json`, `/data/app/auth.db`, and `/data/app/auth.key` when
present. The encryption key and authentication database are a pair. Operators
choose backup schedule, retention, off-site storage, and restore testing.
VariaNotes's ZIP export is an owner-triggered portable vault copy, not a backup
service.

Password and MFA recovery commands change only authentication metadata:

```sh
docker compose exec -it varianotes varianotes auth reset-password
docker compose exec varianotes varianotes auth reset-mfa
```

Restore the matching full `/data` backup before rolling back to a build that
expects different authentication metadata. A rollback is not a data migration.
Markdown vaults remain ordinary files, but VariaNotes does not provide an
in-app version history or remote backup.

For M31 encrypted vault data, also follow the
[encrypted-vault operations and recovery guide](docs/security/m31-operations.md).
The current build keeps encryption activation disabled. Do not promote a
candidate until P01–P18 are complete. P17's fresh security review is complete;
P18's release gate remains open. Record the review and all required checks for
the exact candidate commit.

## Release verification

For each exact candidate image, verify:

- first-run owner setup, sign-in, MFA enrollment and verification, session
  expiry, and password/MFA recovery;
- single-use recovery-code verification and rejection when a code is reused;
- vault creation, existing-folder adoption, activation, rename, removal, and
  ZIP export;
- creating, opening, editing, and saving Markdown notes and ordinary assets;
- stale-save behavior preserves both the browser draft and current server file
  until the owner chooses a result;
- Trash restore and permanent deletion, including operator backup semantics;
- the PWA caches only the application shell and never note or authentication
  responses;
- the image operates without Git, OpenSSH, remotes, or SSH credentials;
- the image remains non-root, read-only at the container root, and persistent
  through the documented `/data` volume.

For an M31 candidate, additionally record the complete P01–P18 checklist and
P17 review result for the exact commit. Recover a complete disposable backup
outside VariaNotes, compare protected notes, binary assets, and ordinary files
byte-for-byte, verify activation remains unavailable until the full gate passes,
and exercise both new-vault and adopted-vault flows. Keep production activation
disabled until all of these checks and their required technical gates pass.

Verify the OCI digest, SBOM, and release attestation. Update release notes,
known limitations, and the changelog before publication. Never move an
immutable version tag; promote the Alpha channel only after all gates pass.

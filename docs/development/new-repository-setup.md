# New VariaNotes repository setup

The source tree is prepared for the independent
[`fred-head/varianotes`](https://github.com/fred-head/varianotes) repository.
Creating the repository, configuring its settings, transferring the source, and
publishing the first release are separate operator actions.

## Create a clean source repository

1. Create an empty GitHub repository named `varianotes` under `fred-head`.
   Do not initialize it with a README, license, or template.
2. Transfer the reviewed VariaNotes source tree and create a new initial commit
   on `main`. Do not copy `.git`, old release tags, branches, pull requests, or
   historical refs from the development workspace.
3. Push the initial commit and verify that pull-request CI and all security
   scans run in the new repository before enabling required checks.

The source tree's Go module, active links, OCI labels, release workflow, and
container image already target `github.com/fred-head/varianotes` and
`ghcr.io/fred-head/varianotes`.

## GitHub settings

- Enable GitHub Actions for the repository and allow the pinned actions used by
  the workflows. Set the default `GITHUB_TOKEN` permission to read-only; release
  jobs declare the additional permissions they need.
- Protect `main` with pull requests, prohibit force pushes and branch deletion,
  and require the passing checks **Backend and Go security**, **Frontend and
  npm security**, **Repository secret scan**, and **Hardened container release
  gate**. Preserve these check names as branch-protection requirements.
- Enable CodeQL analysis for Go, JavaScript/TypeScript, and GitHub Actions using
  the repository's Code Security settings. Keep Dependabot alerts and security
  updates enabled. Enable secret scanning and push protection where available.
- Enable private vulnerability reporting and confirm the security policy link
  resolves in the new repository.

The workflows use the repository-provided `GITHUB_TOKEN`; no personal access
token or release secret is required. The release jobs request `contents:write`
to create a GitHub release, `packages:write` to publish GHCR images, and
`id-token:write` plus `attestations:write` to create provenance attestations.
The scheduled image scan requests `packages:read`.

## GHCR setup

The first successful release workflow publishes
`ghcr.io/fred-head/varianotes`. Confirm the package is associated with the new
repository and that Actions has permission to write it. If the package is
intended for public use, set its visibility to public after publication. Do not
move or delete other registry packages as part of this source transfer.

After the first immutable Alpha image is published, set the repository Actions
variable `VARIANOTES_LATEST_ALPHA_VERSION` to its immutable version tag. The
scheduled workflow uses that value to rescan the newest immutable image; it
also scans the moving Alpha channel. Leaving the variable unset skips the
immutable-image scan until an image exists.

## Before the first release

Confirm the new repository has a green full CI run, the required branch rules
are active, CodeQL and secret scanning report successfully, GHCR permissions
work, and the release workflow can create an attestation. Start with a newly
chosen release tag after the source transfer; do not import historical release
tags. Follow [ALPHA-RELEASE.md](../../ALPHA-RELEASE.md) for image and runtime
verification.

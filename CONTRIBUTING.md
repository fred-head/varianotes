# Contributing to VariaNotes

VariaNotes is in alpha. Focused bug reports, documentation improvements, tests,
and small changes consistent with the project's portability and data-safety
principles are welcome.

## Before opening an issue

- Search existing issues first.
- Include the VariaNotes version, deployment method, browser, relevant sanitized
  logs, expected behavior, and actual behavior.
- Remove note contents, private repository URLs, SSH material, credentials, host
  details, and other sensitive data.
- Report vulnerabilities privately through the process in
  [SECURITY.md](SECURITY.md), never as a public issue.

## Before opening a pull request

1. Read [AGENTS.md](AGENTS.md) for repository invariants and follow only the
   task-specific references it points to.
2. Keep the change focused and avoid unrelated formatting or dependency churn.
3. Add or update focused tests when behavior changes, especially for
   destructive, data-safety, path-security, Git, authentication, or editor
   serialization behavior.
4. Update `CHANGELOG.md` under `Unreleased` for user-visible changes.
5. Choose local validation using the
   [development and testing guide](docs/development/testing.md). Run focused
   checks first; CI remains the broad merge gate. Dependency changes and
   releases follow the full-gate procedures in
   [SECURITY-MAINTENANCE.md](SECURITY-MAINTENANCE.md) and
   [ALPHA-RELEASE.md](ALPHA-RELEASE.md).

6. Explain the user-facing outcome, safety implications, and verification in the
   pull-request description.

Dependency pull requests additionally follow
[SECURITY-MAINTENANCE.md](SECURITY-MAINTENANCE.md). Automated updates are
untrusted changes: do not enable auto-merge, combine unrelated major upgrades,
or accept a production/security-sensitive update based only on a green check.
Review its release notes, advisory context, transitive changes, lockfile, and
focused runtime behavior before merging.

Do not include generated runtime data, test notebooks, private images, keys,
tokens, or real repository configuration. Unless explicitly stated otherwise,
contributions intentionally submitted for inclusion are accepted under the
repository's [Apache License 2.0](LICENSE), consistent with section 5 of that
license.

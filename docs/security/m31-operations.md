# M31 encrypted-vault operations and recovery

This guide covers backup handling, recovery, and the release gate for M31
encrypted vaults. P17's fresh AI security review is complete and PR #24 is
merged. P18 remains open, so encryption activation is not available in the
current build. Do not attempt to activate encryption through an undocumented
route, command, or configuration value. The bounded AI review is not an expert
audit or general security certification.

## Before using encryption

Wait for a release that explicitly says the M31 gate has passed. Before using
that release with important notes:

1. Keep a tested, complete backup of the entire `/data` volume. Stop the
   VariaNotes service while making a filesystem copy, or use a storage snapshot
   that captures the volume consistently.
2. Store the encryption password and the current recovery code outside the
   VariaNotes volume and separately from its backups.
3. Confirm that the recovery code belongs to the backup you are testing. A code
   replacement affects new backups; older backups need the code that was
   current when each backup was made.
4. Test recovery on an isolated machine and destination before relying on the
   backup for production recovery.

A normal VariaNotes backup includes all of `/data`, especially the vaults under
`/data/notebooks`, `/data/app/notebooks.json`, `/data/app/auth.db`,
`/data/app/auth.key` when present, and `/data/app/encryption/`. The vault
registry and encryption metadata are stored outside the note directories. A
vault-only copy is not enough to restore it through VariaNotes. Never restore a
backup over a live vault; stop the service and restore the matching application
metadata and vault files together.

Operators choose backup schedule, retention, off-site storage, and restore
testing. The app's ZIP export is not a backup: it omits application metadata
and Trash.

## Passwords and recovery codes

The password in `key-envelope.json` is the vault encryption password. It may
match the VariaNotes sign-in password or be separate. If an operator resets the
sign-in password without the previous password, the vault encryption password
is left unchanged and becomes separate. Use the actual vault encryption
password for standalone recovery; do not assume the current sign-in password
will unlock an older backup.

The one-time recovery code is for recovery through VariaNotes using the
matching restored application metadata and vault. The standalone recovery
utility below does not accept that code: it needs the vault encryption password
to open the age-wrapped vault key. Keep both secrets separately. If a recovery
code is replaced, keep the previous code for backups made before replacement.

Changing a vault password rewraps keys in the current registered vaults. A
backup made before that change still has its earlier key envelope, so use the
password that matched that backup. Password recovery with the recovery code
updates the envelopes and rotates the code; it does not rewrite note or asset
content.

## Standalone recovery from a complete backup

The source tree contains the `varianotes-external-restore` command. It does not
import VariaNotes packages or contact a VariaNotes service. It checks the
authenticated catalog and each protected file's ciphertext digest, then uses
the official `age` CLI to decrypt protected files. It copies ordinary files
unchanged. It requires Go 1.26 and the official `age` executable on `PATH`;
prepare the source and Go module dependencies on a trusted machine. The command
is not included in the current production image.

Work from an isolated copy of the complete backup. Find the vault ID and path
in the matching `app/notebooks.json`:

```sh
jq -r '.entries[] | [.id, .name, .local_path] | @tsv' /backup/app/notebooks.json
```

The encryption metadata directory is named with the lowercase hexadecimal
SHA-256 digest of that vault ID. For example, calculate it with:

```sh
printf '%s' 'VAULT_ID_FROM_REGISTRY' | sha256sum
```

Run the recovery command from the VariaNotes source root. Use the registered
vault path from the backup and the matching metadata directory. Choose a new
destination outside the backup tree; its parent must already exist and the
destination itself must not exist.

```sh
go run ./cmd/varianotes-external-restore \
  --vault /backup/notebooks/VAULT_DIRECTORY \
  --encryption-metadata /backup/app/encryption/SHA256_VAULT_ID \
  --output /recovery/recovered-vault
```

The program prompts for the vault encryption password without displaying it.
Do not put passwords or recovery codes in command arguments, shell history, or
environment variables. If the command returns an error, it removes its partial
output. A forced stop or host failure can leave a partial directory; inspect
the destination before retrying and use a new empty path. Keep the backup
unchanged while resolving errors. The destination is created with owner-only
permissions. The program creates
short-lived per-file age identity files in the system temporary directory and
removes them on exit; deletion does not promise physical erasure. Use a trusted
machine with a private temporary directory.

For a recovery drill, create a disposable vault with at least one protected
Markdown note, one binary attachment, and one ordinary Markdown file. Preserve
the original bytes and make a matching complete backup of the vault and
application metadata. Recover into a new directory, then compare each restored
file with its original bytes (for example, with `cmp`). Confirm the restored
tree contains ordinary files and no `.age` ciphertext. Repeat with the
matching full backup and recovery code in VariaNotes if testing in-app
recovery. The recovery code is not used by the standalone utility.

The standalone program authenticates protected ciphertext and the catalog. It
does not prove that the operator's backup contains every expected file, and it
cannot authenticate ordinary plaintext files against earlier versions. Keep
operator-generated backup checksums and compare expected content during restore
drills. Do not use this utility to write into or replace a live VariaNotes
vault.

## What remains visible and where plaintext exists

Encryption protects file contents, not all information about a vault. The
filesystem still reveals directory structure, file names, sizes, timestamps,
and which protected paths have `.age` ciphertext. The encryption catalog in
`/data/app/encryption/` records logical and physical paths, object identities,
protection state, folder rules, and ciphertext digests. Vault registration
metadata records vault names and server paths. These values are not note or
asset contents, but they may be sensitive and belong in the protected backup.

While a vault is unlocked, the server process and browser hold keys and
plaintext in memory. Search results, open notes, and unsaved drafts may also be
present in browser memory. Locking hides protected views and rejects later
protected requests; it does not promise physical RAM erasure. Closing or
reloading a tab can discard an unsaved draft. VariaNotes does not claim to
remove plaintext from swap, crash dumps, host snapshots, or copies made by
other applications.

Successful conversion removes the selected plaintext originals from the
active vault after its durable commit. It cannot erase earlier operator
backups, filesystem snapshots, copies synced to other services, or files
previously exported. Protected ZIP exports are plaintext by design. They are
streamed without a server-side ZIP staging file, but the downloaded archive may
remain on the device, in browser-managed downloads, or in a synced folder.
Protect and retain those copies using the same policy as other plaintext
documents.

## M31 release gate

Keep activation disabled until the P18 release gate passes. P17 is complete
following its fresh review under the
[Alpha 3 sign-off rule](m31-p17-ai-review-signoff.md); P18 remains open pending
its release evidence. Record P01–P18 and the review outcome against the exact
candidate commit. Any change after a reviewed commit requires the applicable
fresh review and technical checks on the changed commit.

Before marking P18 complete, the release record must include:

- the P01–P18 checklist and P17 review outcome for the exact commit;
- successful external recovery of a complete test backup with byte-for-byte
  checks for protected notes, binary assets, and ordinary files;
- evidence that no activation path succeeds before the full gate passes;
- successful new-vault and adopted-vault flows on the gated candidate, with
  recovery and backup behavior recorded; and
- the documented limitations above, including metadata visibility, memory,
  earlier plaintext copies, and plaintext ZIP exports.

The gate is not passed on this branch. The new/adopted-vault release flows
remain to be recorded on a candidate eligible to pass the gate. Keep
production activation disabled until those conditions are met.

# VariaNotes single-owner authentication architecture

This document describes the current single-owner authentication boundary.
Password setup, persistent sessions, CSRF/throttling, browser/PWA
reauthentication, password/session administration, optional TOTP, and explicit
disabled-mode configuration are implemented. The completed security review and
residual-risk record lives in [m19-security-review.md](m19-security-review.md).

## Decision and scope

VariaNotes owns one authentication boundary for one fixed internal principal,
`owner`. It does not provide registration, usernames, multiple users, roles,
email addresses, invitations, organizations, or email recovery.

Supported modes are:

- `local`: recommended and fail-closed default. A new database has
  `setup_completed = false` until operator-authorized bootstrap setup.
- `disabled`: accepted only when the operator explicitly sets
  `VARIANOTES_AUTH_MODE=disabled`. The application shows a persistent warning
  and invalidates local authentication artifacts when modes change.

An absent or empty mode never means public access. It resolves to `local`, and
a new database starts in the restricted setup-required state. An unreadable,
corrupt, unsupported, or newer database stops startup instead of falling back
to unauthenticated operation.

## Trust boundaries and threat model

```text
untrusted browser/PWA and network
              |
              v
trusted TLS reverse proxy (optional forwarded headers only from configured IPs)
              |
              v
VariaNotes HTTP authentication boundary
              |
       +------+------------------+
       |                         |
       v                         v
authentication metadata      vault filesystem APIs
/data/app/auth.db             /data/notebooks
```

The design protects vault, maintenance, and management APIs from unauthenticated
network access. It assumes an attacker can send arbitrary HTTP requests, spoof
forwarding headers, steal or replay submitted values, attempt brute force and
setup takeover, trigger restarts, and cause interrupted writes. It does not
protect a host administrator or an attacker with access to the persistent
volume, process memory, container runtime, or TLS private keys.

HTTPS remains mandatory for Internet-facing deployments. Forwarded client IP
and scheme information is untrusted unless the direct peer belongs to an
explicitly configured trusted-proxy allowlist.

## HTTP surface

The deny-by-default HTTP boundary classifies routes as follows:

Public minimum:

- `GET /api/health`, containing liveness and application version only,
- static application-shell files required to render setup/login,
- narrowly scoped setup, login, MFA challenge, password-recovery prepare and
  confirm, and auth-status routes; password recovery requires the separate
  high-entropy code, applies same-origin checks, and exposes no vault,
  filesystem, or host metadata.

Protected:

- every other `/api/` route, including vaults, notes, tree, files, assets,
  search, settings, cleanup, and maintenance.

GET and HEAD must not mutate authentication or application state. Missing or
expired API authentication returns stable JSON `401`, never an HTML redirect.

## Persistent metadata boundary

The `VARIANOTES_*` namespace is the only supported application environment
namespace. Authentication settings have no alternate prefix or fallback.
Database and key paths are operator-configured and are not inferred from
product-specific directory names.

`VARIANOTES_AUTH_METADATA` defaults to `/data/app/auth.db` or to `auth.db` beside
the configured notebook registry. The directory and database use modes `0700`
and `0600`. SQLite runs in the Go process without an external database service.

The database is limited to:

- the fixed owner authentication configuration,
- hashed opaque session identifiers and session metadata,
- hashed one-time setup/recovery artifacts,
- hashed throttle keys and counters,
- security events that contain neither credentials nor note content,
- an interrupted password-change journal containing only encrypted old/new
  vault-key envelopes and a transaction phase,
- an interrupted encryption-recovery journal containing encrypted old/new
  vault-key envelopes, a derived password credential when required, and a
  transaction phase,
- versioned migration records.

It must never contain Markdown, asset bytes, vault trees, external credentials,
password plaintext, session plaintext, TOTP codes, or recovery-code plaintext.
Deleting it can remove VariaNotes access configuration but cannot make ordinary
Markdown vaults unreadable.

## Migration and recovery model

Schema changes are ordered, recorded, transactional, and restart-safe. A
future destructive migration must checkpoint SQLite and create a private,
exclusive backup before its transaction begins. A failed migration rolls back
and prevents startup. A database created by a newer VariaNotes version is never
downgraded implicitly.

M31P07 prepares password rewraps for every registered vault before updating the
login credential. The auth database records the envelope plan and a `prepared`
phase; the credential update and `committed` phase share one SQLite transaction.
Startup rolls back a prepared plan or completes a committed plan before serving
requests. If recovery cannot reach a registered vault, startup fails closed.
Initialization (including `auth reset-mfa`) and credential/envelope transitions
share a cross-process ownership lock in `auth.db.journal-lock` beside the auth
database (using its resolved path). This empty SQLite database holds a
`BEGIN IMMEDIATE` transaction across preflight, journal writes, envelope work,
and completion or rollback. It is separate from the auth database so the
owner can continue writing its durable journal. Other initializations wait;
canceled waiters and lock errors abort before recovery changes any journal.
Canceling the owner request retains ownership until its cleanup returns.
Closing the connection or process death releases ownership automatically;
startup can then recover a prepared or committed transaction. Failed recovery
keeps the journal for retry, and operator/code-based resets reject an unfinished
password-change journal. Never delete or replace the coordination file while
any process may use it. SQLite database hard-link aliases and filesystems that
do not provide SQLite locking are unsupported; this coordination does not
extend the one-active-backend-writer-per-vault contract. See the focused
[C1 regression evidence](m31-c1-remediation.md).

Password recovery without the previous password changes the login credential
only and leaves vault envelopes untouched. The envelope rewrap primitive takes
an explicit encryption password, so a later separate encryption password does
not depend on local login. Password recovery without the prior password records
that separation, so future login-password changes leave the existing envelopes
alone. OIDC identity tokens are never encryption keys.

M31P08 creates a 256-bit recovery code and shows it once. The code itself is
never written to disk; each protected vault stores only a password-encrypted
vault-key envelope made with that code. The displayed form starts with
`VNRC1-` and groups uppercase hexadecimal characters for copying. The owner
must confirm the displayed code before those envelopes become active.
Confirmation verifies the code and
the authenticated catalog for every protected vault. A pending setup challenge
is kept only in process memory, so a restart before confirmation leaves the
previous recovery state unchanged. The confirmation challenge expires after
10 minutes.

Password recovery first checks the current code across every protected vault
and prepares replacement password and code envelopes. It displays the new code
once and requires confirmation before changing any envelope or the login
credential. A durable prepared journal rolls back on restart; after the
credential and committed phase are written in one SQLite transaction, startup
finishes the envelope updates forward. A successful recovery revokes all
sessions, sets the vault password to the new local password, and requires a new
login. Existing MFA remains enabled. Replacing a code while signed in requires
the current login password and, when the encryption password has been
separated, that separate password too.

Store the code outside the VariaNotes data volume and separately from backups.
Application metadata backups contain the encrypted recovery envelopes but not
the code. A restored backup can be recovered with the code that was active
when that backup was made. If both the current vault password and the matching
recovery code are lost, VariaNotes cannot recover those vault keys. The code
recovers access through VariaNotes from a complete matching backup. In this
step, external recovery means keeping the code outside VariaNotes and using it
with the restored application metadata and vault. This is not a standalone
external decryption tool; validating full VariaNotes-free content recovery
remains part of the M31 security review and release gate.
Explicitly disabling authentication is rejected while any registered vault has
encryption metadata or `.age` ciphertext, including content in Trash, and also
fails when a registered vault cannot be inspected.

Changing `local`/`disabled` invalidates sessions, one-time artifacts, and
throttling state atomically and resets setup completion after the encryption
guard passes. Operator-only password and MFA recovery commands require
filesystem/container administration. Password recovery changes authentication
metadata only when performed by an operator without the recovery code. Code-based
recovery also replaces vault-key envelopes in application metadata. Neither
recovery path moves, encrypts, deletes, or rewrites notebook files.

## Authentication implementation history

The following M19 entries record how the current authentication boundary was
introduced. Git-specific examples describe removed workflows and are not part
of the current runtime API.

M19P1 initializes and validates this metadata service. M19P2 adds the
operator-controlled one-time bootstrap, versioned Argon2id credential, and a
restricted first-run boundary that blocks all non-setup APIs. M19P3 uses SCS
v2.9 with random opaque session identifiers, stores only their SHA-256 hashes
and server-side state in SQLite, and applies a deny-by-default API boundary.
Normal sessions have a 12-hour absolute lifetime; remembered sessions have a
30-day absolute lifetime, both with a seven-day idle ceiling. The cookie is
HttpOnly, SameSite Strict, scoped to `/api`, and Secure by default. Plain HTTP
development must explicitly set `VARIANOTES_SESSION_COOKIE_SECURE=false`.

The public M19P3 surface is limited to the static shell, liveness, setup,
login, and authentication status. Missing, expired, and revoked sessions receive
`401` with `authentication_required`.
M19P4 adds a random synchronizer token bound to each server-side session,
Origin/Referer enforcement, bounded progressive login throttling, hashed client
references in security events, and explicit trusted-proxy CIDRs. Forwarded
client or scheme headers are ignored unless the direct peer is configured as a
trusted proxy. M19P5 adds responsive setup/login screens, in-memory CSRF
handling, cross-tab authentication notifications, network/focus rechecks, and
version-aware temporary recovery drafts for interrupted unsaved edits. The
service worker continues to cache only the application shell and never
API/auth responses. M19P6 adds owner-verified password changes, configurable
session lifetimes, opaque session inspection and revocation, plus an interactive
operator password reset that revokes every session without touching notebook
content. M19P7 adds password-first TOTP with a one-step clock-skew window,
single-use TOTP steps, hashed atomic recovery codes, locally rendered enrollment
QR codes, and AES-256-GCM secret encryption using a separate private application
key. Enrollment and replacement remain pending until the new factor verifies
and recovery-code storage is confirmed. M19P8 completes explicit disabled-mode
warnings and migration guidance; every mode transition invalidates all local
authentication artifacts and never alters notebook repositories. M19P9 adds
the complete unauthenticated-route matrix, adversarial MFA/session tests,
browser/PWA lifecycle tests, authenticated container persistence/recovery smoke
coverage, and the documented OWASP-oriented review. No VariaNotes release
candidate may be published unless those checks and the vulnerability gates
pass for the exact candidate.

# Local authentication and recovery

Setup/login, protected sessions, browser/PWA reauthentication,
password/session administration, optional TOTP, and explicit disabled-mode
configuration are implemented and security-reviewed. Every release candidate
must pass the complete CI and security gate.

## Operator-controlled bootstrap

A fresh `local` installation starts with `setupRequired: true`. Visiting the
HTTP service does not create an owner and the server never prints a setup secret
in its normal logs. While setup is required, every notebook, file, asset,
search, vault-management, settings, cleanup, and maintenance API is blocked. Only the
static application shell, liveness, minimal auth status, and token-authorized
setup endpoint remain reachable.

The operator creates a one-time token from a trusted container or host terminal:

```sh
docker compose exec varianotes varianotes auth bootstrap-token
```

The command writes the plaintext token exactly once to its own terminal. The
database stores only a SHA-256 digest of 32 random bytes. Creating another token
invalidates the previous one; a token expires after 15 minutes and is consumed
atomically with successful password setup. No token can be created after setup
has completed or while authentication is disabled.

The setup screen submits the token and the chosen password to
`POST /api/auth/setup`. `GET /api/auth/status` exposes only the configured
mode and whether setup is required. Neither endpoint exposes credential,
notebook, host, or owner metadata.

## Browser and PWA sessions

The login screen supports normal and remembered sessions. Session and CSRF
secrets stay in HttpOnly cookies or memory; they are never written to browser
storage. The SPA checks authentication again when connectivity returns, the
window regains focus, or the page becomes visible. A session expiry replaces
the editor with the login screen instead of reporting a save or connection
failure.
Other open VariaNotes tabs and installed PWA windows are notified without
sharing credentials between them.

Unsaved note text and stale-save decisions remain in the active application
tab's memory; note content is not written to `localStorage` or
`sessionStorage`. While a stale-save decision is open, both the browser draft
and current server file remain available until the owner chooses a result.
Closing or reloading the tab discards its unsaved in-memory draft. This is not
an offline editing queue or backup.

The service worker caches only the application shell. Authentication and all
`/api/` responses remain network-only.

## Password and session administration

The Security section in Settings provides:

- password change after confirming the current password,
- configurable normal, idle, and remembered-session lifetimes,
- opaque browser-session descriptions and last-active timestamps,
- revocation of an individual session or all other sessions,
- logout of the current device.

A password change rotates the current session and revokes all other sessions by
default. Session identifiers shown in the UI are irreversible hashes, not the
cookie credentials. The running backend version is shown at the bottom of
Settings so operators can identify the deployed build.

## Forgotten-password recovery

An operator with access to the container terminal can replace a forgotten owner
password without touching notebook content:

```sh
docker compose exec -it varianotes varianotes auth reset-password
```

The command refuses non-interactive input, reads and confirms the password with
terminal echo disabled, and revokes every session. It changes only auth
metadata. MFA recovery is intentionally separate so password recovery cannot
silently weaken the second factor:

```sh
docker compose exec varianotes varianotes auth reset-mfa
```

That command revokes every session and removes MFA/recovery codes without
changing the owner password or any notebook.

## Optional TOTP MFA

Security settings can enroll any standard 30-second, six-digit TOTP
authenticator. Enrollment requires the current password. The QR code is rendered
inside VariaNotes without an external QR service; MFA remains disabled until a
generated code verifies and the owner confirms that the recovery codes were
stored safely.

VariaNotes accepts one clock step before or after the current step. A successfully
used TOTP step cannot be replayed. Ten high-entropy recovery codes are displayed
once, stored only as SHA-256 domain-separated hashes, and consumed atomically.
Regenerating codes invalidates every old code. Login always asks for the password
first and then accepts either TOTP or one recovery code without revealing which
factor failed.

The TOTP secret is encrypted using AES-256-GCM. The 32-byte application key is
stored at `/data/app/auth.key` by default (or
`VARIANOTES_AUTH_ENCRYPTION_KEY_FILE`) with mode `0600`, separately from
`auth.db`. Both must be backed up. Losing the key fails closed while MFA exists;
an operator may then use the explicit `reset-mfa` command. Neither file contains
canonical notes or assets.

Disabling or replacing MFA requires the current password plus a current TOTP or
unused recovery code. A replacement remains pending, leaving the existing
factor and recovery codes valid until the new authenticator is confirmed. A
pending enrollment expires after 15 minutes, is bound to the browser session
that began it, and can be cancelled explicitly from Settings.

## Explicit disabled mode

Omitting `VARIANOTES_AUTH_MODE` always selects fail-closed `local` mode. A fresh
installation remains setup-required until the operator creates a bootstrap
token and owner password.

`VARIANOTES_AUTH_MODE=disabled` must be written explicitly. Use it only for
localhost, a private LAN, VPN/Tailscale, or a deliberately secured external
layer. Settings and startup logs keep warning while it is active. Interactive
forward-auth can expire independently and return an HTML login page to API
requests, which a browser/PWA cannot treat as VariaNotes authentication.

Changing between `local` and `disabled` invalidates sessions, setup tokens,
password credentials, MFA secrets, recovery codes, and throttling state. A
later return to `local` requires bootstrap setup again. HTTPS termination,
strict proxy trust, and backend network isolation remain applicable in every
mode.

## Password policy and storage

VariaNotes accepts pasted passphrases, Unicode, and leading or trailing
whitespace without modification. New and changed passwords require at least 15
Unicode characters, reject a local set of common or trivially repeated values,
and reject inputs larger than 1024 UTF-8 bytes before password hashing. It does
not impose composition rules or scheduled password changes. Existing shorter
passwords remain valid until deliberately replaced so an upgrade cannot lock
the owner out.

Login and every sensitive password/TOTP verification share bounded credential
work and persistent progressive throttling. Repeated identical security events
are coalesced, records older than 90 days are removed, and the event table is
limited to the newest 2,000 entries to prevent unauthenticated disk growth.

Passwords use Argon2id from `golang.org/x/crypto/argon2` with:

```text
algorithm version: 19 (0x13)
memory:            65536 KiB (64 MiB)
iterations:        3
parallelism:       2
salt:              16 random bytes per hash
derived key:       32 bytes
```

The memory and iteration profile follows
[RFC 9106](https://www.rfc-editor.org/rfc/rfc9106.html)'s constrained-memory
recommendation and exceeds the
[OWASP Password Storage Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
minimum. Parallelism is tuned for a small two-vCPU self-hosted container. A
VariaNotes container should have at least 128 MiB available; 256 MiB or more is
recommended when image handling overlaps authentication. The
implementation uses the maintained
[`golang.org/x/crypto/argon2`](https://pkg.go.dev/golang.org/x/crypto/argon2)
package rather than a VariaNotes-specific cryptographic implementation.

Algorithm version, costs, salt, and derived hash are stored separately so a
successful future login can transparently replace weaker parameters with the
current profile and a fresh salt. Password plaintext is never stored or logged.
Derived values are compared in constant time. A missing credential still runs
the same Argon2id profile before returning the same authentication error, while
oversized inputs are rejected before hashing to bound unauthenticated resource
consumption.

Run the local benchmark on release hardware with:

```sh
go test -run '^$' -bench BenchmarkArgon2idDefaultParameters ./internal/auth
```

Release review should keep a single derivation comfortably below one second on
the minimum supported CPU while retaining at least the documented memory-hard
profile. Any cost reduction requires a security review; increases are applied
through the transparent upgrade path.

## Release gate

Local password authentication, optional TOTP, recovery, and explicit disabled
mode have passed the Milestone 19 implementation review. Every exact candidate
must pass backend tests, frontend tests, race detection, static analysis,
security scans, container build, and the release runtime preflight.

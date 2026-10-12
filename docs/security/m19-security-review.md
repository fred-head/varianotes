# Milestone 19 authentication security review

This review records the product under its former RepoQuill name; that name is
retained here to preserve the historical review context.

Review date: 2026-08-27  
Scope: Alpha 2 single-owner local authentication, optional TOTP MFA, explicit
disabled mode, browser/PWA lifecycle, recovery, and the HTTP authorization
boundary.

This review is a release gate, not a claim that software can be proven free of
all vulnerabilities. Alpha 2 may be tagged only after the pull request CI,
CodeQL, dependency/secret checks, clean production image scan, and hardened
container smoke test all pass without an ignored critical finding.

## Guidance reviewed

The implementation was reviewed against the current OWASP cheat sheets for
[authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html),
[password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html),
[session management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html),
[CSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html),
and [multifactor authentication](https://cheatsheetseries.owasp.org/cheatsheets/Multifactor_Authentication_Cheat_Sheet.html).

## Review results

- Authentication fails closed. Fresh and migrated local-mode instances require
  an operator-created, random, short-lived, one-use bootstrap token. Setup is
  not authorized by being the first browser to connect.
- Passwords use versioned Argon2id hashes with unique salts, bounded inputs,
  constant-time comparison, transparent cost upgrades, Unicode/passphrase
  support, and no composition or periodic-rotation rule.
- Opaque random sessions are server-side and persistent. Only a hash of the
  token is stored. Cookies are `HttpOnly`, `Secure` by default, `SameSite=Strict`,
  and confined to `/api`; tokens rotate after authentication-level changes.
  Idle and absolute expiry, logout, and targeted/all-session revocation are
  enforced server-side.
- Every API route is protected by default except liveness, minimal auth status,
  setup, and the two login steps. Public API responses are regression-tested
  against notebook, Git, SSH, key, and host-path disclosure.
- State changes require a session-bound CSRF token and a same-origin request.
  Forwarded identity headers are ignored unless the immediate peer matches an
  explicit trusted-proxy address or CIDR.
- Login and MFA attempts share progressive per-client and global throttling.
  Delays expire, so an attacker cannot permanently lock the owner out.
- TOTP uses the maintained `pquerna/otp` implementation with a 30-second period,
  six digits, and a documented one-step clock window. Accepted time steps cannot
  be replayed. Enrollment stays pending until verification and recovery-code
  acknowledgement.
- TOTP secrets are AES-256-GCM encrypted with a separate private key. QR codes
  are rendered locally. Recovery codes are random, displayed once, hash-only at
  rest, atomically single-use, and completely replaced on regeneration.
- Password and MFA changes require the current password plus the active second
  factor when enabled. Password reset and MFA reset are separate operator-only
  CLI actions and revoke sessions without touching notebook data.
- The SPA treats `401` as reauthentication, stops normal authenticated work, and
  keeps an unsaved recovery draft tied to notebook, note path, and file version.
  The service worker caches only the application shell, never API/auth/note data.
- Temporary recovery and guided-conflict drafts are tab-scoped. Conflict drafts
  that may contain combined note text are never persisted in `localStorage` and
  are cleared whenever authentication ends.
- Authentication input sizes, trailing/unknown JSON, path inputs, request
  origins, spoofed proxy headers, log control characters, and concurrency around
  one-use factors have adversarial coverage.

## Automated release gate

CI requires Go unit/integration/adversarial tests, race detection, `go vet`,
`govulncheck`, frontend unit/component/PWA tests, lint and production build,
`npm audit`, complete-history Gitleaks, Docker Compose validation, a clean image
build, Trivy HIGH/CRITICAL vulnerability and secret scanning, and a hardened
non-root/read-only container persistence smoke test. The container job is the
final Alpha 2 authentication release gate and depends on every earlier job.

## Accepted limitations and residual risk

- HTTPS remains an operator responsibility. A stolen valid session has the
  owner's authority until expiry or revocation; TOTP does not retroactively
  protect a stolen authenticated session.
- A host administrator, compromised container runtime, process-memory reader,
  or attacker with the persistent auth database and its separate encryption key
  is outside the application security boundary.
- TOTP is phishable and depends on reasonable clocks. Recovery codes and the
  encryption key must be backed up securely; losing the key requires explicit
  operator MFA reset.
- `disabled` mode intentionally provides no built-in authentication. It is safe
  only behind a deliberately managed LAN/VPN/external boundary, and an
  interactive forward-auth layer can still expire independently of RepoQuill.
- RepoQuill is single-owner and online-first. It does not provide multi-user
  authorization, collaborative editing, offline credential queues, passkeys,
  direct OIDC, or automated email recovery.
- Browser/PWA behavior cannot compensate for XSS, malicious browser extensions,
  an untrusted device, or a reverse proxy that serves attacker-controlled
  content on the same origin. CSP and dependency scanning are defense in depth.
- Standard external Markdown images can contact their origin when rendered and
  disclose the viewer's IP address. RepoQuill suppresses referrers but does not
  provide an image privacy proxy in Alpha 2.

Any new protected endpoint, authentication mode, credential type, or relaxation
of these assumptions requires updating the route matrix, threat model, tests,
and this review before release.

# HTTP application boundary

- Preserve deny-by-default authentication on `/api/`. Public exceptions are the
  health/status and setup/login/MFA entry points enforced in `handler.go`.
- Mutations must retain session-bound CSRF and same-origin checks; trust proxy
  headers only through configured proxy identity. See
  `../../docs/security/authentication-architecture.md` and
  `../../docs/security/reverse-proxy.md`.
- Keep note and asset bytes in notebook files, never notebook/auth metadata.
- When changing route access, update the route-boundary cases in
  `handler_test.go`; auth changes also use the uncached gate in
  `../../docs/development/testing.md`.

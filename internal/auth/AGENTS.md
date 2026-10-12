# Authentication constraints

- Keep a single fixed owner. `local` is fail-closed by default; `disabled` must
  remain explicit. Do not add general accounts or custom auth protocols.
- Auth storage contains authentication metadata only, never notes or assets.
  Reuse maintained libraries and existing cryptographic primitives; never log
  credentials, session secrets, MFA material, or note content.
- Preserve reset/migration behavior that changes auth state without touching
  notebooks. Read `../../docs/security/authentication-architecture.md` for
  changes to the trust boundary or persistent auth schema.
- Auth/session/MFA changes require the uncached route and package gate in
  `../../docs/development/testing.md`.

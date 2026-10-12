# Reverse-proxy and TLS security

VariaNotes's built-in authentication does not replace HTTPS. An Internet-facing
deployment must terminate TLS at a reverse proxy and keep the container's HTTP
port reachable only by that proxy. The default Compose mapping binds port 8080
to `127.0.0.1`; do not publish it on `0.0.0.0` unless another host firewall
provides an equivalent boundary.

## Trust only the proxy you operate

VariaNotes ignores `X-Forwarded-For`, `X-Real-IP`, and `X-Forwarded-Proto` from
ordinary clients. To accept them from a proxy, configure its exact source IP or
network:

```yaml
environment:
  VARIANOTES_SESSION_COOKIE_SECURE: "true"
  VARIANOTES_TRUSTED_PROXIES: "172.20.0.5/32"
```

Multiple entries are comma-separated and may be IPv4 or IPv6 addresses/CIDRs.
Do not trust all private networks merely for convenience, and never use
`0.0.0.0/0` or `::/0`. Docker network addresses can change when a network is
recreated, so either assign a stable proxy address or use the smallest dedicated
proxy-network CIDR and ensure untrusted containers cannot join it.

The last trusted proxy must overwrite incoming forwarding headers instead of
blindly preserving values supplied by the client. VariaNotes then walks an
`X-Forwarded-For` chain from the nearest hop toward the client and selects the
rightmost address outside the configured trusted networks. A malformed chain is
ignored completely.

VariaNotes normally derives the permitted browser origin from the request that
reaches it. If a deliberately configured proxy rewrites the public host or
scheme so that exact-origin checks cannot match, list only the browser-visible
HTTPS origin in `VARIANOTES_TRUSTED_ORIGINS`:

```yaml
environment:
  VARIANOTES_TRUSTED_ORIGINS: "https://notes.example.com"
```

Multiple exact origins are comma-separated. This is not a wildcard CORS switch
and does not authenticate a request. Do not add origins merely to bypass a
misconfigured proxy; first preserve the original `Host` and forwarded scheme.

## Nginx example

```nginx
location / {
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_pass http://varianotes:8080;
}
```

## Traefik and Caddy

Traefik and Caddy normally set forwarding headers automatically. Restrict their
trusted-forwarder configuration to the networks that actually precede them,
place VariaNotes and the proxy on a dedicated Docker network, and configure that
proxy network in `VARIANOTES_TRUSTED_PROXIES`. Do not add an interactive
forward-auth layer in front of VariaNotes's API; use VariaNotes's built-in local
authentication so the browser and installed PWA can understand session expiry.

## Request protections

Authenticated state changes require a random synchronizer token bound to the
server-side session and sent as `X-CSRF-Token`. SameSite Strict cookies and exact
Origin/Referer validation remain defense in depth. Cross-site browser requests,
scheme/host mismatches, and browser mutations without a usable origin are
rejected. API clients that do not behave like browsers still need a session and
CSRF token in local-auth mode.

Login failures are throttled with bounded progressive delays per resolved client
address and across the single-owner instance. Delays expire automatically and
never become a permanent network-triggered owner lockout.

The design follows the OWASP guidance for
[synchronizer CSRF tokens](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html),
[session cookies](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html),
and [login throttling](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html).

## Explicit disabled mode

VariaNotes never infers external authentication from forwarded headers or a
reachable login portal. `VARIANOTES_AUTH_MODE=disabled` must be configured
explicitly and leaves the API without its built-in password/MFA boundary. Use
it only when localhost, private network/VPN/Tailscale policy, or a deliberately
configured external layer already constrains access.

Interactive forward-auth products may expire independently and respond to an
API request with HTML or a redirect. VariaNotes's browser and PWA cannot convert
that response into a native session refresh. Built-in `local` mode is therefore
the most robust default. HTTPS, exact trusted-proxy configuration, and backend
network isolation remain necessary even when authentication is disabled.

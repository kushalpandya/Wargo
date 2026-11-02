# Wargo security model

Wargo gives its user a shell, the Docker socket and file access on the host —
effectively root. It is designed so that exposing it to the internet behind a
TLS reverse proxy is reasonable, but treat it like SSH: strong factors, an
up-to-date install, and ideally an extra layer such as Cloudflare Access.

Please report vulnerabilities privately (GitHub security advisory) rather than
in public issues.

## Process and network

- Runs as the **admin user it authenticates**, not as root and not as a
  dedicated service account. PAM (`pam_unix`) can only verify the password of
  the account the process runs as, and running as that user gives the
  terminal and file manager natural ownership semantics.
- Listens on `127.0.0.1:2207` by default. Publish it through nginx, Caddy or
  cloudflared (see [REVERSE_PROXY.md](REVERSE_PROXY.md)).
- Forwarded headers (`X-Forwarded-For`, `CF-Connecting-IP`,
  `X-Forwarded-Proto`) are honoured **only** from `auth.trusted_proxies`.
- Strict headers on every response: CSP (`default-src 'self'`, no inline
  scripts), `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy`,
  `Permissions-Policy`, COOP, and HSTS when served over TLS.
- The systemd unit deliberately avoids filesystem sandboxing
  (`ProtectSystem`, `PrivateTmp`, …) because those settings would apply to
  every shell opened in the web terminal, and leaves `NoNewPrivileges` off so
  on-demand `sudo` works.

## Authentication

- Exactly one account may sign in (`auth.user`), and it must be a member of
  the admin group (`wheel`/`sudo`, auto-detected).
- Sign-in methods:
  1. **Passkey** (WebAuthn, user verification required) — counts as two
     factors on its own.
  2. **OS password + TOTP.** TOTP enrolment is forced on first sign-in when
     `auth.require_totp = true` (default); until then the session can only
     reach the enrolment endpoints. TOTP codes cannot be replayed.
  3. **OS password + recovery code** (ten single-use codes, stored hashed).
- Password checks use the dedicated `/etc/pam.d/wargo` service, which uses
  only `pam_unix` and **not** `pam_faillock`. Distros usually lock the account
  after three failures in `system-auth`; reusing that stack would let anyone
  on the internet lock you out of SSH and sudo. Instead Wargo throttles before
  PAM is reached: per-IP exponential backoff after three failures (capped at
  15 minutes), a global budget of 30 failures per minute, uniform response
  timing and generic error messages.
- Sessions: 256-bit random tokens stored as SHA-256 hashes;
  `HttpOnly; Secure; SameSite=Strict` cookies; 30 minute idle and 12 hour
  absolute lifetime (configurable). Active sessions can be reviewed and
  revoked in Settings → Security.
- CSRF: every state-changing request must carry the per-session token in
  `X-Wargo-CSRF` and, when present, an allowed `Origin`. WebSocket upgrades
  check `Origin` against the request host and `auth.origins`.
- Passkeys are bound to the hostname you register them on and need HTTPS
  (or `localhost`). List every public origin in `auth.origins`.
- Locked out? On the host run `wargo admin reset-auth`; it removes passkeys,
  TOTP, recovery codes and sessions so you can sign in with your password and
  enrol again.

## Privileged actions — no standing root

Wargo never holds root. Actions that need it (signalling another user's
process, editing root-owned files, package upgrades, compose updates, reboot)
ask for approval per request:

- **sudo (default):** the browser shows a prompt; the password is sent once in
  the `X-Wargo-Elevation` header of that single request, piped to
  `sudo -k -S` (cached sudo credentials are never reused) and wiped. It is
  never written to disk or logs. Attempts are throttled — `sudo` itself goes
  through `system-auth` and therefore `pam_faillock`.
- **Passkey via `wargo-privd` (optional):** a small root daemon on a unix
  socket. It keeps its own copy of trusted passkey public keys (written once
  with sudo approval), issues WebAuthn challenges bound to the exact action and
  target, verifies the assertion itself and only then runs an allow-listed
  command. A compromised Wargo web process therefore still cannot obtain root
  without your authenticator. It verifies the peer UID of every connection.
- Root work is done by `wargo helper <op>`, a sub-command of the same binary
  with a closed set of strictly validated operations. It never invokes a
  shell and confines file operations with `os.Root`.
- The root terminal (`sudo -i` in a PTY) asks for the password inside the
  terminal itself; Wargo never sees it.

## File manager

Every operation goes through Go's `os.Root`, confined to the configured
`files.roots`; `..`, absolute paths and symlinks cannot escape. Uploads are
chunked and resumable (so they fit Cloudflare's 100 MB body limit), archive
extraction is size- and count-limited, inline previews are served with
`Content-Security-Policy: sandbox`, and HTML/SVG is never rendered as active
content.

## Integrations

API keys and passwords (Prowlarr, Transmission) are encrypted at rest with
AES-256-GCM using a per-install key in `<data_dir>/secret.key` (0600) and are
never returned to the browser. Prowlarr download links embed the API key, so
Wargo keeps them server-side and hands the browser opaque result IDs.

## Audit log

Sign-ins, account changes, every elevated action and every mutating
operation (process signals, container actions, file changes, torrent
actions, upgrades) are recorded with time, client IP, user agent, result and
the elevation method used. See Settings → Audit log.

## Recommendations

- Put Cloudflare Access (or another identity-aware proxy) in front.
- Register a passkey and keep the recovery codes offline.
- Keep `dev.auth_bypass` off — it is ignored unless Wargo listens on loopback,
  but never enable it on a server you expose.

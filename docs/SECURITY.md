# Wargo security model

Wargo gives its user a shell, the Docker socket and file access on the host —
effectively root. It is designed so that exposing it to the internet behind a
TLS reverse proxy is reasonable, but treat it like SSH: strong factors, an
up-to-date install, and ideally an extra layer such as Cloudflare Access.

Please report vulnerabilities privately (GitHub security advisory) rather than
in public issues.

## Process and network

(This describes the native install; see [Container mode](#container-mode)
for the Docker image.)

- Runs as the **admin user it authenticates**, not as root and not as a
  dedicated service account. PAM (`pam_unix`) can only verify the password of
  the account the process runs as, and running as that user gives the
  terminal and file manager natural ownership semantics.
- Native installs listen on `127.0.0.1:2207` by default; publish them through
  nginx, Caddy or cloudflared (see [REVERSE_PROXY.md](REVERSE_PROXY.md)). The
  Docker image listens on all interfaces (see [Container mode](#container-mode)).
- **Host allow-list (DNS rebinding):** every request must name a known host —
  an IP address, `localhost`, a single-label or private name (`.local`,
  `.lan`, `.home.arpa`, `.internal`…), the machine's hostname, or a host in
  `auth.origins`. Anything else gets HTTP 421, so a malicious web page that
  re-points its own domain at your server can't talk to Wargo.
- Forwarded headers are honoured **only** from `auth.trusted_proxies`; the
  right-most untrusted `X-Forwarded-For` entry is the client.
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
  PAM is reached:
  - exponential backoff per IPv4 address or IPv6 /64 after three failures
    (capped at 15 minutes), counting attempts that are still in flight;
  - a global budget of 30 failures per minute — clients that signed in
    successfully in the last 30 days, and passkey sign-ins, are exempt so an
    attacker can't lock the owner out;
  - at most five second-factor guesses per password entry, and a correct
    password doesn't reset the backoff until sign-in completes;
  - uniform response timing and generic error messages.
- Once any second factor exists, a password alone never creates a session.
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
  without your authenticator. It verifies the peer UID of every connection,
  requires its binary, credentials and their parent directories to be
  root-owned, and opens upgrade log files itself so planted links can't
  redirect root's output.
- Root work is done by `wargo helper <op>`, a sub-command of the same binary
  with a closed set of strictly validated operations. It never invokes a
  shell, confines file operations with `os.Root`, reads only regular files
  without blocking, replaces files atomically without following links, and
  pins signals to the selected process with a pidfd.
- The root terminal (`sudo -i` in a PTY) asks for the password inside the
  terminal itself; Wargo never sees it.

## File manager

Every operation goes through Go's `os.Root`, confined to the configured
`files.roots`; `..`, absolute paths and symlinks cannot escape. Uploads are
chunked and resumable (so they fit Cloudflare's 100 MB body limit), archive
extraction is size- and count-limited, inline previews are served with
`Content-Security-Policy: sandbox`, and HTML/SVG is never rendered as active
content. Named pipes and devices are never opened for reading, renames never
replace a file created in the meantime, and new names may not contain control
characters. Mounted drives (including `/`) can be browsed, subject to normal
Unix permissions for the signed-in user.

## Container mode

When Wargo runs from the Docker image ([DOCKER.md](DOCKER.md)), set up with
`WARGO_HOST_ROOT`, the process model differs from the native one:

- **Wargo is root** inside a privileged container that shares the host's
  PID, network and UTS namespaces and mounts the host's `/`. There is no
  privilege boundary between Wargo and the host: a compromise of the Wargo
  process is a root compromise of the host. "No standing root" does not
  apply; what remains is the *application-level* approval model below and
  the same sign-in, session, CSRF, header and audit protections as natively.
- **Startup self-check.** Wargo refuses to start unless it is root, the host
  root is mounted, it shares the host PID namespace (compares
  `/proc/self/ns/pid` with the host PID 1's) and `nsenter` into the host
  namespaces works.
- **Sign-in.** The container's PAM can't see host accounts, so the password
  is verified against the host's `/etc/shadow` with libxcrypt `crypt_r`
  (yescrypt, sha512crypt, bcrypt, …) and a constant-time comparison. Locked
  (`!`/`*`), password-less and expired accounts (shadow expiry date) are
  rejected; password ageing is not. The same throttling, uniform timing,
  TOTP, passkeys and recovery codes apply. Only a member of the admin group
  can sign in, and never `root`. The admin account defaults to the first
  regular (`uid >= 1000`) member of `wheel`/`sudo`/`admin` and is logged.
- **Acting as the user.** File-manager requests and file jobs switch the
  thread's filesystem identity (fsuid, fsgid, supplementary groups) to the
  admin account with raw per-thread syscalls on a locked OS thread, verify
  the switch, and restore it afterwards; a thread whose identity can't be
  restored is never reused (it is destroyed with its goroutine). Permission
  semantics therefore match a native install. Update checks, `docker
  compose` and container shells run on the host as the user via `nsenter` +
  `setpriv` (or `su`); host and root terminals use `su -l <user>` and the
  host's own `sudo -i`, which asks for the password in the terminal.
- **Administrator approval.** sudo can't run inside the container. Each
  privileged action (signalling another user's process, editing files you
  can't write, package refresh/upgrade, reboot, compose updates) still needs
  approval per request: either the admin account's password (sent once in
  the `X-Wargo-Elevation` header, verified against the host's shadow file,
  throttled, never stored) or a passkey step-up bound to the action and
  target. The action then runs in-process — the same strictly validated
  `wargo helper` operations — or on the host via `nsenter`. Every approval is
  audited with its method (`password` / `passkey`). wargo-privd is not used:
  its guarantee (a compromised web process can't get root) can't hold when
  the process already is root.
- **Self protection.** Wargo identifies its own container and asks for
  explicit confirmation before stopping or restarting it, and refuses to
  recreate (update) it; it never signals its own process or its init.
- **Network.** The image listens on `0.0.0.0:2207` (host network) so it is
  reachable on the LAN out of the box; plain HTTP exposes the password and
  session to the local network. Put TLS in front, or set
  `WARGO_LISTEN=127.0.0.1:2207` to keep it on the server only.

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

## Security review (alpha)

Before the first public alpha the whole code base was reviewed with these
attackers in mind: anyone on the internet without a session; someone who
knows the password but not a second factor; a hostile web page in the
admin's browser (CSRF, cross-site WebSockets, DNS rebinding); a malicious
container image, registry, indexer, file name, archive or torrent; and
another unprivileged user on the host. Highlights of what was fixed:

- DNS-rebinding protection (host allow-list) and Origin checks on every
  state-changing request and WebSocket upgrade.
- No session from a password alone once a second factor exists; tighter,
  lockout-resistant throttling (see Authentication).
- Root helper: strict PID parsing (a wrapped value could have meant "every
  process"), pidfd-pinned signals, non-blocking regular-file reads, atomic
  link-safe writes, validated compose arguments.
- wargo-privd: directory-chain ownership checks and descriptor-based upgrade
  logs.
- Server-side requests: icon URLs from container labels only reach public
  addresses (checked at connect time); the Prowlarr API key is never sent
  across a redirect to another host; compose labels inherited from an image
  are ignored for updates.
- Files: FIFO/device hangs, rename races, long-name trash loops, upload and
  zip caps; config imports open files without following symlinks.
- Bounded growth: audit log retention (180 days / 50,000 entries), capped
  fields, event and job buffers.
- Builds: release binaries need only glibc 2.28 + libpam; CI actions pinned
  to commit SHAs with least-privilege permissions; releases ship
  `SHA256SUMS`; `govulncheck` clean with Go 1.26.10.

**Residual risks** we accept for now:

- wargo-privd's guarantee is "no root without a user-verified passkey touch
  per action". A compromised web process controls what you see, so it could
  turn one approval into any allow-listed helper operation.
- Container mode has no privilege boundary between Wargo and the host.
- Accurate throttling and audit IPs behind a proxy depend on the proxy
  setting `X-Forwarded-For`.
- Signed-in users may see internal error text in API errors.
- PDF previews are served without the `sandbox` CSP directive because
  browsers' PDF viewers refuse to run sandboxed.
- Container base images are pinned by Debian release, not digest — rebuild
  regularly.

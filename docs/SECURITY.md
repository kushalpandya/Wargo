# Wargo security model

Wargo gives the person signed in a shell, the Docker socket and access to the
server's files. In practice that is root access. It is built so that putting
it on the internet behind an HTTPS proxy is reasonable, but treat it like
SSH: use strong sign-in methods, keep it up to date, and ideally add an extra
layer such as Cloudflare Access.

Please report vulnerabilities privately through a
[GitHub security advisory](https://github.com/kushalpandya/wargo/security/advisories/new),
not in public issues.

## How Wargo runs

This section describes the native install. See
[Container mode](#container-mode) for the Docker image.

- Wargo runs as **the admin account that signs in**, not as root and not as a
  separate service account. PAM (`pam_unix`) can only check the password of
  the account the process runs as, and running as you means files created in
  the terminal and file manager belong to you.
- Native installs listen on `127.0.0.1:2207` by default. Publish them through
  nginx, Caddy or cloudflared (see [REVERSE_PROXY.md](REVERSE_PROXY.md)). The
  Docker image listens on every interface (see
  [Container mode](#container-mode)).
- **Known hostnames only (DNS rebinding protection).** Every request must use
  a known hostname: an IP address, `localhost`, a single-word or private name
  (`.local`, `.lan`, `.home.arpa`, `.internal` and so on), the server's
  hostname, or one listed in `auth.origins`. Anything else gets HTTP 421, so
  a malicious web page that points its own domain at your server can't talk
  to Wargo.
- Forwarded headers are trusted **only** from `auth.trusted_proxies`. The
  visitor's address is the right-most `X-Forwarded-For` entry that isn't a
  trusted proxy.
- Strict headers on every response: CSP (`default-src 'self'`, no inline
  scripts), `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy`,
  `Permissions-Policy`, COOP, and HSTS over HTTPS.
- **Requests to the internet** happen only for features you use:
  - the public IP on System → Network (`api.ipify.org`, falling back to
    `ipv4.icanhazip.com`), looked up while the page is open and cached for
    30 minutes;
  - icon downloads from dashboard-icons;
  - container update checks against image registries;
  - launcher health checks and the integrations you set up.
- **System → Events** reads the systemd journal on the server (as your
  account natively, as root in container mode), Docker's recent event
  history, and Wargo's own audit log. Nothing is sent anywhere.
- The systemd unit deliberately doesn't use filesystem sandboxing
  (`ProtectSystem`, `PrivateTmp` and similar), because it would also apply to
  every shell opened in the web terminal. It also leaves `NoNewPrivileges`
  off so sudo works when you approve an action.

## Signing in

- Exactly one account can sign in (`auth.user`), and it must be in the admin
  group (`wheel` or `sudo`, detected automatically).
- Ways to sign in:
  1. **Passkey** (WebAuthn, with user verification). This counts as two
     factors on its own.
  2. **Server password plus a two-factor code (TOTP).** Setting up the code
     is required on first sign-in when `auth.require_totp = true` (the
     default); until then, the session can only reach the setup pages. Each
     code works only once.
  3. **Server password plus a recovery code.** There are ten single-use
     codes, stored hashed.
- Passwords are checked with Wargo's own `/etc/pam.d/wargo` service, which
  uses only `pam_unix` and **not** `pam_faillock`. Linux distributions
  usually lock an account after three wrong passwords; using that same setup
  would let anyone on the internet lock you out of SSH and sudo. Instead,
  Wargo slows down attempts before PAM is involved:
  - after three wrong passwords from one IPv4 address (or IPv6 /64), each
    further attempt waits longer, up to 15 minutes, counting attempts still in
    progress;
  - at most 30 failures per minute overall. Visitors who signed in
    successfully in the last 30 days, and passkey sign-ins, are exempt, so an
    attacker can't lock the owner out;
  - at most five second-factor guesses per password, and a correct password
    doesn't reset the wait until sign-in finishes;
  - every attempt takes the same time and gets the same generic error.
- Once you have a second factor, a password alone never signs you in.
- Sessions use 256-bit random tokens, stored only as SHA-256 hashes, in
  `HttpOnly; Secure; SameSite=Strict` cookies. They end after 30 minutes of
  inactivity or 12 hours in total (both adjustable). You can review and end
  sessions in Settings → Security.
- Cross-site request protection: every request that changes something must
  carry the session's token in `X-Wargo-CSRF` and, if present, an allowed
  `Origin`. WebSocket connections check `Origin` against the request's host
  and `auth.origins`.
- Passkeys are tied to the hostname you register them on, and need HTTPS (or
  `localhost`). List every public address in `auth.origins`.
- Locked out? On the server run `wargo admin reset-auth`. It removes
  passkeys, two-factor codes, recovery codes and sessions, so you can sign in
  with your password and set them up again.

## Admin actions: no permanent root

Wargo never keeps root access. Actions that need it (signalling another
user's process, editing files owned by root, installing updates, compose
updates, restarting the server) ask for approval every time:

- **sudo (default).** The browser asks for your password. It is sent once, in
  the `X-Wargo-Elevation` header of that one request, passed to
  `sudo -k -S` (so sudo never reuses an earlier approval) and then wiped. It
  is never written to disk or to logs. Attempts are slowed down, and sudo
  itself uses the normal `system-auth` rules, including `pam_faillock`.
- **Passkey through `wargo-privd` (optional).** A small root service on a
  local socket. It keeps its own list of trusted passkeys (written once, with
  sudo approval), asks for a passkey confirmation tied to the exact action and
  target, checks it itself, and only then runs a command from a fixed list. So
  even a compromised Wargo web process can't get root without your
  authenticator. It checks the user of every connection, requires its program,
  settings and their parent folders to be owned by root, and opens upgrade log
  files itself so planted links can't redirect root's output.
- Root work is done by `wargo helper <op>`, part of the same program, with a
  fixed set of strictly checked operations. It never runs a shell, keeps file
  operations inside their folder with `os.Root`, only reads regular files
  (without blocking), replaces files in one step without following links, and
  pins signals to the chosen process with a pidfd.
- The root terminal (`sudo -i`) asks for your password inside the terminal
  itself; Wargo never sees it.

## File manager

Every operation goes through Go's `os.Root` and stays inside the folders set
in `files.roots`; `..`, absolute paths and symlinks can't escape. Uploads are
sent in resumable pieces (so they fit Cloudflare's 100 MB limit), archive
extraction has size and file-count limits, previews are served with
`Content-Security-Policy: sandbox`, and HTML and SVG are never run as active
content. Named pipes and devices are never opened for reading, a rename never
replaces a file created in the meantime, and new names can't contain control
characters. You can browse mounted drives (including `/`), with normal Linux
permissions for your account.

## Container mode

When Wargo runs from the Docker image ([DOCKER.md](DOCKER.md)), it works
differently from a native install:

- **Wargo is root** inside a privileged container that shares the host's
  processes, network and hostname and mounts the host's whole filesystem.
  Nothing separates Wargo from the host: if the Wargo process is compromised,
  the host is compromised as root. "No permanent root" doesn't apply. What
  remains is the approval system below, plus the same sign-in, session,
  cross-site, header and audit protections as natively.
- **Startup check.** Wargo won't start unless it is root, the host's
  filesystem is mounted, it shares the host's process list (it compares
  `/proc/self/ns/pid` with the host's PID 1) and it can run commands in the
  host's environment with `nsenter`.
- **Signing in.** The container's PAM can't see the host's accounts, so the
  password is checked against the host's `/etc/shadow` with libxcrypt
  `crypt_r` (yescrypt, sha512crypt, bcrypt and others), using a constant-time
  comparison. Locked (`!` or `*`), password-less and expired accounts are
  refused; password ageing rules are not applied. The same slow-down, equal
  timing, two-factor codes, passkeys and recovery codes apply. Only a member
  of the admin group can sign in, never `root`. By default the account is the
  first regular (`uid >= 1000`) member of `wheel`, `sudo` or `admin`, and it
  is logged at startup.
- **Acting as you.** File manager requests and file jobs switch the
  thread's file access identity (fsuid, fsgid and groups) to your account,
  check the switch, and switch back afterwards. A thread that can't switch
  back is thrown away, never reused. So permissions match a native install.
  Update checks, `docker compose` and container shells run on the host as you,
  through `nsenter` with `setpriv` (or `su`). Host and root terminals use
  `su -l <user>` and the host's own `sudo -i`, which asks for your password in
  the terminal.
- **Approving admin actions.** sudo can't run inside the container, but every
  admin action (signalling another user's process, editing files you can't
  write, refreshing or installing updates, restarting the server, compose
  updates) still needs approval each time: either your account's password
  (sent once in the `X-Wargo-Elevation` header, checked against the host's
  shadow file, slowed down on failure, never stored) or a passkey
  confirmation tied to the action and target. The action then runs inside
  Wargo (the same strictly checked `wargo helper` operations) or on the host
  through `nsenter`. Every approval is logged with its method (`password` or
  `passkey`). `wargo-privd` isn't used: its promise (a compromised web
  process can't get root) can't hold when the process already is root.
- **Protecting itself.** Wargo recognises its own container. It asks you to
  confirm before stopping or restarting it, refuses to recreate (update) it,
  and never signals its own process or its init process.
- **Network.** The image listens on `0.0.0.0:2207` (on the host's network),
  so it is reachable on your local network right away. Over plain HTTP, your
  password and session are visible to others on that network. Put HTTPS in
  front, or set `WARGO_LISTEN=127.0.0.1:2207` to keep it on the server only.

## Integrations

API keys and passwords (Prowlarr, Transmission) are stored encrypted with
AES-256-GCM, using a key unique to each install in `<data_dir>/secret.key`
(readable only by Wargo), and are never sent to the browser. Prowlarr
download links contain the API key, so Wargo keeps them on the server and
gives the browser only anonymous result IDs.

## Audit log

Sign-ins, account changes, every approved admin action and every change Wargo
makes (process signals, container actions, file changes, torrent actions,
upgrades) are logged with the time, visitor IP, browser, result and approval
method. See Settings → Audit log.

## Recommendations

- Put Cloudflare Access (or another login proxy) in front.
- Register a passkey, and keep your recovery codes somewhere offline.
- Keep `dev.auth_bypass` off. Wargo ignores it unless it listens only on
  `127.0.0.1`, but never turn it on for a server you expose.

## Security review (alpha)

Before the first public alpha, the whole code base was reviewed with these
attackers in mind:

- anyone on the internet without a session;
- someone who knows the password but not a second factor;
- a hostile web page open in the admin's browser (cross-site requests,
  cross-site WebSockets, DNS rebinding);
- a malicious container image, registry, indexer, file name, archive or
  torrent;
- another user without admin rights on the same server.

Highlights of what was fixed:

- DNS rebinding protection (known hostnames only) and `Origin` checks on
  every request that changes something and every WebSocket connection.
- No session from a password alone once a second factor exists, and stricter
  throttling that can't be used to lock the owner out (see
  [Signing in](#signing-in)).
- Root helper: strict PID parsing (a wrapped value could have meant "every
  process"), signals pinned with pidfds, non-blocking file reads, link-safe
  writes in one step, and checked compose arguments.
- wargo-privd: ownership checks on the whole folder chain, and upgrade logs
  opened by the service itself.
- Requests made by the server: icon URLs from container labels can only reach
  public addresses (checked when connecting); the Prowlarr API key is never
  sent along a redirect to another host; compose labels inherited from an
  image are ignored for updates.
- Files: hangs on pipes and devices, rename races, trash loops with long
  names, upload and zip limits; config imports open files without following
  symlinks.
- Limited growth: the audit log keeps 180 days or 50,000 entries at most, and
  fields, event and job buffers have size limits.
- Builds: release binaries need only glibc 2.28 and libpam; CI actions are
  pinned to exact commits with minimal permissions; releases include
  `SHA256SUMS`; `govulncheck` reports nothing with Go 1.26.10.

**Remaining risks** we accept for now:

- wargo-privd promises "no root without a passkey confirmation for each
  action". A compromised web process controls what you see, though, so it
  could turn one approval into any operation on the helper's fixed list.
- In container mode nothing separates Wargo from the host.
- Correct throttling and audit IPs behind a proxy depend on the proxy setting
  `X-Forwarded-For`.
- Signed-in users may see internal error text in API errors.
- PDF previews are served without the `sandbox` CSP rule, because browsers'
  PDF viewers refuse to run in a sandbox.
- Container base images are pinned to a Debian release, not an exact image
  digest, so rebuild them regularly.

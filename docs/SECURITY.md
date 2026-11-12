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
- **Requests to the internet** are limited to the following, each of which
  can be turned off or simply not used:
  - the public IP on Home and System → Network (`api.ipify.org`, falling
    back to `ipv4.icanhazip.com`), looked up while one of them is open and
    cached for 30 minutes;
  - the weather on Home (`api.open-meteo.com`, plus Open-Meteo's geocoding
    to find a place from the server's timezone or your search), looked up
    while Home is open and cached for 15 minutes. Turn it off in
    Settings → Appearance;
  - icon downloads from dashboard-icons;
  - container update checks against image registries, in the background
    every 6 hours by default. Change or turn them off in Settings → General;
  - OS update checks, which contact your distro's package mirrors in the
    background, daily by default. Change or turn them off in
    Settings → General;
  - launcher health checks and the integrations you set up (Prowlarr,
    Transmission and Plex). Plex is only contacted at the address you
    configure, read only: a health check about every 30 seconds while
    Wargo is open in a browser, activity while Home's feed is open (at most
    every 10 seconds, failures included), and posters (cached in the
    browser for a day).
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
     factors on its own. Sign-in challenges are sealed tokens bound to the
     visitor's address and the running Wargo process, and each works only
     once. Nothing is kept per attempt, so a flood of sign-in attempts can't
     push out the owner's.
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
  - every failed attempt gets the same generic error and takes at least
    3 seconds, whether or not the username was right, so timing doesn't reveal
    the admin's username. A correct password is not slowed down.
- Once you have a second factor, a password alone never signs you in.
- Sessions use 256-bit random tokens, stored only as SHA-256 hashes, in
  `HttpOnly; Secure; SameSite=Strict` cookies. They end after 30 minutes of
  inactivity or 12 hours in total (both adjustable). With **Keep me signed
  in** ticked at sign-in, a session instead ends after 30 days unused
  (`auth.session_remember`) or 90 days after sign-in
  (`auth.session_remember_max`), whichever comes first; admin actions still
  ask for approval every time. Its cookies are kept by the browser for up to
  400 days (the browsers' limit), but the server enforces the real expiry.
  **Keep me signed in** only applies over HTTPS or from the machine itself
  (`localhost`); on plain HTTP across a network the session ends as usual.
  A password-only session that still has to set up two-factor
  authentication is never remembered, and enabling two-factor
  authentication ends every other such session.
- Signing in again from the same browser replaces its earlier session,
  through a random device id in an `HttpOnly` cookie. Sessions from before
  device ids existed are cleaned up once, by matching browser and address.
  You can review and end sessions, one by one or all others, in Settings →
  Security.
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
  is never written to disk or to logs. sudo's input carries only the
  password: payloads such as edited file content go to the root helper
  through a private temporary file in the data directory, readable only by
  Wargo's account. Attempts are slowed down, and sudo
  itself uses the normal `system-auth` rules, including `pam_faillock`.
- **Passkey through `wargo-privd` (optional).** A small root service on a
  local socket. It keeps its own list of trusted passkeys (written once, with
  sudo approval), asks for a passkey confirmation tied to the exact action and
  target, checks it itself, and only then runs a command from a fixed list. So
  even a compromised Wargo web process can't get root without your
  authenticator. It checks the user of every connection, requires its program,
  settings and their parent folders to be owned by root, refuses package
  manager programs that root doesn't own, that others can change or that live
  in folders root doesn't own, and bare package manager names (it needs the
  full path), and opens upgrade log files itself so planted links can't
  redirect root's output. It only accepts upgrade logs directly in
  `/var/log/wargo`, `/var/lib/wargo` or `/run/wargo`. With a custom
  `data_dir` elsewhere, upgrades approved with a passkey can't write their
  log; approve upgrades with sudo there.
- Root work is done by `wargo helper <op>`, part of the same program, with a
  fixed set of strictly checked operations. It never runs a shell, keeps file
  operations inside their folder with `os.Root`, refuses symbolic links
  anywhere in a path, only reads regular files
  (without blocking), replaces files in one step without following links, and
  pins signals to the chosen process with a pidfd.
- The root terminal (`sudo -i`) asks for your password inside the terminal
  itself; Wargo never sees it.

## File manager

Every operation goes through Go's `os.Root` and stays inside the folders set
in `files.roots`; `..`, absolute paths and symlinks can't escape. Admin
actions on files refuse symbolic links anywhere in the path. Uploads are
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
  the terminal. The Root shell needs util-linux `setpriv` on the host:
  without it, it falls back to `su`, which drops the terminal sudo needs.
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
  confirm before stopping or restarting it, never recreates it directly,
  and never signals its own process or its init process.
- **Updating itself.** To update its own container, Wargo starts a helper
  container (`wargo-self-update`) that recreates it. The helper gets the same
  Docker socket access Wargo already has and nothing more: no network, no
  privileges and no other host files. It only ever runs from the image Wargo just
  pulled for its own image reference (the name and tag its container was
  created with), and only targets Wargo's own container.
- **Network.** The image listens on `0.0.0.0:2207` (on the host's network),
  so it is reachable on your local network right away. Over plain HTTP, your
  password and session are visible to others on that network. Put HTTPS in
  front, or set `WARGO_LISTEN=127.0.0.1:2207` to keep it on the server only.

## Integrations

API keys, passwords and tokens (Prowlarr, Transmission, Plex) are stored
encrypted with AES-256-GCM, using a key unique to each install in
`<data_dir>/secret.key` (readable only by Wargo), and are never sent to the
browser or written to the audit log. Prowlarr download links contain the API
key, so Wargo keeps them on the server and gives the browser only anonymous
result IDs. A saved secret is only sent to the server it was saved for
(same scheme, host and port): testing another address needs the secret
entered again, and saving another address without one clears it.

"Import" reads a secret from a container's config folder only for a
container Wargo detected itself (Prowlarr's `config.xml`, Plex's
`Preferences.xml` from its `/config` mount), as your account. Nothing below
the mount's folder is followed if it is a symbolic link, neither the file
nor any folder on the way (each is opened relative to the previous one, so
it can't be swapped for a link in between). Plex posters are fetched by Wargo and only for Plex
library image paths, so the browser never needs the Plex token.

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
  names, upload and zip limits; config imports don't follow symlinks anywhere
  below the container's config folder.
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
- On a native install, any program on the server can connect to Wargo's
  loopback port and send its own `X-Forwarded-For`, because loopback is a
  trusted proxy by default. Another local account can therefore make its
  sign-in attempts look like they come from another address. Remove loopback
  from `auth.trusted_proxies` if nothing on the server proxies Wargo.
- Signed-in users may see internal error text in API errors.
- PDF previews are served without the `sandbox` CSP rule, because browsers'
  PDF viewers refuse to run in a sandbox.
- Container base images are pinned to a Debian release, not an exact image
  digest, so rebuild them regularly.

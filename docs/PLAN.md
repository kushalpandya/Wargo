# Wargo — Implementation Plan

> A single-binary, low-memory, secure homelab dashboard: system overview, processes,
> containers, terminal, file manager, OS updates, Prowlarr/Transmission integration and
> an app launcher — exposed safely behind nginx / cloudflared.

License: **AGPL-3.0-only** · Default listen: **`127.0.0.1:2207`**

---

## 1. Goals & non-goals

**Goals**
- One responsive SPA (desktop → phone) with a dense, distinctive "Northern" dashboard style.
- Real-time system metrics, process management, Docker management, full PTY terminal,
  complete file manager, OS package updates with live progress.
- Optional integrations: Prowlarr search + Transmission downloads, app launcher.
- Security first: host-account login (PAM), TOTP, passkeys, no standing root by default.
- Tiny footprint: < 40 MB RSS with a few clients, main JS bundle < 350 KB gz.
- Releasable to other homelab owners across Linux distros.

**Non-goals (v1)**
- Multi-user / RBAC (single admin = the host user).
- Running Wargo itself inside a container (native install only).
- Installing AUR packages (listed read-only).
- Windows/macOS hosts.

---

## 2. Stack

| Layer | Choice |
|---|---|
| Language | Go 1.26 (cgo enabled for PAM) |
| HTTP / WS | `net/http` (stdlib mux), `github.com/coder/websocket` |
| Metrics | `github.com/shirou/gopsutil/v4`, direct `/sys/class/hwmon`, `/proc` |
| Docker | `github.com/docker/docker/client` (official SDK) + `docker compose` CLI (optional path) |
| PTY | `github.com/creack/pty` |
| Files | `os.Root` (Go 1.24+) for jailed FS access |
| Storage | SQLite via `modernc.org/sqlite` (pure Go) |
| Auth | `github.com/msteinert/pam/v2`, `github.com/go-webauthn/webauthn`, `github.com/pquerna/otp`, `golang.org/x/crypto/argon2` (recovery codes) |
| Config | TOML (`github.com/pelletier/go-toml/v2`) |
| Frontend | React 19, Vite, TypeScript, React Router (SPA), TanStack Query, Zustand |
| UI | Tailwind CSS v4 with custom design tokens, Radix UI primitives (unstyled) |
| Widgets | xterm.js (+fit, web-links, search), CodeMirror 6, uPlot |
| Release | GoReleaser + nfpm (.deb/.rpm/.apk/.pkg.tar.zst), AUR `wargo-bin`, `install.sh` |

---

## 3. Architecture

```
Browser (React SPA)
   │ HTTPS
   ▼
cloudflared / nginx / caddy  (TLS, optional Cloudflare Access)
   │ http://127.0.0.1:2207
   ▼
wargo  (systemd, User=<admin>, unprivileged)
   ├─ httpapi     REST /api/*, WS /ws/*, embedded SPA (go:embed)
   ├─ auth        PAM, TOTP, WebAuthn, sessions, throttling, CSRF
   ├─ elevate     sudo-password elevation  ──or──►  wargo-privd (root, unix socket)
   ├─ metrics     single collector → ring buffer → fan-out to subscribers
   ├─ procs       list / tree / signal
   ├─ docker      list, stats, logs, actions, exec, updates, events
   ├─ term        PTY sessions (host shell, container exec), optional tmux attach
   ├─ files       os.Root-jailed ops, chunked uploads, range downloads, jobs
   ├─ pkg         distro detection + package-manager backends
   ├─ integrations/{prowlarr,transmission,torrent}
   ├─ launcher    tiles, icons, health pings
   ├─ jobs        long-running tasks with progress (copy, archive, upgrade)
   └─ audit       append-only audit log (SQLite)
```

**Real-time channels**
- `/ws/metrics` — overview stream (1–2 s; collector slows to 10 s when no subscribers).
- `/ws/events` — multiplexed topics: `docker`, `jobs`, `procs` (while page open),
  `transmission` (while page open), `pkg`.
- `/ws/term/{id}` — raw PTY stream (binary frames) + JSON control frames (resize).

---

## 4. Repository layout

```
wargo/
  cmd/
    wargo/            main web service
    wargo-privd/      optional root helper (milestone 11)
  internal/
    config/  httpapi/  auth/  elevate/  audit/  jobs/  store/
    metrics/ procs/  docker/  term/  files/  pkg/  launcher/
    integrations/prowlarr/  integrations/transmission/  integrations/torrent/
    privproto/        wire protocol shared by wargo ↔ wargo-privd
  web/                React app (Vite) → web/dist (embedded)
    src/{app,components,features/*,lib,styles}
  deploy/
    systemd/wargo.service  systemd/wargo-privd.{service,socket}
    pam/wargo
    nginx/wargo.conf  caddy/Caddyfile  cloudflared/README.md
  packaging/          nfpm, goreleaser, AUR PKGBUILD, install.sh
  docs/               PLAN.md, SECURITY.md, INSTALL.md, REVERSE_PROXY.md
  LICENSE  Makefile  go.mod  README.md
```

Config: `/etc/wargo/config.toml` · Data: `/var/lib/wargo/` (`wargo.db`, icons, upload temp)
(for user installs: `$XDG_CONFIG_HOME/wargo`, `$XDG_STATE_HOME/wargo`).

---

## 5. Security model

### 5.1 Process & network
- Runs as the admin host user (needed for PAM self-verification, natural PTY/file ownership).
- Binds `127.0.0.1:2207` by default; `listen` configurable. Port 80 via reverse proxy
  (recommended) or `AmbientCapabilities=CAP_NET_BIND_SERVICE`.
- systemd hardening: `NoNewPrivileges` (off only if sudo elevation is enabled),
  `PrivateTmp`, `ProtectKernelTunables`, `ProtectControlGroups`, `RestrictSUIDSGID`
  (where compatible), `LockPersonality`, `MemoryDenyWriteExecute`.
- `trusted_proxies` config; only then honour `X-Forwarded-For` / `CF-Connecting-IP`.

### 5.2 Authentication
- **Allowed user**: exactly one configured username; must be in the admin group
  (`wheel` / `sudo`, auto-detected, configurable).
- **Login methods**
  1. Passkey (WebAuthn, `userVerification=required`) — sufficient alone.
  2. OS password via PAM **+ TOTP** (TOTP mandatory once enrolled; enrolment forced on first login).
  3. Recovery code (single-use, Argon2id-hashed) → forces re-enrolment.
- **Dedicated PAM service** `/etc/pam.d/wargo`: `pam_unix.so` for auth/account only —
  **no `pam_faillock`**, so internet attackers cannot lock the real OS account.
- **Throttling before PAM**: per-IP exponential backoff, global attempt budget,
  constant-time responses, generic error messages.
- **Sessions**: 256-bit random IDs, stored hashed; cookie `wargo_session`
  `HttpOnly; Secure; SameSite=Strict; Path=/`; idle timeout 30 min, absolute 12 h (configurable).
- **CSRF**: double-submit token header `X-Wargo-CSRF` on all state-changing requests.
- **WebSockets**: strict `Origin` allow-list + short-lived single-use WS tickets.
- **WebAuthn origins**: configurable allow-list of RP IDs/origins (e.g. tunnel domain + LAN hostname);
  passkeys are per RP ID. Requires HTTPS (or `localhost`).
- Optional: validate `Cf-Access-Jwt-Assertion` when Cloudflare Access is configured.
- Local recovery CLI: `wargo admin reset-auth` (requires shell access on the host).

### 5.3 Privilege elevation (no standing root by default)
| Mode | Mechanism |
|---|---|
| **sudo (default)** | UI prompts for sudo password → `sudo -k -S -- <fixed argv>`; password piped via stdin, zeroed after use, never stored/logged. Throttled (sudo uses `system-auth` → faillock). |
| **privd (opt-in, M11)** | `wargo-privd` root service on a unix socket (peer-cred checked). Holds passkey public keys; issues challenges bound to `sha256(action ‖ args ‖ nonce ‖ expiry)`; verifies the WebAuthn assertion itself before acting. A compromised `wargo` cannot escalate without the authenticator. sudo remains fallback. |

Elevated operations (closed allow-list): signal foreign process, privileged file op,
package refresh/upgrade (via `systemd-run`), reboot/poweroff, compose update, root terminal.

`wargo helper <op>` is a sub-command of the same binary run under sudo, with strict argv
validation (no shell, no globbing).

### 5.4 HTTP hardening
CSP (`default-src 'self'`, no inline script), HSTS (when behind TLS), `X-Frame-Options: DENY`,
`Referrer-Policy: same-origin`, `Permissions-Policy`, body size limits per route,
read/write timeouts, JSON-only APIs.

### 5.5 Audit
Append-only `audit_log` table: time, IP, UA, session, action, target, result, elevation method
(sudo / passkey id). Viewable in Settings → Audit. Rotation by age/size.

---

## 6. Data model (SQLite)

```
settings(key PK, value JSON)
sessions(id_hash PK, created_at, last_seen, expires_at, ip, ua, auth_method)
totp(secret_enc, enabled, created_at)
webauthn_credentials(id PK, name, public_key, sign_count, aaguid, transports, rp_id, created_at, last_used)
recovery_codes(hash PK, used_at)
login_attempts(ip, ts, ok)
audit_log(id PK, ts, ip, ua, session, action, target, result, elevation, details JSON)
jobs(id PK, kind, state, progress, started_at, finished_at, unit, log_path, result JSON)
launcher_tiles(id PK, name, url, icon, group_name, sort, new_tab, health_url, enabled)
integrations(name PK, enabled, base_url, secret_enc, extra JSON)
```

Secrets (TOTP, API keys, Transmission password) encrypted with a key in
`/var/lib/wargo/secret.key` (0600).

---

## 7. API surface (summary)

```
POST   /api/auth/login                {username,password} → needs_totp
POST   /api/auth/totp                 {code}
POST   /api/auth/passkey/begin|finish
POST   /api/auth/recovery
POST   /api/auth/logout
GET    /api/auth/me
POST   /api/auth/ws-ticket
*      /api/auth/{totp,passkeys,recovery-codes}   (manage)

POST   /api/elevate/begin             {action,args} → {method: sudo|passkey, challenge?}
(elevated endpoints accept {elevation: {password}|{assertion}})

GET    /api/system/info
GET    /api/system/metrics/history
POST   /api/system/reboot             (elevated)

GET    /api/procs?sort=&q=            GET /api/procs/{pid}
POST   /api/procs/{pid}/signal        {signal}  (elevated if foreign)

GET    /api/containers                GET /api/containers/{id}
POST   /api/containers/{id}/{start|stop|restart}
GET    /api/containers/{id}/logs      (WS/stream)
POST   /api/containers/{id}/update    {strategy: api|compose}
GET    /api/containers/updates        (digest check)

POST   /api/term                      {kind: host|container|root, target?} → {id}
DELETE /api/term/{id}

GET    /api/files/list?root=&path=    GET /api/files/stat
GET    /api/files/download?…          (Range; folders → zip stream)
POST   /api/files/{mkdir|rename|move|copy|delete|chmod|chown|archive|extract}
POST   /api/files/upload/init|chunk|complete       (resumable)
GET|PUT /api/files/content            (editor, size-capped)

GET    /api/pkg/backend               POST /api/pkg/check
GET    /api/pkg/updates               POST /api/pkg/upgrade (elevated) → job

GET    /api/integrations              (capabilities)
PUT    /api/integrations/{name}
GET    /api/prowlarr/search?q=&categories=
POST   /api/prowlarr/grab             {guid,indexerId}
GET    /api/prowlarr/magnet?guid=&indexerId=
GET    /api/prowlarr/torrent?guid=&indexerId=
GET    /api/transmission/torrents
POST   /api/transmission/torrents     {magnet|torrentB64}
POST   /api/transmission/torrents/{id}/{start|stop}
DELETE /api/transmission/torrents/{id}?deleteData=bool

GET|POST|PUT|DELETE /api/launcher/tiles   GET /api/launcher/suggest
GET    /api/jobs                      GET /api/jobs/{id}
GET    /api/audit
GET|PUT /api/settings
```

---

## 8. Feature specs

### 8.1 Overview
CPU (total, per-core, load, freq, model), RAM/swap/cache, temperatures (hwmon: CPU
package, NVMe, GPU), disks (usage + IO), network (per-interface throughput),
uptime/kernel/distro, container summary, pending-updates badge (from last manual check),
optional launcher section. 10-min in-memory history for sparklines.

### 8.2 Processes
Virtualised sortable/searchable table (PID, user, CPU%, mem, state, start, command), tree view,
details drawer. SIGTERM / SIGKILL / SIGHUP with confirm; foreign processes require elevation.
Polling only while the page is open.

### 8.3 Containers
List with state, health, uptime, image, ports, live CPU/mem. Start/stop/restart, streamed logs
(follow, search, timestamps), exec shell (via terminal module).
**Update**:
- Default (no root): pull image → inspect → recreate with identical config, networks, mounts and
  labels (incl. compose labels so CasaOS still recognises it) → remove old on success,
  rollback on failure.
- Compose (elevated when files unreadable): `docker compose -p <project> pull && up -d`
  in the project's working dir (from labels).
- "Update available": compare local repo digest vs registry manifest digest (on demand).
- Network-mode `container:<id>` resolution (e.g. Transmission behind gluetun) for port display.
- CasaOS awareness only when `/var/lib/casaos` exists.

### 8.4 Terminal
xterm.js, tabs, resize, copy/paste, search, web links, font size. Host shell (user's login shell),
container exec, root shell (`sudo -i` in PTY, or privd). Optional tmux attach for persistence.
Sessions reaped after idle timeout.

### 8.5 File manager
Configurable roots (default: `$HOME`, `/DATA` if present). All ops through `os.Root`.
List/grid, breadcrumbs, multi-select, keyboard shortcuts, drag-and-drop.
Ops: mkdir, rename, move, copy, delete (optional trash dir), chmod/chown, zip/tar archive & extract
(with zip-slip protection and size limits), properties.
Uploads: chunked (default 8 MB, below Cloudflare's 100 MB limit), resumable, hashed.
Downloads: streaming with Range; folders as streamed zip.
Preview: image, video, audio, PDF, text; edit text in CodeMirror (size cap, conflict detection by mtime).
Long ops become jobs with progress over `/ws/events`. Privileged ops via elevation.

### 8.6 Prowlarr & Transmission
- **Detection**: Docker labels/image names (`*prowlarr*`, `*transmission*`), follow
  `network_mode: container:` to find published ports; health via `/api/v1/system/status`
  and Transmission `session-get`. Tabs appear only when available + configured.
- **Config**: base URLs; Prowlarr API key (paste, or offer to read `<ApiKey>` from a detected
  `config.xml` in the container's `/config` bind mount, with consent); Transmission
  credentials (prefilled host/port from Prowlarr download-client settings; password entered once).
- **Search**: all enabled indexers (`GET /api/v1/search?query=&type=search`), merged sortable table
  (title, size, seeders/leechers, indexer, age, categories), client-side filter.
  Download URLs stripped server-side (they contain the API key).
- **Per-result actions**:
  - *Copy magnet* — magnetUrl, else server-side resolve downloadUrl (no redirect follow):
    `magnet:` redirect → use; `.torrent` → bencode-parse (size-capped) → info-hash → build magnet with trackers. Cached.
    Clipboard API on HTTPS; fallback pre-selected text box on HTTP.
  - *Download .torrent* — proxied through Wargo with `Content-Disposition`.
  - *Queue in Transmission* — preferred: Prowlarr grab (`POST /api/v1/search {guid,indexerId}`)
    when Prowlarr has an enabled Transmission client; fallback: direct `torrent-add`.
  - Disabled actions show the reason in a tooltip.
- **Downloads tab**: `torrent-get` every 2 s while open (pushed via `/ws/events`):
  name, status, progress, ↓/↑ rate, ETA, size, ratio, error. Actions: pause (`torrent-stop`),
  resume (`torrent-start`), remove → dialog **Cancel / Remove torrent / Remove with data**
  (Cancel default; destructive styled). Handles the `X-Transmission-Session-Id` 409 handshake.
- Capability flags: `prowlarr.available`, `prowlarr.hasTransmissionClient`, `transmission.available`.

### 8.7 OS updates
Explicit user action only. Backend interface:

```go
type Backend interface {
    Name() string
    Refresh(ctx context.Context, e Elevation) error
    ListUpdates(ctx context.Context) ([]Update, error)
    Upgrade(ctx context.Context, e Elevation, sink ProgressSink) error
    RebootRequired(ctx context.Context) (bool, string)
}
```

| Family | Check | Upgrade | Progress |
|---|---|---|---|
| Manjaro | `pamac checkupdates` (+ `--aur` read-only) | `pacman -Syu --noconfirm` | parse `( n/m) upgrading …` |
| Arch/Endeavour | `checkupdates` (pacman-contrib) or elevated refresh + `pacman -Qu` | `pacman -Syu --noconfirm` | same |
| Debian/Ubuntu/RPi OS | `apt-get update` (elev) + `apt-get -s full-upgrade` | `apt-get -y full-upgrade` (noninteractive, `--force-confold`) | `APT::Status-Fd` |
| Fedora/RHEL family | `dnf check-update` | `dnf -y upgrade` | parse |
| openSUSE | `zypper --xmlout list-updates` | `zypper -n --xmlout update` | XML |
| Alpine | `apk version -l '<'` | `apk upgrade` | parse |
| Flatpak (optional) | `flatpak remote-ls --updates` | `flatpak update -y` | parse |

Safety: run upgrade as `systemd-run --unit=wargo-upgrade-<id> --collect` (survives Wargo restarts;
Wargo re-attaches to the log), lock-file pre-check, disk-space check, partial-upgrade warning on
Arch, reboot-required detection (`/var/run/reboot-required`, missing running kernel modules dir,
`dnf needs-restarting -r`), list new `.pacnew`/`.dpkg-dist` files. AUR shown read-only.
UI: check → table (name, current, new, repo, size) → Update all → stages, overall + per-package
progress, collapsible live log (xterm), result summary, reboot button.

### 8.8 App launcher (optional)
Toggle in Settings. Tiles: name, URL, icon (upload or dashboard-icons, cached locally), group,
order, new-tab, optional health URL (status dot). "Suggest from containers" uses published ports.
Show on overview or as its own page.

---

## 9. Frontend design system ("Northern")

- Dark-first; cold slate/ice neutrals; **one** warm accent (ember amber) for status & primary actions;
  semantic colours for ok/warn/crit used sparingly.
- Dense information layout; 1 px borders; minimal shadows; no gradients/glass/emoji.
- Typography: IBM Plex Sans (UI), JetBrains Mono (numbers, paths, logs), `tabular-nums` everywhere.
- Motion only for state change (progress, sparklines, toasts).
- Layout: collapsible sidebar (desktop) → bottom nav / sheet (mobile); command palette (⌘K).
- Accessibility: Radix primitives, focus rings, reduced-motion support.
- Self-hosted fonts and icons (no third-party requests; CSP-friendly).

---

## 10. Milestones & acceptance criteria

1. **Skeleton** — Go module, config loader, embedded Vite/React app, stdlib router, security headers,
   structured logging, systemd unit, nginx/cloudflared snippets, LICENSE (AGPL), Makefile.
   *AC*: `make build` yields one binary; serves SPA on `127.0.0.1:2207`; RSS < 20 MB idle.
2. **Auth** — PAM service file, throttling, sessions, CSRF, WS tickets, TOTP enrolment/verify,
   passkey register/login, recovery codes, `wargo admin reset-auth`, audit log.
   *AC*: cannot reach any API unauthenticated; 3 wrong passwords do **not** trigger faillock;
   passkey login works over HTTPS tunnel.
3. **Sudo elevation** — `/api/elevate`, `wargo helper`, UI prompt component.
   *AC*: password never in logs/DB; `-k` always used; throttled.
4. **Overview** — collector, ring buffer, `/ws/metrics`, dashboard cards/charts, responsive layout.
   *AC*: 1 s updates on desktop & phone; collector idles when no subscribers.
5. **Processes** — list/tree/search/sort, signal (elevated when foreign).
6. **Containers** — list, stats, logs, actions, digest check, API-recreate update with rollback,
   compose update (elevated), CasaOS awareness, `container:` network resolution.
7. **Prowlarr & Transmission** — detection, config UI, search, three result actions, downloads tab,
   remove dialog.
8. **Terminal** — host/container/root PTYs, tabs, resize, optional tmux.
9. **File manager** — all ops, chunked uploads, range downloads, preview/editor, jobs, elevation.
10. **OS updates** — pacman/pamac → apt → dnf/zypper/apk (+flatpak), systemd-run jobs, reboot flow.
11. **wargo-privd** — root helper, socket activation, passkey-bound action approvals.
12. **Packaging & release** — GoReleaser/nfpm (amd64, arm64), AUR, install.sh, first-run wizard
    (distro, Docker access, admin group, proxy headers), docs (INSTALL, SECURITY, REVERSE_PROXY).
13. **Polish** — light theme, launcher refinements, notifications (temp high, container unhealthy,
    updates available after manual check), PWA install.

---

## 11. Testing

- Go unit tests per package; table tests for parsers (pacman/apt/zypper output, bencode, Prowlarr JSON).
- Integration tests with fake backends (Transmission RPC & Prowlarr via `httptest`).
- `os.Root` escape tests (symlinks, `..`, absolute paths) for the file manager.
- Distro matrix for `pkg` via containers (check/list only) in CI.
- Frontend: Vitest + Testing Library; Playwright smoke tests (login, overview, terminal).
- `govulncheck`, `staticcheck`, `npm audit` in CI.

---

## 12. Environment notes (dev host)

- Manjaro, kernel 6.18, Go 1.26, Node (latest), Docker; user `kushal` in `wheel`, `docker`.
- PAM `system-auth` uses `pam_faillock` (defaults: deny=3, unlock 10 min) → dedicated PAM file required.
- `pamac 11.7.4` present; `checkupdates` (pacman-contrib) absent; `systemd-run` present.
- Containers: `prowlarr` (:9696, config at `/DATA/AppData/prowlarr/config`),
  `transmission` via `gluetun` network (:9091). CasaOS apps under `/var/lib/casaos/apps` (root-only).

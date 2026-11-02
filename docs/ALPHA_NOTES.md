# Wargo alpha — status notes

State of the first alpha (`0.1.0-alpha`), what was verified, and the known
gaps to pick up next.

## Try it

```sh
make                                     # web + bin/wargo + bin/wargo-privd
cp config.example.toml dev.local.toml    # set data_dir; for a quick look add:
#   [dev]
#   auth_bypass = true                   # loopback only, never in production
./bin/wargo -config dev.local.toml       # http://127.0.0.1:2207
```

For a real install with login, see [INSTALL.md](INSTALL.md). Password sign-in
needs `/etc/pam.d/wargo` (from `deploy/pam/wargo`).

## Feature status

| Area | Status | Verified against |
|---|---|---|
| Overview (CPU, cores, memory, sensors, fans, network, disk I/O, drives, filesystems) | Done | Real host |
| Processes (list, tree, details, signals) | Done | Real host (read); signals only on own test processes; root process shows approval prompt (cancelled) |
| Containers (list, health, ports, stats, logs, update check) | Done | Real Docker (read-only) |
| Container start/stop/restart/update with rollback | Done | Fake Docker API only |
| Terminal (host, root, container shells, tabs, reconnect) | Done | Host shell live; root/container argv by unit test |
| File manager (browse, preview, edit, upload, archive, permissions) | Done | Sandbox folder; home folder browsed read-only |
| OS updates (check, upgrade via systemd-run, reboot detection) | Done | Real `pamac checkupdates`; upgrade via simulated script only |
| Prowlarr search, magnet, .torrent, queue | Done | Real Prowlarr (search/magnet read-only); queue via fakes |
| Transmission downloads (pause, resume, remove dialog) | Done | Fake Transmission RPC (real one needs your RPC password) |
| App launcher | Done | Real container suggestions |
| Settings (appearance, security, integrations, launcher, audit, about) | Done | Dev instance + virtual authenticator |
| Auth: PAM + TOTP, passkeys, recovery codes, sessions | Done | Unit tests + virtual authenticator; PAM login **not** exercised (needs your password) |
| Admin approval via sudo | Done | Prompt flow verified; no sudo ever executed |
| wargo-privd (passkey-approved root actions) | Done | Ran as normal user on a temp socket; root path not exercised |
| Packaging (install.sh, nfpm, AUR, CI) | Drafted | Not run |

Every page passes `web/scripts/audit.mjs` (no horizontal overflow, touch
targets ≥ 24px on phones, axe-core WCAG 2.1 AA) at desktop, tablet and phone
in light and dark.

Memory: ~23 MB RSS idle, ~42 MB with several live pages streaming (Go heap
~5–6 MB; the rest is runtime and mapped binary). Main JS bundle 140 KB gzip;
terminal, editor and charts load on demand.

## Before first real use

1. `sudo install -m 0644 deploy/pam/wargo /etc/pam.d/wargo` — without it PAM
   falls back to the `other` service.
2. Set `auth.origins` to your public URL(s), e.g. the cloudflared hostname, so
   WebSockets and passkeys work behind the proxy.
3. In Settings → Integrations, connect Prowlarr ("Detect" + "Import API key")
   and enter the Transmission RPC password.
4. Consider Cloudflare Access in front of the hostname.

## Known gaps / next steps

- **Containers:** compose updates need the `docker compose` plugin (not
  installed here) and CasaOS compose files are root-only, so "auto" uses API
  recreation (labels preserved). No registry auth for private images.
  Update-check results are in memory only.
- **Updates:** dnf/zypper/apk paths are tested only with sample output. Running
  upgrades can't be cancelled (intentional). Download sizes are often unknown.
- **Files:** no server-side thumbnails (grid loads full images). Recursive
  chmod/chown and trash moves don't elevate. Listings cap at 20k entries.
  Interrupted uploads need the file re-selected after a reload.
- **Terminal:** tmux mode untested (tmux not installed). Tab names stored per
  browser.
- **Torrents:** some indexers report a .torrent link that actually redirects
  to a magnet; the button is disabled after the first attempt.
- **privd:** the socket-activation unit needs `SocketGroup` set by hand; the
  plain service unit works without it.
- **Launcher:** health checks accept self-signed TLS (they only probe
  reachability).
- Package/AUR/CI files have never been run.

# Wargo release candidate: status notes

Where the release candidate (`v1.0.0-rc`) stands: what was tested and how, and
the known gaps to pick up next.

## Try it from source

```sh
make                                     # web UI + bin/wargo + bin/wargo-privd
cp config.example.toml dev.local.toml    # set data_dir; for a quick look add:
#   [dev]
#   auth_bypass = true                   # only works on 127.0.0.1, never use it on a real server
./bin/wargo -config dev.local.toml       # http://127.0.0.1:2207
```

For a real install with sign-in, see [INSTALL.md](INSTALL.md). Password
sign-in needs `/etc/pam.d/wargo` (from `deploy/pam/wargo`).

## Feature status

| Area | Status | Tested against |
|---|---|---|
| Home (live tiles, host details, feed, weather) and System → Activity Monitor (CPU per core, memory, sensors, fans, network, public IP, disk activity, drives, partitions) | Done | Real server; weather against Open-Meteo |
| Processes (list, tree, details, signals) | Done | Real server (reading); signals only on test processes; a root process shows the approval prompt (cancelled) |
| System → Events (journal, kernel, containers, Wargo) | Done | Real journal on Manjaro, natively and from the container; Docker events with a fake Docker |
| Containers (list, health, ports, usage, logs, update check) | Done | Real Docker (read only) |
| Container start, stop, restart and update with rollback | Done | Fake Docker API only |
| Terminal (host, root and container shells, tabs, reconnect) | Done | Host shell live; root and container commands by unit test |
| File manager (browse, preview, edit, upload, archives, permissions) | Done | Test folder; home folder browsed read only |
| OS updates (check, background check, upgrade through systemd-run, restart detection) | Done | Real `pamac checkupdates`; upgrades only with a simulated script |
| Prowlarr search, magnet, .torrent, queue | Done | Real Prowlarr (search and magnet read only); queue with fakes |
| Transmission downloads (pause, resume, remove) | Done | Fake Transmission (the real one needs your password) |
| Plex (now playing, recently watched and added on Home; token import) | Done | Real Plex Media Server (read only) |
| App launcher (square tiles, edit, drag to reorder) | Done | Real container suggestions; order saved on the server |
| Settings (general, appearance with accent colours, integrations, security, about with the audit log) | Done | Dev instance and a virtual authenticator |
| Sign-in: PAM plus two-factor code, passkeys, recovery codes, sessions (remembered sign-ins, one per browser) | Done | Unit tests, a virtual authenticator and passkey sign-in on the main install; native PAM sign-in not tried yet (needs your password) |
| Admin approval through sudo | Done | Prompt tested; sudo never actually run |
| wargo-privd (passkey-approved root actions) | Done | Run as a normal user on a test socket; the root path not tried |
| Container mode (Docker image, compose) | Done | Running privileged on the author's server as the daily install: sign-in against the host's shadow file, two-factor, terminal, files, metrics and events |
| Packaging and CI (install.sh, deb/rpm/apk/Arch packages, images) | Done | CI builds both architectures and all packages; the first tagged release is their first public run |

Every page passes `web/scripts/audit.mjs` (no sideways scrolling, touch
targets of at least 24 px on phones, axe-core WCAG 2.1 AA) at desktop, tablet
and phone sizes, in light and dark themes.

Memory: about 23 MB idle and about 42 MB with several live pages open (Go
itself uses 5 to 6 MB; the rest is the runtime and the program file). The
main JavaScript bundle is about 110 KB compressed; the terminal, editor and charts
load only when needed.

## Before first real use

1. Native install: `sudo install -m 0644 deploy/pam/wargo /etc/pam.d/wargo`
   (the installer does this). Without it, PAM falls back to the `other`
   service.
2. Set `auth.origins` (Docker: `WARGO_ORIGINS`) to your public address, for
   example your Cloudflare Tunnel hostname, so WebSockets and passkeys work
   behind the proxy.
3. In Settings → Integrations, connect Prowlarr ("Detect", then "Import API
   key"), enter the Transmission password, and connect Plex ("Detect", then
   "Import token").
4. Consider putting Cloudflare Access in front of the hostname.

## Known gaps and next steps

- **Containers:** compose updates need the `docker compose` plugin, and
  CasaOS compose files can only be read by root, so "auto" recreates
  containers through the Docker API instead (keeping their labels). Private
  images that need a registry login aren't supported.
- **Updates:** dnf, zypper and apk are tested only with sample output.
  Running upgrades can't be cancelled (on purpose). Download sizes are often
  unknown.
- **Files:** no thumbnails made on the server (the grid loads full images).
  Changing permissions or owners recursively, and moving to the trash, don't
  ask for admin approval. Folder listings stop at 20,000 entries. An
  interrupted upload needs the file picked again after a page reload.
- **System → Events:** reads the systemd journal (hosts without systemd have
  none), Docker's recent event history (cleared when Docker restarts) and
  Wargo's audit log. Natively, system and kernel messages need your account
  in `systemd-journal`, `adm` or `wheel`. Container health changes seen only
  by Wargo's own check ("Not responding") aren't recorded yet.
- **Terminal:** tmux mode hasn't been tried (tmux isn't installed here). Tab
  names are saved per browser.
- **Torrents:** some indexers give a .torrent link that actually redirects to
  a magnet link; the button is disabled after the first try.
- **privd:** the socket-activated unit needs `SocketGroup` set by hand; the
  plain service unit works without it.
- **Launcher:** health checks accept self-signed HTTPS certificates (they
  only check that the app answers).
- **AUR package:** the PKGBUILD hasn't been published or built yet.

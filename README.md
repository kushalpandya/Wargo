# Wargo

A single-binary, low-memory dashboard for running a homelab server — inspired
by the wargs of *A Song of Ice and Fire*, who see through other creatures' eyes.

Wargo replaces a handful of separate tools with one responsive web app you can
safely publish through a reverse proxy:

- **Overview** — live CPU (per core), memory, temperatures and fans, network
  and disk throughput with 10-minute history, installed drives and filesystem
  usage, and container health at a glance.
- **Processes** — searchable, sortable process list and tree; signal or kill
  processes (other users' processes need admin approval).
- **Containers** — status, health, ports, live resource usage, logs, start /
  stop / restart, registry update checks and one-click updates (compose-aware,
  CasaOS-friendly, with rollback).
- **Terminal** — full PTY terminal with tabs, host / root / container shells,
  reconnect after reloads and a mobile key bar.
- **Files** — complete file manager confined to configured roots: browse,
  upload (chunked and resumable), download, rename, move, copy, trash,
  permissions, archives, previews and a code editor.
- **Updates** — check and install OS package updates on demand (pamac, pacman,
  apt, dnf, zypper, apk; AUR and Flatpak listed read-only) with live progress,
  running in a separate systemd unit so nothing is left half-installed.
- **Torrents** — search every Prowlarr indexer, copy magnets, download
  `.torrent` files or queue them in Transmission, and manage downloads.
- **Apps** — an optional launcher of your self-hosted services with health
  status, suggested from your running containers.

Light, dark and system themes; works from phones to wide desktops.

## Security at a glance

Sign in with your **host account** (PAM) plus **TOTP**, or with a
**passkey**. Wargo never holds root: actions that need it ask for your sudo
password (used once, never stored) or a passkey approval via the optional
`wargo-privd` helper. Logins are throttled without touching `pam_faillock`, so
nobody can lock your real account from the internet. Details in
[docs/SECURITY.md](docs/SECURITY.md).

## Install

See [docs/INSTALL.md](docs/INSTALL.md) and
[docs/REVERSE_PROXY.md](docs/REVERSE_PROXY.md). In short:

```sh
make
sudo ./packaging/install.sh
sudo systemctl enable --now wargo@$USER
# open http://127.0.0.1:2207
```

Or run the container image (privileged, host namespaces — read
[docs/DOCKER.md](docs/DOCKER.md) first):

```sh
WARGO_USER=$USER docker compose -f deploy/docker/compose.yaml up -d
```

## Stack

Go (stdlib `net/http`, gopsutil, creack/pty, `os.Root`, SQLite, PAM,
go-webauthn) serving an embedded React + Tailwind single-page app (Radix
primitives, TanStack Query, uPlot, xterm.js, CodeMirror). Docker is spoken to
directly over its socket without the SDK. Idle memory use is around 25 MB.

## Status

Alpha. Expect rough edges; please file issues.

## License

[AGPL-3.0-only](LICENSE)

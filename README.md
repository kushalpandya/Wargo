<p align="center">
  <img src="web/public/favicon.svg" width="72" height="72" alt="">
</p>

<h1 align="center">Wargo</h1>

<p align="center">
  A fast, single-binary control panel for your homelab server.<br>
  Monitor it, manage containers, open a terminal, browse files and install updates — from any browser.
</p>

---

<!-- Screenshot: docs/screenshots/home.png -->

## What you get

- **Home** — CPU, memory, network and disks at a glance, your machine's specs and every container's health and version.
- **System** — live charts, sensors and fans, drives and the process list (kill or signal processes).
- **Containers** — status, logs, live usage, start/stop/restart, and one-click image updates with rollback.
- **Files** — a full file manager with split view, drives, bookmarks, uploads, previews and a code editor.
- **Terminal** — real shells in your browser, including root and container shells, with tabs.
- **Updates** — check and install OS package updates and stale container images when *you* choose.
- **Torrents** — search all your Prowlarr indexers and manage Transmission downloads.
- **Apps** — an optional launcher for your other self-hosted services.

Light and dark themes, works on phones, uses about 25 MB of RAM.

## Install

On the server, as your normal (sudo-capable) user:

```sh
curl -fsSL https://raw.githubusercontent.com/kushalpandya/wargo/main/install.sh | sh
```

Open **http://127.0.0.1:2207** and sign in with your usual Linux username and
password. Prefer Docker? See the [install guide](docs/INSTALL.md#docker-install).

To use it from other devices, put it behind HTTPS (Cloudflare Tunnel, Caddy
or nginx) — [here's how](docs/INSTALL.md#access-from-anywhere).

## Secure by design

- Sign in with your server account plus two-factor, or with a passkey.
- Wargo never holds root: admin actions ask for your sudo password or a passkey every time.
- Failed logins can't lock you out of your server; they're throttled by Wargo.
- Every file operation is confined to the folders you choose; every admin action is audited.

Read more in [SECURITY.md](docs/SECURITY.md).

## Status

Wargo is in **alpha**. It works well on the author's server, but expect rough
edges and breaking changes before 1.0. Feedback and bug reports are very
welcome in [issues](https://github.com/kushalpandya/wargo/issues).

## Documentation

[Install](docs/INSTALL.md) · [Docker](docs/DOCKER.md) · [Reverse proxy](docs/REVERSE_PROXY.md) · [Security](docs/SECURITY.md) · [Contributing](CONTRIBUTING.md)

## License

[AGPL-3.0](LICENSE) © Kushal Pandya

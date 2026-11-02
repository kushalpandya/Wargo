<p align="center">
  <img src="web/public/favicon.svg" width="72" height="72" alt="">
</p>

<h1 align="center">Wargo</h1>

<p align="center">
  A fast, single-binary control panel for your homelab server.<br>
  Watch it, manage containers, open a terminal, browse files and install updates, all from your browser.
</p>

---

<!-- Screenshot: docs/screenshots/home.png -->

## What you get

- **Home:** CPU, memory, network and disks at a glance, your server's specs, every container's health, and your app shortcuts.
- **System:** live charts for each CPU core, sensors, network, drives and partitions, a process list, and an events log of recent errors and warnings.
- **Containers:** status, logs and live usage. Start, stop or restart them, and update images in one click (rolled back if the update fails).
- **Files:** a full file manager with split view, drives, bookmarks, uploads, previews and a code editor.
- **Terminal:** real shells in your browser, including root and container shells, in tabs.
- **Updates:** check for and install OS updates and newer container images, only when you choose.
- **Torrents:** search all your Prowlarr indexers and manage Transmission downloads.
- **Apps:** shortcuts to your other self-hosted services, shown on Home or on their own page.

Light and dark themes, works on phones, and uses about 25 MB of memory.

## Install

On the server, as your normal user (one that can use sudo):

```sh
curl -fsSL https://raw.githubusercontent.com/kushalpandya/wargo/main/install.sh | sh
```

Then open **http://127.0.0.1:2207** on the server and sign in with your usual
Linux username and password. Prefer Docker? See the
[install guide](docs/INSTALL.md#docker-install).

To use Wargo from other devices, put it behind HTTPS (Cloudflare Tunnel, Caddy
or nginx). [Here's how](docs/INSTALL.md#access-from-anywhere).

## Secure by design

- Sign in with your server account plus a two-factor code, or with a passkey.
- Wargo never keeps root access. Admin actions ask for your sudo password or a passkey every time.
- Wrong passwords are slowed down by Wargo, so they can't lock your account on the server.
- File operations stay inside the folders you choose, and every admin action is logged.

Read more in [SECURITY.md](docs/SECURITY.md).

## Status

Wargo is in **alpha**. It works well on the author's server, but expect rough
edges and breaking changes before 1.0. Feedback and bug reports are very
welcome in [issues](https://github.com/kushalpandya/wargo/issues).

## Documentation

[Install](docs/INSTALL.md) · [Docker](docs/DOCKER.md) · [Reverse proxy](docs/REVERSE_PROXY.md) · [Security](docs/SECURITY.md) · [Contributing](CONTRIBUTING.md)

## License

[AGPL-3.0](LICENSE) © Kushal Pandya

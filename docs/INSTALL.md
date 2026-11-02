# Installing Wargo

Wargo runs on any 64-bit Linux server (x86-64 or ARM64). Pick **one** way to
install it:

| | Native (recommended) | Docker |
|---|---|---|
| Needs | systemd, glibc 2.31+ (Debian 11+, Ubuntu 20.04+, Fedora, Arch/Manjaro, openSUSE…) | Docker with Compose |
| Install | one command | one `compose.yaml` |
| Admin actions | your sudo password or a passkey, enforced by the OS | your password or a passkey, enforced by Wargo |

Both give you every feature. The native install is a little safer because
Wargo itself never runs as root; see [SECURITY.md](SECURITY.md).

---

## Native install

Run this as **your normal user** (the account you'll sign in with; it must be
in the `wheel` or `sudo` group). It asks for your sudo password once.

```sh
curl -fsSL https://raw.githubusercontent.com/kushalpandya/wargo/main/install.sh | sh
```

Then open **http://127.0.0.1:2207** on the server, sign in with your usual
username and password, and set up two-factor authentication when asked.

That's it. The installer:

- downloads the latest release for your CPU and verifies its checksum,
- installs `/usr/local/bin/wargo`, a systemd service (`wargo@<you>`), a small
  PAM file and `/etc/wargo/config.toml`,
- starts Wargo on `127.0.0.1:2207`, running as your user (not root).

**Upgrade:** run the same command again — your settings are kept.
**Specific version:** `… | sh -s -- --version v1.0.0-alpha.1`
**Uninstall:** `… | sh -s -- --uninstall` (add `--purge` to delete data too)
**Preview:** `… | sh -s -- --dry-run` shows every step without changing anything.

<details>
<summary>Prefer to look before you run?</summary>

```sh
curl -fsSLO https://raw.githubusercontent.com/kushalpandya/wargo/main/install.sh
less install.sh
sh install.sh
```

Offline / air-gapped: download `wargo_<version>_linux_<arch>.tar.gz` from the
[releases page](https://github.com/kushalpandya/wargo/releases) and run
`sh install.sh --archive wargo_<version>_linux_<arch>.tar.gz`.
</details>

Useful commands:

```sh
systemctl status wargo@$USER        # is it running?
journalctl -u wargo@$USER -f        # logs
sudo systemctl restart wargo@$USER  # after editing /etc/wargo/config.toml
```

---

## Docker install

```sh
mkdir wargo && cd wargo
curl -fsSLO https://raw.githubusercontent.com/kushalpandya/wargo/main/deploy/docker/compose.yaml
WARGO_USER=$USER docker compose up -d
```

Open **http://127.0.0.1:2207** and sign in with your **host** account's
username and password.

The container needs broad access to the host (privileged, host PID and
network, the host filesystem at `/host`) so it can show processes, open
shells and install updates. It is not a sandbox — treat it like the native
install. Details, settings and the reasons for each option are in
[DOCKER.md](DOCKER.md).

**Upgrade:** `docker compose pull && docker compose up -d`

---

## Access from anywhere

Wargo listens only on `127.0.0.1` so it's never exposed by accident. To use
it from your phone or another computer, put it behind HTTPS and tell Wargo
its public address.

1. Pick a way to publish it (any one):
   - **Cloudflare Tunnel** — point a hostname at `http://127.0.0.1:2207`.
   - **Caddy** — `wargo.example.com { reverse_proxy 127.0.0.1:2207 }`
   - **nginx** — see [`deploy/nginx/wargo.conf`](../deploy/nginx/wargo.conf)
     (it needs the WebSocket headers shown there).
2. Add the public URL to `/etc/wargo/config.toml` (Docker: the
   `WARGO_ORIGINS` variable):

   ```toml
   [auth]
   origins = ["https://wargo.example.com"]
   ```

3. Restart Wargo (`sudo systemctl restart wargo@$USER`).

Passkeys only work over HTTPS. More options, including protecting the
hostname with Cloudflare Access, are in [REVERSE_PROXY.md](REVERSE_PROXY.md).

---

## After installing

- **Two-factor:** required on first sign-in. Add a passkey in
  Settings → Security for one-tap sign-in.
- **Locked out?** On the server run `wargo admin reset-auth` (Docker:
  `docker exec wargo wargo admin reset-auth`), then sign in with your password.
- **Integrations:** connect Prowlarr and Transmission in Settings →
  Integrations ("Detect" finds them if they run in Docker).
- **Optional — passkeys for admin actions (native only):** see
  [Passkey approvals](#optional-passkey-approvals-native) below.

## Troubleshooting

| Problem | Fix |
|---|---|
| "Invalid username or password" with the right password | Make sure `/etc/pam.d/wargo` exists (the installer adds it) and that your account is in `wheel`/`sudo`. |
| "421 Misdirected Request" or "This address isn't allowed" | Add the URL you use to `auth.origins` (Docker: `WARGO_ORIGINS`) and restart. Wargo only answers host names it knows, to block DNS-rebinding attacks. |
| Live charts or the terminal don't load behind a proxy | Add the public URL to `auth.origins`, and make sure the proxy forwards WebSockets. |
| "Passkeys require HTTPS" | Use the HTTPS address, not `http://<ip>:2207`. |
| Admin approval says sudo is blocked | Don't add `NoNewPrivileges=yes` to the service; sudo needs it off. |
| Service won't start | `journalctl -u wargo@$USER -e` shows why (usually a typo in the config). |

## Optional: passkey approvals (native)

By default, admin actions (killing another user's process, installing
updates, rebooting…) ask for your sudo password. You can approve them with a
passkey instead by enabling the small root helper `wargo-privd`:

```sh
sudo install -m 0600 /dev/stdin /etc/wargo/privd.toml <<EOF
allowed_uid = $(id -u)
origins = ["https://wargo.example.com"]
EOF
sudo systemctl enable --now wargo-privd
```

Then in Settings → Security → Admin approvals, choose which passkeys to
trust (asks for your sudo password once).

## Building from source

Requirements: Go 1.26+, Node.js 22+, a C compiler and PAM headers
(`pam` on Arch, `libpam0g-dev` on Debian/Ubuntu, `pam-devel` on Fedora).

```sh
git clone https://github.com/kushalpandya/wargo && cd wargo
make                                   # web UI + bin/wargo + bin/wargo-privd
tar -czf /tmp/wargo_dev_linux_amd64.tar.gz --transform 's#^bin/##' \
  bin/wargo bin/wargo-privd LICENSE README.md config.example.toml deploy docs
sh install.sh --archive /tmp/wargo_dev_linux_amd64.tar.gz
```

For development with live reload see [CONTRIBUTING.md](../CONTRIBUTING.md).

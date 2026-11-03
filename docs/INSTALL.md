# Installing Wargo

Wargo runs on any 64-bit Linux server (x86-64 or ARM64). Pick **one** way to
install it:

| | Native (recommended) | Docker |
|---|---|---|
| Needs | systemd and glibc 2.28 or newer (Debian 10+, Ubuntu 20.04+, RHEL/Rocky/Alma 8+, Fedora, Arch/Manjaro, openSUSE and others) | Docker with Compose |
| Install | one command | one `compose.yaml` file |
| Admin actions | your sudo password or a passkey, checked by the OS | your password or a passkey, checked by Wargo |
| Reachable from | the server only, until you add HTTPS | your local network |

Both give you every feature. The native install is a little safer because
Wargo itself never runs as root. See [SECURITY.md](SECURITY.md).

---

## Native install

Run this as **your normal user**, the account you'll sign in with. It must be
in the `wheel` or `sudo` group. The installer asks for your sudo password once.

```sh
curl -fsSL https://raw.githubusercontent.com/kushalpandya/wargo/main/install.sh | sh
```

Then open **http://127.0.0.1:2207** on the server, sign in with your usual
username and password, and set up two-factor sign-in when asked.

That's it. The installer:

- downloads the latest release for your CPU and checks its checksum,
- installs `/usr/local/bin/wargo`, a systemd service (`wargo@<you>`), a small
  PAM file and `/etc/wargo/config.toml`,
- starts Wargo on `127.0.0.1:2207`, running as your user (not root).

**Upgrade:** run the same command again. Your settings are kept.
**Pick a version:** `… | sh -s -- --version v1.0.0-rc`
**Uninstall:** `… | sh -s -- --uninstall` (add `--purge` to delete its data too)
**Preview:** `… | sh -s -- --dry-run` shows every step without changing anything.

<details>
<summary>Want to read the script before running it?</summary>

```sh
curl -fsSLO https://raw.githubusercontent.com/kushalpandya/wargo/main/install.sh
less install.sh
sh install.sh
```

No internet on the server? Download `wargo_<version>_linux_<arch>.tar.gz`
from the [releases page](https://github.com/kushalpandya/wargo/releases), copy
it over and run `sh install.sh --archive wargo_<version>_linux_<arch>.tar.gz`.
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

Open **http://<server-ip>:2207** from any device on your network and sign in
with your **server** account's username and password.

The container needs wide access to the server (privileged mode, the host's
processes and network, and the host's files at `/host`) so it can show
processes, open shells and install updates. It is not a sandbox, so treat it
like the native install. [DOCKER.md](DOCKER.md) explains each option and
every setting.

**Upgrade:** `docker compose pull && docker compose up -d`

---

## Access from anywhere

The native install listens only on `127.0.0.1`, so it is never exposed by
accident. The Docker install listens on your local network over plain HTTP.
To use Wargo safely from your phone or another computer, outside your home
network, put it behind HTTPS and tell Wargo its public address.

1. Pick one way to publish it:
   - **Cloudflare Tunnel:** point a hostname at `http://127.0.0.1:2207`.
   - **Caddy:** `wargo.example.com { reverse_proxy 127.0.0.1:2207 }`
   - **nginx:** see [`deploy/nginx/wargo.conf`](../deploy/nginx/wargo.conf).
     It needs the WebSocket headers shown there.
2. Add the public address to `/etc/wargo/config.toml` (for Docker, use the
   `WARGO_ORIGINS` setting):

   ```toml
   [auth]
   origins = ["https://wargo.example.com"]
   ```

3. Restart Wargo (`sudo systemctl restart wargo@$USER`, or recreate the
   container).

Passkeys only work over HTTPS. [REVERSE_PROXY.md](REVERSE_PROXY.md) has more
options, including putting Cloudflare Access in front of the address.

---

## After installing

- **Two-factor:** you set it up on your first sign-in. Add a passkey in
  Settings → Security to sign in with one tap.
- **Locked out?** On the server run `wargo admin reset-auth` (Docker:
  `docker exec wargo wargo admin reset-auth`), then sign in with your password.
- **Integrations:** connect Prowlarr and Transmission in Settings →
  Integrations. "Detect" finds them if they run in Docker.
- **Optional, native only:** approve admin actions with a passkey instead of
  your sudo password. See [Passkey approvals](#optional-passkey-approvals-native).

## Troubleshooting

| Problem | Fix |
|---|---|
| "Invalid username or password" even though the password is right | Check that `/etc/pam.d/wargo` exists (the installer adds it) and that your account is in `wheel` or `sudo`. |
| "421 Misdirected Request" or "This address isn't allowed" | Add the address you use to `auth.origins` (Docker: `WARGO_ORIGINS`) and restart. Wargo only answers to addresses it knows, which blocks DNS rebinding attacks. |
| Live charts or the terminal don't load behind a proxy | Add the public address to `auth.origins`, and make sure the proxy passes WebSockets through. |
| "Passkeys require HTTPS" | Use the HTTPS address, not `http://<ip>:2207`. |
| Admin approval says sudo is blocked | Don't add `NoNewPrivileges=yes` to the service. sudo needs it turned off. |
| The service won't start | `journalctl -u wargo@$USER -e` shows why, usually a typo in the config. |

## Optional: passkey approvals (native)

By default, admin actions (stopping another user's process, installing
updates, restarting the server and so on) ask for your sudo password. To
approve them with a passkey instead, turn on the small root helper
`wargo-privd`:

```sh
sudo install -m 0600 /dev/stdin /etc/wargo/privd.toml <<EOF
allowed_uid = $(id -u)
origins = ["https://wargo.example.com"]
EOF
sudo systemctl enable --now wargo-privd
```

Then choose which passkeys to trust in Settings → Security → Admin approvals.
This asks for your sudo password once.

## Building from source

Requirements: Go 1.26+, Node.js 22+, a C compiler and the PAM headers
(`pam` on Arch, `libpam0g-dev` on Debian/Ubuntu, `pam-devel` on Fedora).

```sh
git clone https://github.com/kushalpandya/wargo && cd wargo
make                                   # web UI + bin/wargo + bin/wargo-privd
tar -czf /tmp/wargo_dev_linux_amd64.tar.gz --transform 's#^bin/##' \
  bin/wargo bin/wargo-privd LICENSE README.md config.example.toml deploy docs
sh install.sh --archive /tmp/wargo_dev_linux_amd64.tar.gz
```

For development with live reload, see [CONTRIBUTING.md](../CONTRIBUTING.md).

# Installing Wargo

Wargo is a single binary that runs natively on Linux (amd64, arm64) as your
admin user. It is not meant to run in a container: it needs real PTYs, the host
process table, PAM and the package manager.

## Requirements

- Linux with systemd and PAM (`pam_unix`)
- Your user in the admin group (`wheel` or `sudo`)
- Optional: Docker (your user in the `docker` group), `sudo`, `tmux`
- Building from source: Go 1.26+, a C compiler and PAM headers
  (`pam` on Arch, `libpam0g-dev` on Debian/Ubuntu, `pam-devel` on Fedora),
  Node.js 22+

## Quick install (from a release or source build)

```sh
make               # builds web/dist, bin/wargo and bin/wargo-privd
sudo ./packaging/install.sh
sudo systemctl enable --now wargo@$USER
```

`install.sh` copies the binary to `/usr/local/bin`, installs the PAM service
(`/etc/pam.d/wargo`), the systemd unit, `/etc/wargo/config.toml` (from
`config.example.toml`, if absent) and creates `/var/lib/wargo` owned by you.
It changes nothing else.

Open `http://127.0.0.1:2207`, sign in with your OS username and password, and
enrol two-factor authentication. Then publish it with a reverse proxy — see
[REVERSE_PROXY.md](REVERSE_PROXY.md).

## Manual install

```sh
sudo install -m 0755 bin/wargo /usr/local/bin/wargo
sudo install -m 0644 deploy/pam/wargo /etc/pam.d/wargo
sudo install -m 0644 deploy/systemd/wargo@.service /etc/systemd/system/
sudo install -d -m 0755 /etc/wargo
sudo install -m 0640 -g "$(id -gn)" config.example.toml /etc/wargo/config.toml
sudo install -d -m 0700 -o "$USER" -g "$(id -gn)" /var/lib/wargo
sudo systemctl daemon-reload
sudo systemctl enable --now wargo@$USER
```

## Optional: passkey approvals for admin actions (wargo-privd)

```sh
sudo install -m 0755 bin/wargo-privd /usr/local/bin/wargo-privd
sudo install -m 0600 deploy/privd.toml /etc/wargo/privd.toml   # edit allowed_uid / origins
sudo install -m 0644 deploy/systemd/wargo-privd.service deploy/systemd/wargo-privd.socket /etc/systemd/system/
sudo systemctl enable --now wargo-privd.socket
```

Then in Settings → Security → Admin approvals, choose which passkeys to trust
(this asks for your sudo password once).

## Updating

Replace `/usr/local/bin/wargo` and `sudo systemctl restart wargo@$USER`.
Running package upgrades are unaffected — they run in their own systemd unit.

## Uninstalling

```sh
sudo systemctl disable --now wargo@$USER wargo-privd.socket
sudo rm -f /usr/local/bin/wargo /usr/local/bin/wargo-privd /etc/pam.d/wargo \
  /etc/systemd/system/wargo@.service /etc/systemd/system/wargo-privd.*
sudo rm -rf /etc/wargo /var/lib/wargo /var/lib/wargo-privd
```

## Development

```sh
cp config.example.toml dev.local.toml   # set listen, data_dir, [dev] auth_bypass = true
make build && ./bin/wargo -config dev.local.toml
cd web && npm ci && WARGO_API=http://127.0.0.1:2207 npm run dev
```

UI review helpers (use the system Chrome):

```sh
node web/scripts/shot.mjs  http://127.0.0.1:5173/ /tmp/shot --sizes=desktop,tablet,phone --schemes=dark,light
node web/scripts/audit.mjs http://127.0.0.1:5173/            # overflow, touch targets, axe WCAG AA
```

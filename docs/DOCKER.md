# Running Wargo in Docker (container mode)

Wargo can run from a container image instead of a native install. It keeps
almost every feature: metrics, processes, terminal, file manager, OS updates,
Docker management and sign-in with your server account. It does this by
looking at the host (the server itself) through a few deliberate openings in
the container.

> **This container is not a sandbox.** It runs privileged, shares the host's
> processes, network and hostname, and mounts the host's whole filesystem.
> Anyone who controls Wargo controls the server as root. Protect it exactly
> like a native install (see [SECURITY.md](SECURITY.md)): strong sign-in,
> HTTPS in front, and ideally a login proxy such as Cloudflare Access.

## Quick start

```yaml
# deploy/docker/compose.yaml
services:
  wargo:
    image: ghcr.io/kushalpandya/wargo:${WARGO_TAG:-latest}
    container_name: wargo
    restart: unless-stopped
    privileged: true
    pid: host
    network_mode: host
    uts: host
    volumes:
      - /:/host
      - /var/run/docker.sock:/var/run/docker.sock
      - wargo-data:/data
    environment:
      WARGO_HOST_ROOT: /host
      WARGO_USER: ${WARGO_USER:-}   # account to sign in with; default: first wheel/sudo member with uid >= 1000
volumes:
  wargo-data:
```

```sh
WARGO_USER=$USER docker compose -f deploy/docker/compose.yaml up -d
docker logs wargo        # shows the account in use and any setup problem
```

The container listens on **port 2207 on every network interface**, so you can
reach it from your local network at `http://<server-ip>:2207` (or
`http://<hostname>.local:2207`). Plain HTTP on your network works, but your
password and session are sent unencrypted, and passkeys and terminal
copy/paste need HTTPS or `localhost`. Outside a trusted home network, either
put HTTPS in front ([REVERSE_PROXY.md](REVERSE_PROXY.md)), or keep Wargo on
the server only with `WARGO_LISTEN=127.0.0.1:2207` and reach it through a
proxy, a tunnel or `ssh -L 2207:127.0.0.1:2207 <server>`.

Sign in with your **server** account's password, then set up a two-factor
code or a passkey, just like a native install.

**Image tags:** each release gets its own tag (for example `v1.0.0-alpha.1`).
`latest` and `next` point to the newest release; until 1.0, `latest` includes
pre-releases. To stay on one version, set `WARGO_TAG=v1.0.0-alpha.1`. Images
are built for amd64 and arm64.

## Settings

Environment variables are enough to run Wargo. If `/data/config.toml` exists
(same format as [config.example.toml](../config.example.toml)), Wargo reads it
too, and environment variables win over the file.

| Variable | What it does | Default |
|---|---|---|
| `WARGO_HOST_ROOT` | Where the host's `/` is mounted. Setting it turns on container mode. | `/host` (set in the image) |
| `WARGO_USER` | The server account that can sign in | first regular member of `wheel`, `sudo` or `admin` |
| `WARGO_LISTEN` | Address and port to listen on | `0.0.0.0:2207` (every interface) |
| `WARGO_ORIGINS` | Your public addresses, separated by commas. **Required** for every public hostname you use; Wargo refuses other hostnames (HTTP 421) to block DNS rebinding attacks. IP addresses, `localhost`, the server's hostname and `.local`/`.lan` names work without it. | none |
| `WARGO_TRUSTED_PROXIES` | Address ranges (CIDRs) of proxies whose forwarded headers Wargo trusts | none |
| `WARGO_FILES_ROOTS` | Folders shown in the file manager, as `Name=/path,Other=/path` (paths on the host) | your home folder, plus `/DATA` if it exists |
| `WARGO_DATA_DIR` | Where the database, keys and uploads in progress are kept | `/data` |
| `WARGO_CONFIG` | Path of the config file | `/data/config.toml` |

These variables also work for native installs.

**Locked out?** Run `docker exec wargo wargo admin reset-auth`, then sign in
with your password.

## Why each run option is needed

| Option | Needed for |
|---|---|
| `/:/host` | Files; the host's `/etc/passwd`, `group` and `shadow` (sign-in); `/proc` and `/sys`; OS details; package databases; upgrade logs |
| `pid: host` | The process list, sending signals, and running commands on the host |
| `privileged: true` | Running commands inside the host's environment (`nsenter`), acting as your account for file access, and reading host-only system information |
| `network_mode: host` | Network statistics and interfaces, and listening on the server's own address |
| `uts: host` | Showing the server's real hostname |
| `/var/run/docker.sock` | The Containers page, app suggestions for the launcher, and finding integrations |

When it starts, Wargo checks these and stops with a clear message if one is
missing, for example "not in the host PID namespace: set pid: host".

## How it works

This section is for the curious. All container-specific code lives in
`internal/host`.

- **Paths.** Every host path Wargo reads (`/etc/os-release`, `/sys/...`,
  `/proc/1/mountinfo`, `/var/lib/pacman`, `/dev/disk/by-label`, mount points,
  compose files, file manager folders) is looked up under `/host`. Symlinks
  are followed *inside* the host's filesystem, so links like
  `/home -> /var/home` can't point into the container. The metrics library
  (gopsutil) is told where the host's `/proc`, `/sys`, `/etc` and so on are,
  and reads the host's mount table from PID 1.
- **Users.** Wargo reads the host's `passwd`, `group` and `shadow` files
  directly, because the container itself only knows the image's accounts.
- **Host commands** run inside the host's environment through
  `nsenter -t 1 -m -u -i -n -C -- …`, using the host's own programs. Commands
  that natively run without root (update checks, `docker compose`, container
  shells) switch to your account with the host's
  `setpriv --reuid --regid --init-groups` (or `su` if that's missing), so they
  get exactly your user and groups.
- **Terminal.** A host shell is `su -l <user>` on the host, a real login with
  your groups, home folder and shell. The root shell is
  `su -l <user> -c 'exec sudo -i'`, so the host's sudo asks for your password
  in the terminal, just like natively. Container shells use the host's
  `docker exec` as your user, and tmux runs on the host.
- **Files.** Each file manager request, and each step of a background job,
  runs with the file access identity of your account (switched per thread,
  on a locked thread). So permissions match a native install exactly, and new
  files belong to you. Only actions you approve as administrator run as root.
  Uploads in progress are kept in `/data/uploads`, and the trash is on the
  host in `~/.local/share/wargo/trash`.
- **OS updates.** Update checks run on the host as your account. Upgrades run
  as a temporary systemd unit on the host (`systemd-run`) with a fixed wrapper
  script. The log goes to `/var/log/wargo/<id>.log` on the host and Wargo
  follows it there, so an upgrade keeps going even if Wargo restarts.
- **Approvals.** sudo can't help inside a container, so admin actions are
  approved with your account password (checked against the host's shadow
  file, with attempts slowed down) or a passkey. See
  [SECURITY.md](SECURITY.md).

## Limitations

- Wargo doesn't update its own container. The Containers page marks it, asks
  you to confirm before stopping or restarting it, and won't recreate it.
  Update it with `docker compose pull && docker compose up -d`.
- `wargo-privd` isn't used; Wargo checks passkey approvals itself.
- Accounts that come from LDAP, SSSD or systemd-homed (not listed in
  `/etc/passwd` and `/etc/shadow`) can't sign in. Use a local account.
- The host needs a kernel that supports `nsenter` (any modern Linux). For the
  best account switching it also needs util-linux `setpriv`, which Debian,
  Ubuntu, Arch, Fedora and openSUSE include; otherwise `su` is used.
- Terminal sessions run inside Wargo's container, so stopping or restarting
  the container closes open shells, including a tmux server Wargo started.

## Building the image yourself

```sh
docker build -t wargo:local .
docker run --rm wargo:local version
```

The build has three stages: Node builds the web UI, Go builds the binary
(linked with PAM and libxcrypt), and the final image is
`debian:bookworm-slim` with `util-linux`, `libpam0g`, `libcrypt1`,
`ca-certificates` and `tini`.

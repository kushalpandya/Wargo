# Running Wargo in Docker (container mode)

Wargo can run from a container image instead of a native install. It keeps
almost every feature — metrics, processes, terminal, file manager, OS
updates, Docker management, sign-in with your host account — by looking at
the host through a few deliberate holes in the container.

> **This container is not a sandbox.** It is privileged, shares the host's
> PID, network and UTS namespaces and mounts the host's `/`. Anything that
> controls Wargo controls the host as root. Treat it exactly like a native
> install (see [SECURITY.md](SECURITY.md)): strong sign-in factors, TLS in
> front, ideally an identity-aware proxy.

## Quick start

```yaml
# deploy/docker/compose.yaml
services:
  wargo:
    image: ghcr.io/kushalpandya/wargo:latest
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
      WARGO_USER: ${WARGO_USER:-}   # host admin account; default: first wheel/sudo member with uid >= 1000
volumes:
  wargo-data:
```

```sh
WARGO_USER=$USER docker compose -f deploy/docker/compose.yaml up -d
docker logs wargo        # shows the account in use and any self-check problem
```

Wargo listens on **`127.0.0.1:2207`** by default — reachable from a reverse
proxy or tunnel on the same host ([REVERSE_PROXY.md](REVERSE_PROXY.md)). For
LAN access set `WARGO_LISTEN=0.0.0.0:2207`, but only behind TLS: passkeys and
`Secure` cookies need HTTPS anyway.

Sign in with your **host** account's password (then set up TOTP or a
passkey, as natively). Image tags: `vX.Y.Z[-pre]` per release, `latest` for
the newest stable release, `edge` for `main`. Images are built for amd64 and
arm64.

## Configuration

Everything works with environment variables alone; a TOML file at
`/data/config.toml` (same format as [config.example.toml](../config.example.toml))
is read when present. Environment variables override the file.

| Variable | Meaning | Default |
|---|---|---|
| `WARGO_HOST_ROOT` | Where the host's `/` is mounted; enables container mode | `/host` (set in the image) |
| `WARGO_USER` | Host admin account allowed to sign in | first regular member of `wheel`/`sudo`/`admin` |
| `WARGO_LISTEN` | Listen address | `127.0.0.1:2207` |
| `WARGO_ORIGINS` | Public URLs (comma-separated) for WebSockets/passkeys | — |
| `WARGO_TRUSTED_PROXIES` | CIDRs whose forwarded headers are trusted | — |
| `WARGO_FILES_ROOTS` | File manager places, `Name=/path,Other=/path` (host paths) | the admin's home, plus `/DATA` if present |
| `WARGO_DATA_DIR` | Database, keys, upload staging | `/data` |
| `WARGO_CONFIG` | Config file path | `/data/config.toml` |

These variables also work for native installs.

Recovery (`wargo admin reset-auth`): `docker exec wargo wargo admin reset-auth`.

## Required run options and why

| Option | Needed for |
|---|---|
| `/:/host` | Files, host `/etc/passwd`/`group`/`shadow` (sign-in), `/proc`, `/sys`, os-release, package databases, upgrade logs |
| `pid: host` | Process list and signals; entering the host namespaces |
| `privileged: true` | `nsenter` into the host's mount/IPC/cgroup namespaces, per-thread identity switching, reading host-only `/proc` entries |
| `network_mode: host` | Network metrics and interfaces; listening on the host |
| `uts: host` | The host's hostname |
| `/var/run/docker.sock` | Containers page, launcher suggestions, integrations discovery |

On start Wargo checks these and exits with an explicit message when one is
missing (for example "not in the host PID namespace: set pid: host").

## How it works

All mode-specific logic lives in `internal/host`:

- **Paths.** Every host path Wargo reads (`/etc/os-release`, `/sys/...`,
  `/proc/1/mountinfo`, `/var/lib/pacman`, `/dev/disk/by-label`, mount points,
  compose files, file-manager roots) is mapped below `/host`. Symlinks are
  resolved *inside* the host root, so `/home -> /var/home` or absolute links
  can't point into the container. gopsutil gets `HOST_PROC`, `HOST_SYS`,
  `HOST_ETC`, `HOST_VAR`, `HOST_RUN`, `HOST_DEV`, `HOST_ROOT` and
  `HOST_PROC_MOUNTINFO` (PID 1's mount table, i.e. the host's mounts).
- **Users.** The host's `passwd`, `group` and `shadow` files are parsed
  directly (the container's `os/user` would only know the image's accounts).
- **Host commands** run through `nsenter -t 1 -m -u -i -n -C -- …` in the
  host's namespaces, with the host's binaries. Commands that natively run
  unprivileged (update checks, `docker compose`, container shells) drop to
  the admin account with the host's `setpriv --reuid --regid --init-groups`
  (falling back to `su`), so they get exactly the user's uid, gid and
  supplementary groups.
- **Terminal.** Host shells are `su -l <user>` on the host (real login
  session, groups, home and shell); the root shell is `su -l <user> -c 'exec
  sudo -i'`, so the host's sudo asks for your password inside the terminal
  exactly like natively; container shells use the host's `docker exec` as
  your user; tmux runs on the host.
- **Files.** Each file-manager request and each background job step runs with
  the calling thread's filesystem identity (fsuid/fsgid and supplementary
  groups) switched to the admin account, using raw per-thread syscalls on a
  locked OS thread. Permissions therefore match a native install exactly and
  new files belong to you. Only "Administrator approval" retries run as root.
  Uploads are staged in `/data/uploads` (owned by the admin); the trash lives
  on the host in `~/.local/share/wargo/trash`.
- **OS updates.** Checks run on the host as your user. Upgrades run as a
  transient host systemd unit (`systemd-run`) whose command is the host's
  `/bin/sh` with a constant wrapper script; the log is written to
  `/var/log/wargo/<id>.log` on the host and followed by Wargo through the
  host mount, so an upgrade survives a Wargo restart.
- **Approval prompts.** sudo can't help inside a container, so admin actions
  are approved with your account password (verified against the host's
  shadow file, throttled) or a passkey. See [SECURITY.md](SECURITY.md).

## Limitations

- Wargo does not update its own container. The Containers page marks it,
  asks for explicit confirmation before stopping or restarting it and refuses
  to recreate it; update with `docker compose pull && docker compose up -d`.
- `wargo-privd` is not used; passkey approvals are verified by Wargo itself.
- Host accounts from LDAP/SSSD/systemd-homed (not in `/etc/passwd` and
  `/etc/shadow`) can't sign in; use a local account.
- The host needs `nsenter` support in the kernel (any modern Linux) and, for
  the best user switching, util-linux `setpriv` (present on Debian, Ubuntu,
  Arch, Fedora, openSUSE; otherwise `su` is used).
- Terminal processes are children of Wargo and live in its container cgroup:
  stopping or restarting the container ends open shells, including a tmux
  server that Wargo started.

## Building locally

```sh
docker build -t wargo:local .
docker run --rm wargo:local version
```

The image is multi-stage: Node builds the web UI, Go builds the binary with
cgo (PAM and libxcrypt), and the runtime is `debian:bookworm-slim` with
`util-linux`, `libpam0g`, `libcrypt1`, `ca-certificates` and `tini`.

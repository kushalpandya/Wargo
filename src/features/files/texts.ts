// SPDX-License-Identifier: AGPL-3.0-only
// Text files of the demo server: notes, compose files, configs and logs.
import { CONTAINERS, HOST } from "../host";
import { DAY, HOUR, MIN, seeded } from "../../core/rand";

const img = (name: string) => CONTAINERS.find((c) => c.name === name)?.image ?? `${name}:latest`;
const day = (msAgo: number) => new Date(Date.now() - msAgo).toISOString().slice(0, 10);

export const README = `# Home folder

This is the demo account on **${HOST.hostname}** (${HOST.os}).

- \`stacks/\` holds one compose project per folder (media, immich, nextcloud, ...).
- \`scripts/\` has the nightly backup and a few helpers run from cron.
- Big things (movies, shows, photos, backups) live on the DATA drive at \`/DATA\`.

Everything here is made up for the Wargo demo. Feel free to edit, move or delete
files: changes stay in this browser and "Reset demo" puts everything back.
`;

export const NOTES = `# Homelab notes

## To do
- [x] Move Immich library to /DATA/Photos
- [x] Nightly restic backup to /mnt/backup
- [ ] Upgrade Jellyfin to 10.10.5 (healthcheck keeps failing on 10.10.3)
- [ ] Look into why nextcloud-cron exits with code 1
- [ ] Replace sdb (Backup) once it hits 5 years of power-on time
- [ ] Try the Tailscale exit node from the phone

## Network
| Thing          | Address            |
|----------------|--------------------|
| Router         | ${HOST.gateway}        |
| ${HOST.hostname.padEnd(14)} | ${HOST.lanIp}       |
| Tailscale      | ${HOST.tailscaleIp}      |
| Pi-hole admin  | http://${HOST.lanIp}:8053/admin |

## Ports
${CONTAINERS.filter((c) => c.port)
  .map((c) => `- ${c.name}: ${c.port}`)
  .join("\n")}

## Disks
- NVMe 1 TB: system, docker volumes
- sda 8 TB (ext4): /DATA, media and photos
- sdb 8 TB (btrfs): /mnt/backup, snapshots + restic repo

> Remember: \`docker compose pull && docker compose up -d\` from each folder,
> or just use the Updates page in Wargo.
`;

export function budgetCsv(): string {
  const rows = [["Month", "Category", "Item", "Amount (EUR)"]];
  const items: [string, string, number][] = [
    ["Hardware", "WD Red Plus 8 TB", 189.9],
    ["Power", "Electricity share (server, ~45 W)", 11.8],
    ["Services", "Domain renewal", 12.99],
    ["Services", "Backblaze B2 offsite", 4.62],
    ["Hardware", "Noctua NF-A12x25 fan", 32.9],
    ["Power", "Electricity share (server, ~45 W)", 12.1],
    ["Hardware", "UPS battery replacement", 54.0],
    ["Services", "Backblaze B2 offsite", 4.88],
    ["Hardware", "Crucial 32 GB DDR4 kit", 71.5],
    ["Power", "Electricity share (server, ~45 W)", 11.6],
  ];
  const months = ["2026-01", "2026-02", "2026-03", "2026-04", "2026-05", "2026-06", "2026-07", "2026-08", "2026-09", "2026-10"];
  items.forEach(([c, i, a], k) => rows.push([months[k], c, i, a.toFixed(2)]));
  const total = items.reduce((s, [, , a]) => s + a, 0);
  rows.push(["", "", "Total", total.toFixed(2)]);
  return rows.map((r) => r.map((v) => (v.includes(",") ? `"${v}"` : v)).join(",")).join("\n") + "\n";
}

export const SHOPPING = `Server wishlist
===============

* second NVMe for a ZFS mirror of the docker volumes
* 2.5 GbE switch (the router is the bottleneck now)
* Intel Arc A310 for Jellyfin transcoding (AV1!)
* label printer for the cables, seriously
`;

export const ROUTER_NOTES = `# Router setup

1. Admin UI: http://${HOST.gateway} (password in Vaultwarden).
2. DHCP range 192.168.1.100 to 192.168.1.199. Static leases:
   - ${HOST.hostname}: ${HOST.lanIp}
   - nas: 192.168.1.30
   - pi: 192.168.1.40
3. DNS: hand out ${HOST.lanIp} (Pi-hole) as the only resolver.
4. Port forwards: none. Remote access goes through Tailscale.
5. Firmware: check for updates every few months, auto-update is off.
`;

export const UPS_NOTES = `UPS CP1500 notes
----------------

Capacity: 1500 VA / 900 W, runtime at ~60 W load: about 55 minutes.
USB to ${HOST.hostname}, monitored by NUT (upsmon in primary mode).

Shutdown: at 20% battery, or after 10 minutes on battery.
Battery replaced: 2026-04. Next: 2029.
Self test: monthly, first Sunday 10:00.
`;

// ---- docker ------------------------------------------------------------------

export const COMPOSE_MEDIA = `# Media stack: Jellyfin + the *arr apps + download clients.
name: media

x-common: &common
  restart: unless-stopped
  environment:
    PUID: 1000
    PGID: 1000
    TZ: Europe/Berlin

services:
  jellyfin:
    image: ${img("jellyfin")}
    container_name: jellyfin
    restart: unless-stopped
    user: 1000:1000
    ports:
      - 8096:8096
    volumes:
      - ./jellyfin/config:/config
      - ./jellyfin/cache:/cache
      - /DATA/Media:/media:ro
    devices:
      - /dev/dri:/dev/dri
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://localhost:8096/health"]
      interval: 30s
      retries: 3

  sonarr:
    <<: *common
    image: ${img("sonarr")}
    container_name: sonarr
    ports:
      - 8989:8989
    volumes:
      - ./sonarr:/config
      - /DATA:/data

  radarr:
    <<: *common
    image: ${img("radarr")}
    container_name: radarr
    ports:
      - 7878:7878
    volumes:
      - ./radarr:/config
      - /DATA:/data

  prowlarr:
    <<: *common
    image: ${img("prowlarr")}
    container_name: prowlarr
    ports:
      - 9696:9696
    volumes:
      - ./prowlarr:/config

  transmission:
    <<: *common
    image: ${img("transmission")}
    container_name: transmission
    ports:
      - 9091:9091
      - 51413:51413
      - 51413:51413/udp
    volumes:
      - ./transmission:/config
      - /DATA/Downloads:/downloads

  qbittorrent:
    <<: *common
    image: ${img("qbittorrent")}
    container_name: qbittorrent
    ports:
      - 8085:8085
    volumes:
      - ./qbittorrent:/config
      - /DATA/Downloads:/downloads
`;

export const COMPOSE_IMMICH = `# Immich: photo library. https://immich.app
name: immich

services:
  immich-server:
    container_name: immich-server
    image: ${img("immich-server")}
    volumes:
      - \${UPLOAD_LOCATION}:/usr/src/app/upload
      - /etc/localtime:/etc/localtime:ro
    env_file:
      - .env
    ports:
      - 2283:2283
    depends_on:
      - redis
      - database
    restart: always
    healthcheck:
      disable: false

  redis:
    container_name: immich-redis
    image: ${img("immich-redis")}
    healthcheck:
      test: redis-cli ping || exit 1
    restart: always

  database:
    container_name: immich-postgres
    image: ${img("immich-postgres")}
    environment:
      POSTGRES_PASSWORD: \${DB_PASSWORD}
      POSTGRES_USER: \${DB_USERNAME}
      POSTGRES_DB: \${DB_DATABASE_NAME}
      POSTGRES_INITDB_ARGS: "--data-checksums"
    volumes:
      - \${DB_DATA_LOCATION}:/var/lib/postgresql/data
    restart: always
`;

export const ENV_IMMICH = `# Immich settings (see the docs for all options)
UPLOAD_LOCATION=/DATA/immich
DB_DATA_LOCATION=./postgres
IMMICH_VERSION=${img("immich-server").split(":")[1]}
TZ=Europe/Berlin

DB_PASSWORD=demo-not-a-real-password
DB_USERNAME=postgres
DB_DATABASE_NAME=immich
`;

export const COMPOSE_NEXTCLOUD = `name: nextcloud

services:
  nextcloud:
    image: ${img("nextcloud")}
    container_name: nextcloud
    restart: unless-stopped
    ports:
      - 8080:80
    env_file: .env
    volumes:
      - ./html:/var/www/html
      - /DATA/nextcloud:/var/www/html/data

  nextcloud-cron:
    image: ${img("nextcloud-cron")}
    container_name: nextcloud-cron
    restart: unless-stopped
    entrypoint: /cron.sh
    volumes_from:
      - nextcloud
`;

export const ENV_NEXTCLOUD = `NEXTCLOUD_TRUSTED_DOMAINS=cloud.example.home ${HOST.lanIp} ${HOST.tailscaleIp}
NEXTCLOUD_ADMIN_USER=admin
NEXTCLOUD_ADMIN_PASSWORD=demo-not-a-real-password
SQLITE_DATABASE=nextcloud
PHP_MEMORY_LIMIT=1G
PHP_UPLOAD_LIMIT=16G
`;

export const COMPOSE_HOME = `name: home

services:
  home-assistant:
    image: ${img("home-assistant")}
    container_name: home-assistant
    network_mode: host
    restart: unless-stopped
    privileged: true
    volumes:
      - ./config:/config
      - /etc/localtime:/etc/localtime:ro
      - /run/dbus:/run/dbus:ro
`;

export const COMPOSE_NETWORK = `name: network

services:
  pihole:
    image: ${img("pihole")}
    container_name: pihole
    restart: unless-stopped
    ports:
      - 53:53/tcp
      - 53:53/udp
      - 8053:80
    environment:
      TZ: Europe/Berlin
      WEBPASSWORD_FILE: /run/secrets/pihole
      FTLCONF_LOCAL_IPV4: ${HOST.lanIp}
    volumes:
      - ./etc-pihole:/etc/pihole
      - ./etc-dnsmasq.d:/etc/dnsmasq.d
`;

export const COMPOSE_TOOLS = `name: tools

services:
  vaultwarden:
    image: ${img("vaultwarden")}
    container_name: vaultwarden
    restart: unless-stopped
    ports:
      - 8222:80
    environment:
      SIGNUPS_ALLOWED: "false"
      DOMAIN: https://vault.example.home
    volumes:
      - ./vaultwarden:/data

  uptime-kuma:
    image: ${img("uptime-kuma")}
    container_name: uptime-kuma
    restart: unless-stopped
    ports:
      - 3001:3001
    volumes:
      - ./uptime-kuma:/app/data
      - /var/run/docker.sock:/var/run/docker.sock:ro

  paperless:
    image: ${img("paperless")}
    container_name: paperless
    restart: unless-stopped
    ports:
      - 8010:8000
    volumes:
      - ./paperless/data:/usr/src/paperless/data
      - /DATA/Documents/paperless:/usr/src/paperless/media
`;

// ---- scripts -------------------------------------------------------------------

export const BACKUP_SH = `#!/usr/bin/env bash
# Nightly backup: restic snapshot of the important bits to the backup drive,
# then a btrfs snapshot of the repo. Run from cron at 03:30.
set -euo pipefail

export RESTIC_REPOSITORY=/mnt/backup/restic
export RESTIC_PASSWORD_FILE="$HOME/.config/restic/password"

SOURCES=(
  "$HOME/docker"
  "$HOME/Documents"
  /DATA/Photos
  /DATA/Backups
)

log() { printf '%s %s\\n' "$(date -Is)" "$*"; }

log "dumping immich database"
docker exec immich-postgres pg_dumpall -U postgres | zstd -q > "/DATA/Backups/immich-db-$(date +%F).sql.zst"

log "restic backup"
restic backup --one-file-system --exclude-caches "\${SOURCES[@]}"

log "pruning"
restic forget --prune --keep-daily 7 --keep-weekly 5 --keep-monthly 12

log "btrfs snapshot"
sudo btrfs subvolume snapshot -r /mnt/backup/restic "/mnt/backup/snapshots/restic-$(date +%F)"

log "done"
`;

export const UPDATE_SH = `#!/usr/bin/env bash
# Pull and restart every compose project under ~/docker.
set -euo pipefail
for dir in "$HOME"/stacks/*/; do
  [ -f "$dir/compose.yaml" ] || continue
  echo "==> $(basename "$dir")"
  docker compose --project-directory "$dir" pull -q
  docker compose --project-directory "$dir" up -d --remove-orphans
done
docker image prune -f
`;

export const DISKS_SH = `#!/usr/bin/env bash
# Quick SMART summary for all disks.
for d in /dev/nvme0n1 /dev/sda /dev/sdb; do
  echo "== $d"
  sudo smartctl -H -A "$d" | grep -E 'overall-health|Power_On_Hours|Temperature|Percentage Used'
done
`;

// ---- dotfiles ------------------------------------------------------------------

export const BASHRC = `# ~/.bashrc: executed by bash(1) for non-login shells.

case $- in
    *i*) ;;
      *) return;;
esac

HISTCONTROL=ignoreboth
HISTSIZE=10000
HISTFILESIZE=20000
shopt -s histappend checkwinsize

PS1='\\[\\e[1;32m\\]\\u@\\h\\[\\e[0m\\]:\\[\\e[1;34m\\]\\w\\[\\e[0m\\]\\$ '

alias ll='ls -alF --color=auto'
alias la='ls -A'
alias dc='docker compose'
alias dps='docker ps --format "table {{.Names}}\\t{{.Status}}\\t{{.Ports}}"'
alias logs='sudo journalctl -f'

export EDITOR=vim
export PATH="$HOME/.local/bin:$PATH"

if [ -f /usr/share/bash-completion/bash_completion ]; then
  . /usr/share/bash-completion/bash_completion
fi
`;

export const PROFILE = `# ~/.profile: executed by the command interpreter for login shells.
if [ -n "$BASH_VERSION" ]; then
    if [ -f "$HOME/.bashrc" ]; then
        . "$HOME/.bashrc"
    fi
fi
if [ -d "$HOME/.local/bin" ] ; then
    PATH="$HOME/.local/bin:$PATH"
fi
`;

export const BASH_HISTORY = `docker compose pull
docker compose up -d
df -h
sudo smartctl -a /dev/sdb
cd ~/stacks/immich
docker compose logs -f immich-server
htop
sudo apt update && sudo apt upgrade
restic snapshots
ls -la /DATA/Media/Movies
du -sh /DATA/*
tailscale status
`;

export const GITCONFIG = `[user]
	name = Demo User
	email = demo@example.home
[init]
	defaultBranch = main
[pull]
	rebase = true
`;

export const SSH_CONFIG = `Host nas
    HostName 192.168.1.30
    User admin

Host pi
    HostName 192.168.1.40
    User pi
    IdentityFile ~/.ssh/id_ed25519
`;

export const AUTHORIZED_KEYS = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIM4f9wDemoKeyLaptopXq7c3Vb2h0R9kTzW1pYdE demo@laptop
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB2kPhoneKeyDemoQ8s1Lm4nVx7yZt5eRcWuJ0a demo@phone
`;

export const HTOPRC = `# Beware! This file is rewritten by htop when settings are changed in the interface.
htop_version=3.2.2
fields=0 48 17 18 38 39 40 2 46 47 49 1
hide_kernel_threads=1
hide_userland_threads=1
show_program_path=0
highlight_base_name=1
tree_view=0
color_scheme=0
delay=15
`;

export const RCLONE_CONF = `[b2]
type = b2
account = 000demo0000000000000001
key = demo-not-a-real-key
hard_delete = true
`;

export const HOMEPAGE_SERVICES = `- Media:
    - Jellyfin:
        href: http://${HOST.lanIp}:8096
        icon: jellyfin.svg
    - Immich:
        href: http://${HOST.lanIp}:2283
        icon: immich.svg
- Tools:
    - Vaultwarden:
        href: http://${HOST.lanIp}:8222
        icon: vaultwarden.svg
`;

// ---- /etc ------------------------------------------------------------------------

export const OS_RELEASE = `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION="12 (bookworm)"
VERSION_CODENAME=bookworm
ID=debian
HOME_URL="https://www.debian.org/"
SUPPORT_URL="https://www.debian.org/support"
BUG_REPORT_URL="https://bugs.debian.org/"
`;

export const HOSTS = `127.0.0.1	localhost
127.0.1.1	${HOST.hostname}.lan	${HOST.hostname}
${HOST.lanIp}	${HOST.hostname}.lan	${HOST.hostname}

# The following lines are desirable for IPv6 capable hosts
::1     localhost ip6-localhost ip6-loopback
ff02::1 ip6-allnodes
ff02::2 ip6-allrouters
`;

export const FSTAB = `# /etc/fstab: static file system information.
#
# <file system>                           <mount point>  <type>  <options>                  <dump> <pass>
UUID=3f6c2a8e-1d4b-4e8a-9b71-5c0e2d9f7a13  /              ext4    errors=remount-ro          0      1
UUID=7A1C-3E9F                             /boot/efi      vfat    umask=0077                 0      1
UUID=b8e4d0c2-6a5f-4c3e-8d21-0f9a7b6e5c44  /DATA          ext4    defaults,noatime,nofail    0      2
UUID=e2a9c7f1-4b3d-4f6e-a815-9d2c0b7e3f58  /mnt/backup    btrfs   defaults,noatime,compress=zstd:3,nofail  0  0
/dev/nvme0n1p3                             none           swap    sw                         0      0
`;

export const RESOLV = `# Generated by NetworkManager
search lan
nameserver 127.0.0.1
nameserver ${HOST.gateway}
`;

export const PASSWD = `root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
bin:x:2:2:bin:/bin:/usr/sbin/nologin
sys:x:3:3:sys:/dev:/usr/sbin/nologin
www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin
backup:x:34:34:backup:/var/backups:/usr/sbin/nologin
_apt:x:42:65534::/nonexistent:/usr/sbin/nologin
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
messagebus:x:100:107::/nonexistent:/usr/sbin/nologin
sshd:x:101:65534::/run/sshd:/usr/sbin/nologin
systemd-network:x:998:998:systemd Network Management:/:/usr/sbin/nologin
systemd-timesync:x:997:997:systemd Time Synchronization:/:/usr/sbin/nologin
demo:x:1000:1000:Demo User,,,:/home/demo:/bin/bash
`;

export const GROUP = `root:x:0:
daemon:x:1:
bin:x:2:
sys:x:3:
adm:x:4:demo
tty:x:5:
disk:x:6:
sudo:x:27:demo
www-data:x:33:
backup:x:34:
shadow:x:42:
staff:x:50:
users:x:100:demo
systemd-journal:x:999:
docker:x:989:demo
demo:x:1000:
nogroup:x:65534:
`;

export const SSHD_CONFIG = `# Wargo demo: sshd_config (only changes from the Debian defaults)
Include /etc/ssh/sshd_config.d/*.conf

PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM yes
X11Forwarding no
PrintMotd no
AcceptEnv LANG LC_*
Subsystem sftp /usr/lib/openssh/sftp-server
`;

export const DEBIAN_SOURCES = `Types: deb
URIs: https://deb.debian.org/debian
Suites: bookworm bookworm-updates
Components: main contrib non-free non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg

Types: deb
URIs: https://security.debian.org/debian-security
Suites: bookworm-security
Components: main contrib non-free non-free-firmware
Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg
`;

export const DOCKER_SOURCES = `Types: deb
URIs: https://download.docker.com/linux/debian
Suites: bookworm
Components: stable
Signed-By: /etc/apt/keyrings/docker.asc
`;

export const DAEMON_JSON = `{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" },
  "data-root": "/var/lib/docker",
  "features": { "buildkit": true }
}
`;

export const BACKUP_SERVICE = `[Unit]
Description=Nightly photo backup
After=network-online.target mnt-backup.mount
Requires=mnt-backup.mount

[Service]
Type=oneshot
User=demo
ExecStart=/usr/bin/rsync -a --delete /DATA/Photos/ /mnt/backup/photos/
Nice=10
IOSchedulingClass=idle
`;

export const BACKUP_TIMER = `[Unit]
Description=Run the photo backup every night

[Timer]
OnCalendar=*-*-* 02:00:00
RandomizedDelaySec=15m
Persistent=true

[Install]
WantedBy=timers.target
`;

export const CRONTAB = `# /etc/crontab: system-wide crontab
SHELL=/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin

# m h dom mon dow user	command
17 *	* * *	root	cd / && run-parts --report /etc/cron.hourly
25 6	* * *	root	test -x /usr/sbin/anacron || { cd / && run-parts --report /etc/cron.daily; }
30 3	* * *	demo	/home/demo/scripts/backup.sh >> /var/log/backup.log 2>&1
0  5	* * 0	demo	/home/demo/scripts/update-containers.sh
`;

export const INTERFACES = `# This file describes the network interfaces available on your system
source /etc/network/interfaces.d/*

auto lo
iface lo inet loopback

allow-hotplug enp3s0
iface enp3s0 inet static
    address ${HOST.lanCidr}
    gateway ${HOST.gateway}
`;

export const SUDOERS = `#
# This file MUST be edited with the 'visudo' command as root.
#
Defaults	env_reset
Defaults	mail_badpass
Defaults	secure_path="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Defaults	use_pty

root	ALL=(ALL:ALL) ALL
%sudo	ALL=(ALL:ALL) ALL

@includedir /etc/sudoers.d
`;

export const SHADOW = `root:*:20000:0:99999:7:::
daemon:*:20000:0:99999:7:::
demo:$y$j9T$demo.not.a.real.hash$0000000000000000000000000000000000000:20300:0:99999:7:::
`;

export const MOTD = `
The programs included with the Debian GNU/Linux system are free software;
the exact distribution terms for each program are described in the
individual files in /usr/share/doc/*/copyright.

Debian GNU/Linux comes with ABSOLUTELY NO WARRANTY, to the extent
permitted by applicable law.
`;

export const WARGO_TOML = `# Wargo configuration
listen = "0.0.0.0:8800"
data_dir = "/var/lib/wargo"

[files]
show_mounts = true
trash_dir = "/home/demo/.local/share/wargo/trash"
max_edit_size = "4MiB"

[[files.roots]]
name = "Home"
path = "/home/demo"

[[files.roots]]
name = "DATA"
path = "/DATA"
`;

export const GRUB_CFG = `#
# DO NOT EDIT THIS FILE
#
# It is automatically generated by grub-mkconfig using templates
# from /etc/grub.d and settings from /etc/default/grub
#
set default="0"
set timeout=5
menuentry 'Debian GNU/Linux' --class debian --class gnu-linux --class gnu --class os {
	load_video
	insmod gzio
	insmod part_gpt
	insmod ext2
	search --no-floppy --fs-uuid --set=root 3f6c2a8e-1d4b-4e8a-9b71-5c0e2d9f7a13
	echo	'Loading Linux ${HOST.kernel} ...'
	linux	/boot/vmlinuz-${HOST.kernel} root=UUID=3f6c2a8e-1d4b-4e8a-9b71-5c0e2d9f7a13 ro quiet
	echo	'Loading initial ramdisk ...'
	initrd	/boot/initrd.img-${HOST.kernel}
}
`;

// ---- logs ------------------------------------------------------------------------

function stamp(ms: number) {
  const d = new Date(Date.now() - ms);
  const mon = d.toLocaleString("en-US", { month: "short" });
  const p = (n: number) => String(n).padStart(2, "0");
  return `${mon} ${String(d.getDate()).padStart(2, " ")} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/** A plausible /var/log/syslog for the last ~12 hours. */
export function syslog(): string {
  const r = seeded(42);
  const h = HOST.hostname;
  const lines: [number, string][] = [];
  const templates: (() => string)[] = [
    () => `${h} systemd[1]: Started session-${Math.floor(r() * 900 + 100)}.scope - Session of User demo.`,
    () => `${h} CRON[${Math.floor(r() * 30000 + 2000)}]: (root) CMD (cd / && run-parts --report /etc/cron.hourly)`,
    () => `${h} dockerd[812]: time="${new Date().toISOString().slice(0, 19)}Z" level=info msg="ignoring event" container=${Math.floor(r() * 1e12).toString(16)} module=libcontainerd namespace=moby topic=/tasks/delete type="*events.TaskDelete"`,
    () => `${h} kernel: [${(r() * 2e6).toFixed(6)}] br-3f1a9c2d7e4b: port ${Math.floor(r() * 9 + 1)}(veth${Math.floor(r() * 1e7).toString(16)}) entered forwarding state`,
    () => `${h} systemd[1]: docker-${Math.floor(r() * 1e12).toString(16)}.scope: Deactivated successfully.`,
    () => `${h} tailscaled[701]: magicsock: derp-4 connected; connGen=1`,
    () => `${h} systemd-timesyncd[512]: Contacted time server 194.36.144.87:123 (0.debian.pool.ntp.org).`,
    () => `${h} smartd[633]: Device: /dev/sdb [SAT], SMART Usage Attribute: 194 Temperature_Celsius changed from ${Math.floor(r() * 4 + 60)} to ${Math.floor(r() * 4 + 61)}`,
    () => `${h} wargo[1024]: level=INFO msg="session started" user=demo ip=192.168.1.42`,
    () => `${h} systemd[1]: Starting apt-daily.service - Daily apt download activities...`,
    () => `${h} systemd[1]: apt-daily.service: Deactivated successfully.`,
    () => `${h} containerd[790]: time="${new Date().toISOString().slice(0, 19)}Z" level=info msg="shim disconnected" namespace=moby`,
  ];
  let t = 12 * HOUR;
  while (t > 0) {
    lines.push([t, templates[Math.floor(r() * templates.length)]()]);
    t -= Math.floor(r() * 6 * MIN + 20_000);
  }
  lines.push([3.5 * HOUR, `${h} systemd[1]: Failed to start backup-photos.service - Nightly photo backup.`]);
  lines.push([3.49 * HOUR, `${h} rsync[48211]: rsync: [sender] link_stat "/DATA/Photos/2026/.thumbnails" failed: No such file or directory (2)`]);
  lines.push([2 * HOUR, `${h} docker[812]: jellyfin health check failed: curl: (7) Failed to connect to localhost port 8096`]);
  lines.sort((a, b) => b[0] - a[0]);
  return lines.map(([ms, l]) => `${stamp(ms)} ${l}`).join("\n") + "\n";
}

export function authLog(): string {
  const h = HOST.hostname;
  const rows: [number, string][] = [
    [9 * HOUR, `sshd[30211]: Accepted publickey for demo from 192.168.1.42 port 51544 ssh2: ED25519 SHA256:q7c3Vb2h0R9kTzW1pYdE`],
    [9 * HOUR - 2000, `sshd[30211]: pam_unix(sshd:session): session opened for user demo(uid=1000) by (uid=0)`],
    [7 * HOUR, `sudo:     demo : TTY=pts/0 ; PWD=/home/demo ; USER=root ; COMMAND=/usr/bin/apt update`],
    [5 * HOUR, `sshd[33102]: Invalid user admin from 100.101.42.7 port 40122`],
    [3 * HOUR, `wargo[1024]: elevation approved for demo: files.op write /etc/hosts`],
    [40 * MIN, `sudo:     demo : TTY=pts/1 ; PWD=/home/demo/stacks/media ; USER=root ; COMMAND=/usr/bin/docker compose up -d`],
  ];
  return rows.map(([ms, l]) => `${stamp(ms)} ${h} ${l}`).join("\n") + "\n";
}

export function dpkgLog(): string {
  const pk = ["docker-ce:amd64 5:27.3.0-1~debian.12~bookworm 5:27.3.1-1~debian.12~bookworm", "tailscale:amd64 1.76.1 1.76.6", "linux-image-6.1.0-28-amd64:amd64 <none> 6.1.119-1", "openssl:amd64 3.0.14-1~deb12u2 3.0.15-1~deb12u1"];
  return pk.map((p, i) => `${day((12 - i * 3) * DAY)} 06:2${i}:14 upgrade ${p}`).join("\n") + "\n";
}

export function backupLog(): string {
  const out: string[] = [];
  for (let d = 6; d >= 0; d--) {
    const date = day(d * DAY + 8 * HOUR);
    out.push(`${date}T03:30:01+01:00 dumping immich database`);
    out.push(`${date}T03:30:42+01:00 restic backup`);
    out.push(`Files:         ${120 + d * 7} new, ${410 - d * 3} changed, 48211 unmodified`);
    out.push(`Added to the repository: ${(1.2 + d * 0.31).toFixed(3)} GiB`);
    out.push(`${date}T03:41:${10 + d}+01:00 pruning`);
    out.push(`${date}T03:44:${20 + d}+01:00 btrfs snapshot`);
    out.push(`${date}T03:44:${21 + d}+01:00 done`);
  }
  return out.join("\n") + "\n";
}

export function subtitles(title: string): string {
  const lines = ["[Music]", "Where are we going?", "Somewhere the signal can't follow.", "You said that last time.", "This time I mean it.", "[Door creaks]", "Is anyone here?"];
  return (
    lines
      .map((l, i) => {
        const s = 60 + i * 4;
        const t = (x: number) => `00:${String(Math.floor(x / 60)).padStart(2, "0")}:${String(x % 60).padStart(2, "0")},${i % 2 ? "500" : "000"}`;
        return `${i + 1}\n${t(s)} --> ${t(s + 3)}\n${l}`;
      })
      .join("\n\n") + `\n\n${lines.length + 1}\n00:01:30,000 --> 00:01:34,000\n${title}\n`
  );
}

export function nfo(title: string, year: string): string {
  return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>${title}</title>
  <year>${year}</year>
  <source>Blu-ray</source>
  <dateadded>${day(40 * DAY)} 21:14:02</dateadded>
</movie>
`;
}

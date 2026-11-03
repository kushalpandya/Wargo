// SPDX-License-Identifier: AGPL-3.0-only
// Terminal: a simulated bash rendered through xterm. Sessions (host, root,
// container) live in memory while the page is open and speak the same
// WebSocket protocol as Wargo's PTY bridge: binary output, JSON control
// messages (resize, title, exit) and the 4000-4004 close codes.
import { ws, type RequestHandler, type WebSocketHandler } from "msw";
import type { TermInfo, TermKind } from "@/features/terminal/session";
import type { Container, LogLine } from "@/features/containers/types";
import { api, badRequest, body, fail, json, noContent, notFound } from "../core/http";
import { DAY, GB, HOUR, MB, MIN, TB, seeded } from "../core/rand";
import { CONTAINERS, DEMO_USER, HOME, HOST } from "./host";
import {
  PORTS, SELF_PID, allocPid, containerId, containerIp, containerStart, makeSpec, registerProcSource, sample, specs, type Spec,
} from "./processes";

// ============================================================================
// ANSI helpers
// ============================================================================

const E = "\x1b[";
const A = {
  reset: `${E}0m`, bold: `${E}1m`, dim: `${E}2m`, red: `${E}31m`, green: `${E}32m`, yellow: `${E}33m`, blue: `${E}34m`, magenta: `${E}35m`, cyan: `${E}36m`,
  bred: `${E}01;31m`, bgreen: `${E}01;32m`, bblue: `${E}01;34m`, bcyan: `${E}01;36m`, bmagenta: `${E}01;35m`, inv: `${E}30;42m`,
};
// eslint-disable-next-line no-control-regex
const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07/g, "");
const vlen = (s: string) => stripAnsi(s).length;
const padR = (s: string, n: number) => s + " ".repeat(Math.max(0, n - vlen(s)));
const padL = (s: string, n: number) => " ".repeat(Math.max(0, n - vlen(s))) + s;

/** Lays out rows as aligned columns (like docker's tabwriter, 3 spaces apart). */
function table(rows: string[][], gap = 3): string {
  const w: number[] = [];
  for (const r of rows) r.forEach((c, i) => (w[i] = Math.max(w[i] ?? 0, vlen(c))));
  return rows.map((r) => r.map((c, i) => (i === r.length - 1 ? c : padR(c, w[i] + gap))).join("").trimEnd()).join("\n") + "\n";
}

/** Human sizes like coreutils -h (1024-based, K M G T). */
function hsize(n: number): string {
  if (n < 1024) return String(Math.round(n));
  const u = ["K", "M", "G", "T", "P"];
  let v = n;
  let i = -1;
  while (v >= 1024 && i < u.length - 1) (v /= 1024), i++;
  return (v < 10 ? (Math.ceil(v * 10) / 10).toFixed(1) : String(Math.ceil(v))) + u[i];
}
/** free -h style (Gi, Mi). */
function hfree(n: number): string {
  const u = ["B", "Ki", "Mi", "Gi", "Ti"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) (v /= 1024), i++;
  return (v < 10 ? v.toFixed(1) : String(Math.round(v))) + u[i];
}
/** Docker's human duration ("3 weeks", "About an hour"). */
function dockerAgo(ms: number): string {
  const s = ms / 1000;
  if (s < 1) return "Less than a second";
  if (s < 60) return `${Math.floor(s)} seconds`;
  const m = s / 60;
  if (m < 1.5) return "About a minute";
  if (m < 60) return `${Math.round(m)} minutes`;
  const h = m / 60;
  if (Math.round(h) === 1) return "About an hour";
  if (h < 48) return `${Math.round(h)} hours`;
  const d = h / 24;
  if (d < 14) return `${Math.floor(d)} days`;
  if (d < 56) return `${Math.floor(d / 7)} weeks`;
  if (d < 730) return `${Math.floor(d / 30)} months`;
  return `${Math.floor(d / 365)} years`;
}
const two = (n: number) => String(n).padStart(2, "0");
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const hhmm = (d: Date) => `${two(d.getHours())}:${two(d.getMinutes())}`;
const hhmmss = (d: Date) => `${hhmm(d)}:${two(d.getSeconds())}`;
function tzName() {
  try {
    return new Intl.DateTimeFormat("en-US", { timeZoneName: "short" }).formatToParts(new Date()).find((p) => p.type === "timeZoneName")?.value ?? "UTC";
  } catch {
    return "UTC";
  }
}
function upFor(): string {
  const s = Math.floor(Date.now() / 1000) - HOST.bootTime;
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  return `${d} days, ${String(h).padStart(2)}:${two(m)}`;
}

function hashOf(s: string) {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

// ============================================================================
// Virtual filesystem
// ============================================================================

interface Meta {
  owner?: string;
  group?: string;
  mode?: string;
  mtime?: number;
}
interface Dir extends Meta {
  kind: "d";
  c: Record<string, Node>;
}
interface File extends Meta {
  kind: "f";
  body?: string | (() => string);
  size?: number;
  exec?: boolean;
}
interface Link extends Meta {
  kind: "l";
  to: string;
}
type Node = Dir | File | Link;

const D = (c: Record<string, Node> = {}, m: Meta = {}): Dir => ({ kind: "d", c, ...m });
const F = (body: string | (() => string), m: Omit<File, "kind" | "body"> = {}): File => ({ kind: "f", body, ...m });
const BIN = (size: number, m: Omit<File, "kind" | "size"> = {}): File => ({ kind: "f", size, ...m });
const L = (to: string): Link => ({ kind: "l", to });
const X = (size = 140_000): File => ({ kind: "f", size, exec: true, mode: "-rwxr-xr-x" });

function chown<T extends Node>(n: T, owner: string, group = owner): T {
  n.owner ??= owner;
  n.group ??= group;
  if (n.kind === "d") for (const k of Object.values(n.c)) chown(k, owner, group);
  return n;
}

const fileText = (f: File) => (typeof f.body === "function" ? f.body() : (f.body ?? ""));
const fileSize = (f: File) => f.size ?? new TextEncoder().encode(fileText(f)).length;

const OS_RELEASE = `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION="12 (bookworm)"
VERSION_CODENAME=bookworm
ID=debian
HOME_URL="https://www.debian.org/"
SUPPORT_URL="https://www.debian.org/support"
BUG_REPORT_URL="https://bugs.debian.org/"
`;
const ALPINE_RELEASE = `NAME="Alpine Linux"
ID=alpine
VERSION_ID=3.20.3
PRETTY_NAME="Alpine Linux v3.20"
HOME_URL="https://alpinelinux.org/"
BUG_REPORT_URL="https://gitlab.alpinelinux.org/alpine/aports/-/issues"
`;

const NOTES = `homelab notes
=============

- [x] move media to /DATA (8 TB WD Red, sda1)
- [x] btrfs snapshots of /DATA/Photos to /mnt/backup every night
- [ ] fix backup-photos.service (rsync trips over .thumbnails)
- [ ] sda has pending sectors (smartd keeps warning): order a spare drive
- [ ] jellyfin health check keeps timing out, look at the transcode cache
- [ ] update immich (v1.123.0) after reading the breaking changes
- [ ] nextcloud-cron exits with 1: config.php owner is 33, run as www-data

ports: jellyfin 8096, sonarr 8989, radarr 7878, prowlarr 9696,
       immich 2283, nextcloud 8080, home assistant 8123, pi-hole 8053,
       vaultwarden 8222, uptime kuma 3001, paperless 8010

tailscale: ${HOST.tailscaleIp} (homelab.tail1234.ts.net)
`;

const BASHRC = `# ~/.bashrc: executed by bash(1) for non-login shells.

# If not running interactively, don't do anything
case $- in
    *i*) ;;
      *) return;;
esac

HISTCONTROL=ignoreboth
shopt -s histappend
HISTSIZE=1000
HISTFILESIZE=2000

PS1='\${debian_chroot:+(\$debian_chroot)}\\[\\033[01;32m\\]\\u@\\h\\[\\033[00m\\]:\\[\\033[01;34m\\]\\w\\[\\033[00m\\]\\$ '

# If this is an xterm set the title to user@host:dir
case "$TERM" in
xterm*|rxvt*)
    PS1="\\[\\e]0;\${debian_chroot:+(\$debian_chroot)}\\u@\\h: \\w\\a\\]$PS1"
    ;;
esac

alias ls='ls --color=auto'
alias grep='grep --color=auto'
alias ll='ls -alF'
alias la='ls -A'
alias l='ls -CF'
alias dc='docker compose'
`;

const PROFILE = `# ~/.profile: executed by the command interpreter for login shells.
if [ -n "$BASH_VERSION" ]; then
    if [ -f "$HOME/.bashrc" ]; then
        . "$HOME/.bashrc"
    fi
fi

if [ -d "$HOME/bin" ] ; then
    PATH="$HOME/bin:$PATH"
fi
`;

const BACKUP_SH = `#!/usr/bin/env bash
# Nightly: snapshot photos and documents to the backup drive.
set -euo pipefail

SRC=/DATA/Photos
DST=/mnt/backup/photos
STAMP=$(date +%Y-%m-%d)

rsync -aH --delete --exclude '.thumbnails' "$SRC/" "$DST/current/"
btrfs subvolume snapshot -r "$DST/current" "$DST/snap-$STAMP"
find "$DST" -maxdepth 1 -name 'snap-*' -mtime +30 -exec btrfs subvolume delete {} +
`;

const NETWORK_MD = `# Network

| what          | address            |
|---------------|--------------------|
| router        | ${HOST.gateway}        |
| homelab (LAN) | ${HOST.lanIp}       |
| tailscale     | ${HOST.tailscaleIp}       |
| pi-hole DNS   | ${HOST.lanIp}:53    |

Docker networks: one per compose project (172.18-23.0.0/16).
`;

/** compose.yaml for one project, from host.ts CONTAINERS. */
function composeFile(group: string): string {
  const lines = ["services:"];
  for (const c of CONTAINERS.filter((x) => x.group === group)) {
    lines.push(`  ${c.name}:`, `    image: ${c.image}`, `    container_name: ${c.name}`);
    if (c.image.includes("linuxserver")) lines.push("    environment:", "      - PUID=1000", "      - PGID=1000", "      - TZ=Europe/Berlin");
    const ports = PORTS[c.name] ?? [];
    if (c.name === "home-assistant") lines.push("    network_mode: host");
    if (ports.length) lines.push("    ports:", ...ports.map(([priv, pub, proto]) => `      - ${pub}:${priv}${proto ? `/${proto}` : ""}`));
    lines.push("    volumes:", `      - ./${c.name}:/config`);
    if (group === "media") lines.push("      - /DATA/media:/media");
    lines.push("    restart: unless-stopped", "");
  }
  return lines.join("\n");
}

function stacks(): Dir {
  const out: Record<string, Node> = {};
  for (const g of ["media", "immich", "nextcloud", "home", "network", "tools"]) {
    const c: Record<string, Node> = { "compose.yaml": F(composeFile(g), { mtime: Date.now() - (5 + g.length) * DAY }), ".env": F(`TZ=Europe/Berlin\nPUID=1000\nPGID=1000\n`, { mode: "-rw-------" }) };
    for (const ct of CONTAINERS.filter((x) => x.group === g)) c[ct.name] = D({ config: D() });
    out[g] = D(c);
  }
  return D(out);
}

const now0 = Date.now();
const home = chown(
  D(
    {
      ".bash_history": F("docker ps\ncd stacks/media\ndocker compose pull\ndocker compose up -d\nhtop\n", { mode: "-rw-------" }),
      ".bash_logout": F("# ~/.bash_logout: executed by bash(1) when login shell exits.\n"),
      ".bashrc": F(BASHRC),
      ".profile": F(PROFILE),
      ".ssh": D({ authorized_keys: F("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK3mQ0Zc7rD3f9pVwK1y8u2bLx4tHn6sRj5aE0oP9vWq demo@laptop\n", { mode: "-rw-------" }) }, { mode: "drwx------" }),
      bin: D({ "backup.sh": F(BACKUP_SH, { exec: true, mode: "-rwxr-xr-x" }) }),
      Documents: D({
        "homelab-network.md": F(NETWORK_MD, { mtime: now0 - 40 * DAY }),
        "ups-runbook.txt": F("On power loss: NUT shuts the server down at 20% battery.\nAfter power returns: check the RAID-less drives with smartctl -a.\n", { mtime: now0 - 90 * DAY }),
        receipts: D({ "2026-08-wd-red-8tb.pdf": BIN(184_220), "2026-02-ryzen-5700g.pdf": BIN(96_118) }),
        "taxes-2025.ods": BIN(48_312, { mtime: now0 - 200 * DAY }),
      }),
      Downloads: D({
        "debian-12.8.0-amd64-netinst.iso": BIN(661_651_456, { mtime: now0 - 300 * DAY }),
        "jellyfin-backup-2026-10-28.tar.gz": BIN(1_284_511_232, { mtime: now0 - 6 * DAY }),
        "ubuntu-24.04.1-live-server-amd64.iso": BIN(2_773_874_688, { mtime: now0 - 80 * DAY }),
      }),
      docker: L("stacks"),
      stacks: stacks(),
      "notes.txt": F(NOTES, { mtime: now0 - 2 * DAY - 3 * HOUR }),
    },
    { mode: "drwx------" },
  ),
  DEMO_USER,
);

function cpuinfo(): string {
  const blocks: string[] = [];
  for (let i = 0; i < HOST.threads; i++) {
    blocks.push(`processor\t: ${i}
vendor_id\t: AuthenticAMD
cpu family\t: 25
model\t\t: 80
model name\t: ${HOST.cpuModel}
stepping\t: 0
microcode\t: 0xa50000c
cpu MHz\t\t: ${(1400 + ((i * 937) % 3200)).toFixed(3)}
cache size\t: 512 KB
physical id\t: 0
siblings\t: ${HOST.threads}
core id\t\t: ${i % HOST.cores}
cpu cores\t: ${HOST.cores}
apicid\t\t: ${i}
fpu\t\t: yes
cpuid level\t: 16
wp\t\t: yes
flags\t\t: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov pat pse36 clflush mmx fxsr sse sse2 ht syscall nx mmxext fxsr_opt pdpe1gb rdtscp lm constant_tsc rep_good nopl nonstop_tsc cpuid extd_apicid aperfmperf rapl pni pclmulqdq monitor ssse3 fma cx16 sse4_1 sse4_2 movbe popcnt aes xsave avx f16c rdrand lahf_lm cmp_legacy svm extapic cr8_legacy abm sse4a misalignsse 3dnowprefetch osvw ibs skinit wdt tce topoext perfctr_core perfctr_nb bpext perfctr_llc mwaitx cpb cat_l3 cdp_l3 hw_pstate ssbd mba ibrs ibpb stibp vmmcall fsgsbase bmi1 avx2 smep bmi2 erms invpcid cqm rdt_a rdseed adx smap clflushopt clwb sha_ni xsaveopt xsavec xgetbv1 xsaves cqm_llc cqm_occup_llc cqm_mbm_total cqm_mbm_local clzero irperf xsaveerptr rdpru wbnoinvd cppc arat npt lbrv svm_lock nrip_save tsc_scale vmcb_clean flushbyasid decodeassists pausefilter pfthreshold avic v_vmsave_vmload vgif v_spec_ctrl umip pku ospke vaes vpclmulqdq rdpid overflow_recov succor smca fsrm
bogomips\t: 7585.71
TLB size\t: 2560 4K pages
clflush size\t: 64
cache_alignment\t: 64
address sizes\t: 48 bits physical, 48 bits virtual
power management: ts ttp tm hwpstate cpb eff_freq_ro [13] [14]
`);
  }
  return blocks.join("\n");
}

const memUsedPct = () => 41 + Math.sin(Date.now() / 600_000) * 1.5;
function meminfo(): string {
  const kb = (n: number) => `${String(Math.round(n / 1024)).padStart(8)} kB`;
  const t = HOST.memTotal;
  const used = (memUsedPct() / 100) * t;
  const cached = 9.4 * GB;
  return [
    `MemTotal:       ${kb(t)}`, `MemFree:        ${kb(t - used - cached - 0.6 * GB)}`, `MemAvailable:   ${kb(t - used)}`, `Buffers:        ${kb(0.6 * GB)}`,
    `Cached:         ${kb(cached)}`, `SwapCached:     ${kb(12 * MB)}`, `Active:         ${kb(used * 0.8)}`, `Inactive:       ${kb(cached * 0.7)}`,
    `SwapTotal:      ${kb(HOST.swapTotal)}`, `SwapFree:       ${kb(HOST.swapTotal - 210 * MB)}`, `Dirty:          ${kb(1.2 * MB)}`, `Shmem:          ${kb(412 * MB)}`,
  ].join("\n") + "\n";
}

const procDir = () =>
  D({
    cpuinfo: F(cpuinfo, { size: 0, mode: "-r--r--r--" }),
    meminfo: F(meminfo, { size: 0, mode: "-r--r--r--" }),
    uptime: F(() => `${(Date.now() / 1000 - HOST.bootTime).toFixed(2)} ${((Date.now() / 1000 - HOST.bootTime) * 14.2).toFixed(2)}\n`, { size: 0, mode: "-r--r--r--" }),
    loadavg: F(() => `${loads().join(" ")} 2/1322 ${allocPeek()}\n`, { size: 0, mode: "-r--r--r--" }),
    version: F(`Linux version ${HOST.kernel} (debian-kernel@lists.debian.org) (gcc-12 (Debian 12.2.0-14) 12.2.0, GNU ld (GNU Binutils for Debian) 2.40) #1 SMP PREEMPT_DYNAMIC Debian 6.1.119-1 (2024-11-22)\n`, { size: 0, mode: "-r--r--r--" }),
  }, { mode: "dr-xr-xr-x" });

let pidPeek = 0;
const allocPeek = () => pidPeek || 912345;

const SHOWS = ["Severance", "The Bear", "Shogun", "Slow Horses", "Andor", "Silo", "The Last of Us", "Fallout", "Blue Eye Samurai"];
const MOVIES = ["Dune Part Two (2024)", "Oppenheimer (2023)", "Past Lives (2023)", "The Holdovers (2023)", "Civil War (2024)", "Furiosa (2024)", "Perfect Days (2023)", "Arrival (2016)"];

const USR_BIN = ["apt", "awk", "bash", "cat", "chmod", "cp", "curl", "date", "df", "docker", "du", "echo", "fastfetch", "find", "free", "grep", "head", "htop", "id", "ip", "less", "ls", "lsblk", "mkdir", "mv", "nano", "neofetch", "ping", "ps", "python3", "rm", "rsync", "sed", "smartctl", "sort", "ssh", "sudo", "systemctl", "tail", "tailscale", "tar", "tmux", "top", "uname", "uptime", "vim", "watch", "wc", "wget", "which", "whoami"];

function hostFs(): Dir {
  const media = D({
    movies: D(Object.fromEntries(MOVIES.map((m) => [m, D({ [`${m}.mkv`]: BIN(8 * GB + (hashOf(m) % 9) * GB) })]))),
    tv: D(Object.fromEntries(SHOWS.map((s) => [s, D({ "Season 01": D(), "Season 02": D() })]))),
    music: D({ "Radiohead": D(), "Daft Punk": D(), "Nils Frahm": D() }),
  });
  return chown(
    D({
      bin: L("usr/bin"),
      boot: D({ efi: D({}, { mode: "drwx------" }), [`vmlinuz-${HOST.kernel}`]: BIN(8_082_400), [`initrd.img-${HOST.kernel}`]: BIN(61_239_811), [`config-${HOST.kernel}`]: BIN(260_011), [`System.map-${HOST.kernel}`]: BIN(83_644) }),
      DATA: chown(D({ media, Photos: D({ "2024": D(), "2025": D(), "2026": D() }), downloads: D({ complete: D(), incomplete: D() }), "lost+found": D({}, { owner: "root", group: "root", mode: "drwx------" }) }), DEMO_USER),
      dev: D({ null: BIN(0, { mode: "crw-rw-rw-" }), nvme0n1: BIN(0, { mode: "brw-rw----" }), sda: BIN(0, { mode: "brw-rw----" }), sdb: BIN(0, { mode: "brw-rw----" }) }),
      etc: D({
        "os-release": F(OS_RELEASE),
        debian_version: F("12.8\n"),
        hostname: F(`${HOST.hostname}\n`),
        hosts: F(`127.0.0.1\tlocalhost\n127.0.1.1\t${HOST.hostname}\n${HOST.lanIp}\t${HOST.hostname}.lan ${HOST.hostname}\n\n::1     localhost ip6-localhost ip6-loopback\nff02::1 ip6-allnodes\nff02::2 ip6-allrouters\n`),
        fstab: F(`# <file system> <mount point> <type> <options> <dump> <pass>\nUUID=6f1c2a9e-4b7d-4a1e-9c33-1f2e8d7b5a10 /           ext4  errors=remount-ro 0 1\nUUID=3A1B-9C2D                            /boot/efi   vfat  umask=0077        0 1\nUUID=b4e2c7d1-0a9f-4e55-8d21-7c6f3a2e9b84 none        swap  sw                0 0\nLABEL=DATA                                /DATA       ext4  defaults,noatime  0 2\nLABEL=Backup                              /mnt/backup btrfs defaults,compress=zstd,noatime 0 0\n`),
        "resolv.conf": F(`nameserver 127.0.0.53\noptions edns0 trust-ad\nsearch lan\n`),
        issue: F("Debian GNU/Linux 12 \\n \\l\n\n"),
        timezone: F("Europe/Berlin\n"),
        passwd: F(`root:x:0:0:root:/root:/bin/bash\ndaemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\nwww-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\nnobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\nmessagebus:x:100:107::/nonexistent:/usr/sbin/nologin\navahi:x:104:110:Avahi mDNS daemon,,,:/run/avahi-daemon:/usr/sbin/nologin\nsystemd-resolve:x:991:991:systemd Resolver:/:/usr/sbin/nologin\npolkitd:x:996:996:polkit:/nonexistent:/usr/sbin/nologin\nsystemd-timesync:x:997:997:systemd Time Synchronization:/:/usr/sbin/nologin\nsystemd-coredump:x:999:999:systemd Core Dumper:/:/usr/sbin/nologin\ndemo:x:1000:1000:Demo User,,,:/home/demo:/bin/bash\n`),
        shadow: F("root:*:20000:0:99999:7:::\n", { mode: "-rw-r-----", group: "shadow" }),
        samba: D({ "smb.conf": F(`[global]\n   workgroup = WORKGROUP\n   server string = homelab\n   server role = standalone server\n   map to guest = bad user\n\n[DATA]\n   path = /DATA\n   read only = no\n   valid users = demo\n\n[backup]\n   path = /mnt/backup\n   read only = yes\n   valid users = demo\n`) }),
        docker: D({ "daemon.json": F(`{\n  "log-driver": "json-file",\n  "log-opts": { "max-size": "10m", "max-file": "3" },\n  "default-address-pools": [{ "base": "172.17.0.0/12", "size": 16 }]\n}\n`) }),
        ssh: D({ sshd_config: F("Include /etc/ssh/sshd_config.d/*.conf\nPermitRootLogin no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM yes\nX11Forwarding no\nPrintMotd no\nSubsystem sftp /usr/lib/openssh/sftp-server\n") }),
      }),
      home: D({ [DEMO_USER]: home }),
      lib: L("usr/lib"),
      lib64: L("usr/lib64"),
      "lost+found": D({}, { mode: "drwx------" }),
      media: D(),
      mnt: D({ backup: chown(D({ photos: D({ current: D(), "snap-2026-11-01": D(), "snap-2026-11-02": D() }), documents: D(), "@home": D() }), DEMO_USER) }),
      opt: D(),
      proc: procDir(),
      root: D({ ".bashrc": F("# ~/.bashrc: executed by bash(1) for non-login shells.\nexport LS_OPTIONS='--color=auto'\nalias ls='ls $LS_OPTIONS'\n"), ".profile": F(PROFILE) }, { mode: "drwx------" }),
      run: D({ docker: D(), "docker.sock": BIN(0, { mode: "srw-rw----", group: "docker" }) }),
      sbin: L("usr/sbin"),
      srv: D(),
      sys: D({}, { mode: "dr-xr-xr-x" }),
      tmp: D({}, { mode: "drwxrwxrwt" }),
      usr: D({ bin: D(Object.fromEntries(USR_BIN.map((n) => [n, X(80_000 + (hashOf(n) % 900_000))]))), lib: D(), lib64: D(), local: D({ bin: D({ wargo: X(24_118_272) }) }), sbin: D(), share: D() }),
      var: D({ lib: D({ docker: D({}, { mode: "drwx--x---" }), wargo: chown(D({ "wargo.db": BIN(3_407_872, { mode: "-rw-------" }) }, { mode: "drwx------" }), DEMO_USER) }), log: D({ journal: D() }), tmp: D({}, { mode: "drwxrwxrwt" }) }),
    }),
    "root",
  );
}

const ALPINE = new Set(["sonarr", "radarr", "prowlarr", "transmission", "home-assistant", "immich-redis"]);
const NO_BASH = new Set(["immich-redis"]);

function containerFs(name: string): Dir {
  const alpine = ALPINE.has(name);
  const lsio = /linuxserver/.test(CONTAINERS.find((c) => c.name === name)?.image ?? "");
  const extra: Record<string, Node> = {};
  if (lsio || name === "jellyfin" || name === "home-assistant") extra.config = D({ "config.xml": F("<Config>\n  <Port>8989</Port>\n  <UrlBase></UrlBase>\n  <LogLevel>info</LogLevel>\n</Config>\n") });
  if (lsio) extra.app = D({ [name]: D({ bin: D() }) });
  if (name === "jellyfin" || ["sonarr", "radarr"].includes(name)) extra.media = D({ movies: D(), tv: D(), music: D() });
  if (name === "jellyfin") extra.cache = D({ transcodes: D() });
  if (name === "immich-redis" || name === "immich-postgres") extra.data = D({ "dump.rdb": BIN(1_841_221) });
  if (name === "immich-server") extra.usr = D({ src: D({ app: D({ dist: D(), "package.json": F('{\n  "name": "immich",\n  "version": "1.122.0"\n}\n') }) }), bin: D(), lib: D() });
  if (name === "nextcloud") extra.var = D({ www: D({ html: D({ "config": D(), "index.php": F("<?php\nrequire __DIR__ . '/lib/versioncheck.php';\n"), "occ": X(283) }) }) });
  const id12 = containerId(name).slice(0, 12);
  return chown(
    D({
      bin: alpine ? D() : L("usr/bin"),
      dev: D(),
      etc: D({ "os-release": F(alpine ? ALPINE_RELEASE : OS_RELEASE), hostname: F(`${id12}\n`), hosts: F(`127.0.0.1\tlocalhost\n${containerIp(name) || HOST.lanIp}\t${id12}\n`), "resolv.conf": F("nameserver 127.0.0.11\noptions ndots:0\n") }),
      home: D(),
      lib: alpine ? D() : L("usr/lib"),
      mnt: D(),
      opt: D(),
      proc: procDir(),
      root: D({}, { mode: "drwx------" }),
      run: D(),
      sbin: alpine ? D() : L("usr/sbin"),
      srv: D(),
      sys: D({}, { mode: "dr-xr-xr-x" }),
      tmp: D({}, { mode: "drwxrwxrwt" }),
      usr: D({ bin: D(), lib: D(), local: D(), share: D() }),
      var: D({ log: D(), lib: D() }),
      ...extra,
    }),
    "root",
  );
}

const HOST_FS = hostFs();
const ctrFsCache = new Map<string, Dir>();
const fsFor = (where: string) => (where === "host" ? HOST_FS : (ctrFsCache.get(where) ?? ctrFsCache.set(where, containerFs(where)).get(where)!));

/** Normalizes p against cwd (handles ., .., ~ already expanded). */
function normPath(cwd: string, p: string): string {
  const parts = (p.startsWith("/") ? p : `${cwd}/${p}`).split("/");
  const out: string[] = [];
  for (const s of parts) {
    if (!s || s === ".") continue;
    if (s === "..") out.pop();
    else out.push(s);
  }
  return "/" + out.join("/");
}

/** Looks up a path; follows links (also the final one when follow is set). */
function lookup(root: Dir, path: string, follow = true, depth = 0): Node | null {
  if (depth > 8) return null;
  const parts = path.split("/").filter(Boolean);
  let cur: Node = root;
  let at = "/";
  for (let i = 0; i < parts.length; i++) {
    if (cur.kind === "l") {
      const t = lookup(root, normPath(parentOf(at), cur.to), true, depth + 1);
      if (!t) return null;
      cur = t;
    }
    if (cur.kind !== "d") return null;
    const next: Node | undefined = cur.c[parts[i]];
    if (!next) return null;
    cur = next;
    at = normPath(at, parts[i]);
  }
  if (follow && cur.kind === "l") return lookup(root, normPath(parentOf(at), cur.to), true, depth + 1);
  return cur;
}
const parentOf = (p: string) => normPath(p, "..");
const baseName = (p: string) => p.split("/").filter(Boolean).pop() ?? "/";

/** Private dirs/files other users can't read. */
function canRead(n: Node, user: string): boolean {
  if (user === "root") return true;
  const m = n.mode ?? "";
  if (n.owner === user) return true;
  if (n.kind === "d") return m === "" || m[7] === "r";
  return m === "" || m[7] === "r";
}

function modeOf(n: Node): string {
  if (n.mode) return n.mode;
  if (n.kind === "d") return "drwxr-xr-x";
  if (n.kind === "l") return "lrwxrwxrwx";
  return n.exec ? "-rwxr-xr-x" : "-rw-r--r--";
}

function mtimeOf(n: Node, path: string): number {
  if (n.mtime) return n.mtime;
  const h = hashOf(path);
  if (path.startsWith("/proc") || path === "/tmp" || path === "/run") return Date.now() - (h % 3600) * 1000;
  return Date.now() - (2 + (h % 380)) * DAY - (h % 86_400) * 1000;
}

function lsDate(t: number): string {
  const d = new Date(t);
  const recent = Date.now() - t < 180 * DAY;
  return `${MONTHS[d.getMonth()]} ${String(d.getDate()).padStart(2)} ${recent ? hhmm(d) : " " + d.getFullYear()}`;
}

function colorName(name: string, n: Node, color: boolean): string {
  if (!color) return name;
  let c = "";
  if (n.kind === "d") c = n.mode === "drwxrwxrwt" ? `${E}30;42m` : A.bblue;
  else if (n.kind === "l") c = A.bcyan;
  else if (n.exec) c = A.bgreen;
  else if (/\.(tar|gz|tgz|zip|xz|7z|zst)$/.test(name)) c = A.bred;
  else if (/\.(jpg|png|gif|mkv|mp4|iso)$/.test(name)) c = /\.iso$/.test(name) ? A.bred : A.bmagenta;
  else if (n.mode?.startsWith("s")) c = A.bmagenta;
  else if (n.mode?.startsWith("b") || n.mode?.startsWith("c")) c = `${E}40;33;01m`;
  return c ? `${c}${name}${A.reset}` : name;
}

// ============================================================================
// Shell engine
// ============================================================================

interface Frame {
  user: "demo" | "root";
  cwd: string;
  /** "host" or a container name. */
  where: string;
  /** bash -l prints "logout" on exit. */
  login: boolean;
  vars: Record<string, string>;
}

interface Host {
  write(s: string): void;
  setTitle(t: string): void;
  end(code: number): void;
  cols(): number;
  rows(): number;
  pids(): number[];
  tty(): string;
}

interface Ctx {
  sh: Shell;
  args: string[];
  /** Output goes to another command (no colors, no truncation). */
  piped: boolean;
  stdin: string | null;
  out(s: string): void;
  err(s: string): void;
  sleep(ms: number): Promise<void>;
}
type Cmd = (c: Ctx) => number | void | Promise<number | void>;

class Cancelled extends Error {}

type Tok = { w: string; glob: boolean } | { op: string };

const HOST_PATH = "/usr/local/bin:/usr/bin:/bin:/usr/local/games:/usr/games";
const ROOT_PATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";

function frameVars(user: "demo" | "root", where: string): Record<string, string> {
  const h = user === "root" ? "/root" : HOME;
  const base = { TERM: "xterm-256color", COLORTERM: "truecolor", LANG: "en_US.UTF-8" };
  if (where !== "host") {
    const id12 = containerId(where).slice(0, 12);
    return { PATH: "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", HOSTNAME: id12, HOME: "/root", ...base, ...(CONTAINERS.find((c) => c.name === where)?.image.includes("linuxserver") ? { PUID: "1000", PGID: "1000", TZ: "Europe/Berlin" } : {}) };
  }
  return { SHELL: "/bin/bash", USER: user, LOGNAME: user, HOME: h, PATH: user === "root" ? ROOT_PATH : HOST_PATH, ...base, SHLVL: "1", XDG_RUNTIME_DIR: user === "root" ? "/run/user/0" : "/run/user/1000", ...(user === "demo" ? { XDG_SESSION_TYPE: "tty" } : { MAIL: "/var/mail/root", SUDO_USER: DEMO_USER, SUDO_UID: "1000" }) };
}

const ALIASES: Record<string, string> = { ll: "ls -alF", la: "ls -A", l: "ls -CF", dc: "docker compose" };

class Shell {
  frames: Frame[] = [];
  gone?: Frame;
  line = "";
  cur = 0;
  hist: string[] = [];
  hIdx = -1;
  saved = "";
  mode: "prompt" | "password" | "busy" | "dead" = "busy";
  pw = "";
  pwDone: ((ok: boolean) => void) | null = null;
  abort: (() => void) | null = null;
  last = 0;
  sudoUntil = 0;

  constructor(readonly host: Host) {}

  get isDead() {
    return this.mode === "dead";
  }

  ownPids() {
    return this.host.pids();
  }
  tty() {
    return this.host.tty();
  }

  get f(): Frame {
    // After the last exit, keep answering with the outer frame's values.
    return this.frames[this.frames.length - 1] ?? (this.gone ??= { user: "demo", cwd: HOME, where: "host", login: false, vars: {} });
  }
  get fs(): Dir {
    return fsFor(this.f.where);
  }
  get home(): string {
    return this.f.where === "host" ? (this.f.user === "root" ? "/root" : HOME) : "/root";
  }
  /** sh (busybox) instead of bash in images without bash. */
  get ash(): boolean {
    return this.f.where !== "host" && NO_BASH.has(this.f.where);
  }
  get shellName(): string {
    return this.ash ? "sh" : "bash";
  }

  out(s: string) {
    this.host.write(s.replace(/\r?\n/g, "\r\n"));
  }

  push(user: "demo" | "root", where: string, login: boolean, cwd?: string) {
    this.frames.push({ user, where, login, cwd: cwd ?? (where === "host" ? (user === "root" ? "/root" : HOME) : where === "immich-redis" ? "/data" : "/"), vars: frameVars(user, where) });
  }

  shortPath(): string {
    const c = this.f.cwd;
    const h = this.home;
    if (this.f.where !== "host") return c;
    return c === h ? "~" : c.startsWith(h + "/") ? "~" + c.slice(h.length) : c;
  }

  promptText(): string {
    const f = this.f;
    const p = this.shortPath();
    if (f.where !== "host") {
      const id12 = containerId(f.where).slice(0, 12);
      return this.ash ? `${p} # ` : `root@${id12}:${p}# `;
    }
    if (f.user === "root") return `${A.bred}root@${HOST.hostname}${A.reset}:${A.bblue}${p}${A.reset}# `;
    return `\x1b]0;${DEMO_USER}@${HOST.hostname}: ${p}\x07${A.bgreen}${DEMO_USER}@${HOST.hostname}${A.reset}:${A.bblue}${p}${A.reset}$ `;
  }

  prompt() {
    if (this.mode === "dead") return;
    const f = this.f;
    if (f.where === "host" && f.user === "demo") this.host.setTitle(`${DEMO_USER}@${HOST.hostname}: ${this.shortPath()}`);
    this.mode = "prompt";
    this.line = "";
    this.cur = 0;
    this.hIdx = -1;
    this.out(this.promptText());
  }

  /** Leaves the current frame; ends the session when it was the last one. */
  exitFrame(code: number) {
    const ash = this.ash;
    const f = this.frames.pop();
    if (f?.login) this.out("logout\n");
    else if (!ash) this.out("exit\n");
    if (!this.frames.length) {
      this.mode = "dead";
      this.host.end(code);
    }
  }

  askPassword(): Promise<boolean> {
    this.mode = "password";
    this.pw = "";
    this.out(`[sudo] password for ${DEMO_USER}: `);
    return new Promise((res) => (this.pwDone = res));
  }

  // ---- input --------------------------------------------------------------

  feed(data: string) {
    let i = 0;
    while (i < data.length) {
      const ch = data[i];
      if (ch === "\x1b") {
        const next = data[i + 1];
        if (next === "[" || next === "O") {
          let j = i + 2;
          while (j < data.length && !/[@-~]/.test(data[j])) j++;
          this.seq(data.slice(i + 2, j + 1));
          i = j + 1;
          continue;
        }
        i += 2; // Alt+key: ignore
        continue;
      }
      // Paste: a line break submits the line, the rest is typed after.
      this.key(ch);
      i++;
    }
  }

  private seq(s: string) {
    if (this.mode !== "prompt") return;
    const fin = s[s.length - 1];
    if (fin === "A") this.histMove(1);
    else if (fin === "B") this.histMove(-1);
    else if (fin === "C" && this.cur < this.line.length) (this.cur++, this.out(`${E}C`));
    else if (fin === "D" && this.cur > 0) (this.cur--, this.out(`${E}D`));
    else if (fin === "H" || s === "1~") this.setLine(this.line, 0);
    else if (fin === "F" || s === "4~") this.setLine(this.line, this.line.length);
    else if (s === "3~" && this.cur < this.line.length) {
      const rest = this.line.slice(this.cur + 1);
      this.line = this.line.slice(0, this.cur) + rest;
      this.out(rest + " " + `${E}${rest.length + 1}D`);
    }
  }

  private key(ch: string) {
    if (this.mode === "dead") return;
    if (this.mode === "busy") {
      if (ch === "\x03") this.abort?.();
      return;
    }
    if (this.mode === "password") {
      if (ch === "\r" || ch === "\n") {
        this.out("\n");
        const done = this.pwDone;
        this.pwDone = null;
        this.mode = "busy";
        done?.(true);
      } else if (ch === "\x03") {
        this.out("^C\n");
        const done = this.pwDone;
        this.pwDone = null;
        this.mode = "busy";
        done?.(false);
      } else if (ch === "\x7f" || ch === "\b") this.pw = this.pw.slice(0, -1);
      else if (ch >= " ") this.pw += ch;
      return;
    }
    switch (ch) {
      case "\r":
      case "\n":
        return this.enter();
      case "\x7f":
      case "\b":
        if (this.cur > 0) {
          const rest = this.line.slice(this.cur);
          this.line = this.line.slice(0, this.cur - 1) + rest;
          this.cur--;
          this.out("\b" + rest + " " + `${E}${rest.length + 1}D`);
        }
        return;
      case "\x03":
        this.out("^C\n");
        this.last = 130;
        return this.prompt();
      case "\x04":
        if (!this.line) {
          this.out("\n");
          this.exitFrame(0);
          if (!this.isDead) this.prompt();
        }
        return;
      case "\x0c":
        this.out(`${E}H${E}2J`);
        this.out(this.promptText() + this.line);
        if (this.line.length > this.cur) this.out(`${E}${this.line.length - this.cur}D`);
        return;
      case "\x01":
        return this.setLine(this.line, 0);
      case "\x05":
        return this.setLine(this.line, this.line.length);
      case "\x15":
        return this.setLine(this.line.slice(this.cur), 0);
      case "\x0b":
        return this.setLine(this.line.slice(0, this.cur), this.cur);
      case "\x17": {
        const left = this.line.slice(0, this.cur).replace(/\S+\s*$/, "");
        return this.setLine(left + this.line.slice(this.cur), left.length);
      }
      case "\t":
        return this.complete();
    }
    if (ch < " ") return;
    const rest = this.line.slice(this.cur);
    this.line = this.line.slice(0, this.cur) + ch + rest;
    this.cur++;
    this.out(ch + rest + (rest ? `${E}${rest.length}D` : ""));
  }

  /** Replaces the edited line and places the cursor. */
  private setLine(next: string, cursor: number) {
    if (this.cur > 0) this.out(`${E}${this.cur}D`);
    this.out(next + `${E}K`);
    this.line = next;
    this.cur = cursor;
    if (next.length > cursor) this.out(`${E}${next.length - cursor}D`);
  }

  private histMove(dir: number) {
    if (!this.hist.length) return;
    if (this.hIdx === -1) {
      if (dir < 0) return;
      this.saved = this.line;
    }
    const n = Math.min(this.hist.length - 1, Math.max(-1, this.hIdx + dir));
    this.hIdx = n;
    const v = n === -1 ? this.saved : this.hist[this.hist.length - 1 - n];
    this.setLine(v, v.length);
  }

  private complete() {
    const left = this.line.slice(0, this.cur);
    const m = /(\S*)$/.exec(left)!;
    const word = m[1];
    const first = left.trim() === word;
    let cands: string[];
    if (first && !word.includes("/")) {
      cands = Object.keys(this.f.where === "host" ? HOST_CMDS : CTR_CMDS).concat(Object.keys(ALIASES)).filter((c) => c.startsWith(word)).sort();
    } else {
      const slash = word.lastIndexOf("/");
      const dirPart = slash >= 0 ? word.slice(0, slash + 1) : "";
      const base = word.slice(slash + 1);
      const dirAbs = normPath(this.f.cwd, this.expandTilde(dirPart || "."));
      const d = lookup(this.fs, dirAbs);
      if (!d || d.kind !== "d" || !canRead(d, this.f.user)) return;
      cands = Object.keys(d.c)
        .filter((n) => n.startsWith(base) && (base.startsWith(".") || !n.startsWith(".")))
        .sort()
        .map((n) => {
          const node = lookup(this.fs, normPath(dirAbs, n));
          return dirPart + n + (node?.kind === "d" ? "/" : "");
        });
    }
    if (!cands.length) return;
    let common = cands[0];
    for (const c of cands) while (!c.startsWith(common)) common = common.slice(0, -1);
    if (cands.length === 1 && !common.endsWith("/")) common += " ";
    if (common.length > word.length) {
      const add = common.slice(word.length);
      const next = left + add + this.line.slice(this.cur);
      this.setLine(next, this.cur + add.length);
    } else if (cands.length > 1) {
      this.out("\n" + columns(cands.map((c) => c.replace(/\/$/, "").split("/").pop()! + (c.endsWith("/") ? "/" : "")), this.host.cols()));
      this.out(this.promptText() + this.line);
      if (this.line.length > this.cur) this.out(`${E}${this.line.length - this.cur}D`);
    }
  }

  expandTilde(w: string) {
    return w === "~" ? this.home : w.startsWith("~/") ? this.home + w.slice(1) : w;
  }

  private async enter() {
    const line = this.line;
    this.out("\n");
    if (line.trim() && line !== this.hist[this.hist.length - 1] && !line.startsWith(" ")) this.hist.push(line);
    if (this.hist.length > 500) this.hist.shift();
    this.mode = "busy";
    try {
      await this.run(line);
    } catch (e) {
      if (!(e instanceof Cancelled)) this.out(`${this.shellName}: ${String((e as Error)?.message ?? e)}\n`);
    }
    this.abort = null;
    if (!this.isDead) this.prompt();
  }

  // ---- parsing ------------------------------------------------------------

  tokenize(line: string): Tok[] | string {
    const out: Tok[] = [];
    let w = "";
    let has = false;
    let glob = false;
    const v = this.f.vars;
    const varVal = (name: string) => (name === "?" ? String(this.last) : name === "PWD" ? this.f.cwd : name === "HOSTNAME" && this.f.where === "host" ? HOST.hostname : name === "$" ? "4182" : (v[name] ?? ""));
    const end = () => {
      if (has) out.push({ w, glob });
      w = "";
      has = false;
      glob = false;
    };
    const readVar = (i: number): [string, number] => {
      if (line[i + 1] === "{") {
        const j = line.indexOf("}", i);
        if (j < 0) return ["$", i + 1];
        return [varVal(line.slice(i + 2, j)), j + 1];
      }
      const m = /^(\?|\$|[A-Za-z_][A-Za-z0-9_]*)/.exec(line.slice(i + 1));
      if (!m) return ["$", i + 1];
      return [varVal(m[1]), i + 1 + m[1].length];
    };
    let i = 0;
    while (i < line.length) {
      const c = line[i];
      if (c === " " || c === "\t") (end(), i++);
      else if (c === "#" && !has) break;
      else if (c === "'") {
        const j = line.indexOf("'", i + 1);
        if (j < 0) return "unexpected EOF while looking for matching `''";
        w += line.slice(i + 1, j);
        has = true;
        i = j + 1;
      } else if (c === '"') {
        let j = i + 1;
        for (; j < line.length && line[j] !== '"'; j++) {
          if (line[j] === "\\" && j + 1 < line.length && '"\\$`'.includes(line[j + 1])) w += line[++j];
          else if (line[j] === "$") {
            const [val, nj] = readVar(j);
            w += val;
            j = nj - 1;
          } else w += line[j];
        }
        if (j >= line.length) return 'unexpected EOF while looking for matching `"\'';
        has = true;
        i = j + 1;
      } else if (c === "\\") {
        w += line[i + 1] ?? "";
        has = true;
        i += 2;
      } else if (c === "$") {
        const [val, nj] = readVar(i);
        w += val;
        has = true;
        i = nj;
      } else if (c === "~" && !has && (line[i + 1] === undefined || line[i + 1] === "/" || line[i + 1] === " ")) {
        w += this.home;
        has = true;
        i++;
      } else if (c === "2" && !has && line[i + 1] === ">") {
        end();
        if (line.slice(i, i + 4) === "2>&1") (out.push({ op: "2>&1" }), (i += 4));
        else (out.push({ op: "2>" }), (i += 2));
      } else if ("|&;<>".includes(c)) {
        end();
        const two = line.slice(i, i + 2);
        if (["||", "&&", ">>"].includes(two)) (out.push({ op: two }), (i += 2));
        else (out.push({ op: c }), i++);
      } else {
        if (c === "*" || c === "?") glob = true;
        w += c;
        has = true;
        i++;
      }
    }
    end();
    return out;
  }

  globExpand(w: string): string[] {
    const abs = normPath(this.f.cwd, w);
    const dir = parentOf(abs);
    const pat = baseName(abs);
    const d = lookup(this.fs, dir);
    if (!d || d.kind !== "d") return [w];
    const re = new RegExp("^" + pat.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$");
    const hits = Object.keys(d.c).filter((n) => re.test(n) && (pat.startsWith(".") || !n.startsWith("."))).sort();
    if (!hits.length) return [w];
    const prefix = w.includes("/") ? w.slice(0, w.lastIndexOf("/") + 1) : "";
    return hits.map((h) => prefix + h);
  }

  // ---- execution ----------------------------------------------------------

  async run(line: string) {
    const toks = this.tokenize(line);
    if (typeof toks === "string") {
      this.out(`${this.shellName}: ${toks}\n`);
      this.last = 2;
      return;
    }
    // Split into pipelines joined by ; && ||.
    let pipe: Tok[] = [];
    let join = ";";
    const steps: { join: string; toks: Tok[] }[] = [];
    for (const t of toks) {
      if ("op" in t && [";", "&&", "||", "&"].includes(t.op)) {
        steps.push({ join, toks: pipe });
        join = t.op === "&" ? ";" : t.op;
        pipe = [];
      } else pipe.push(t);
    }
    steps.push({ join, toks: pipe });
    for (const s of steps) {
      if (!s.toks.length) continue;
      if (s.join === "&&" && this.last !== 0) continue;
      if (s.join === "||" && this.last === 0) continue;
      this.last = await this.pipeline(s.toks);
      if (this.isDead) return;
    }
  }

  private async pipeline(toks: Tok[]): Promise<number> {
    const stages: Tok[][] = [[]];
    for (const t of toks) {
      if ("op" in t && t.op === "|") stages.push([]);
      else stages[stages.length - 1].push(t);
    }
    let stdin: string | null = null;
    let code = 0;
    for (let i = 0; i < stages.length; i++) {
      const lastStage = i === stages.length - 1;
      const words: string[] = [];
      let outFile: string | null = null;
      let errNull = false;
      let errToOut = false;
      let inFile: string | null = null;
      const st = stages[i];
      for (let k = 0; k < st.length; k++) {
        const t = st[k];
        if ("op" in t) {
          const nx = st[k + 1];
          const target = nx && "w" in nx ? nx.w : null;
          if (t.op === "2>&1") errToOut = true;
          else if (!target) {
            this.out(`${this.shellName}: syntax error near unexpected token \`newline'\n`);
            return 2;
          } else if (t.op === ">" || t.op === ">>") (outFile = target), k++;
          else if (t.op === "2>") (errNull = target === "/dev/null"), k++;
          else if (t.op === "<") (inFile = target), k++;
          continue;
        }
        if (t.glob) words.push(...this.globExpand(t.w));
        else words.push(t.w);
      }
      if (inFile) {
        const n = lookup(this.fs, normPath(this.f.cwd, inFile));
        if (!n || n.kind !== "f") {
          this.out(`${this.shellName}: ${inFile}: No such file or directory\n`);
          return 1;
        }
        stdin = fileText(n);
      }
      if (outFile && outFile !== "/dev/null") {
        this.out(`${this.shellName}: ${outFile}: Read-only file system (the demo can't change files)\n`);
        return 1;
      }
      let buf = "";
      const toTerm = lastStage && !outFile;
      const ctx: Ctx = {
        sh: this,
        args: words.slice(1),
        piped: !toTerm,
        stdin,
        out: (s) => (toTerm ? this.out(s) : (buf += s)),
        err: (s) => (errNull ? undefined : errToOut && !toTerm ? (buf += s) : this.out(s)),
        sleep: (ms) =>
          new Promise<void>((res, rej) => {
            const t = setTimeout(() => {
              this.abort = null;
              res();
            }, ms);
            this.abort = () => {
              clearTimeout(t);
              this.out("^C\n");
              this.abort = null;
              rej(new Cancelled());
            };
          }),
      };
      if (!words.length) continue;
      try {
        code = (await this.exec(words[0], ctx)) ?? 0;
      } catch (e) {
        if (e instanceof Cancelled) return 130;
        throw e;
      }
      if (this.mode === "dead") return code;
      stdin = buf;
    }
    return code;
  }

  async exec(name: string, c: Ctx): Promise<number> {
    if (ALIASES[name] && this.f.where === "host") {
      const [n, ...rest] = ALIASES[name].split(" ");
      return this.exec(n, { ...c, args: [...rest, ...c.args] });
    }
    if (name.includes("=") && /^[A-Za-z_]\w*=/.test(name)) {
      const [k, ...v] = name.split("=");
      this.f.vars[k] = v.join("=");
      return c.args.length ? this.exec(c.args[0], { ...c, args: c.args.slice(1) }) : 0;
    }
    const table = this.f.where === "host" ? HOST_CMDS : CTR_CMDS;
    let cmdName = name;
    if (name.includes("/")) {
      const abs = normPath(this.f.cwd, name);
      const node = lookup(this.fs, abs);
      if (!node) {
        c.err(`${this.shellName}: ${name}: No such file or directory\n`);
        return 127;
      }
      if (node.kind === "d") {
        c.err(`${this.shellName}: ${name}: Is a directory\n`);
        return 126;
      }
      const b = baseName(abs);
      if (!/\/(usr\/)?(local\/)?s?bin\//.test(abs) || !table[b]) {
        c.err(`${this.shellName}: ${name}: running scripts isn't available in the demo\n`);
        return 126;
      }
      cmdName = b;
    }
    const fn = table[cmdName];
    if (fn) return (await fn(c)) ?? 0;
    if (UNSUPPORTED.has(cmdName) && (this.f.where === "host" || ["vi", "apt", "apt-get", "curl"].includes(cmdName))) {
      c.err(`${cmdName}: not available in the demo shell. Type ${A.bold}help${A.reset} to see what works.\n`);
      return 1;
    }
    if (this.ash) c.err(`sh: ${cmdName}: not found\n`);
    else c.err(`bash: ${cmdName}: command not found in the demo (type ${A.bold}help${A.reset} to see what works)\n`);
    return 127;
  }
}

const UNSUPPORTED = new Set(["vim", "vi", "nvim", "nano", "emacs", "less", "git", "python", "python3", "node", "curl", "wget", "ssh", "scp", "rsync", "tmux", "screen", "make", "gcc", "tar", "zip", "unzip", "mount", "umount", "journalctl", "smartctl", "tailscale", "reboot", "shutdown", "poweroff", "passwd", "crontab", "watch", "find", "sed", "awk", "du", "stat", "file", "dig", "nslookup", "traceroute", "ss", "netstat", "lsof", "kill", "killall", "pkill"]);

/** ls-style column layout (column-major). */
function columns(items: string[], width: number): string {
  if (!items.length) return "";
  const w = Math.max(...items.map(vlen)) + 2;
  const ncol = Math.max(1, Math.floor(width / w));
  const nrow = Math.ceil(items.length / ncol);
  const rows: string[] = [];
  for (let r = 0; r < nrow; r++) {
    let s = "";
    for (let c = 0; c < ncol; c++) {
      const it = items[c * nrow + r];
      if (it !== undefined) s += c === ncol - 1 || (c + 1) * nrow + r >= items.length ? it : padR(it, w);
    }
    rows.push(s.trimEnd());
  }
  return rows.join("\n") + "\n";
}

// ============================================================================
// Shared data for commands
// ============================================================================

function loads(): string[] {
  const busy = sample().procs.reduce((a, p) => a + p.cpu, 0) / 100;
  return [busy + 0.21, busy * 0.94 + 0.27, busy * 0.9 + 0.33].map((v) => v.toFixed(2));
}

function memStats() {
  const total = HOST.memTotal;
  const used = (memUsedPct() / 100) * total;
  const cache = 9.4 * GB + 0.6 * GB;
  return { total, used, cache, free: total - used - cache, shared: 412 * MB, avail: total - used, swapTotal: HOST.swapTotal, swapUsed: 210 * MB };
}

interface Row {
  s: Spec;
  pid: number;
  user: string;
  cpu: number;
  rss: number;
  memPct: number;
  state: string;
}

/** Processes joined with their specs; inside a container, its own processes renumbered. */
function procRows(where: string): Row[] {
  const l = sample();
  const by = new Map(specs().map((s) => [s.pid, s]));
  let rows: Row[] = l.procs.flatMap((p) => {
    const s = by.get(p.pid);
    return s ? [{ s, pid: p.pid, user: p.user, cpu: p.cpu, rss: p.memRSS, memPct: p.memPct, state: p.state }] : [];
  });
  if (where !== "host") {
    rows = rows.filter((r) => r.s.ctr === where).sort((a, b) => a.s.start - b.s.start || a.pid - b.pid);
    rows = rows.map((r, i) => ({ ...r, pid: i + 1, user: r.s.uid === 0 ? "root" : r.s.uid === 1000 ? "abc" : String(r.s.uid) }));
  }
  return rows;
}

function cpuTime(s: Spec): number {
  return s.kernel ? 0 : Math.floor((s.cpu / 100) * ((Date.now() - s.start) / 1000));
}

function ttyOf(s: Spec): string {
  if (s.name === "agetty") return "tty1";
  const t = (s as Spec & { tty?: string }).tty;
  if (t) return t;
  if (s.cgroup.includes("session-41") && s.uid === 1000 && s.name !== "sshd") return "pts/0";
  if (s.cgroup.includes("tmux-spawn") && s.name !== "tmux: server") return "pts/1";
  return "?";
}

function statOf(r: Row): string {
  let st = r.state;
  if (r.s.nice < 0) st += "<";
  if (r.s.pid === 1 || r.s.cmd.startsWith("-") || ["sshd", "systemd", "s6-svscan", "tmux: server"].includes(r.s.name)) st += "s";
  if (r.s.threads > 1) st += "l";
  if (r.s.name === "htop" || r.s.name === "watch") st += "+";
  return st;
}

function psStart(t: number): string {
  const d = new Date(t);
  if (Date.now() - t < DAY) return hhmm(d);
  if (d.getFullYear() === new Date().getFullYear()) return `${MONTHS[d.getMonth()]}${two(d.getDate())}`;
  return String(d.getFullYear());
}

const userCol = (u: string) => (u.length > 8 ? u.slice(0, 7) + "+" : u);

// Image sizes from features/containers.ts.
const IMAGE_SIZE: Record<string, number> = {
  jellyfin: 1.42 * GB, sonarr: 203 * MB, radarr: 211 * MB, prowlarr: 178 * MB, transmission: 96 * MB, qbittorrent: 187 * MB, "immich-server": 1.63 * GB,
  "immich-postgres": 452 * MB, "immich-redis": 41 * MB, nextcloud: 1.21 * GB, "nextcloud-cron": 1.21 * GB, "home-assistant": 1.84 * GB, pihole: 312 * MB,
  vaultwarden: 247 * MB, "uptime-kuma": 446 * MB, paperless: 1.38 * GB,
};
const CREATED_DAYS: Record<string, number> = {
  jellyfin: 19, sonarr: 11, radarr: 19, prowlarr: 19, transmission: 34, qbittorrent: 34, "immich-server": 6, "immich-postgres": 62, "immich-redis": 62,
  nextcloud: 27, "nextcloud-cron": 27, "home-assistant": 14, pihole: 48, vaultwarden: 21, "uptime-kuma": 40, paperless: 30,
};

/** Live container list from the demo's Containers API (falls back to host.ts). */
async function containers(): Promise<Container[]> {
  try {
    const r = await fetch("/api/containers", { credentials: "same-origin" });
    if (r.ok) return (await r.json()) as Container[];
  } catch {
    /* fall through */
  }
  return CONTAINERS.map((c) => {
    const id = containerId(c.name);
    const started = containerStart(c.name);
    return {
      id: id.slice(0, 12), fullId: id, name: c.name, image: c.image, imageId: "", state: c.state, created: Date.now() - (CREATED_DAYS[c.name] ?? 20) * DAY,
      status: c.state === "exited" ? `Exited (${c.exitCode ?? 0}) 2 hours ago` : `Up ${dockerAgo(Date.now() - started)}${c.state === "paused" ? " (Paused)" : c.health ? ` (${c.health})` : ""}`,
      ports: (PORTS[c.name] ?? []).map(([priv, pub, t]) => ({ private: priv, public: pub, type: t ?? "tcp" })), networkMode: "", version: "", imageRef: c.image.split(":")[0], tag: c.image.split(":")[1] ?? "latest",
    } as unknown as Container;
  });
}

async function findContainer(ref: string): Promise<Container | null> {
  const list = await containers();
  return list.find((c) => c.name === ref || c.id === ref || c.fullId === ref || (ref.length >= 4 && c.fullId.startsWith(ref))) ?? null;
}

// ============================================================================
// Commands
// ============================================================================

function readFile(c: Ctx, cmd: string, p: string): string | null {
  const sh = c.sh;
  const abs = normPath(sh.f.cwd, p);
  const n = lookup(sh.fs, abs);
  if (!n) return c.err(`${cmd}: ${p}: No such file or directory\n`), null;
  if (n.kind === "d") return c.err(`${cmd}: ${p}: Is a directory\n`), null;
  if (n.kind !== "f") return null;
  if (!canRead(n, sh.f.user) || !canRead(lookup(sh.fs, parentOf(abs)) ?? n, sh.f.user)) return c.err(`${cmd}: ${p}: Permission denied\n`), null;
  if (n.body === undefined) return c.err(`${cmd}: ${p}: binary file (${hsize(fileSize(n))}), not shown in the demo\n`), null;
  return fileText(n);
}

/** Text from files given as args, else stdin. */
function inputs(c: Ctx, cmd: string, files: string[]): string | null {
  if (!files.length) return c.stdin ?? "";
  let out = "";
  let ok = false;
  for (const f of files) {
    const t = readFile(c, cmd, f);
    if (t !== null) (out += t), (ok = true);
  }
  return ok ? out : null;
}

const linesOf = (s: string) => {
  const l = s.split("\n");
  if (l[l.length - 1] === "") l.pop();
  return l;
};

function ls(c: Ctx): number {
  const sh = c.sh;
  let long = false, all = false, almost = false, human = false, one = false, classify = false, dirOnly = false;
  const paths: string[] = [];
  for (const a of c.args) {
    if (a.startsWith("-") && a.length > 1 && !a.startsWith("--")) {
      for (const ch of a.slice(1)) {
        if (ch === "l") long = true;
        else if (ch === "a") all = true;
        else if (ch === "A") almost = true;
        else if (ch === "h") human = true;
        else if (ch === "1") one = true;
        else if (ch === "F") classify = true;
        else if (ch === "d") dirOnly = true;
        else if (!"Ctr".includes(ch)) return c.err(`ls: invalid option -- '${ch}'\nTry 'ls --help' for more information.\n`), 2;
      }
    } else if (!a.startsWith("--")) paths.push(a);
  }
  if (!paths.length) paths.push(".");
  const color = !c.piped;
  let code = 0;
  const fmtEntry = (name: string, n: Node, path: string) => {
    let disp = colorName(name, n, color);
    if (classify) disp += n.kind === "d" ? "/" : n.kind === "l" ? "@" : n.kind === "f" && n.exec ? "*" : "";
    return { name, n, path, disp };
  };
  type Ent = ReturnType<typeof fmtEntry>;
  const longRows = (ents: Ent[]) => {
    const rows = ents.map((e) => {
      const n = e.n;
      const links = n.kind === "d" ? 2 + Object.values(n.c).filter((x) => x.kind === "d").length : 1;
      const size = n.kind === "d" ? 4096 : n.kind === "l" ? n.to.length : fileSize(n);
      return [modeOf(n), String(links), n.owner ?? "root", n.group ?? n.owner ?? "root", human ? hsize(size) : String(size), lsDate(mtimeOf(n, e.path)), e.disp + (n.kind === "l" ? ` -> ${n.to}` : "")];
    });
    const w = [0, 1, 2, 3, 4].map((i) => Math.max(...rows.map((r) => r[i].length)));
    return rows.map((r) => `${r[0]} ${padL(r[1], w[1])} ${padR(r[2], w[2])} ${padR(r[3], w[3])} ${padL(r[4], w[4])} ${r[5]} ${r[6]}`).join("\n") + (rows.length ? "\n" : "");
  };
  const show = (ents: Ent[]) => {
    if (long) return longRows(ents);
    if (one || c.piped) return ents.map((e) => e.disp).join("\n") + (ents.length ? "\n" : "");
    return columns(ents.map((e) => e.disp), sh.host.cols());
  };
  const files: Ent[] = [];
  const dirs: { p: string; abs: string; d: Dir }[] = [];
  for (const p of paths) {
    const abs = normPath(sh.f.cwd, p);
    const raw = lookup(sh.fs, abs, false);
    const n = raw?.kind === "l" && !long ? lookup(sh.fs, abs) : raw;
    if (!n) {
      c.err(`ls: cannot access '${p}': No such file or directory\n`);
      code = 2;
      continue;
    }
    const target = n.kind === "l" ? lookup(sh.fs, abs) : n;
    if (target?.kind === "d" && !dirOnly && !(long && n.kind === "l" && !p.endsWith("/"))) dirs.push({ p, abs, d: target });
    else files.push(fmtEntry(p, n, abs));
  }
  if (files.length) c.out(show(files));
  dirs.forEach(({ p, abs, d }, i) => {
    if (!canRead(d, sh.f.user)) {
      c.err(`ls: cannot open directory '${p}': Permission denied\n`);
      code = 2;
      return;
    }
    if (dirs.length + files.length > 1) c.out(`${i || files.length ? "\n" : ""}${p}:\n`);
    let names = Object.keys(d.c).sort((a, b) => a.replace(/^\./, "").localeCompare(b.replace(/^\./, ""), "en", { sensitivity: "base" }));
    if (!all && !almost) names = names.filter((n) => !n.startsWith("."));
    const ents = names.map((n) => fmtEntry(n, d.c[n], normPath(abs, n)));
    if (all) ents.unshift(fmtEntry(".", d, abs), fmtEntry("..", lookup(sh.fs, parentOf(abs)) ?? d, parentOf(abs)));
    if (long) {
      const blocks = ents.reduce((a, e) => a + (e.n.kind === "d" ? 4 : e.n.kind === "f" ? Math.ceil(fileSize(e.n) / 4096) * 4 : 0), 0);
      c.out(`total ${human ? hsize(blocks * 1024) : blocks}\n`);
    }
    c.out(show(ents));
  });
  return code;
}

function cd(c: Ctx): number {
  const sh = c.sh;
  const f = sh.f;
  let p = c.args[0] ?? sh.home;
  if (p === "-") {
    p = f.vars.OLDPWD ?? f.cwd;
    c.out(p + "\n");
  }
  const abs = normPath(f.cwd, p);
  const n = lookup(sh.fs, abs);
  if (!n) return c.err(`${sh.shellName}: cd: ${p}: No such file or directory\n`), 1;
  if (n.kind !== "d") return c.err(`${sh.shellName}: cd: ${p}: Not a directory\n`), 1;
  if (!canRead(n, f.user)) return c.err(`${sh.shellName}: cd: ${p}: Permission denied\n`), 1;
  f.vars.OLDPWD = f.cwd;
  f.cwd = abs;
  return 0;
}

function cat(c: Ctx): number {
  const args = c.args.filter((a) => a !== "-");
  if (!args.length) {
    if (c.stdin !== null) c.out(c.stdin);
    return 0;
  }
  let code = 0;
  for (const a of args) {
    const t = readFile(c, "cat", a);
    if (t === null) code = 1;
    else c.out(t);
  }
  return code;
}

function headTail(which: "head" | "tail") {
  return (c: Ctx): number => {
    let n = 10;
    const files: string[] = [];
    for (let i = 0; i < c.args.length; i++) {
      const a = c.args[i];
      if (a === "-n") n = Number(c.args[++i]) || 10;
      else if (/^-n\d+$/.test(a)) n = Number(a.slice(2));
      else if (/^-\d+$/.test(a)) n = Number(a.slice(1));
      else if (a === "-f") continue;
      else files.push(a);
    }
    const t = inputs(c, which, files);
    if (t === null) return 1;
    const l = linesOf(t);
    const sel = which === "head" ? l.slice(0, n) : l.slice(Math.max(0, l.length - n));
    if (sel.length) c.out(sel.join("\n") + "\n");
    return 0;
  };
}

function grep(c: Ctx): number {
  let ci = false, inv = false, num = false, count = false;
  const rest: string[] = [];
  for (const a of c.args) {
    if (/^-[ivnc]+$/.test(a)) {
      if (a.includes("i")) ci = true;
      if (a.includes("v")) inv = true;
      if (a.includes("n")) num = true;
      if (a.includes("c")) count = true;
    } else if (a.startsWith("--color")) continue;
    else rest.push(a);
  }
  const pat = rest.shift();
  if (pat === undefined) return c.err("Usage: grep [OPTION]... PATTERNS [FILE]...\n"), 2;
  const t = inputs(c, "grep", rest);
  if (t === null) return 2;
  let re: RegExp;
  try {
    re = new RegExp(pat, ci ? "gi" : "g");
  } catch {
    re = new RegExp(pat.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), ci ? "gi" : "g");
  }
  const hits: string[] = [];
  linesOf(t).forEach((line, i) => {
    const plain = stripAnsi(line);
    re.lastIndex = 0;
    const m = re.test(plain);
    if (m === inv) return;
    const shown = !inv && !c.piped ? plain.replace(re, (x) => `${E}01;31m${E}K${x}${E}m${E}K`) : plain;
    hits.push((num ? `${A.green}${i + 1}${A.reset}${A.cyan}:${A.reset}` : "") + shown);
  });
  if (count) c.out(`${hits.length}\n`);
  else if (hits.length) c.out(hits.join("\n") + "\n");
  return hits.length ? 0 : 1;
}

function wc(c: Ctx): number {
  const flags = c.args.filter((a) => a.startsWith("-")).join("");
  const files = c.args.filter((a) => !a.startsWith("-"));
  const t = inputs(c, "wc", files);
  if (t === null) return 1;
  const l = (t.match(/\n/g) ?? []).length;
  const w = t.split(/\s+/).filter(Boolean).length;
  const b = new TextEncoder().encode(t).length;
  const parts = flags ? [flags.includes("l") && l, flags.includes("w") && w, flags.includes("c") && b].filter((x) => x !== false) : [l, w, b];
  c.out(parts.map((x) => (parts.length > 1 ? String(x).padStart(7) : String(x))).join(" ") + (files.length === 1 ? ` ${files[0]}` : "") + "\n");
  return 0;
}

function sortCmd(c: Ctx): number {
  const flags = c.args.filter((a) => a.startsWith("-")).join("");
  const t = inputs(c, "sort", c.args.filter((a) => !a.startsWith("-")));
  if (t === null) return 2;
  let l = linesOf(t);
  l = flags.includes("n") ? l.sort((a, b) => parseFloat(a) - parseFloat(b)) : l.sort((a, b) => a.localeCompare(b));
  if (flags.includes("r")) l.reverse();
  if (l.length) c.out(l.join("\n") + "\n");
  return 0;
}

function uniq(c: Ctx): number {
  const counts = c.args.includes("-c");
  const t = inputs(c, "uniq", c.args.filter((a) => !a.startsWith("-")));
  if (t === null) return 1;
  const out: [string, number][] = [];
  for (const l of linesOf(t)) {
    if (out.length && out[out.length - 1][0] === l) out[out.length - 1][1]++;
    else out.push([l, 1]);
  }
  c.out(out.map(([l, n]) => (counts ? `${String(n).padStart(7)} ${l}` : l)).join("\n") + (out.length ? "\n" : ""));
  return 0;
}

function echo(c: Ctx): number {
  let a = c.args;
  let nl = true;
  let esc = false;
  while (a[0] && /^-[neE]+$/.test(a[0])) {
    if (a[0].includes("n")) nl = false;
    if (a[0].includes("e")) esc = true;
    a = a.slice(1);
  }
  let s = a.join(" ");
  if (esc) s = s.replace(/\\n/g, "\n").replace(/\\t/g, "\t").replace(/\\e|\\033|\\x1b/g, "\x1b");
  c.out(s + (nl ? "\n" : ""));
  return 0;
}

function idCmd(c: Ctx): number {
  if (c.sh.f.user === "root") c.out("uid=0(root) gid=0(root) groups=0(root)\n");
  else c.out("uid=1000(demo) gid=1000(demo) groups=1000(demo),24(cdrom),25(floppy),27(sudo),29(audio),30(dip),44(video),46(plugdev),100(users),106(netdev),993(docker)\n");
  return 0;
}

function hostName(sh: Shell) {
  return sh.f.where === "host" || sh.f.where === "home-assistant" ? HOST.hostname : containerId(sh.f.where).slice(0, 12);
}

function uname(c: Ctx): number {
  const a = c.args.join("");
  const hn = hostName(c.sh);
  const parts: Record<string, string> = { s: "Linux", n: hn, r: HOST.kernel, v: "#1 SMP PREEMPT_DYNAMIC Debian 6.1.119-1 (2024-11-22)", m: "x86_64", o: "GNU/Linux" };
  if (!a) return c.out("Linux\n"), 0;
  if (a.includes("a")) return c.out(`Linux ${hn} ${HOST.kernel} ${parts.v} x86_64 ${c.sh.f.where !== "host" && ALPINE.has(c.sh.f.where) ? "Linux" : "GNU/Linux"}\n`), 0;
  c.out(["s", "n", "r", "v", "m", "o"].filter((k) => a.includes(k)).map((k) => parts[k]).join(" ") + "\n");
  return 0;
}

function uptimeCmd(c: Ctx): number {
  if (c.args.includes("-p")) {
    const s = Math.floor(Date.now() / 1000) - HOST.bootTime;
    c.out(`up 3 weeks, ${Math.floor(s / 86400) - 21} days, ${Math.floor((s % 86400) / 3600)} hours, ${Math.floor((s % 3600) / 60)} minutes\n`);
    return 0;
  }
  c.out(` ${hhmmss(new Date())} up ${upFor()},  2 users,  load average: ${loads().join(", ")}\n`);
  return 0;
}

function dateCmd(c: Ctx): number {
  const d = new Date();
  if (c.args[0]?.startsWith("+")) {
    const f = c.args[0].slice(1);
    c.out(f.replace(/%Y/g, String(d.getFullYear())).replace(/%m/g, two(d.getMonth() + 1)).replace(/%d/g, two(d.getDate())).replace(/%H/g, two(d.getHours())).replace(/%M/g, two(d.getMinutes())).replace(/%S/g, two(d.getSeconds())).replace(/%s/g, String(Math.floor(d.getTime() / 1000))) + "\n");
    return 0;
  }
  c.out(`${DAYS[d.getDay()]} ${MONTHS[d.getMonth()]} ${String(d.getDate()).padStart(2)} ${hhmmss(d)} ${tzName()} ${d.getFullYear()}\n`);
  return 0;
}

function free(c: Ctx): number {
  const m = memStats();
  const h = c.args.some((a) => a.includes("h"));
  const f = (n: number) => (h ? hfree(n) : String(Math.round(n / 1024)));
  c.out(
    table([
      ["", "total", "used", "free", "shared", "buff/cache", "available"],
      ["Mem:", f(m.total), f(m.used), f(m.free), f(m.shared), f(m.cache), f(m.avail)],
      ["Swap:", f(m.swapTotal), f(m.swapUsed), f(m.swapTotal - m.swapUsed)],
    ].map((r) => r.map((x, i) => (i === 0 ? padR(x, 6) : padL(x, i === 1 ? 14 : 11)))), 0),
  );
  return 0;
}

const FILESYSTEMS: [string, number, number, string][] = [
  ["udev", 16 * GB, 0, "/dev"],
  ["tmpfs", 3.2 * GB, 1.9 * MB, "/run"],
  ["/dev/nvme0n1p2", 915 * GB, 182 * GB, "/"],
  ["tmpfs", 16 * GB, 0, "/dev/shm"],
  ["tmpfs", 5 * MB, 12 * 1024, "/run/lock"],
  ["/dev/nvme0n1p1", 511 * MB, 6 * MB, "/boot/efi"],
  ["/dev/sda1", 7.2 * TB, 4.9 * TB, "/DATA"],
  ["/dev/sdb1", 7.2 * TB, 3.1 * TB, "/mnt/backup"],
  ["tmpfs", 3.2 * GB, 48 * 1024, "/run/user/1000"],
];

function df(c: Ctx): number {
  const h = c.args.some((a) => a.includes("h"));
  const list: [string, number, number, string][] =
    c.sh.f.where === "host" ? FILESYSTEMS : [["overlay", 915 * GB, 182 * GB, "/"], ["tmpfs", 64 * MB, 0, "/dev"], ["shm", 64 * MB, 0, "/dev/shm"], ["/dev/nvme0n1p2", 915 * GB, 182 * GB, "/etc/hosts"]];
  const want = c.args.filter((a) => !a.startsWith("-")).map((p) => normPath(c.sh.f.cwd, p));
  const rows = list.filter(([, , , mnt]) => !want.length || want.some((w) => w === mnt || (mnt !== "/" && w.startsWith(mnt + "/")) || (mnt === "/" && !list.some(([, , , m2]) => m2 !== "/" && (w === m2 || w.startsWith(m2 + "/"))))));
  const f = (n: number) => (h ? (n === 0 ? "0" : hsize(n)) : String(Math.round(n / 1024)));
  c.out(
    table([
      ["Filesystem", h ? "Size" : "1K-blocks", "Used", "Avail", "Use%", "Mounted on"],
      ...rows.map(([dev, size, used, mnt]) => [dev, f(size), f(used), f(size - used), `${Math.ceil((used / size) * 100)}%`, mnt]),
    ].map((r) => r.map((x, i) => (i === 0 || i === 5 ? x : padL(x, h ? 5 : 11)))), 1),
  );
  return 0;
}

function lsblk(c: Ctx): number {
  c.out(`NAME        MAJ:MIN RM   SIZE RO TYPE MOUNTPOINTS
sda           8:0    0   7.3T  0 disk 
└─sda1        8:1    0   7.3T  0 part /DATA
sdb           8:16   0   7.3T  0 disk 
└─sdb1        8:17   0   7.3T  0 part /mnt/backup
sdc           8:32   1  59.6G  0 disk 
└─sdc1        8:33   1  59.6G  0 part 
nvme0n1     259:0    0 931.5G  0 disk 
├─nvme0n1p1 259:1    0   512M  0 part /boot/efi
├─nvme0n1p2 259:2    0   930G  0 part /
└─nvme0n1p3 259:3    0     4G  0 part [SWAP]
`);
  return 0;
}

function ipCmd(c: Ctx): number {
  const sub = c.args[0] ?? "";
  if (sub.startsWith("r")) {
    c.out(`default via ${HOST.gateway} dev enp3s0 proto dhcp src ${HOST.lanIp} metric 100 \n172.17.0.0/16 dev docker0 proto kernel scope link src 172.17.0.1 linkdown \n172.18.0.0/16 dev br-3c9a1f0e2b7d proto kernel scope link src 172.18.0.1 \n192.168.1.0/24 dev enp3s0 proto kernel scope link src ${HOST.lanIp} metric 100 \n`);
    return 0;
  }
  if (!sub.startsWith("a")) return c.err('Usage: ip [ OPTIONS ] OBJECT { COMMAND | help }\nIn the demo try "ip a" or "ip r".\n'), 1;
  const g = (s: string) => `${A.green}${s}${A.reset}`;
  const b = (s: string) => `${A.magenta}${s}${A.reset}`;
  const y = (s: string) => `${A.yellow}${s}${A.reset}`;
  const col = !c.piped;
  const o = `1: ${col ? g("lo") : "lo"}: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN group default qlen 1000
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
    inet ${col ? b("127.0.0.1") : "127.0.0.1"}/8 scope host lo
       valid_lft forever preferred_lft forever
2: ${col ? g("enp3s0") : "enp3s0"}: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP group default qlen 1000
    link/ether ${col ? y("a8:a1:59:3c:7e:12") : "a8:a1:59:3c:7e:12"} brd ff:ff:ff:ff:ff:ff
    inet ${col ? b(HOST.lanIp) : HOST.lanIp}/24 brd 192.168.1.255 scope global dynamic noprefixroute enp3s0
       valid_lft 71245sec preferred_lft 71245sec
3: ${col ? g("docker0") : "docker0"}: <NO-CARRIER,BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue state DOWN group default
    link/ether 02:42:5e:1d:9a:04 brd ff:ff:ff:ff:ff:ff
    inet 172.17.0.1/16 brd 172.17.255.255 scope global docker0
       valid_lft forever preferred_lft forever
4: ${col ? g("br-3c9a1f0e2b7d") : "br-3c9a1f0e2b7d"}: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP group default
    link/ether 02:42:8b:33:c1:7a brd ff:ff:ff:ff:ff:ff
    inet 172.18.0.1/16 brd 172.18.255.255 scope global br-3c9a1f0e2b7d
       valid_lft forever preferred_lft forever
5: ${col ? g("tailscale0") : "tailscale0"}: <POINTOPOINT,MULTICAST,NOARP,UP,LOWER_UP> mtu 1280 qdisc fq_codel state UNKNOWN group default qlen 500
    link/none 
    inet ${col ? b(HOST.tailscaleIp) : HOST.tailscaleIp}/32 scope global tailscale0
       valid_lft forever preferred_lft forever
`;
  c.out(o);
  return 0;
}

function fakeIp(host: string): string | null {
  const known: Record<string, string> = { localhost: "127.0.0.1", [HOST.hostname]: HOST.lanIp, router: HOST.gateway, "router.lan": HOST.gateway, "one.one.one.one": "1.1.1.1", "dns.google": "8.8.8.8", "google.com": "142.250.185.78", "debian.org": "151.101.2.132", "github.com": "140.82.121.4" };
  if (/^\d+\.\d+\.\d+\.\d+$/.test(host)) return host;
  if (known[host]) return known[host];
  if (!/^[a-z0-9.-]+\.[a-z]{2,}$/i.test(host)) return null;
  const h = hashOf(host);
  return `${[93, 104, 151, 172, 185][h % 5]}.${(h >> 8) & 255}.${(h >> 16) & 255}.${(h >> 24) & 254 || 1}`;
}

async function ping(c: Ctx): Promise<number> {
  let n = 4;
  let host = "";
  for (let i = 0; i < c.args.length; i++) {
    if (c.args[i] === "-c") n = Math.max(1, Math.min(50, Number(c.args[++i]) || 4));
    else if (!c.args[i].startsWith("-")) host = c.args[i];
  }
  if (!host) return c.err("ping: usage error: Destination address required\n"), 1;
  const ip = fakeIp(host);
  if (!ip) return c.err(`ping: ${host}: Name or service not known\n`), 2;
  const local = ip.startsWith("127.") || ip.startsWith("192.168.") || ip.startsWith("172.") || ip.startsWith("100.");
  const base = ip.startsWith("127.") ? 0.03 : local ? 0.4 : 11 + (hashOf(host) % 25);
  c.out(`PING ${host} (${ip}) 56(84) bytes of data.\n`);
  const times: number[] = [];
  const t0 = Date.now();
  let i = 0;
  try {
    for (; i < n; i++) {
      if (i) await c.sleep(1000);
      const t = base * (0.85 + Math.random() * 0.4);
      times.push(t);
      c.out(`64 bytes from ${host === ip ? ip : `${host} (${ip})`}: icmp_seq=${i + 1} ttl=${local ? 64 : 57} time=${t < 1 ? t.toFixed(3) : t.toFixed(1)} ms\n`);
    }
  } catch (e) {
    if (!(e instanceof Cancelled)) throw e;
  }
  const min = Math.min(...times), max = Math.max(...times), avg = times.reduce((a, b) => a + b, 0) / times.length;
  c.out(`\n--- ${host} ping statistics ---\n${times.length} packets transmitted, ${times.length} received, 0% packet loss, time ${Date.now() - t0}ms\nrtt min/avg/max/mdev = ${min.toFixed(3)}/${avg.toFixed(3)}/${max.toFixed(3)}/${((max - min) / 3).toFixed(3)} ms\n`);
  return 0;
}

function ps(c: Ctx): number {
  const sh = c.sh;
  const flags = c.args.join(" ");
  const rows = procRows(sh.f.where);
  const own = sh.ownPids();
  if (!flags || flags === "-f") {
    const mine = sh.f.where === "host" ? rows.filter((r) => own.includes(r.s.pid)) : rows.filter((r) => r.s.name === "bash" || r.s.name === "sh").slice(-1);
    const shellPid = mine[mine.length - 1]?.pid ?? 1;
    const tty = sh.tty();
    c.out(`    PID TTY          TIME CMD\n${String(shellPid).padStart(7)} ${padR(tty, 12)} 00:00:00 ${sh.shellName}\n${String(shellPid + 31).padStart(7)} ${padR(tty, 12)} 00:00:00 ps\n`);
    return 0;
  }
  const full = /aux|-e|-A|ax/.test(flags);
  if (!full) return c.err("ps: in the demo try ps or ps aux\n"), 1;
  const ef = /-e|-A/.test(flags);
  const lines: string[] = [];
  if (ef) {
    lines.push("UID          PID    PPID  C STIME TTY          TIME CMD");
    for (const r of rows.sort((a, b) => a.pid - b.pid)) {
      const t = cpuTime(r.s);
      lines.push(`${padR(userCol(r.user), 8)} ${String(r.pid).padStart(7)} ${String(sh.f.where === "host" ? r.s.ppid : 0).padStart(7)} ${String(Math.min(99, Math.round(r.cpu))).padStart(2)} ${padR(psStart(r.s.start), 5)} ${padR(ttyOf(r.s), 8)} ${two(Math.floor(t / 3600))}:${two(Math.floor(t / 60) % 60)}:${two(t % 60)} ${r.s.kernel ? `[${r.s.name}]` : r.s.cmd}`);
    }
  } else {
    lines.push("USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND");
    for (const r of rows.sort((a, b) => a.pid - b.pid)) {
      const t = cpuTime(r.s);
      const life = Math.max(1, (Date.now() - r.s.start) / 1000);
      lines.push(
        `${padR(userCol(r.user), 8)} ${String(r.pid).padStart(7)} ${padL(((cpuTime(r.s) / life) * 100).toFixed(1), 4)} ${padL(r.memPct.toFixed(1), 4)} ${String(Math.round(r.s.vms / 1024)).padStart(6)} ${String(Math.round(r.rss / 1024)).padStart(5)} ${padR(ttyOf(r.s), 8)} ${padR(statOf(r), 4)} ${padR(psStart(r.s.start), 5)} ${String(Math.floor(t / 60)).padStart(3)}:${two(t % 60)} ${r.s.kernel ? `[${r.s.name}]` : r.s.cmd}`,
      );
    }
  }
  const width = sh.host.cols();
  if (c.piped) c.out(lines.join("\n") + "\n");
  else {
    const max = 28;
    const shown = lines.length > max + 1 ? [...lines.slice(0, 1), ...lines.slice(lines.length - max)] : lines;
    c.out(shown.map((l) => (l.length > width ? l.slice(0, width - 1) : l)).join("\n") + "\n");
    if (lines.length > max + 1) c.out(`${A.dim}(${lines.length - 1 - max} older processes not shown; pipe to grep or see System > Processes)${A.reset}\n`);
  }
  return 0;
}

function top(c: Ctx): number {
  const sh = c.sh;
  const rows = procRows(sh.f.where);
  const m = memStats();
  const all = sample().procs;
  const cpu = all.reduce((a, p) => a + p.cpu, 0) / HOST.threads;
  const running = all.filter((p) => p.state === "R").length;
  const stopped = all.filter((p) => p.state === "T").length;
  const mib = (n: number) => (n / MB).toFixed(1).padStart(8);
  const w = sh.host.cols();
  const out = [
    `top - ${hhmmss(new Date())} up ${upFor()},  2 users,  load average: ${loads().join(", ")}`,
    `Tasks: ${String(all.length).padStart(3)} total, ${String(running).padStart(3)} running, ${String(all.length - running - stopped).padStart(3)} sleeping, ${String(stopped).padStart(3)} stopped,   0 zombie`,
    `%Cpu(s): ${(cpu * 0.82).toFixed(1).padStart(4)} us, ${(cpu * 0.15).toFixed(1).padStart(4)} sy,  0.0 ni, ${(100 - cpu).toFixed(1).padStart(4)} id,  0.4 wa,  0.0 hi, ${(cpu * 0.03).toFixed(1)} si,  0.0 st`,
    `MiB Mem : ${mib(m.total)} total, ${mib(m.free)} free, ${mib(m.used)} used, ${mib(m.cache)} buff/cache`,
    `MiB Swap: ${mib(m.swapTotal)} total, ${mib(m.swapTotal - m.swapUsed)} free, ${mib(m.swapUsed)} used. ${mib(m.avail)} avail Mem`,
    "",
    `${A.inv.replace("42", "47")}    PID USER      PR  NI    VIRT    RES    SHR S  %CPU  %MEM     TIME+ COMMAND${" ".repeat(Math.max(0, w - 79))}${A.reset}`,
  ];
  const topRows = [...rows].sort((a, b) => b.cpu - a.cpu || b.rss - a.rss).slice(0, Math.max(8, Math.min(20, sh.host.rows() - 10)));
  for (const r of topRows) {
    const t = cpuTime(r.s);
    const kb = (n: number) => (n > 9_999_999 * 1024 ? (n / GB).toFixed(1) + "g" : String(Math.round(n / 1024)));
    const line = `${String(r.pid).padStart(7)} ${padR(userCol(r.user), 9)} ${padL(r.s.name.startsWith("migration") ? "rt" : String(20 + r.s.nice), 2)} ${padL(String(r.s.nice), 3)} ${padL(kb(r.s.vms), 7)} ${padL(kb(r.rss), 6)} ${padL(kb(r.s.shared), 6)} ${r.state} ${padL(r.cpu.toFixed(1), 5)} ${padL(r.memPct.toFixed(1), 5)} ${padL(`${Math.floor(t / 60)}:${two(t % 60)}.${two(Math.floor(Math.random() * 100))}`, 9)} ${r.s.kernel ? r.s.name : r.s.name.replace(/^-/, "")}`;
    out.push(r.cpu > 20 ? `${A.bold}${line}${A.reset}` : line);
  }
  c.out(out.map((l) => l).join("\n") + "\n");
  c.out(`\n${A.dim}top is interactive on a real server; the demo prints one snapshot. For a live view open System > Processes.${A.reset}\n`);
  return 0;
}

function htop(c: Ctx): number {
  const sh = c.sh;
  const w = Math.max(60, sh.host.cols());
  const rows = procRows(sh.f.where);
  const all = sample().procs;
  const total = all.reduce((a, p) => a + p.cpu, 0) / HOST.threads;
  const r = seeded(Math.floor(Date.now() / 2000));
  const per = Array.from({ length: HOST.threads }, (_, i) => Math.min(100, Math.max(0.5, total * (0.4 + r() * 1.3) + (i === 3 || i === 9 ? 25 : 0))));
  const half = Math.floor((w - 2) / 2);
  const bar = (label: string, pct: number, text: string, width: number, colorUsed = A.green) => {
    const inner = width - label.length - 2;
    const fill = Math.round((pct / 100) * (inner - text.length));
    const usedPart = "|".repeat(Math.max(0, fill));
    return `${A.cyan}${label}${A.reset}${A.bold}[${A.reset}${colorUsed}${usedPart}${A.reset}${" ".repeat(Math.max(0, inner - fill - text.length))}${A.dim}${text}${A.reset}${A.bold}]${A.reset}`;
  };
  const lines: string[] = [];
  for (let i = 0; i < HOST.threads / 2; i++) {
    const a = per[i], b = per[i + HOST.threads / 2];
    lines.push(" " + bar(String(i).padStart(3), a, `${a.toFixed(1)}%`, half - 1) + "  " + bar(String(i + 8).padStart(3), b, `${b.toFixed(1)}%`, half - 1));
  }
  const m = memStats();
  const gib = (n: number) => (n / GB).toFixed(1) + "G";
  const threads = all.reduce((a, p) => a + p.threads, 0);
  const kthr = all.filter((p) => p.kernel).length;
  lines.push(" " + bar("Mem", (m.used / m.total) * 100, `${gib(m.used)}/${gib(m.total)}`, half - 1) + `   ${A.cyan}Tasks: ${A.reset}${A.bold}${all.length - kthr}${A.reset}, ${threads} thr, ${kthr} kthr; ${A.green}${all.filter((p) => p.state === "R").length} running${A.reset}`);
  lines.push(" " + bar("Swp", (m.swapUsed / m.swapTotal) * 100, `${Math.round(m.swapUsed / MB)}M/${gib(m.swapTotal)}`, half - 1, A.red) + `   ${A.cyan}Load average: ${A.reset}${A.bold}${loads().join(" ")}${A.reset}`);
  const s = Math.floor(Date.now() / 1000) - HOST.bootTime;
  lines.push(" ".repeat(half + 3) + `${A.cyan}Uptime: ${A.reset}${A.bold}${Math.floor(s / 86400)} days, ${two(Math.floor((s % 86400) / 3600))}:${two(Math.floor((s % 3600) / 60))}:${two(s % 60)}${A.reset}`);
  lines.push("");
  const hdr = "    PID USER       PRI  NI  VIRT   RES   SHR S  CPU%▽MEM%   TIME+  Command";
  lines.push(`${A.inv}${padR(hdr, w)}${A.reset}`);
  const sz = (n: number) => (n >= GB ? (n / GB).toFixed(1) + "G" : n >= 100 * MB ? Math.round(n / MB) + "M" : Math.round(n / 1024) + "");
  const shown = rows.filter((x) => !x.s.kernel).sort((a, b) => b.cpu - a.cpu).slice(0, Math.max(8, Math.min(18, sh.host.rows() - 16)));
  for (const x of shown) {
    const t = cpuTime(x.s);
    const line = `${String(x.pid).padStart(7)} ${padR(userCol(x.user), 9)} ${padL(String(20 + x.s.nice), 4)} ${padL(String(x.s.nice), 3)} ${padL(sz(x.s.vms), 5)} ${padL(sz(x.rss), 5)} ${padL(sz(x.s.shared), 5)} ${x.state} ${padL(x.cpu.toFixed(1), 5)} ${padL(x.memPct.toFixed(1), 4)} ${padL(`${Math.floor(t / 3600)}:${two(Math.floor(t / 60) % 60)}.${two(t % 60)}`, 8)}  ${x.s.cmd}`;
    lines.push(line.length > w ? line.slice(0, w) : line);
  }
  lines.push(`${A.inv}F1${A.reset}Help  ${A.inv}F2${A.reset}Setup ${A.inv}F3${A.reset}Search${A.inv}F4${A.reset}Filter${A.inv}F5${A.reset}Tree  ${A.inv}F6${A.reset}SortBy${A.inv}F7${A.reset}Nice -${A.inv}F8${A.reset}Nice +${A.inv}F9${A.reset}Kill  ${A.inv}F10${A.reset}Quit`);
  c.out(lines.join("\n") + "\n");
  c.out(`\n${A.dim}htop is interactive on a real server; the demo prints one snapshot. For a live view open System > Processes.${A.reset}\n`);
  return 0;
}

// ---- docker -----------------------------------------------------------------

const MANAGE = new Set(["start", "stop", "restart", "kill", "rm", "pull", "pause", "unpause", "run", "create", "compose", "update", "rmi", "build", "push", "prune", "system", "network", "volume", "cp", "commit", "tag", "login"]);

async function docker(c: Ctx): Promise<number> {
  const [sub, ...rest] = c.args;
  if (!sub || sub === "--help" || sub === "help") {
    c.out("Usage:  docker [OPTIONS] COMMAND\n\nWorks in the demo:\n  ps [-a]       List containers\n  images        List images\n  logs [-f] [-n N] NAME\n  stats         Resource usage (one snapshot)\n  exec -it NAME sh|bash\n\nTo start, stop or update containers use the Containers page.\n");
    return 0;
  }
  if (sub === "ps" || (sub === "container" && rest[0] === "ls")) {
    const all = rest.includes("-a") || rest.includes("--all");
    const quiet = rest.includes("-q");
    const list = (await containers()).filter((x) => all || x.state !== "exited").sort((a, b) => b.created - a.created);
    if (quiet) return c.out(list.map((x) => x.id).join("\n") + (list.length ? "\n" : "")), 0;
    const ports = (x: Container) =>
      x.ports.map((p) => (p.public ? `0.0.0.0:${p.public}->${p.private}/${p.type}` : `${p.private}/${p.type}`)).join(", ");
    const cmd = (x: Container) => {
      const s = x.command ?? "";
      return `"${s.length > 20 ? s.slice(0, 19) + "…" : s}"`;
    };
    c.out(table([["CONTAINER ID", "IMAGE", "COMMAND", "CREATED", "STATUS", "PORTS", "NAMES"], ...list.map((x) => [x.id, x.image, cmd(x), `${dockerAgo(Date.now() - x.created)} ago`, x.status, ports(x), x.name])]));
    return 0;
  }
  if (sub === "images" || (sub === "image" && rest[0] === "ls")) {
    const list = await containers();
    const seen = new Set<string>();
    const rows: string[][] = [];
    for (const x of list.sort((a, b) => b.created - a.created)) {
      if (seen.has(x.image)) continue;
      seen.add(x.image);
      const [repo, tag] = [x.image.slice(0, x.image.lastIndexOf(":")), x.image.slice(x.image.lastIndexOf(":") + 1)];
      const iid = (x.imageId || containerId("image:" + x.image)).replace(/^sha256:/, "").slice(0, 12);
      const size = IMAGE_SIZE[x.name] ?? 300 * MB;
      rows.push([repo, tag, iid, `${dockerAgo(Date.now() - x.created - 2 * DAY)} ago`, size >= GB ? `${(size / 1e9).toFixed(2)}GB` : `${Math.round(size / 1e6)}MB`]);
    }
    c.out(table([["REPOSITORY", "TAG", "IMAGE ID", "CREATED", "SIZE"], ...rows]));
    return 0;
  }
  if (sub === "logs") {
    let tail = 30;
    let follow = false;
    let name = "";
    for (let i = 0; i < rest.length; i++) {
      const a = rest[i];
      if (a === "-f" || a === "--follow") follow = true;
      else if (a === "-n" || a === "--tail") tail = a === "--tail" && rest[i + 1] === "all" ? 500 : Number(rest[++i]) || 30;
      else if (a.startsWith("--tail=")) tail = Number(a.slice(7)) || 30;
      else if (!a.startsWith("-")) name = a;
    }
    if (!name) return c.err('"docker logs" requires exactly 1 argument.\n'), 1;
    const ct = await findContainer(name);
    if (!ct) return c.err(`Error response from daemon: No such container: ${name}\n`), 1;
    const get = async (n: number): Promise<LogLine[]> => {
      try {
        const r = await fetch(`/api/containers/${encodeURIComponent(ct.name)}/logs?tail=${n}`, { credentials: "same-origin" });
        if (r.ok) return ((await r.json()) as { lines: LogLine[] }).lines ?? [];
      } catch {
        /* ignore */
      }
      return [];
    };
    const show = (ls: LogLine[]) => ls.forEach((l) => (l.stream === "err" ? c.err(l.text + "\n") : c.out(l.text + "\n")));
    let lines = await get(tail);
    show(lines);
    if (!follow) return 0;
    let lastTs = lines[lines.length - 1]?.ts ?? "";
    for (;;) {
      await c.sleep(2500);
      lines = (await get(10)).filter((l) => (l.ts ?? "") > lastTs);
      show(lines);
      if (lines.length) lastTs = lines[lines.length - 1].ts ?? lastTs;
    }
  }
  if (sub === "stats") {
    const list = (await containers()).filter((x) => x.state !== "exited");
    const rows = procRows("host");
    const out = [["CONTAINER ID", "NAME", "CPU %", "MEM USAGE / LIMIT", "MEM %", "NET I/O", "BLOCK I/O", "PIDS"]];
    for (const x of list.sort((a, b) => a.name.localeCompare(b.name))) {
      const mine = rows.filter((r) => r.s.ctr === x.name);
      const cpu = x.state === "paused" ? 0 : mine.reduce((a, r) => a + r.cpu, 0);
      const mem = mine.reduce((a, r) => a + r.rss, 0);
      const h = hashOf(x.name);
      out.push([x.id, x.name, `${cpu.toFixed(2)}%`, `${hfree(mem)}B / ${hfree(HOST.memTotal)}B`.replace("BB", "B"), `${((mem / HOST.memTotal) * 100).toFixed(2)}%`, `${hsize((h % 900) * MB)}B / ${hsize((h % 300) * MB)}B`, `${hsize((h % 2000) * MB)}B / ${hsize((h % 700) * MB)}B`, String(mine.reduce((a, r) => a + r.s.threads, 0))]);
    }
    c.out(table(out));
    if (!rest.includes("--no-stream")) c.out(`${A.dim}(one snapshot in the demo)${A.reset}\n`);
    return 0;
  }
  if (sub === "exec") {
    const args = rest.filter((a) => !/^-(it|ti|i|t|-interactive|-tty)$/.test(a));
    const [name, ...cmd] = args;
    if (!name) return c.err('"docker exec" requires at least 2 arguments.\n'), 1;
    const ct = await findContainer(name);
    if (!ct) return c.err(`Error response from daemon: No such container: ${name}\n`), 1;
    if (ct.state === "exited") return c.err(`Error response from daemon: container ${ct.fullId} is not running\n`), 1;
    if (ct.state === "paused") return c.err(`Error response from daemon: container ${ct.fullId} is paused, unpause the container before exec\n`), 1;
    if (!cmd.length) return c.err('"docker exec" requires at least 2 arguments.\n'), 1;
    const sh = c.sh;
    const interactive = ["sh", "bash", "ash", "/bin/sh", "/bin/bash"].includes(cmd[0]) && cmd.length === 1;
    if (interactive && cmd[0].includes("bash") && NO_BASH.has(ct.name)) return c.err(`OCI runtime exec failed: exec failed: unable to start container process: exec: "bash": executable file not found in $PATH: unknown\n`), 126;
    sh.push("root", ct.name, false);
    if (interactive) return 0;
    const code = await sh.exec(cmd[0], { ...c, args: cmd.slice(1) });
    sh.frames.pop();
    return code;
  }
  if (sub === "version" || sub === "-v" || sub === "--version") {
    c.out("Docker version 27.3.1, build ce12230\n");
    return 0;
  }
  if (MANAGE.has(sub)) {
    c.err(`docker ${sub}: use the Containers page to manage containers in the demo.\n`);
    return 1;
  }
  c.err(`docker: '${sub}' is not a docker command in the demo.\nSee 'docker --help'\n`);
  return 1;
}

// ---- systemctl ------------------------------------------------------------------

const UNITS: Record<string, { desc: string; docs?: string; extra?: string; logs: string[] }> = {
  "docker.service": { desc: "Docker Application Container Engine", docs: "https://docs.docker.com", extra: "TriggeredBy: ● docker.socket", logs: ['level=info msg="Container failed to exit within 10s of signal 15 - using the force" container=qbittorrent', 'level=warning msg="Health check for container jellyfin error: context deadline exceeded"', 'level=info msg="ignoring event" module=libcontainerd namespace=moby topic=/tasks/delete type="*events.TaskDelete"'] },
  "containerd.service": { desc: "containerd container runtime", docs: "https://containerd.io", logs: ['level=info msg="loading plugin \\"io.containerd.event.v1.publisher\\"..." runtime=io.containerd.runc.v2 type=io.containerd.event.v1', 'level=info msg="shim disconnected" id=nextcloud-cron namespace=moby'] },
  "ssh.service": { desc: "OpenBSD Secure Shell server", docs: "man:sshd(8)\n             man:sshd_config(5)", logs: ["Accepted publickey for demo from 192.168.1.45 port 51822 ssh2: ED25519 SHA256:q1V8kX0n3p6yR2tLwB9mZ7cF4hJ5sD1gA8eU0oI3rTk", "pam_unix(sshd:session): session opened for user demo(uid=1000) by (uid=0)", "error: kex_exchange_identification: Connection closed by remote host"] },
  "wargo.service": { desc: "Wargo homelab dashboard", docs: "https://github.com/kushalpandya/wargo", logs: ['level=INFO msg="http: listening" addr=0.0.0.0:8800', 'level=INFO msg="term: session opened" kind=host', 'level=WARN msg="sign-in failed" user=admin remote=198.51.100.23'] },
  "smbd.service": { desc: "Samba SMB Daemon", docs: "man:smbd(8)\n             man:samba(7)\n             man:smb.conf(5)", logs: ["smbd version 4.17.12-Debian started.", "Copyright Andrew Tridgell and the Samba Team 1992-2022"] },
  "nmbd.service": { desc: "Samba NMB Daemon", docs: "man:nmbd(8)", logs: ["*****", "  Samba name server HOMELAB is now a local master browser for workgroup WORKGROUP on subnet 192.168.1.20", "*****"] },
  "tailscaled.service": { desc: "Tailscale node agent", docs: "https://tailscale.com/kb/", logs: ["magicsock: derp-4 connected; connGen=1", "wgengine: Reconfig: configuring userspace WireGuard config (with 3/5 peers)", "health(warnable=no-derp-home): ok"] },
  "cron.service": { desc: "Regular background program processing daemon", docs: "man:cron(8)", logs: ["(root) CMD (command -v debian-sa1 > /dev/null && debian-sa1 1 1)", "pam_unix(cron:session): session closed for user root"] },
  "systemd-resolved.service": { desc: "Network Name Resolution", docs: "man:systemd-resolved.service(8)", logs: ["Using degraded feature set UDP instead of UDP+EDNS0 for DNS server 192.168.1.1.", "Clock change detected. Flushing caches."] },
  "fail2ban.service": { desc: "Fail2Ban Service", docs: "man:fail2ban(1)", logs: ["Server ready"] },
  "NetworkManager.service": { desc: "Network Manager", docs: "man:NetworkManager(8)", logs: ['<info>  [1730000000.1234] dhcp4 (enp3s0): state changed new lease, address=192.168.1.20'] },
  "smartmontools.service": { desc: "Self Monitoring and Reporting Technology (SMART) Daemon", docs: "man:smartd(8) man:smartd.conf(5)", logs: ["Device: /dev/sda [SAT], 8 Currently unreadable (pending) sectors", "Device: /dev/sdb [SAT], SMART Usage Attribute: 194 Temperature_Celsius changed from 64 to 65"] },
};

function unitSpecs(unit: string): Spec[] {
  return specs().filter((s) => s.cgroup === `0::/system.slice/${unit}`).sort((a, b) => a.pid - b.pid);
}

function systemctl(c: Ctx): number {
  const [sub, raw] = c.args;
  const sh = c.sh;
  if (!sub || sub === "list-units") {
    const rows = [["  UNIT", "LOAD", "ACTIVE", "SUB", "DESCRIPTION"]];
    for (const [u, d] of Object.entries(UNITS).sort()) rows.push([`  ${u}`, "loaded", "active", "running", d.desc]);
    rows.push(["  backup-photos.service", "loaded", "failed", "failed", "Nightly photo backup"]);
    c.out(table(rows, 1).replace("  backup-photos.service", `${A.bred}●${A.reset} backup-photos.service`));
    c.out(`\nLOAD   = Reflects whether the unit definition was properly loaded.\nACTIVE = The high-level unit activation state, i.e. generalization of SUB.\nSUB    = The low-level unit activation state, values depend on unit type.\n${rows.length - 1} loaded units listed.\n`);
    return 0;
  }
  if (sub === "status") {
    if (!raw) {
      c.out(`${A.bgreen}●${A.reset} ${HOST.hostname}\n    State: ${A.red}degraded${A.reset}\n    Units: 412 loaded (incl. loaded aliases)\n     Jobs: 0 queued\n   Failed: 1 units\n    Since: ${new Date(HOST.bootTime * 1000).toString().slice(0, 24)}; ${upFor().split(",")[0]} ago\n  systemd: 252.31-1~deb12u1\n   CGroup: /\n`);
      return 0;
    }
    const unit = raw.includes(".") ? raw : `${raw === "sshd" ? "ssh" : raw === "samba" ? "smbd" : raw === "tailscale" ? "tailscaled" : raw}.service`;
    if (unit === "backup-photos.service") {
      const t = new Date(Date.now() - 210 * MIN);
      c.out(`${A.bred}×${A.reset} backup-photos.service - Nightly photo backup\n     Loaded: loaded (/etc/systemd/system/backup-photos.service; static)\n     Active: ${A.bred}failed${A.reset} (Result: exit-code) since ${t.toString().slice(0, 24)}; 3h 30min ago\n    Process: 802113 ExecStart=/home/demo/bin/backup.sh ${A.red}(code=exited, status=23)${A.reset}\n   Main PID: 802113 (code=exited, status=23)\n        CPU: 1.204s\n\n${MONTHS[t.getMonth()]} ${two(t.getDate())} ${hhmmss(t)} ${HOST.hostname} backup.sh[802114]: rsync: [sender] link_stat "/DATA/Photos/2026/.thumbnails" failed: No such file or directory (2)\n${MONTHS[t.getMonth()]} ${two(t.getDate())} ${hhmmss(t)} ${HOST.hostname} systemd[1]: Failed to start backup-photos.service - Nightly photo backup.\n`);
      return 3;
    }
    const u = UNITS[unit];
    const procs = unitSpecs(unit);
    if (!u || !procs.length) return c.err(`Unit ${unit} could not be found.\n`), 4;
    const main = procs.find((p) => p.ppid === 1) ?? procs[0];
    const since = new Date(main.start);
    const mem = procs.reduce((a, p) => a + p.rss, 0);
    const tasks = procs.reduce((a, p) => a + p.threads, 0);
    const cpuS = procs.reduce((a, p) => a + cpuTime(p), 0);
    const ago = dockerAgo(Date.now() - main.start).replace("About an", "1").replace("About a", "1");
    const tree = procs.map((p, i) => `${i === procs.length - 1 ? "└─" : "├─"}${String(p.pid).padStart(String(procs[procs.length - 1].pid).length)} ${p.cmd}`);
    const w = sh.host.cols();
    const lines = [
      `${A.bgreen}●${A.reset} ${unit} - ${u.desc}`,
      `     Loaded: loaded (/lib/systemd/system/${unit}; enabled; preset: enabled)`,
      `     Active: ${A.bgreen}active (running)${A.reset} since ${DAYS[since.getDay()]} ${since.getFullYear()}-${two(since.getMonth() + 1)}-${two(since.getDate())} ${hhmmss(since)} ${tzName()}; ${ago} ago`,
      ...(u.extra ? [u.extra] : []),
      ...(u.docs ? [`       Docs: ${u.docs}`] : []),
      `   Main PID: ${main.pid} (${main.name})`,
      `      Tasks: ${tasks} (limit: 38245)`,
      `     Memory: ${(mem / MB).toFixed(1)}M`,
      `        CPU: ${Math.floor(cpuS / 3600)}h ${Math.floor(cpuS / 60) % 60}min ${(cpuS % 60) + 0.123}s`,
      `     CGroup: /system.slice/${unit}`,
      ...tree.map((t) => `             ${t}`),
      "",
      ...u.logs.map((l, i) => {
        const t = new Date(Date.now() - (u.logs.length - i) * 17 * MIN);
        return `${MONTHS[t.getMonth()]} ${two(t.getDate())} ${hhmmss(t)} ${HOST.hostname} ${main.name}[${main.pid}]: ${l}`;
      }),
    ];
    c.out(lines.map((l) => (vlen(l) > w && !c.piped ? l.slice(0, w - 1) : l)).join("\n") + "\n");
    return 0;
  }
  if (["start", "stop", "restart", "reload", "enable", "disable", "mask", "kill", "daemon-reload"].includes(sub)) {
    const unit = raw ? (raw.includes(".") ? raw : `${raw}.service`) : "";
    if (sh.f.user !== "root") {
      c.err(`Failed to ${sub} ${unit}: Interactive authentication required.\nSee system logs and 'systemctl status ${unit}' for details.\n`);
      return 1;
    }
    c.err(`systemctl ${sub}: changing services isn't available in the demo.\n`);
    return 1;
  }
  c.err(`Unknown command verb '${sub}'.\n`);
  return 1;
}

// ---- neofetch ---------------------------------------------------------------------

const DEBIAN_LOGO = [
  "       _,met$$$$$gg.",
  "    ,g$$$$$$$$$$$$$$$P.",
  '  ,g$$P"     """Y$$.".',
  " ,$$P'              `$$$.",
  "',$$P       ,ggs.     `$$b:",
  "`d$$'     ,$P\"'   .    $$$",
  " $$P      d$'     ,    $$P",
  " $$:      $$.   -    ,d$$'",
  " $$;      Y$b._   _,d$P'",
  " Y$$.    `.`\"Y$$$$P\"'",
  " `$$b      \"-.__",
  "  `Y$$",
  "   `Y$$.",
  "     `$$b.",
  "       `Y$$b.",
  "          `\"Y$b._",
  "              `\"\"\"",
];

function neofetch(c: Ctx): number {
  const sh = c.sh;
  const m = memStats();
  const s = Math.floor(Date.now() / 1000) - HOST.bootTime;
  const k = (label: string, v: string) => `${A.bred}${label}${A.reset}: ${v}`;
  const user = sh.f.where === "host" ? sh.f.user : "root";
  const head = `${A.bred}${user}${A.reset}@${A.bred}${hostName(sh)}${A.reset}`;
  const info = [
    head,
    "-".repeat(user.length + 1 + hostName(sh).length),
    k("OS", `${HOST.os} x86_64`),
    k("Host", "ASRock B550M Pro4"),
    k("Kernel", `Linux ${HOST.kernel}`),
    k("Uptime", `${Math.floor(s / 86400)} days, ${Math.floor((s % 86400) / 3600)} hours, ${Math.floor((s % 3600) / 60)} mins`),
    k("Packages", "1342 (dpkg), 4 (flatpak)"),
    k("Shell", "bash 5.2.15"),
    k("Terminal", "wargo"),
    k("CPU", `${HOST.cpuModel.replace(" with Radeon Graphics", "")} (${HOST.threads}) @ 4.67 GHz`),
    k("GPU", "AMD Radeon Vega 8 (Cezanne) [Integrated]"),
    k("Memory", `${(m.used / GB).toFixed(2)} GiB / ${(m.total / GB).toFixed(2)} GiB (${Math.round((m.used / m.total) * 100)}%)`),
    k("Swap", `${(m.swapUsed / MB).toFixed(2)} MiB / ${(m.swapTotal / GB).toFixed(2)} GiB (5%)`),
    k("Disk (/)", "182.00 GiB / 915.00 GiB (20%) - ext4"),
    k("Disk (/DATA)", "4.90 TiB / 7.20 TiB (68%) - ext4"),
    k("Local IP (enp3s0)", HOST.lanCidr),
    k("Locale", "en_US.UTF-8"),
    "",
    [40, 41, 42, 43, 44, 45, 46, 47].map((n) => `${E}${n}m   `).join("") + A.reset,
    [100, 101, 102, 103, 104, 105, 106, 107].map((n) => `${E}${n}m   `).join("") + A.reset,
  ];
  const n = Math.max(DEBIAN_LOGO.length, info.length);
  const out: string[] = [];
  for (let i = 0; i < n; i++) out.push(`${A.bred}${padR(DEBIAN_LOGO[i] ?? "", 30)}${A.reset}  ${info[i] ?? ""}`.trimEnd());
  c.out(out.join("\n") + "\n");
  return 0;
}

// ---- help, sudo and the rest ----------------------------------------------------------

function help(c: Ctx): number {
  const host = c.sh.f.where === "host";
  const g = (t: string) => `${A.bold}${t}${A.reset}`;
  const cmd = (s: string) => `${A.green}${s}${A.reset}`;
  const lines = [
    `${g("Wargo demo shell")}: a simulated ${c.sh.shellName} running in your browser. Nothing here touches a real server.`,
    "",
    `${g("Files")}      ${cmd("ls")} [-la]  ${cmd("cd")}  ${cmd("pwd")}  ${cmd("cat")}  ${cmd("head")}  ${cmd("tail")}  ${cmd("grep")}  ${cmd("wc")}  ${cmd("sort")}`,
    `${g("System")}     ${cmd("whoami")}  ${cmd("id")}  ${cmd("hostname")}  ${cmd("uname")} -a  ${cmd("uptime")}  ${cmd("date")}  ${cmd("free")} -h  ${cmd("df")} -h${host ? `  ${cmd("lsblk")}  ${cmd("ip")} a  ${cmd("ping")}` : ""}`,
    `${g("Processes")}  ${cmd("ps")} [aux]  ${cmd("top")}  ${cmd("htop")}`,
    ...(host
      ? [
          `${g("Docker")}     ${cmd("docker ps")} [-a]  ${cmd("docker images")}  ${cmd("docker logs")} [-f] <name>  ${cmd("docker stats")}  ${cmd("docker exec -it")} <name> sh`,
          `${g("Services")}   ${cmd("systemctl")}  ${cmd("systemctl status")} <unit>   (docker, ssh, wargo, smbd, tailscaled, cron...)`,
          `${g("Fun")}        ${cmd("neofetch")}  ${cmd("fastfetch")}  ${cmd("history")}  ${cmd("sudo -i")} (any password works)`,
        ]
      : []),
    `${g("Shell")}      ${cmd("echo")}  ${cmd("env")}  ${cmd("which")}  ${cmd("clear")}  ${cmd("exit")}   pipes (|), && and ; work too`,
    "",
    `${A.dim}Keys: arrows for history, Tab to complete, Ctrl+C to cancel, Ctrl+L to clear.${A.reset}`,
  ];
  c.out(lines.join("\n") + "\n");
  return 0;
}

async function sudo(c: Ctx): Promise<number> {
  const sh = c.sh;
  const args = [...c.args];
  if (!args.length) return c.err("usage: sudo -h | -K | -k | -V\nusage: sudo [-i|-s] [command [arg ...]]\n"), 1;
  if (sh.f.user !== "root" && Date.now() > sh.sudoUntil) {
    const ok = await sh.askPassword();
    if (!ok) return 1;
    sh.sudoUntil = Date.now() + 15 * MIN;
  }
  if (args[0] === "-i" || args[0] === "-s" || (args[0] === "su" && args.length <= 2)) {
    sh.push("root", "host", args[0] === "-i" || args[1] === "-", args[0] === "-s" ? sh.f.cwd : undefined);
    return 0;
  }
  while (args[0]?.startsWith("-")) args.shift();
  if (!args.length) return 0;
  sh.push("root", "host", false, sh.f.cwd);
  try {
    return await sh.exec(args[0], { ...c, args: args.slice(1) });
  } finally {
    if (sh.f.user === "root" && sh.frames.length > 1) sh.frames.pop();
  }
}

function history(c: Ctx): number {
  c.out(c.sh.hist.map((h, i) => `${String(i + 1).padStart(5)}  ${h}`).join("\n") + (c.sh.hist.length ? "\n" : ""));
  return 0;
}

function envCmd(c: Ctx): number {
  const v = { ...c.sh.f.vars, PWD: c.sh.f.cwd };
  if (c.args.length && !c.args[0].startsWith("-")) {
    const val = v[c.args[0] as keyof typeof v];
    if (val === undefined) return 1;
    c.out(val + "\n");
    return 0;
  }
  c.out(Object.entries(v).sort().map(([k, x]) => `${k}=${x}`).join("\n") + "\n");
  return 0;
}

function which(c: Ctx): number {
  const tbl = c.sh.f.where === "host" ? HOST_CMDS : CTR_CMDS;
  let code = 0;
  for (const a of c.args) {
    if (!tbl[a] || ["cd", "exit", "logout", "history", "export", "help", "echo", "pwd", "type"].includes(a)) {
      code = 1;
      continue;
    }
    c.out((a === "wargo" ? "/usr/local/bin/" : ["systemctl", "systemd"].includes(a) ? "/usr/bin/" : "/usr/bin/") + a + "\n");
  }
  return code;
}

const readOnly = (name: string) => (c: Ctx) => {
  const target = c.args.filter((a) => !a.startsWith("-")).pop() ?? "";
  c.err(`${name}: cannot change ${target ? `'${target}'` : "files"}: Read-only file system (the demo can't change files)\n`);
  return 1;
};

function apt(c: Ctx): Promise<number> | number {
  const sub = c.args[0];
  const sh = c.sh;
  if (sub === "update") {
    if (sh.f.user !== "root") {
      c.err("Reading package lists... Done\nE: Could not open lock file /var/lib/apt/lists/lock - open (13: Permission denied)\nE: Unable to lock directory /var/lib/apt/lists/\nW: Problem unlinking the file /var/cache/apt/pkgcache.bin - RemoveCaches (13: Permission denied)\n");
      return 100;
    }
    return (async () => {
      const src = ["http://deb.debian.org/debian bookworm InRelease", "http://deb.debian.org/debian bookworm-updates InRelease", "http://security.debian.org/debian-security bookworm-security InRelease", "https://download.docker.com/linux/debian bookworm InRelease", "https://pkgs.tailscale.com/stable/debian bookworm InRelease"];
      for (let i = 0; i < src.length; i++) {
        await c.sleep(250 + Math.random() * 400);
        c.out(`Hit:${i + 1} ${src[i]}\n`);
      }
      await c.sleep(400);
      c.out("Reading package lists... Done\nBuilding dependency tree... Done\nReading state information... Done\n");
      c.out(`${A.dim}See the Updates page for what can be upgraded.${A.reset}\n`);
      return 0;
    })();
  }
  if (sub === "list") {
    c.out("Listing... Done\n");
    c.out(`${A.dim}See the Updates page for upgradable packages.${A.reset}\n`);
    return 0;
  }
  c.err(`apt ${sub ?? ""}: installing or upgrading packages isn't available in the demo. Use the Updates page.\n`.replace("apt :", "apt:"));
  return 1;
}

const exitCmd = (c: Ctx) => {
  const code = Number(c.args[0] ?? c.sh.last) || 0;
  c.sh.exitFrame(code);
  return code;
};

const SHARED: Record<string, Cmd> = {
  help, ls, cd, pwd: (c) => (c.out(c.sh.f.cwd + "\n"), 0), cat, head: headTail("head"), tail: headTail("tail"), grep, wc, sort: sortCmd, uniq, more: cat,
  echo, whoami: (c) => (c.out(c.sh.f.user + "\n"), 0), id: idCmd, hostname: (c) => (c.out((c.args[0] === "-I" ? (c.sh.f.where === "host" ? `${HOST.lanIp} 172.18.0.1 172.17.0.1 ${HOST.tailscaleIp} ` : containerIp(c.sh.f.where)) : hostName(c.sh)) + "\n"), 0),
  uname, uptime: uptimeCmd, date: dateCmd, free, df, ps, top, htop,
  clear: (c) => (c.out(`${E}H${E}2J${E}3J`), 0), reset: (c) => (c.out(`${E}H${E}2J${E}3J`), 0),
  exit: exitCmd, history, env: envCmd, printenv: envCmd,
  export: (c) => {
    for (const a of c.args) if (a.includes("=")) c.sh.f.vars[a.split("=")[0]] = a.split("=").slice(1).join("=");
    return 0;
  },
  which, type: (c) => (c.args.forEach((a) => c.out(`${a} is ${ALIASES[a] ? `aliased to \`${ALIASES[a]}'` : `/usr/bin/${a}`}\n`)), 0),
  sleep: async (c) => void (await c.sleep(Math.min(60, Number(c.args[0]) || 0) * 1000)),
  true: () => 0, false: () => 1, nproc: (c) => (c.out(`${HOST.threads}\n`), 0),
  touch: readOnly("touch"), mkdir: readOnly("mkdir"), rm: readOnly("rm"), rmdir: readOnly("rmdir"), mv: readOnly("mv"), cp: readOnly("cp"), chmod: readOnly("chmod"), chown: readOnly("chown"), ln: readOnly("ln"),
};

const HOST_CMDS: Record<string, Cmd> = {
  ...SHARED,
  logout: (c) => {
    if (!c.sh.f.login) return c.err("bash: logout: not login shell: use `exit'\n"), 1;
    return exitCmd(c);
  },
  lsblk, ip: ipCmd, ping, docker, systemctl, neofetch, fastfetch: neofetch, sudo, apt, "apt-get": apt,
  su: (c) => (c.err("su: Authentication failure (root has no password here; try sudo -i)\n"), 1),
  groups: (c) => (c.out(c.sh.f.user === "root" ? "root\n" : "demo cdrom floppy sudo audio dip video plugdev users netdev docker\n"), 0),
  who: (c) => {
    const t = new Date(Date.now() - 2 * HOUR - 14 * MIN);
    c.out(`demo     pts/0        ${t.getFullYear()}-${two(t.getMonth() + 1)}-${two(t.getDate())} ${hhmm(t)} (192.168.1.45)\ndemo     pts/1        ${new Date(Date.now() - 4 * DAY).toISOString().slice(0, 10)} ${hhmm(new Date(Date.now() - 4 * DAY))} (tmux(2841).%0)\n`);
    return 0;
  },
  lscpu: (c) => {
    c.out(`Architecture:            x86_64\n  CPU op-mode(s):        32-bit, 64-bit\n  Address sizes:         48 bits physical, 48 bits virtual\n  Byte Order:            Little Endian\nCPU(s):                  ${HOST.threads}\n  On-line CPU(s) list:   0-${HOST.threads - 1}\nVendor ID:               AuthenticAMD\n  Model name:            ${HOST.cpuModel}\n    CPU family:          25\n    Model:               80\n    Thread(s) per core:  2\n    Core(s) per socket:  ${HOST.cores}\n    Socket(s):           1\n    CPU max MHz:         4672.0698\n    CPU min MHz:         400.0000\nVirtualization:          AMD-V\nCaches (sum of all):\n  L1d:                   256 KiB (8 instances)\n  L1i:                   256 KiB (8 instances)\n  L2:                    4 MiB (8 instances)\n  L3:                    16 MiB (1 instance)\n`);
    return 0;
  },
};

const CTR_CMDS: Record<string, Cmd> = { ...SHARED };

// ============================================================================
// Sessions and the WebSocket bridge
// ============================================================================

interface WsClient {
  send(data: string | ArrayBuffer): void;
  close(code?: number, reason?: string): void;
}

const SCROLLBACK = 256 << 10;
const MAX_SESSIONS = 8;
const EXITED_LINGER = 2 * MIN;
const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/;
const enc = new TextEncoder();
const SIGNUM: Record<string, number> = { HUP: 1, INT: 2, KILL: 9, TERM: 15 };

class Session implements Host {
  readonly id = Array.from(crypto.getRandomValues(new Uint8Array(12)), (b) => b.toString(16).padStart(2, "0")).join("");
  readonly created = Date.now();
  title: string;
  ring = "";
  client: WsClient | null = null;
  exited = false;
  exitCode = 0;
  endedAt = 0;
  procs: Spec[] = [];
  readonly shell: Shell;
  readonly dec = new TextDecoder();
  constructor(
    readonly kind: TermKind,
    readonly target: string | undefined,
    public w: number,
    public h: number,
    readonly ttyN: number,
  ) {
    this.title = kind === "root" ? "root" : kind === "container" ? (target ?? "") : "bash";
    this.shell = new Shell(this);
  }

  write(s: string) {
    if (!s) return;
    this.ring += s;
    if (this.ring.length > SCROLLBACK) this.ring = this.ring.slice(this.ring.length - SCROLLBACK);
    if (this.client) {
      const b = enc.encode(s);
      this.client.send(b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer);
    }
  }
  setTitle(t: string) {
    if (t === this.title) return;
    this.title = t;
    this.client?.send(JSON.stringify({ type: "title", title: t }));
  }
  end(code: number) {
    if (this.exited) return;
    this.exited = true;
    this.exitCode = code;
    this.endedAt = Date.now();
    this.procs = [];
    this.shell.mode = "dead";
    this.shell.abort?.();
    const cl = this.client;
    if (cl) {
      cl.send(JSON.stringify({ type: "exit", code }));
      // Let the exit message go out before the close frame.
      setTimeout(() => cl.close(4000, "exited"), 40);
    }
  }
  cols() {
    return this.w;
  }
  rows() {
    return this.h;
  }
  pids() {
    return this.procs.map((p) => p.pid);
  }
  tty() {
    return `pts/${this.ttyN}`;
  }
  info(): TermInfo {
    return {
      id: this.id, kind: this.kind, ...(this.target ? { target: this.target } : {}), title: this.title, createdAt: new Date(this.created).toISOString(),
      attached: !!this.client, cols: this.w, rows: this.h, exited: this.exited, ...(this.exited ? { exitCode: this.exitCode } : {}),
    };
  }
}

const sessions = new Map<string, Session>();
let ttySeq = 2;

function reap() {
  for (const [id, s] of sessions) if (s.exited && !s.client && Date.now() - s.endedAt > EXITED_LINGER) sessions.delete(id);
}

/** Spawns the session's processes and prints what the shell prints first. */
async function start(s: Session) {
  const sh = s.shell;
  const tty = s.tty();
  const base = { cgroup: "0::/system.slice/wargo.service", start: Date.now() };
  if (s.kind === "host") {
    s.procs.push(makeSpec({ ...base, pid: allocPid(), ppid: SELF_PID, name: "bash", uid: 1000, cmd: "/bin/bash -l", exe: "/usr/bin/bash", cwd: HOME, rss: 5 * MB, fds: 4 }));
    sh.push("demo", "host", true);
    sh.out(`${A.dim}Welcome to the Wargo demo. This is a simulated shell running in your browser; type ${A.reset}${A.bold}help${A.reset}${A.dim} to see what works.${A.reset}\n`);
    sh.prompt();
    return;
  }
  if (s.kind === "root") {
    const sudoPid = allocPid();
    s.procs.push(makeSpec({ ...base, pid: sudoPid, ppid: SELF_PID, name: "sudo", uid: 0, cmd: "sudo -i", rss: 6 * MB }));
    sh.push("demo", "host", false);
    sh.out(`${A.dim}Demo: any password works here.${A.reset}\n`);
    const ok = await sh.askPassword();
    if (!ok || s.exited) {
      if (!s.exited) s.end(1);
      return;
    }
    sh.frames = [];
    sh.push("root", "host", true);
    sh.sudoUntil = Date.now() + 15 * MIN;
    s.procs.push(makeSpec({ ...base, pid: allocPid(), ppid: sudoPid, name: "bash", uid: 0, cmd: "-bash", rss: 5 * MB }));
    sh.prompt();
    return;
  }
  const name = s.target ?? "";
  const ct = await findContainer(name);
  if (!ct) {
    sh.out(`Error response from daemon: No such container: ${name}\n`);
    return s.end(1);
  }
  if (ct.state === "exited") {
    sh.out(`Error response from daemon: container ${ct.fullId} is not running\n`);
    return s.end(1);
  }
  if (ct.state === "paused") {
    sh.out(`Error response from daemon: container ${ct.fullId} is paused, unpause the container before exec\n`);
    return s.end(1);
  }
  const shimPid = specs().find((x) => x.name === "containerd-shim" && x.cmd.includes(ct.fullId))?.pid;
  s.procs.push(makeSpec({ ...base, pid: allocPid(), ppid: SELF_PID, name: "docker", uid: 1000, threads: 9, rss: 31 * MB, exe: "/usr/bin/docker", cwd: HOME, cmd: `docker exec -it -e TERM=xterm-256color -e COLORTERM=truecolor ${name} sh -c command -v bash >/dev/null && exec bash || exec sh` }));
  if (shimPid) s.procs.push(makeSpec({ pid: allocPid(), ppid: shimPid, name: NO_BASH.has(ct.name) ? "sh" : "bash", uid: 0, cmd: NO_BASH.has(ct.name) ? "sh" : "bash", ctr: ct.name, cgroup: `0::/system.slice/docker-${ct.fullId}.scope`, rss: 4 * MB }));
  void tty;
  sh.push("root", ct.name, false);
  sh.prompt();
}

registerProcSource({
  list: () =>
    [...sessions.values()].flatMap((s) =>
      s.procs.map((p) => {
        const f = s.shell.frames[0];
        const q = { ...p, tty: s.tty() } as Spec & { tty: string };
        if (p.name === "bash" && p.uid === 1000 && f) q.cwd = f.cwd;
        return q;
      }),
    ),
  signal(pid, sig) {
    for (const s of sessions.values()) {
      const i = s.procs.findIndex((p) => p.pid === pid);
      if (i < 0) continue;
      const p = s.procs[i];
      // Interactive shells ignore SIGTERM and SIGINT.
      const shell = p.name === "bash" || p.name === "sh";
      if (sig === "KILL" || sig === "HUP" || (!shell && (sig === "TERM" || sig === "INT"))) {
        s.write(sig === "KILL" ? "" : "\r\n");
        s.end(128 + SIGNUM[sig]);
        return true;
      }
      return sig !== "STOP" && sig !== "CONT";
    }
    return false;
  },
});

function bytesOf(data: unknown): Promise<Uint8Array | string> | Uint8Array | string {
  if (typeof data === "string") return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (ArrayBuffer.isView(data)) return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  if (data instanceof Blob) return data.arrayBuffer().then((b) => new Uint8Array(b));
  return "";
}

const termSocket = ws.link(/\/ws\/term\/[^/?#]+/);

const termHandler = termSocket.addEventListener("connection", ({ client }) => {
  const id = decodeURIComponent(new URL(client.url).pathname.split("/").pop() ?? "");
  const s = sessions.get(id);
  const c: WsClient = { send: (d) => client.send(d), close: (code, reason) => client.close(code, reason) };
  if (!s) {
    setTimeout(() => client.close(4004, "no such session"), 0);
    return;
  }
  if (s.client) {
    const old = s.client;
    s.client = null;
    old.close(4001, "attached elsewhere");
  }
  s.client = c;
  if (s.ring) {
    const b = enc.encode(s.ring);
    c.send(b.buffer.slice(b.byteOffset, b.byteOffset + b.byteLength) as ArrayBuffer);
  }
  c.send(JSON.stringify({ type: "title", title: s.title }));
  if (s.exited) {
    c.send(JSON.stringify({ type: "exit", code: s.exitCode }));
    setTimeout(() => client.close(4000, "exited"), 40);
    return;
  }
  // Keep input in order even when some frames arrive as Blobs.
  let chain: Promise<void> = Promise.resolve();
  client.addEventListener("message", (ev) => {
    chain = chain.then(async () => {
      const d = await bytesOf(ev.data);
      if (typeof d === "string") {
        try {
          const m = JSON.parse(d) as { type?: string; cols?: number; rows?: number };
          if (m.type === "resize" && m.cols && m.rows) (s.w = Math.min(1000, m.cols)), (s.h = Math.min(500, m.rows));
          else if (m.type === "ping") c.send(JSON.stringify({ type: "pong" }));
        } catch {
          /* ignore */
        }
        return;
      }
      if (s.client === c) s.shell.feed(s.dec.decode(d, { stream: true }));
    });
  });
  client.addEventListener("close", () => {
    if (s.client === c) s.client = null;
  });
});

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  api.get("/api/term", () => {
    reap();
    return json([...sessions.values()].sort((a, b) => a.created - b.created).map((s) => s.info()));
  }),
  api.post("/api/term", async ({ request }) => {
    const req = await body<{ kind?: string; target?: string; cols?: number; rows?: number; tmux?: boolean }>(request);
    const kind = req.kind as TermKind;
    if (kind !== "host" && kind !== "root" && kind !== "container") return badRequest("unknown terminal kind");
    const target = kind === "container" ? (req.target ?? "") : undefined;
    if (kind === "container" && !NAME_RE.test(target ?? "")) return badRequest(`invalid container name "${target ?? ""}"`);
    reap();
    if ([...sessions.values()].filter((s) => !s.exited).length >= MAX_SESSIONS) return fail(429, "too_many_sessions", "Too many terminal sessions are open; close one first");
    const cols = req.cols && req.cols > 0 ? Math.min(req.cols, 1000) : 80;
    const rows = req.rows && req.rows > 0 ? Math.min(req.rows, 500) : 24;
    const s = new Session(kind, target, cols, rows, ttySeq++);
    sessions.set(s.id, s);
    void start(s);
    return json({ id: s.id }, 201);
  }),
  api.del("/api/term/:id", ({ params }) => {
    const s = sessions.get(String(params.id));
    if (!s) return notFound();
    sessions.delete(s.id);
    s.shell.mode = "dead";
    s.shell.abort?.();
    s.procs = [];
    const cl = s.client;
    s.client = null;
    if (cl) cl.close(4003, "session closed");
    return noContent();
  }),
  termHandler,
];

// SPDX-License-Identifier: AGPL-3.0-only
// Processes: a believable process table for the demo server (systemd,
// kernel threads, Docker and the containers in host.ts, Samba, Wargo...),
// with CPU and memory that drift over time, details and signals.
import type { RequestHandler, WebSocketHandler } from "msw";
import type { Proc, ProcDetails, ProcList } from "@/features/processes/types";
import { api, badRequest, body, fail, json, notFound } from "../core/http";
import { topic, refresh } from "../core/bus";
import { persisted } from "../core/store";
import { requireApproval } from "../core/elevation";
import { DAY, HOUR, MB, MIN, Walk, seeded } from "../core/rand";
import { CONTAINERS, HOST } from "./host";

// --- table model -----------------------------------------------------------

/** One process of the made-up table (static facts plus a few dynamic ones). */
export interface Spec {
  pid: number;
  ppid: number;
  name: string;
  uid: number;
  cmd: string;
  threads: number;
  /** Usual state when idle ("S", "I"...). */
  state: string;
  nice: number;
  start: number;
  /** Typical CPU, percent of one core. */
  cpu: number;
  rss: number;
  vms: number;
  shared: number;
  swap: number;
  kernel?: boolean;
  /** Container name, for processes inside one. */
  ctr?: string;
  cgroup: string;
  exe?: string;
  cwd?: string;
  env: number;
  fds: number;
  io: [number, number];
  ctx: [number, number];
}

const USERS: Record<number, string> = {
  0: "root",
  33: "www-data",
  100: "messagebus",
  104: "avahi",
  996: "polkitd",
  991: "systemd-resolve",
  997: "systemd-timesync",
  999: "systemd-coredump",
  1000: "demo",
  65534: "nobody",
};
export const userName = (uid: number) => USERS[uid] ?? String(uid);

export const SELF_PID = 1187;
const ADMIN_UID = 1000;
const BOOT = HOST.bootTime * 1000;

/** Full container id, the same one features/containers.ts reports. */
export function containerId(name: string): string {
  const seed = "container:" + name;
  let h = 2166136261;
  for (let i = 0; i < seed.length; i++) h = Math.imul(h ^ seed.charCodeAt(i), 16777619);
  const r = seeded(h >>> 0);
  let s = "";
  while (s.length < 64) s += Math.floor(r() * 16).toString(16);
  return s;
}

const rnd = seeded(20261103);
const used = new Set<number>();
let seq = 1;

/** Pids handed out in time order: early boot is sequential, later ones spread out. */
function pidAt(t: number) {
  const days = Math.max(0, (t - BOOT) / DAY);
  let pid = days < 0.01 ? ++seq : Math.floor(4200 + days * 36_000 + rnd() * 900);
  while (used.has(pid)) pid++;
  used.add(pid);
  return pid;
}

type Opt = Partial<Spec> & { name: string; ppid: number; cmd: string };

const table: Spec[] = [];

function add(o: Opt): number {
  const start = o.start ?? BOOT + 4000;
  const pid = o.pid ?? pidAt(start);
  used.add(pid);
  const rss = o.rss ?? 0;
  const kernel = !!o.kernel;
  table.push({
    uid: 0,
    threads: 1,
    state: kernel ? "I" : "S",
    nice: 0,
    cpu: 0,
    vms: kernel ? 0 : Math.round(rss * (3 + rnd() * 6)),
    shared: kernel ? 0 : Math.round(rss * (0.15 + rnd() * 0.3)),
    swap: 0,
    cgroup: kernel ? "0::/" : "0::/system.slice",
    env: kernel ? 0 : 6 + Math.floor(rnd() * 24),
    fds: kernel ? 0 : 4 + Math.floor(rnd() * 20),
    io: kernel ? [0, 0] : [Math.round(rnd() * 400 * MB), Math.round(rnd() * 200 * MB)],
    ctx: [Math.floor(rnd() * 900_000), Math.floor(rnd() * 40_000)],
    ...o,
    pid,
    start,
    rss,
    kernel,
  });
  return pid;
}

// --- kernel ---------------------------------------------------------------

function kernelThreads() {
  const k = (name: string, state = "I", start = BOOT) => add({ name, ppid: 2, cmd: "", kernel: true, state, start, nice: name.includes("H") ? -20 : 0 });
  for (const n of ["rcu_gp", "rcu_par_gp", "slub_flushwq", "netns"]) k(n);
  k("kworker/0:0H-events_highpri");
  k("mm_percpu_wq");
  k("rcu_tasks_kthread");
  k("rcu_tasks_rude_kthread");
  k("rcu_tasks_trace_kthread");
  for (let c = 0; c < HOST.threads; c++) {
    k(`ksoftirqd/${c}`, "S");
    if (c === 0) k("rcu_preempt");
    k(`migration/${c}`, "S");
    k(`cpuhp/${c}`, "S");
    if (c > 0) k(`kworker/${c}:0H-events_highpri`);
  }
  for (const n of ["kdevtmpfs", "inet_frag_wq", "kauditd", "khungtaskd", "oom_reaper", "writeback", "kcompactd0", "ksmd", "khugepaged", "kintegrityd", "kblockd", "blkcg_punt_bio", "tpm_dev_wq", "ata_sff", "md", "edac-poller", "devfreq_wq", "watchdogd"])
    k(n, ["kdevtmpfs", "kauditd", "khungtaskd", "oom_reaper", "kcompactd0", "ksmd", "khugepaged"].includes(n) ? "S" : "I");
  k("kswapd0", "S");
  k("kthrotld");
  k("acpi_thermal_pm");
  for (let i = 0; i < 6; i++) {
    k(`scsi_eh_${i}`, "S");
    k(`scsi_tmf_${i}`);
  }
  for (const n of ["nvme-wq", "nvme-reset-wq", "nvme-delete-wq", "kstrp", "zswap-shrink", "ipv6_addrconf", "cryptd", "ttm"]) k(n);
  for (const n of ["gfx_0.0.0", "comp_1.0.0", "comp_1.1.0", "comp_1.2.0", "comp_1.3.0", "comp_1.0.1", "comp_1.1.1", "sdma0", "amdgpu-reset-de", "card0-crtc0", "card0-crtc1", "card0-crtc2", "card0-crtc3"])
    k(n, n.startsWith("card") || n.startsWith("gfx") || n.startsWith("comp") || n === "sdma0" ? "S" : "I");
  k("jbd2/nvme0n1p2-8", "S", BOOT + 1500);
  k("ext4-rsv-conver", "I", BOOT + 1500);
  k("jbd2/sda1-8", "S", BOOT + 2600);
  k("ext4-rsv-conver", "I", BOOT + 2600);
  for (const n of ["btrfs-worker", "btrfs-worker-hi", "btrfs-delalloc", "btrfs-flush_del", "btrfs-cache", "btrfs-fixup", "btrfs-endio", "btrfs-endio-met", "btrfs-rmw", "btrfs-endio-wri", "btrfs-compresse", "btrfs-freespace", "btrfs-delayed-m", "btrfs-qgroup-re"])
    k(n, "I", BOOT + 2700);
  k("btrfs-cleaner", "S", BOOT + 2700);
  k("btrfs-transacti", "S", BOOT + 2700);
  k("kworker/u33:0-hci0", "I", BOOT + 3000);
  // Long-lived and recently spawned kworkers.
  const queues = ["events", "events_power_efficient", "mm_percpu_wq", "cgroup_destroy", "events_freezable", "rcu_gp", "inet_frag_wq"];
  for (let c = 0; c < HOST.threads; c++) {
    k(`kworker/${c}:1-${queues[c % queues.length]}`, "I", BOOT + 2000 + c * 30);
    if (c % 3 === 0) k(`kworker/${c}:1H-kblockd`, "I", BOOT + 2200);
  }
  const unbound = ["events_unbound", "btrfs-endio-write", "ext4-rsv-conversion", "flush-8:0", "writeback", "events_unbound", "btrfs-worker", "kcryptd"];
  for (let i = 0; i < 12; i++) k(`kworker/u32:${i}-${unbound[i % unbound.length]}`, "I", Date.now() - (i + 1) * 47 * MIN);
  for (let c = 0; c < HOST.threads; c += 2) k(`kworker/${c}:2-events`, "I", Date.now() - (c + 3) * 11 * MIN);
}

// --- host services ----------------------------------------------------------

const sys = (unit: string) => `0::/system.slice/${unit}`;

function hostServices() {
  const s = (o: Opt & { unit: string }) => add({ cgroup: sys(o.unit), start: BOOT + 3000 + rnd() * 6000, ...o });
  add({ name: "systemd-journal", ppid: 1, cmd: "/lib/systemd/systemd-journald", rss: 52 * MB, cgroup: sys("systemd-journald.service"), start: BOOT + 1800, cpu: 0.2, fds: 61 });
  add({ name: "systemd-udevd", ppid: 1, cmd: "/lib/systemd/systemd-udevd", rss: 9 * MB, cgroup: sys("systemd-udevd.service"), start: BOOT + 2000 });
  s({ name: "systemd-resolve", ppid: 1, uid: 991, cmd: "/lib/systemd/systemd-resolved", rss: 13 * MB, unit: "systemd-resolved.service" });
  s({ name: "systemd-timesyn", ppid: 1, uid: 997, cmd: "/lib/systemd/systemd-timesyncd", threads: 2, rss: 7 * MB, unit: "systemd-timesyncd.service" });
  const avahi = s({ name: "avahi-daemon", ppid: 1, uid: 104, cmd: "avahi-daemon: running [homelab.local]", rss: 4 * MB, unit: "avahi-daemon.service" });
  s({ name: "cron", ppid: 1, cmd: "/usr/sbin/cron -f", rss: 3 * MB, unit: "cron.service" });
  s({ name: "dbus-daemon", ppid: 1, uid: 100, cmd: "/usr/bin/dbus-daemon --system --address=systemd: --nofork --nopidfile --systemd-activation --syslog-only", rss: 5 * MB, unit: "dbus.service" });
  s({ name: "polkitd", ppid: 1, uid: 996, cmd: "/usr/lib/polkit-1/polkitd --no-debug", threads: 3, rss: 9 * MB, unit: "polkit.service" });
  s({ name: "smartd", ppid: 1, cmd: "/usr/sbin/smartd -n", rss: 6 * MB, unit: "smartmontools.service" });
  s({ name: "systemd-logind", ppid: 1, cmd: "/lib/systemd/systemd-logind", rss: 8 * MB, unit: "systemd-logind.service" });
  add({ name: "avahi-daemon", ppid: avahi, uid: 104, cmd: "avahi-daemon: chroot helper", rss: 1 * MB, cgroup: sys("avahi-daemon.service") });
  s({ name: "NetworkManager", ppid: 1, cmd: "/usr/sbin/NetworkManager --no-daemon", threads: 3, rss: 19 * MB, unit: "NetworkManager.service" });
  s({ name: "fail2ban-server", ppid: 1, cmd: "/usr/bin/python3 /usr/bin/fail2ban-server -xf start", threads: 5, rss: 41 * MB, cpu: 0.1, unit: "fail2ban.service" });
  s({ name: "unattended-upgr", ppid: 1, cmd: "/usr/bin/python3 /usr/share/unattended-upgrades/unattended-upgrade-shutdown --wait-for-signal", threads: 2, rss: 22 * MB, unit: "unattended-upgrades.service" });
  const sshd = s({ name: "sshd", ppid: 1, cmd: "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups", rss: 9 * MB, unit: "ssh.service" });
  s({ name: "tailscaled", ppid: 1, cmd: "/usr/sbin/tailscaled --state=/var/lib/tailscale/tailscaled.state --socket=/run/tailscale/tailscaled.sock --port=41641", threads: 14, rss: 58 * MB, cpu: 0.4, unit: "tailscaled.service", fds: 31 });
  const containerd = s({ name: "containerd", ppid: 1, cmd: "/usr/bin/containerd", threads: 22, rss: 61 * MB, cpu: 0.3, unit: "containerd.service", fds: 140 });
  s({ name: "agetty", ppid: 1, cmd: "/sbin/agetty -o -p -- \\u --noclear - linux", rss: 2 * MB, unit: "getty@tty1.service" });
  const nmbd = s({ name: "nmbd", ppid: 1, cmd: "/usr/sbin/nmbd --foreground --no-process-group", rss: 14 * MB, unit: "nmbd.service" });
  void nmbd;
  const smbd = s({ name: "smbd", ppid: 1, cmd: "/usr/sbin/smbd --foreground --no-process-group", rss: 22 * MB, unit: "smbd.service" });
  add({ name: "smbd-notifyd", ppid: smbd, cmd: "smbd: notifier", rss: 8 * MB, cgroup: sys("smbd.service") });
  add({ name: "smbd-cleanupd", ppid: smbd, cmd: "smbd: cleanupd", rss: 7 * MB, cgroup: sys("smbd.service") });
  add({ name: "smbd[192.168.1.", ppid: smbd, cmd: "smbd: client [192.168.1.45]", rss: 31 * MB, cgroup: sys("smbd.service"), start: Date.now() - 1 * DAY - 2 * HOUR, cpu: 0.2 });
  s({ name: "wsdd", ppid: 1, uid: 65534, cmd: "/usr/bin/python3 /usr/sbin/wsdd --shortlog --chroot=/run/wsdd -i enp3s0", rss: 16 * MB, unit: "wsdd.service" });
  const dockerd = add({ name: "dockerd", ppid: 1, cmd: "/usr/bin/dockerd -H fd:// --containerd=/run/containerd/containerd.sock", threads: 38, rss: 112 * MB, cpu: 0.8, cgroup: sys("docker.service"), start: BOOT + 11_000, fds: 212 });
  void containerd;

  // The user's systemd instance and Wargo itself.
  const userSlice = "0::/user.slice/user-1000.slice/user@1000.service/init.scope";
  const sdUser = add({ name: "systemd", ppid: 1, uid: 1000, cmd: "/lib/systemd/systemd --user", rss: 11 * MB, cgroup: userSlice, start: BOOT + 13_000, exe: "/usr/lib/systemd/systemd", cwd: "/" });
  add({ name: "(sd-pam)", ppid: sdUser, uid: 1000, cmd: "(sd-pam)", rss: 5 * MB, cgroup: userSlice, start: BOOT + 13_100, exe: "/usr/lib/systemd/systemd", cwd: "/" });
  add({
    pid: SELF_PID, name: "wargo", ppid: 1, uid: 1000, cmd: "/usr/local/bin/wargo serve", threads: 14, rss: 25 * MB, cpu: 1.2,
    cgroup: sys("wargo.service"), start: Date.now() - 6 * DAY - 3 * HOUR, exe: "/usr/local/bin/wargo", cwd: "/var/lib/wargo", fds: 27, env: 14,
  });

  // An SSH login running htop, and a long-lived tmux.
  const login = Date.now() - 2 * HOUR - 14 * MIN;
  const sess = "0::/user.slice/user-1000.slice/session-41.scope";
  const priv = add({ name: "sshd", ppid: sshd, cmd: "sshd: demo [priv]", rss: 11 * MB, cgroup: sess, start: login });
  const pts = add({ name: "sshd", ppid: priv, uid: 1000, cmd: "sshd: demo@pts/0", rss: 7 * MB, cgroup: sess, start: login + 300, exe: "/usr/sbin/sshd", cwd: "/" });
  const bash = add({ name: "bash", ppid: pts, uid: 1000, cmd: "-bash", rss: 5 * MB, cgroup: sess, start: login + 400, exe: "/usr/bin/bash", cwd: "/home/demo/stacks/media", fds: 4 });
  add({ name: "htop", ppid: bash, uid: 1000, cmd: "htop", rss: 6 * MB, cpu: 1.1, cgroup: sess, start: Date.now() - 25 * MIN, exe: "/usr/bin/htop", cwd: "/home/demo/stacks/media", fds: 5 });
  const tmuxStart = Date.now() - 4 * DAY - 7 * HOUR;
  const tmuxCg = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/tmux-spawn-2f1e.scope";
  const tmux = add({ name: "tmux: server", ppid: sdUser, uid: 1000, cmd: "tmux new -s main", rss: 4 * MB, cgroup: tmuxCg, start: tmuxStart, exe: "/usr/bin/tmux", cwd: "/home/demo" });
  const tb = add({ name: "bash", ppid: tmux, uid: 1000, cmd: "-bash", rss: 5 * MB, cgroup: tmuxCg, start: tmuxStart + 200, exe: "/usr/bin/bash", cwd: "/home/demo", fds: 4 });
  add({ name: "watch", ppid: tb, uid: 1000, cmd: "watch -n 10 df -h /DATA /mnt/backup", rss: 3 * MB, cgroup: tmuxCg, start: tmuxStart + 60_000, exe: "/usr/bin/watch", cwd: "/home/demo", fds: 4 });
  const tb2 = add({ name: "bash", ppid: tmux, uid: 1000, cmd: "-bash", rss: 5 * MB, cgroup: tmuxCg, start: Date.now() - 2 * DAY, exe: "/usr/bin/bash", cwd: "/home/demo/stacks/immich", fds: 4 });
  void tb2;
  return dockerd;
}

// --- containers ---------------------------------------------------------------

interface Ctr {
  name: string;
  start: number;
  ip: string;
}

function linuxserver(c: Ctr, shim: number, apps: { svc: string; name: string; cmd: string; uid: number; rss: number; threads: number; cpu: number; exe?: string; cwd?: string; fds?: number }[]) {
  const o = (x: Opt) => ctrProc(c, x);
  const scan = o({ name: "s6-svscan", ppid: shim, cmd: "/package/admin/s6/command/s6-svscan -d4 -- /run/service", rss: 1 * MB });
  const sd = o({ name: "s6-supervise", ppid: scan, cmd: "s6-supervise s6-linux-init-shutdownd", rss: 1 * MB });
  o({ name: "s6-linux-init-s", ppid: sd, cmd: "/package/admin/s6-linux-init/command/s6-linux-init-shutdownd -d3 -c /run/s6/basedir -g 3000 -C -B", rss: 1 * MB });
  const one = o({ name: "s6-supervise", ppid: scan, cmd: "s6-supervise s6rc-oneshot-runner", rss: 1 * MB });
  o({ name: "s6-ipcserverd", ppid: one, cmd: "/package/admin/s6/command/s6-ipcserverd -1 -- /package/admin/s6/command/s6-ipcserver-access -v0 -E -l0 -i data/rules -- /package/admin/s6/command/s6-sudod -t 30000 -- /package/admin/s6-rc/command/s6-rc-oneshot-run -l ../.. --", rss: 1 * MB });
  o({ name: "s6-supervise", ppid: scan, cmd: "s6-supervise s6rc-fdholder", rss: 1 * MB });
  const out: number[] = [];
  for (const a of apps) {
    const sv = o({ name: "s6-supervise", ppid: scan, cmd: `s6-supervise ${a.svc}`, rss: 1 * MB });
    out.push(o({ name: a.name, ppid: sv, cmd: a.cmd, uid: a.uid, rss: a.rss, threads: a.threads, cpu: a.cpu, exe: a.exe, cwd: a.cwd, fds: a.fds, start: c.start + 4000 }));
  }
  return out;
}

function ctrProc(c: Ctr, o: Opt) {
  return add({ start: c.start + 800, ctr: c.name, cgroup: `0::/system.slice/docker-${containerId(c.name)}.scope`, ...o });
}

function containers(dockerd: number) {
  const now = Date.now();
  for (const def of CONTAINERS) {
    if (def.state === "exited") continue;
    const c: Ctr = { name: def.name, start: containerStart(def.name), ip: containerIp(def.name) };
    const id = containerId(c.name);
    const immichIp = containerIp("immich-server");
    for (const [priv, pub, proto] of PORTS[def.name] ?? []) {
      add({ name: "docker-proxy", ppid: dockerd, cmd: `/usr/bin/docker-proxy -proto ${proto ?? "tcp"} -host-ip 0.0.0.0 -host-port ${pub} -container-ip ${c.ip} -container-port ${priv}`, threads: 7, rss: 4 * MB, cgroup: sys("docker.service"), start: c.start + 300 });
    }
    const shim = add({ name: "containerd-shim", ppid: 1, cmd: `/usr/bin/containerd-shim-runc-v2 -namespace moby -id ${id} -address /run/containerd/containerd.sock`, threads: 11, rss: 13 * MB, cgroup: sys("containerd.service"), start: c.start + 500 });
    const o = (x: Opt) => ctrProc(c, x);
    switch (def.name) {
      case "jellyfin": {
        const j = o({ name: "jellyfin", ppid: shim, uid: 1000, cmd: "/jellyfin/jellyfin --datadir /config --cachedir /cache --ffmpeg /usr/lib/jellyfin-ffmpeg/ffmpeg", threads: 44, rss: 812 * MB, cpu: 6, exe: "/jellyfin/jellyfin", cwd: "/", fds: 236, env: 18 });
        o({
          name: "ffmpeg", ppid: j, uid: 1000, threads: 17, rss: 356 * MB, cpu: 148, start: now - 12 * MIN, exe: "/usr/lib/jellyfin-ffmpeg/ffmpeg", cwd: "/", fds: 14,
          cmd: '/usr/lib/jellyfin-ffmpeg/ffmpeg -analyzeduration 200M -probesize 1G -fflags +genpts -f matroska -i file:"/media/tv/Severance/Season 02/Severance - S02E03 - Who Is Alive.mkv" -map_metadata -1 -map_chapters -1 -threads 0 -map 0:0 -map 0:1 -codec:v:0 libx264 -preset veryfast -crf 23 -maxrate 8000000 -bufsize 16000000 -codec:a:0 aac -ac 2 -ab 256000 -f hls -max_delay 5000000 -hls_time 3 -hls_segment_type mpegts -start_number 0 -hls_segment_filename "/cache/transcodes/8f3c1e0a2b9d4e6f%d.ts" -hls_playlist_type vod -hls_list_size 0 -y "/cache/transcodes/8f3c1e0a2b9d4e6f.m3u8"',
        });
        break;
      }
      case "sonarr":
        linuxserver(c, shim, [{ svc: "svc-sonarr", name: "Sonarr", cmd: "/app/sonarr/bin/Sonarr -nobrowser -data=/config", uid: 1000, rss: 318 * MB, threads: 27, cpu: 0.6, exe: "/app/sonarr/bin/Sonarr", cwd: "/app/sonarr/bin", fds: 142 }]);
        break;
      case "radarr":
        linuxserver(c, shim, [{ svc: "svc-radarr", name: "Radarr", cmd: "/app/radarr/bin/Radarr -nobrowser -data=/config", uid: 1000, rss: 286 * MB, threads: 25, cpu: 0.5, exe: "/app/radarr/bin/Radarr", cwd: "/app/radarr/bin", fds: 131 }]);
        break;
      case "prowlarr":
        linuxserver(c, shim, [{ svc: "svc-prowlarr", name: "Prowlarr", cmd: "/app/prowlarr/bin/Prowlarr -nobrowser -data=/config", uid: 1000, rss: 204 * MB, threads: 22, cpu: 0.3, exe: "/app/prowlarr/bin/Prowlarr", cwd: "/app/prowlarr/bin", fds: 98 }]);
        break;
      case "transmission":
        linuxserver(c, shim, [{ svc: "svc-transmission", name: "transmission-da", cmd: "/usr/bin/transmission-daemon -g /config -c /watch -f", uid: 911, rss: 74 * MB, threads: 4, cpu: 3.2, fds: 188 }]);
        break;
      case "immich-server": {
        const tini = o({ name: "tini", ppid: shim, cmd: "tini -- /bin/bash start.sh", rss: 1 * MB });
        const m = o({ name: "immich", ppid: tini, cmd: "immich", threads: 11, rss: 468 * MB, cpu: 1.8 });
        o({ name: "immich-api", ppid: m, cmd: "immich-api", threads: 11, rss: 391 * MB, cpu: 1.1 });
        break;
      }
      case "immich-postgres": {
        const pg = o({ name: "postgres", ppid: shim, uid: 999, cmd: 'postgres -c shared_preload_libraries=vectors.so -c search_path="$user", public, vectors -c logging_collector=on -c max_wal_size=2GB -c shared_buffers=512MB -c wal_compression=on', rss: 96 * MB, cpu: 0.1 });
        for (const n of ["logger", "checkpointer", "background writer", "walwriter", "autovacuum launcher", "vectors", "logical replication launcher"])
          o({ name: "postgres", ppid: pg, uid: 999, cmd: `postgres: ${n} `, rss: (n === "checkpointer" ? 180 : n === "vectors" ? 64 : 14) * MB, cpu: n === "walwriter" ? 0.1 : 0 });
        for (let i = 0; i < 5; i++)
          o({ name: "postgres", ppid: pg, uid: 999, cmd: `postgres: postgres immich ${immichIp}(${41022 + i * 37}) idle`, rss: (38 + i * 9) * MB, cpu: i === 0 ? 0.3 : 0, start: c.start + 30_000 });
        break;
      }
      case "immich-redis":
        o({ name: "redis-server", ppid: shim, uid: 999, cmd: "redis-server *:6379", threads: 5, rss: 14 * MB, cpu: 0.2 });
        break;
      case "nextcloud": {
        const ap = o({ name: "apache2", ppid: shim, cmd: "apache2 -DFOREGROUND", rss: 31 * MB });
        for (let i = 0; i < 7; i++) o({ name: "apache2", ppid: ap, uid: 33, cmd: "apache2 -DFOREGROUND", rss: (62 + i * 8) * MB, cpu: i < 2 ? 0.2 : 0, start: i < 5 ? c.start + 2000 : now - (i - 3) * HOUR });
        break;
      }
      case "home-assistant":
        linuxserver(c, shim, [{ svc: "home-assistant", name: "python3", cmd: "python3 -m homeassistant --config /config", uid: 0, rss: 634 * MB, threads: 49, cpu: 3.4, fds: 77 }]);
        break;
      case "pihole":
        linuxserver(c, shim, [
          { svc: "pihole-FTL", name: "pihole-FTL", cmd: "/usr/bin/pihole-FTL -f", uid: 999, rss: 38 * MB, threads: 6, cpu: 0.7 },
          { svc: "lighttpd", name: "lighttpd", cmd: "lighttpd -D -f /etc/lighttpd/lighttpd.conf", uid: 33, rss: 6 * MB, threads: 1, cpu: 0 },
          { svc: "cron", name: "cron", cmd: "/usr/sbin/cron -f", uid: 0, rss: 2 * MB, threads: 1, cpu: 0 },
        ]);
        break;
      case "vaultwarden":
        o({ name: "vaultwarden", ppid: shim, cmd: "/vaultwarden", threads: 13, rss: 39 * MB, cpu: 0.1 });
        break;
      case "uptime-kuma": {
        const di = o({ name: "dumb-init", ppid: shim, cmd: "/usr/bin/dumb-init -- extra/entrypoint.sh node server/server.js", rss: 1 * MB });
        o({ name: "node", ppid: di, cmd: "node server/server.js", threads: 11, rss: 143 * MB, cpu: 1.3 });
        break;
      }
      case "paperless": {
        const apps = linuxserver(c, shim, [
          { svc: "svc-webserver", name: "granian", cmd: "/usr/local/bin/python3 /usr/local/bin/granian --interface asginl --ws paperless.asgi:application", uid: 1000, rss: 168 * MB, threads: 4, cpu: 0, exe: "/usr/local/bin/python3.12", cwd: "/usr/src/paperless/src" },
          { svc: "svc-worker", name: "celery", cmd: "/usr/local/bin/python3 /usr/local/bin/celery --app paperless worker --loglevel INFO --without-mingle --without-gossip", uid: 1000, rss: 212 * MB, threads: 3, cpu: 0, exe: "/usr/local/bin/python3.12", cwd: "/usr/src/paperless/src" },
          { svc: "svc-scheduler", name: "celery", cmd: "/usr/local/bin/python3 /usr/local/bin/celery --app paperless beat --loglevel INFO", uid: 1000, rss: 141 * MB, threads: 1, cpu: 0, exe: "/usr/local/bin/python3.12", cwd: "/usr/src/paperless/src" },
          { svc: "svc-consumer", name: "python3", cmd: "python3 manage.py document_consumer", uid: 1000, rss: 156 * MB, threads: 2, cpu: 0, exe: "/usr/local/bin/python3.12", cwd: "/usr/src/paperless/src" },
        ]);
        for (let i = 0; i < 2; i++) o({ name: "celery", ppid: apps[1], uid: 1000, cmd: "/usr/local/bin/python3 /usr/local/bin/celery --app paperless worker --loglevel INFO --without-mingle --without-gossip", rss: 188 * MB, threads: 2, exe: "/usr/local/bin/python3.12", cwd: "/usr/src/paperless/src" });
        break;
      }
    }
  }
}

// Published ports [container, host, proto], as in features/containers.ts.
export const PORTS: Record<string, [number, number, string?][]> = {
  jellyfin: [[8096, 8096], [1900, 1900, "udp"], [7359, 7359, "udp"]],
  sonarr: [[8989, 8989]],
  radarr: [[7878, 7878]],
  prowlarr: [[9696, 9696]],
  transmission: [[9091, 9091], [51413, 51413], [51413, 51413, "udp"]],
  "immich-server": [[2283, 2283]],
  nextcloud: [[80, 8080]],
  pihole: [[53, 53], [53, 53, "udp"], [80, 8053]],
  vaultwarden: [[80, 8222]],
  "uptime-kuma": [[3001, 3001]],
  paperless: [[8000, 8010]],
};

// Start times, mirroring features/containers.ts.
const STARTED_AGO: Record<string, number> = {
  jellyfin: 19 * DAY - 40 * MIN,
  sonarr: 11 * DAY - 3 * HOUR,
  "immich-server": 6 * DAY - 2 * HOUR,
  "home-assistant": 14 * DAY - 25 * MIN,
};
const GROUPS = ["media", "immich", "nextcloud", "home", "network", "tools"];

function nameHash(s: string) {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

/** When a container (last) started, unix ms. */
export function containerStart(name: string) {
  const ago = STARTED_AGO[name];
  return ago ? Date.now() - ago : BOOT + 20_000 + (nameHash(name) % 30_000);
}

/** A container's address on its compose network ("" for host networking). */
export function containerIp(name: string) {
  const def = CONTAINERS.find((c) => c.name === name);
  if (!def || name === "home-assistant") return "";
  const members = CONTAINERS.filter((c) => c.group === def.group);
  return `172.${18 + Math.max(0, GROUPS.indexOf(def.group))}.0.${2 + members.indexOf(def)}`;
}

function build() {
  add({ pid: 1, name: "systemd", ppid: 0, cmd: "/sbin/init", rss: 13 * MB, cgroup: "0::/init.scope", start: BOOT + 300, fds: 118, env: 7 });
  add({ pid: 2, name: "kthreadd", ppid: 0, cmd: "", kernel: true, state: "S", start: BOOT + 300 });
  seq = 2;
  kernelThreads();
  seq = 290;
  const dockerd = hostServices();
  seq = 1300;
  containers(dockerd);
}
build();

const paused = new Set(CONTAINERS.filter((c) => c.state === "paused").map((c) => c.name));

// --- dynamic state ------------------------------------------------------------

/** Demo changes: killed and stopped processes. */
const changes = persisted("procs", () => ({ killed: [] as number[], stopped: [] as number[] }));

/** Extra processes (terminal sessions) provided by other demo modules. */
type ExtraSource = { list: () => Spec[]; signal: (pid: number, sig: string) => boolean };
const extras: ExtraSource[] = [];
export function registerProcSource(src: ExtraSource) {
  extras.push(src);
}

/** A fresh pid for a process starting now. */
export function allocPid() {
  return pidAt(Date.now());
}

/** Builds a Spec for an extra process with sensible defaults. */
export function makeSpec(o: Opt & { pid: number }): Spec {
  return {
    uid: 0, threads: 1, state: "S", nice: 0, cpu: 0, start: Date.now(), rss: 5 * MB, vms: 9 * MB, shared: 3 * MB, swap: 0, cgroup: "0::/system.slice/wargo.service",
    env: 16, fds: 4, io: [0, 0], ctx: [120, 4], ...o,
  };
}

const walks = new Map<number, Walk>();
const memDrift = new Map<number, Walk>();

function cpuNow(s: Spec, stopped: boolean): number {
  if (stopped || (s.ctr && paused.has(s.ctr))) return 0;
  if (s.cpu >= 0.3) {
    let w = walks.get(s.pid);
    if (!w) walks.set(s.pid, (w = new Walk(s.cpu, s.cpu, Math.max(0.2, s.cpu * 0.18), 0, Math.max(5, s.cpu * 1.6), 0.02)));
    return w.next();
  }
  const blip = s.kernel ? 0.01 : 0.05;
  return Math.random() < blip ? 0.1 + Math.random() * (s.kernel ? 0.4 : 0.9) : s.cpu;
}

function rssNow(s: Spec): number {
  if (!s.rss) return 0;
  let w = memDrift.get(s.pid);
  if (!w) memDrift.set(s.pid, (w = new Walk(1, 1, s.cpu > 1 ? 0.01 : 0.002, 0.95, 1.08, 0.005)));
  return Math.round((s.rss * w.next()) / 4096) * 4096;
}

/** All live specs: the table minus killed ones, plus extras. */
export function specs(): Spec[] {
  const killed = new Set(changes.get().killed);
  return [...table.filter((s) => !killed.has(s.pid)), ...extras.flatMap((e) => e.list())];
}

let last: ProcList | null = null;

function toProc(s: Spec, stopped: Set<number>): Proc {
  const st = stopped.has(s.pid);
  const cpu = Math.round(cpuNow(s, st) * 10) / 10;
  const memRSS = rssNow(s);
  let state = st ? "T" : s.state;
  if (!st && !s.kernel && cpu > 20) state = "R";
  else if (!st && cpu > 1 && Math.random() < 0.15) state = "R";
  return {
    pid: s.pid, ppid: s.ppid, name: s.name, user: userName(s.uid), uid: s.uid, state, cpu, memRSS,
    memPct: (memRSS / HOST.memTotal) * 100, threads: s.threads, startTime: Math.round(s.start), command: s.cmd, nice: s.nice,
    ...(s.kernel ? { kernel: true } : {}),
  };
}

/** The current process list (rescanned at most once a second, like the server). */
export function sample(force = false): ProcList {
  const t = Date.now();
  if (last && !force && t - last.sampledAt < 1000) return last;
  const stopped = new Set(changes.get().stopped);
  const procs = specs().map((s) => toProc(s, stopped));
  return (last = { procs, uid: ADMIN_UID, self: SELF_PID, totalMem: HOST.memTotal, cpus: HOST.threads, sampledAt: t });
}

function details(pid: number): ProcDetails | null {
  const l = sample();
  const p = l.procs.find((x) => x.pid === pid);
  const s = specs().find((x) => x.pid === pid);
  if (!p || !s) return null;
  const own = s.uid === ADMIN_UID;
  const parent = l.procs.find((x) => x.pid === p.ppid);
  const life = Math.max(1, (Date.now() - s.start) / DAY);
  const id = s.ctr ? containerId(s.ctr) : undefined;
  return {
    ...p,
    cmdline: s.cmd,
    ...(s.cmd ? { args: s.cmd.startsWith("-") || s.cmd.includes(": ") ? [s.cmd] : splitArgs(s.cmd) } : {}),
    ...(own ? { exe: s.exe ?? guessExe(s), cwd: s.cwd ?? "/", envCount: s.env, fdCount: s.fds } : {}),
    cgroup: s.cgroup,
    ...(id ? { containerId: id, containerName: s.ctr } : {}),
    children: l.procs.filter((x) => x.ppid === pid).map((x) => x.pid).sort((a, b) => a - b),
    mem: { rss: p.memRSS, vms: s.vms, shared: s.shared, swap: s.swap },
    ...(own ? { io: { read: Math.round(s.io[0] * life), write: Math.round(s.io[1] * life) } } : {}),
    status: { realUid: s.uid, volCtxSwitches: Math.round(s.ctx[0] * life), invCtxSwitches: Math.round(s.ctx[1] * life), ...(parent ? { parentName: parent.name } : {}) },
    readable: own,
  };
}

function splitArgs(cmd: string): string[] {
  // Shell-like: quotes group words and are dropped.
  const out: string[] = [];
  let cur = "";
  let has = false;
  let q = "";
  for (const ch of cmd) {
    if (q) {
      if (ch === q) q = "";
      else cur += ch;
    } else if (ch === '"' || ch === "'") (q = ch), (has = true);
    else if (ch === " ") {
      if (has) out.push(cur);
      cur = "";
      has = false;
    } else (cur += ch), (has = true);
  }
  if (has) out.push(cur);
  return out;
}

function guessExe(s: Spec) {
  const first = s.cmd.split(" ")[0].replace(/^-/, "");
  return first.startsWith("/") ? first : `/usr/bin/${first}`;
}

/** Removes pid and its descendants. */
function kill(pid: number) {
  const all = specs();
  const gone = new Set([pid]);
  let grew = true;
  while (grew) {
    grew = false;
    for (const s of all) if (!gone.has(s.pid) && gone.has(s.ppid)) (gone.add(s.pid), (grew = true));
  }
  const tablePids = new Set(table.map((s) => s.pid));
  for (const g of gone) for (const e of extras) e.signal(g, "KILL");
  changes.update((c) => {
    c.killed.push(...[...gone].filter((g) => tablePids.has(g)));
    c.stopped = c.stopped.filter((x) => !gone.has(x));
  });
}

const SIGNALS = ["TERM", "KILL", "HUP", "INT", "STOP", "CONT", "USR1", "USR2"];
const SHELLS = new Set(["bash", "htop", "watch", "sudo", "docker", "sh"]);

// --- handlers -------------------------------------------------------------------

topic("procs", { snapshot: () => sample(), tick: () => sample(true), every: 2000 });

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  api.get("/api/procs", () => json(sample())),
  api.get("/api/procs/:pid", ({ params }) => {
    const pid = Number(params.pid);
    if (!Number.isInteger(pid) || pid <= 0) return badRequest("invalid pid");
    const d = details(pid);
    return d ? json(d) : notFound();
  }),
  api.post("/api/procs/:pid/signal", async ({ request, params }) => {
    const pid = Number(params.pid);
    if (!Number.isInteger(pid)) return badRequest("invalid pid");
    const req = await body<{ signal?: string }>(request);
    const name = String(req.signal ?? "").toUpperCase().replace(/^SIG/, "");
    if (!SIGNALS.includes(name)) return badRequest("unsupported signal");
    if (pid <= 1) return fail(403, "forbidden", "Refusing to signal PID 1 or below");
    if (pid === SELF_PID) return fail(403, "forbidden", "Refusing to signal Wargo itself");
    const s = specs().find((x) => x.pid === pid);
    if (!s) return notFound();
    if (s.uid !== ADMIN_UID) {
      const need = requireApproval(request, "procs.signal", `SIG${name} pid ${pid} (${s.name}, user ${userName(s.uid)})`);
      if (need) return need;
    }
    if (s.kernel || pid === 2) return fail(403, "forbidden", "Kernel threads ignore signals");
    // Terminal sessions handle their own signals.
    const handled = extras.some((e) => e.signal(pid, name));
    if (!handled) {
      if (name === "TERM" || name === "KILL" || name === "INT" || (name === "HUP" && SHELLS.has(s.name))) kill(pid);
      else if (name === "STOP") changes.update((c) => void (c.stopped.includes(pid) || c.stopped.push(pid)));
      else if (name === "CONT") changes.update((c) => void (c.stopped = c.stopped.filter((x) => x !== pid)));
    }
    sample(true);
    refresh("procs");
    return json({ ok: true, pid, signal: name });
  }),
];

// SPDX-License-Identifier: AGPL-3.0-only
// System: live metrics (and their 10-minute history), server info and
// specs, storage, public IP, Wargo's own process (About) and Events.
import type { Point, Snapshot, Temp } from "@/features/system/useMetrics";
import type { Drive, Filesystem } from "@/features/system/Storage";
import { api, json, query } from "../core/http";
import { topic } from "../core/bus";
import { DAY, GB, HOUR, MB, MIN, TB, Walk, between, clamp, seeded } from "../core/rand";
import { DEMO_USER, HOST } from "./host";

// --- live metrics --------------------------------------------------------

const cores = Array.from({ length: HOST.threads }, (_, i) => new Walk(8 + i, 6 + (i % 4) * 3, 7, 0, 100, 0.04));
const mem = new Walk(41, 42, 0.4, 20, 90, 0.005);
const rx = new Walk(2.4 * MB, 1.8 * MB, 0.9 * MB, 20e3, 60 * MB, 0.03);
const tx = new Walk(380e3, 300e3, 160e3, 5e3, 12 * MB, 0.03);
const dr = new Walk(1.2 * MB, 0.8 * MB, 1.4 * MB, 0, 220 * MB, 0.03);
const dw = new Walk(2.1 * MB, 1.6 * MB, 1.2 * MB, 0, 180 * MB, 0.03);
let rxTotal = 1.83 * TB;
let txTotal = 412 * GB;

const IF_STATIC = [
  { name: "enp3s0", addrs: [HOST.lanCidr, "fd12:3456:789a::20/64"], link: { kind: "ethernet" as const, up: true, rateMbps: 1000, connection: "Wired connection 1" } },
  { name: "tailscale0", addrs: [`${HOST.tailscaleIp}/32`], link: { kind: "vpn" as const, up: true, connection: "Tailscale" } },
];

function temps(cpuPct: number): Temp[] {
  const t = 41 + cpuPct * 0.32 + Math.random() * 1.5;
  return [
    { chip: "k10temp", label: "Tctl", value: Math.round(t * 10) / 10, high: 90, crit: 95, kind: "cpu" },
    { chip: "k10temp", label: "Tccd1", value: Math.round((t - 3) * 10) / 10, kind: "core" },
    { chip: "nvme", label: "Composite", value: 38 + Math.round(Math.random() * 2), high: 80, crit: 85, kind: "disk" },
    { chip: "drivetemp", label: "sda", value: 34, high: 55, crit: 60, kind: "disk" },
    { chip: "drivetemp", label: "sdb", value: 35, high: 55, crit: 60, kind: "disk" },
    { chip: "amdgpu", label: "edge", value: 39 + Math.round(Math.random()), crit: 100, kind: "gpu" },
    { chip: "acpitz", label: "temp1", value: 27.8, crit: 105, kind: "board" },
  ];
}

let last: Snapshot | null = null;

function sample(ts = Date.now()): Snapshot {
  const per = cores.map((c) => Math.round(c.next() * 10) / 10);
  const cpu = per.reduce((a, b) => a + b, 0) / per.length;
  const memPct = mem.next();
  const used = (memPct / 100) * HOST.memTotal;
  const r = rx.next();
  const t = tx.next();
  rxTotal += r;
  txTotal += t;
  const readRate = dr.next();
  const writeRate = dw.next();
  const loadBase = (cpu / 100) * HOST.threads;
  const tt = temps(cpu);
  return (last = {
    ts,
    cpu: { total: cpu, perCore: per, load: [loadBase + 0.3, loadBase * 0.9 + 0.4, loadBase * 0.8 + 0.5], freqMHz: 2400 + cpu * 18 + Math.random() * 120 },
    memory: {
      total: HOST.memTotal,
      used,
      available: HOST.memTotal - used,
      cached: 9.4 * GB,
      buffers: 0.6 * GB,
      swapTotal: HOST.swapTotal,
      swapUsed: 210 * MB,
      usedPct: memPct,
    },
    temps: tt,
    fans: [
      { chip: "nct6798", label: "CPU fan", rpm: Math.round(820 + cpu * 9 + Math.random() * 20) },
      { chip: "nct6798", label: "Case fan", rpm: Math.round(640 + Math.random() * 15) },
    ],
    net: [
      { ...IF_STATIC[0], rxRate: r * 0.94, txRate: t * 0.9, rxTotal, txTotal },
      { ...IF_STATIC[1], rxRate: r * 0.06, txRate: t * 0.1, rxTotal: rxTotal * 0.04, txTotal: txTotal * 0.07 },
    ],
    primaryIf: "enp3s0",
    netRx: r,
    netTx: t,
    disks: [
      { name: "nvme0n1", readRate: readRate * 0.6, writeRate: writeRate * 0.7, busy: clamp((readRate + writeRate) / (40 * MB) * 100, 0, 100) * 0.5 },
      { name: "sda", readRate: readRate * 0.3, writeRate: writeRate * 0.25, busy: clamp((readRate + writeRate) / (20 * MB) * 100, 0, 100) * 0.4 },
      { name: "sdb", readRate: readRate * 0.1, writeRate: writeRate * 0.05, busy: 1 + Math.random() * 2 },
    ],
    diskRead: readRate,
    diskWrite: writeRate,
    uptime: Math.floor(Date.now() / 1000) - HOST.bootTime,
    procs: 312 + Math.round(Math.random() * 6),
    cpuTemp: tt[0].value,
  });
}

const toPoint = (s: Snapshot): Point => ({
  t: s.ts,
  cpu: s.cpu.total,
  mem: s.memory.usedPct,
  swap: (s.memory.swapUsed / s.memory.swapTotal) * 100,
  rx: s.netRx,
  tx: s.netTx,
  dr: s.diskRead,
  dw: s.diskWrite,
  temp: s.cpuTemp,
  load: s.cpu.load[0],
  cores: s.cpu.perCore,
});

// Ten minutes of history, made up once per page load.
let history: Point[] | null = null;
function getHistory() {
  if (!history) {
    const start = Date.now() - 600_000;
    history = Array.from({ length: 600 }, (_, i) => toPoint(sample(start + i * 1000)));
  }
  return history;
}

topic("metrics", {
  snapshot: () => last ?? sample(),
  every: 1000,
  tick: () => {
    const s = sample();
    getHistory().push(toPoint(s));
    if (history!.length > 600) history!.shift();
    return s;
  },
});

// --- storage ---------------------------------------------------------------

const filesystems: Filesystem[] = [
  { device: "/dev/nvme0n1p2", mount: "/", fstype: "ext4", total: 915 * GB, used: 182 * GB, free: 686 * GB, usedPct: 19.9, inodesPct: 6, readOnly: false, partition: "nvme0n1p2" },
  { device: "/dev/nvme0n1p1", mount: "/boot/efi", fstype: "vfat", total: 511 * MB, used: 6 * MB, free: 505 * MB, usedPct: 1.2, inodesPct: 0, readOnly: false, label: "EFI", partition: "nvme0n1p1" },
  { device: "/dev/sda1", mount: "/DATA", fstype: "ext4", total: 7.2 * TB, used: 4.9 * TB, free: 2.3 * TB, usedPct: 68.1, inodesPct: 2, readOnly: false, label: "DATA", partition: "sda1" },
  { device: "/dev/sdb1", mount: "/mnt/backup", fstype: "btrfs", total: 7.2 * TB, used: 3.1 * TB, free: 4.1 * TB, usedPct: 43.0, inodesPct: 0, readOnly: false, label: "Backup", partition: "sdb1" },
];

const drives: Drive[] = [
  {
    name: "nvme0n1",
    model: "Samsung SSD 980 PRO 1TB",
    serial: "S5GXNX0T123456",
    size: 1000 * 1e9,
    rotational: false,
    removable: false,
    transport: "nvme",
    temp: 39,
    partitions: [
      { name: "nvme0n1p1", size: 512 * MB, label: "EFI", mounts: ["/boot/efi"] },
      { name: "nvme0n1p2", size: 930 * GB, mounts: ["/"] },
      { name: "nvme0n1p3", size: 4 * GB, label: "swap" },
    ],
    used: 182 * GB + 6 * MB,
    total: 915 * GB + 511 * MB,
  },
  {
    name: "sda",
    model: "WDC WD80EFZZ-68BTXN0",
    serial: "WD-CA1234567",
    size: 8 * 1e12,
    rotational: true,
    removable: false,
    transport: "sata",
    temp: 34,
    partitions: [{ name: "sda1", size: 7.2 * TB, label: "DATA", mounts: ["/DATA"] }],
    used: 4.9 * TB,
    total: 7.2 * TB,
  },
  {
    name: "sdb",
    model: "ST8000VN004-3CP101",
    serial: "WWZ1ABCD",
    size: 8 * 1e12,
    rotational: true,
    removable: false,
    transport: "sata",
    temp: 35,
    partitions: [{ name: "sdb1", size: 7.2 * TB, label: "Backup", mounts: ["/mnt/backup"] }],
    used: 3.1 * TB,
    total: 7.2 * TB,
  },
  {
    name: "sdc",
    model: "SanDisk Ultra Fit",
    size: 64 * 1e9,
    rotational: false,
    removable: true,
    transport: "usb",
    partitions: [{ name: "sdc1", size: 59.6 * GB, label: "INSTALLER" }],
    used: 0,
    total: 0,
  },
];

// --- events ------------------------------------------------------------------

type Level = "error" | "warning";
interface Ev {
  source: "system" | "kernel" | "containers" | "wargo";
  level: Level;
  origin: string;
  unit?: string;
  message: string;
  count: number;
  /** Minutes ago of the newest and oldest occurrence. */
  lastMin: number;
  firstMin: number;
}

const EVENTS: Ev[] = [
  { source: "kernel", level: "error", origin: "kernel", message: "usb 1-2.2: device descriptor read/64, error -32", count: 1286, lastMin: 3, firstMin: 600 },
  { source: "containers", level: "warning", origin: "jellyfin", unit: "jellyfin/jellyfin:10.10.3", message: "Health check failing (unhealthy)", count: 2, lastMin: 18, firstMin: 95 },
  { source: "system", level: "error", origin: "systemd", unit: "init.scope", message: "Failed to start backup-photos.service - Nightly photo backup.", count: 1, lastMin: 210, firstMin: 210 },
  { source: "system", level: "error", origin: "rsync", unit: "backup-photos.service", message: "rsync: [sender] link_stat \"/DATA/Photos/2026/.thumbnails\" failed: No such file or directory (2)", count: 1, lastMin: 211, firstMin: 211 },
  { source: "wargo", level: "warning", origin: "sign-in", message: "Failed password sign-in from 198.51.100.23 as admin", count: 6, lastMin: 47, firstMin: 52 },
  { source: "kernel", level: "warning", origin: "kernel", message: "ata1.00: exception Emask 0x0 SAct 0x0 SErr 0x0 action 0x6 frozen", count: 1, lastMin: 1340, firstMin: 1340 },
  { source: "system", level: "warning", origin: "smartd", unit: "smartmontools.service", message: "Device: /dev/sda [SAT], 8 Currently unreadable (pending) sectors", count: 24, lastMin: 30, firstMin: 1410 },
  { source: "containers", level: "error", origin: "nextcloud-cron", unit: "nextcloud:29-apache", message: "Exited with code 1", count: 3, lastMin: 125, firstMin: 380 },
  { source: "system", level: "warning", origin: "dockerd", unit: "docker.service", message: "level=warning msg=\"Health check for container 3f1c… error: context deadline exceeded\"", count: 9, lastMin: 18, firstMin: 96 },
  { source: "kernel", level: "warning", origin: "kernel", message: "[UFW BLOCK] IN=enp3s0 OUT= SRC=45.148.10.7 DST=192.168.1.20 PROTO=TCP DPT=22", count: 412, lastMin: 1, firstMin: 1435 },
  { source: "system", level: "warning", origin: "systemd-resolved", unit: "systemd-resolved.service", message: "Using degraded feature set UDP instead of UDP+EDNS0 for DNS server 192.168.1.1.", count: 3, lastMin: 520, firstMin: 1300 },
  { source: "system", level: "error", origin: "sshd", unit: "ssh.service", message: "error: kex_exchange_identification: Connection closed by remote host", count: 37, lastMin: 9, firstMin: 1420 },
  { source: "containers", level: "warning", origin: "qbittorrent", unit: "lscr.io/linuxserver/qbittorrent", message: "Stopped by a signal (exit code 143)", count: 1, lastMin: 3800, firstMin: 3800 },
  { source: "kernel", level: "error", origin: "kernel", message: "amdgpu 0000:30:00.0: [drm] *ERROR* dc_dmub_srv_log_diagnostic_data: DMCUB error - collecting diagnostic data", count: 2, lastMin: 6200, firstMin: 8100 },
  { source: "wargo", level: "error", origin: "containers.update", message: "immich-server: pull failed: toomanyrequests: You have reached your pull rate limit", count: 1, lastMin: 4300, firstMin: 4300 },
];

const RANGES: Record<string, number> = { "1h": 60, "24h": 1440, "7d": 10080 };

function events(range: string, warnings: boolean) {
  const minutes = RANGES[range] ?? 1440;
  const now = Date.now();
  const list = EVENTS.filter((e) => e.lastMin <= minutes && (warnings || e.level === "error")).map((e, i) => {
    // Repeats inside the window only: scale long-running counts down.
    const span = Math.max(1, e.firstMin - e.lastMin);
    const inWindow = Math.min(e.firstMin, minutes) - e.lastMin;
    const count = e.count === 1 ? 1 : Math.max(1, Math.round((e.count * Math.max(inWindow, 1)) / span));
    return {
      key: `ev${i}`,
      source: e.source,
      level: e.level,
      origin: e.origin,
      unit: e.unit,
      message: e.message,
      count,
      first: now - Math.min(e.firstMin, minutes) * MIN,
      last: now - e.lastMin * MIN - Math.round(Math.random() * 20_000),
    };
  });
  list.sort((a, b) => b.last - a.last);
  const occ = (l: Level) => list.filter((e) => e.level === l).reduce((a, e) => a + e.count, 0);
  return {
    events: list,
    errors: occ("error"),
    warnings: warnings ? occ("warning") : 0,
    sources: {
      system: { available: true },
      kernel: { available: true },
      containers: { available: true, note: "Docker keeps only its most recent events in memory." },
      wargo: { available: true },
    },
    since: now - minutes * MIN,
  };
}

// --- handlers ----------------------------------------------------------------

const started = Date.now() - 6 * DAY - 3 * HOUR;
const rss = seeded(7);

export const systemHandlers = [
  api.get("/api/system/info", () =>
    json({
      hostname: HOST.hostname,
      os: HOST.os,
      osId: HOST.osId,
      kernel: HOST.kernel,
      arch: HOST.arch,
      cpuModel: HOST.cpuModel,
      cores: HOST.cores,
      threads: HOST.threads,
      memTotal: HOST.memTotal,
      bootTime: HOST.bootTime,
      goVersion: "go1.26.10",
      specs: {
        model: "ASRock B550M Pro4",
        board: "ASRock B550M Pro4",
        gpus: ["AMD Radeon Vega 8 (Cezanne)"],
        shell: "bash 5.2.15",
        init: "systemd 252",
        timezone: "Europe/Berlin",
        locale: "en_US.UTF-8",
        localIp: HOST.lanIp,
        packages: [
          { manager: "dpkg", count: 1342 },
          { manager: "flatpak", count: 4 },
        ],
        logo: false,
      },
    }),
  ),
  api.get("/api/system/metrics", () => json(last ?? sample())),
  api.get("/api/system/metrics/history", () => json({ interval: 1000, points: getHistory() })),
  api.get("/api/system/storage", () => json({ drives, filesystems })),
  api.get("/api/system/public-ip", () => json({ ip: HOST.publicIp, source: "api.ipify.org", checkedAt: Date.now() - 4 * MIN })),
  api.get("/api/system/events", ({ request }) => {
    const q = query(request);
    return json(events(q.get("range") ?? "24h", (q.get("level") ?? "warning") === "warning"));
  }),
  api.get("/api/system/self", () =>
    json({
      version: `${__WARGO_DEMO_REF__} (demo)`,
      goVersion: "go1.26.10",
      pid: 1187,
      rssBytes: Math.round(between(23, 27, rss) * MB),
      heapBytes: Math.round(between(5, 6.5, rss) * MB),
      goroutines: 38 + Math.round(Math.random() * 6),
      uptimeSec: Math.floor((Date.now() - started) / 1000),
      dataDir: "/var/lib/wargo",
      user: DEMO_USER,
    }),
  ),
  api.get("/api/system/logo", () => new Response(null, { status: 404 })),
];

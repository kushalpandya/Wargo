// SPDX-License-Identifier: AGPL-3.0-only
// OS updates (internal/pkg): apt on Debian 12. Checks and upgrades need
// admin approval like the real server; an upgrade streams a believable
// apt-get log through a "pkg.upgrade" job, then asks for a reboot.
import type { RequestHandler, WebSocketHandler } from "msw";
import { delay } from "msw";
import type { CheckResult, PkgStatus, PkgUpdate, Precheck, RunRecord } from "@/features/updates/api";
import { api, badRequest, body, fail, json, notFound } from "../core/http";
import { refresh, topic } from "../core/bus";
import { requireApproval } from "../core/elevation";
import { startJob, type JobSnapshot, type JobStep } from "../core/jobs";
import { persisted } from "../core/store";
import { DAY, GB, HOUR, MIN } from "../core/rand";
import { HOST } from "./host";
import { recordAudit } from "./settings";

const KB = 1000;
const NEW_KERNEL = "linux-image-6.1.0-29-amd64";
const WARGO = "/usr/local/bin/wargo";
const LOG_DIR = "/var/lib/wargo/pkg";
const UPGRADE_ARGV = [
  "apt-get", "-y", "-q",
  "-o", "APT::Status-Fd=1",
  "-o", "Dpkg::Options::=--force-confdef",
  "-o", "Dpkg::Options::=--force-confold",
  "-o", "Dpkg::Use-Pty=0",
  "full-upgrade",
];

// [name, current, new, repo, download size]
type Row = [string, string, string, string, number];
const PENDING: Row[] = [
  ["amd64-microcode", "3.20240820.1~deb12u1", "3.20250311.1~deb12u1", "stable-security", 196 * KB],
  ["curl", "7.88.1-10+deb12u8", "7.88.1-10+deb12u12", "stable-security", 316 * KB],
  ["libcurl4", "7.88.1-10+deb12u8", "7.88.1-10+deb12u12", "stable-security", 391 * KB],
  ["git", "1:2.39.5-0+deb12u1", "1:2.39.5-0+deb12u2", "stable-security", 7_264 * KB],
  ["git-man", "1:2.39.5-0+deb12u1", "1:2.39.5-0+deb12u2", "stable-security", 2_051 * KB],
  ["linux-image-amd64", "6.1.119-1", "6.1.123-1", "stable-security", 1_484],
  ["libssl3", "3.0.15-1~deb12u1", "3.0.16-1~deb12u1", "stable-security", 2_027 * KB],
  ["openssl", "3.0.15-1~deb12u1", "3.0.16-1~deb12u1", "stable-security", 1_427 * KB],
  ["openssh-client", "1:9.2p1-2+deb12u3", "1:9.2p1-2+deb12u5", "stable-security", 992 * KB],
  ["openssh-server", "1:9.2p1-2+deb12u3", "1:9.2p1-2+deb12u5", "stable-security", 457 * KB],
  ["openssh-sftp-server", "1:9.2p1-2+deb12u3", "1:9.2p1-2+deb12u5", "stable-security", 65 * KB],
  ["python3.11", "3.11.2-6+deb12u4", "3.11.2-6+deb12u5", "stable-security", 573 * KB],
  ["python3.11-minimal", "3.11.2-6+deb12u4", "3.11.2-6+deb12u5", "stable-security", 2_065 * KB],
  ["libpython3.11-minimal", "3.11.2-6+deb12u4", "3.11.2-6+deb12u5", "stable-security", 817 * KB],
  ["libpython3.11-stdlib", "3.11.2-6+deb12u4", "3.11.2-6+deb12u5", "stable-security", 1_798 * KB],
  ["rsync", "3.2.7-1+deb12u1", "3.2.7-1+deb12u2", "stable-security", 418 * KB],
  ["base-files", "12.4+deb12u8", "12.4+deb12u9", "stable", 71 * KB],
  ["libc-bin", "2.36-9+deb12u8", "2.36-9+deb12u9", "stable", 608 * KB],
  ["libc6", "2.36-9+deb12u8", "2.36-9+deb12u9", "stable", 2_755 * KB],
  ["locales", "2.36-9+deb12u8", "2.36-9+deb12u9", "stable", 3_903 * KB],
  ["libsystemd0", "252.31-1~deb12u1", "252.33-1~deb12u1", "stable", 333 * KB],
  ["libudev1", "252.31-1~deb12u1", "252.33-1~deb12u1", "stable", 110 * KB],
  ["systemd", "252.31-1~deb12u1", "252.33-1~deb12u1", "stable", 3_036 * KB],
  ["systemd-sysv", "252.31-1~deb12u1", "252.33-1~deb12u1", "stable", 42 * KB],
  ["udev", "252.31-1~deb12u1", "252.33-1~deb12u1", "stable", 1_708 * KB],
  ["tzdata", "2024a-0+deb12u1", "2024b-0+deb12u1", "stable-updates", 255 * KB],
  ["containerd.io", "1.7.23-1", "1.7.24-1", "bookworm", 30_352 * KB],
  ["docker-buildx-plugin", "0.17.1-1~debian.12~bookworm", "0.19.3-1~debian.12~bookworm", "bookworm", 30_157 * KB],
  ["docker-ce", "5:27.3.1-1~debian.12~bookworm", "5:27.4.1-1~debian.12~bookworm", "bookworm", 25_571 * KB],
  ["docker-ce-cli", "5:27.3.1-1~debian.12~bookworm", "5:27.4.1-1~debian.12~bookworm", "bookworm", 14_865 * KB],
  ["docker-ce-rootless-extras", "5:27.3.1-1~debian.12~bookworm", "5:27.4.1-1~debian.12~bookworm", "bookworm", 9_589 * KB],
  ["docker-compose-plugin", "2.29.7-1~debian.12~bookworm", "2.32.1-1~debian.12~bookworm", "bookworm", 12_778 * KB],
  ["tailscale", "1.76.6", "1.78.1", "bookworm", 29_430 * KB],
];

const FLATPAK: PkgUpdate[] = [
  { name: "org.freedesktop.Platform", current: "23.08.26", new: "23.08.27", repo: "flathub", size: 0, source: "flatpak" },
  { name: "org.freedesktop.Platform.GL.default", current: "24.3.1", new: "24.3.3", repo: "flathub", size: 0, source: "flatpak" },
];

const toUpdate = ([name, current, next, repo, size]: Row): PkgUpdate => ({ name, current, new: next, repo, size, source: "repo" });

function sortUpdates(u: PkgUpdate[]) {
  return [...u].sort((a, b) => (a.repo !== b.repo ? (a.repo < b.repo ? -1 : 1) : a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
}

function checkResult(updates: PkgUpdate[], at: number, durationMs: number): CheckResult {
  const u = sortUpdates(updates);
  return { at: new Date(at).toISOString(), durationMs, updates: u, extras: FLATPAK, downloadSize: u.reduce((a, x) => a + x.size, 0) };
}

const runId = (t: number) => {
  const d = new Date(t);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
};

function record(id: string, packages: number, start: number, end: number | undefined): RunRecord {
  return {
    id,
    unit: "wargo-upgrade-" + id,
    log: `${LOG_DIR}/${id}.log`,
    backend: "apt",
    packages,
    startedAt: new Date(start).toISOString(),
    ...(end ? { finishedAt: new Date(end).toISOString() } : {}),
    ok: false,
    upgraded: 0,
    rebootRequired: false,
  };
}

interface State {
  /** What the next check would find. */
  pending: PkgUpdate[];
  lastCheck?: CheckResult;
  lastRun?: RunRecord;
  reboot: { required: boolean; reason?: string };
  /** Log lines per run id. */
  logs: Record<string, string[]>;
  /** A run that was going when the page closed (finished on next load). */
  inflight?: RunRecord;
}

function initial(): State {
  const now = Date.now();
  // The previous upgrade, 11 days ago (19 packages).
  const prevStart = now - 11 * DAY - 23 * HOUR;
  const prevId = runId(prevStart);
  const prev: RunRecord = {
    ...record(prevId, 19, prevStart, prevStart + 3 * MIN + 12_000),
    ok: true,
    exitStatus: 0,
    upgraded: 19,
    stage: "Done",
  };
  const prevPkgs: Row[] = [
    ["bind9-dnsutils", "1:9.18.28-1~deb12u2", "1:9.18.33-1~deb12u2", "stable-security", 403 * KB],
    ["bind9-host", "1:9.18.28-1~deb12u2", "1:9.18.33-1~deb12u2", "stable-security", 305 * KB],
    ["bind9-libs", "1:9.18.28-1~deb12u2", "1:9.18.33-1~deb12u2", "stable-security", 1_183 * KB],
    ["libexpat1", "2.5.0-1+deb12u1", "2.5.0-1+deb12u2", "stable-security", 99 * KB],
    ["libglib2.0-0", "2.74.6-2+deb12u4", "2.74.6-2+deb12u5", "stable-security", 1_421 * KB],
    ["libsqlite3-0", "3.40.1-2+deb12u0", "3.40.1-2+deb12u1", "stable-security", 838 * KB],
    ["needrestart", "3.6-4+deb12u1", "3.6-4+deb12u2", "stable-security", 61 * KB],
    ["libgnutls30", "3.7.9-2+deb12u3", "3.7.9-2+deb12u4", "stable", 1_404 * KB],
    ["libnss3", "2:3.87.1-1", "2:3.87.1-1+deb12u1", "stable", 1_331 * KB],
    ["libxml2", "2.9.14+dfsg-1.3~deb12u1", "2.9.14+dfsg-1.3~deb12u2", "stable", 687 * KB],
    ["distro-info-data", "0.58+deb12u2", "0.58+deb12u3", "stable", 6 * KB],
    ["libgstreamer1.0-0", "1.22.0-2", "1.22.0-2+deb12u1", "stable", 1_150 * KB],
    ["intel-media-va-driver", "23.1.1+dfsg1-1", "23.1.1+dfsg1-1+deb12u1", "stable", 2_910 * KB],
    ["smartmontools", "7.3-pre1", "7.3-pre1+deb12u1", "stable", 591 * KB],
    ["wget", "1.21.3-1+b2", "1.21.3-1+deb12u1", "stable", 937 * KB],
    ["libnghttp2-14", "1.52.0-1+deb12u1", "1.52.0-1+deb12u2", "stable", 73 * KB],
    ["libtiff6", "4.5.0-6+deb12u1", "4.5.0-6+deb12u2", "stable", 316 * KB],
    ["docker-compose-plugin", "2.29.2-1~debian.12~bookworm", "2.29.7-1~debian.12~bookworm", "bookworm", 12_731 * KB],
    ["tailscale", "1.76.1", "1.76.6", "bookworm", 29_218 * KB],
  ];
  return {
    pending: PENDING.map(toUpdate),
    lastCheck: checkResult(PENDING.map(toUpdate), now - 2 * HOUR - 14 * MIN, 6_840),
    lastRun: prev,
    reboot: { required: false },
    logs: { [prevId]: [...plan(prevPkgs.map(toUpdate), false).flatMap((s) => s.log ?? []), "Wargo: upgrade finished with exit status 0"] },
  };
}

const state = persisted<State>("pkg", initial, 1);

// Live run (in memory; jobs don't survive a reload).
let running: { job: JobSnapshot; rec: RunRecord } | null = null;
let checking = false;

function status(): PkgStatus {
  const s = state.get();
  return {
    distro: HOST.os,
    distroId: HOST.osId,
    backend: "apt",
    available: true,
    extras: ["flatpak"],
    needsRootToCheck: true,
    upgradeCommand: UPGRADE_ARGV.join(" "),
    checking,
    ...(s.lastCheck ? { lastCheck: s.lastCheck } : {}),
    ...(running ? { running: running.job } : {}),
    ...(s.lastRun ? { lastRun: s.lastRun } : {}),
    reboot: s.reboot,
  };
}

topic("pkg", { snapshot: status });

// ---- the apt-get output ----

const fmtSize = (b: number) => (b >= 1e6 ? `${(b / 1e6).toFixed(1)} MB` : `${(b / 1e3).toFixed(1)} kB`);
const arch = (u: PkgUpdate) => (u.name === "tzdata" || u.name.endsWith("-man") || u.name === "locales" ? "all" : "amd64");
const debName = (u: PkgUpdate) => `${u.name}_${u.new.replace(/^\d+:/, "")}_${arch(u)}.deb`;

function mirror(u: PkgUpdate) {
  switch (u.repo) {
    case "stable-security":
      return "http://security.debian.org/debian-security bookworm-security/main";
    case "stable-updates":
      return "http://deb.debian.org/debian bookworm-updates/main";
    case "bookworm":
      return u.name === "tailscale" ? "https://pkgs.tailscale.com/stable/debian bookworm/main" : "https://download.docker.com/linux/debian bookworm/stable";
    default:
      return "http://deb.debian.org/debian bookworm/main";
  }
}

function wrap(words: string[]) {
  const lines: string[] = [];
  let cur = " ";
  for (const w of words) {
    if (cur.length + w.length + 1 > 78) {
      lines.push(cur);
      cur = " ";
    }
    cur += " " + w;
  }
  if (cur.trim()) lines.push(cur);
  return lines;
}

/** Steps of an apt full-upgrade of these packages (~30 s for ~33 packages). */
function plan(ups: PkgUpdate[], kernel: boolean): JobStep[] {
  // dpkg's usual order: libraries and base first, the rest alphabetically.
  const order = [...ups].sort((a, b) => a.name.localeCompare(b.name));
  const total = ups.reduce((a, u) => a + u.size, 0) + (kernel ? 68_942 * KB : 0);
  const files = order.length + (kernel ? 1 : 0);
  const steps: JobStep[] = [
    { ms: 700, stage: "Preparing", current: "Reading package lists", log: ["Reading package lists..."] },
    { ms: 500, stage: "Preparing", current: "Building dependency tree", log: ["Building dependency tree...", "Reading state information..."] },
    {
      ms: 900,
      stage: "Preparing",
      current: "Calculating upgrade",
      log: [
        "Calculating upgrade...",
        ...(kernel ? ["The following NEW packages will be installed:", `  ${NEW_KERNEL}`] : []),
        "The following packages will be upgraded:",
        ...wrap(order.map((u) => u.name)),
        `${order.length} upgraded, ${kernel ? 1 : 0} newly installed, 0 to remove and 0 not upgraded.`,
        `Need to get ${fmtSize(total)} of archives.`,
        `After this operation, ${kernel ? "412 MB of additional" : "1,268 kB of additional"} disk space will be used.`,
      ],
    },
  ];
  let n = 0;
  const get = (url: string, name: string, a: string, ver: string, size: number) => {
    n++;
    steps.push({
      ms: Math.min(900, 120 + size / 60_000),
      stage: "Downloading",
      current: `Retrieving file ${n} of ${files}`,
      log: [`Get:${n} ${url} ${a} ${name} ${a} ${ver} [${fmtSize(size)}]`],
    });
  };
  for (const u of order) {
    if (kernel && u.name === "linux-image-amd64") get(mirror(u), NEW_KERNEL, "amd64", "6.1.123-1", 68_942 * KB);
    get(mirror(u), u.name, arch(u), u.new, u.size);
  }
  steps.push({
    ms: 400,
    stage: "Downloading",
    current: "",
    log: [`Fetched ${fmtSize(total)} in ${Math.max(2, Math.round(total / 21e6))}s (${(21 + Math.random() * 6).toFixed(1)} MB/s)`, "Reading changelogs...", "Preconfiguring packages ..."],
  });
  steps.push({ ms: 300, stage: "Installing", current: "Running dpkg", log: ["(Reading database ... 98412 files and directories currently installed.)"] });
  for (const u of order) {
    if (kernel && u.name === "linux-image-amd64") {
      steps.push({
        ms: 1800,
        stage: "Installing",
        current: NEW_KERNEL,
        log: [
          "Selecting previously unselected package linux-image-6.1.0-29-amd64.",
          "Preparing to unpack .../linux-image-6.1.0-29-amd64_6.1.123-1_amd64.deb ...",
          "Unpacking linux-image-6.1.0-29-amd64 (6.1.123-1) ...",
        ],
      });
    }
    steps.push({
      ms: 250 + Math.min(500, u.size / 40_000),
      stage: "Installing",
      current: u.name,
      log: [`Preparing to unpack .../${debName(u)} ...`, `Unpacking ${u.name} (${u.new}) over (${u.current}) ...`],
    });
  }
  for (const u of order) {
    const extra: string[] = [];
    if (u.name === "openssh-server") extra.push("Configuration file '/etc/ssh/sshd_config' modified, keeping your version.", "Installing new version as /etc/ssh/sshd_config.dpkg-dist");
    if (u.name === "tzdata") extra.push("", "Current default time zone: 'Europe/Berlin'", `Local time is now:      ${new Date().toString().slice(0, 24)} CET.`, `Universal Time is now:  ${new Date().toUTCString().slice(0, 25)} UTC.`);
    if (u.name === "locales") extra.push("Generating locales (this might take a while)...", "  en_US.UTF-8... done", "Generation complete.");
    steps.push({
      ms: u.name === "locales" ? 1400 : u.name === "docker-ce" ? 1200 : 200 + Math.random() * 200,
      stage: "Installing",
      current: u.name,
      log: [`Setting up ${u.name} (${u.new}) ...`, ...extra],
    });
  }
  if (kernel) {
    steps.push({
      ms: 900,
      stage: "Installing",
      current: NEW_KERNEL,
      log: [
        "Setting up linux-image-6.1.0-29-amd64 (6.1.123-1) ...",
        "I: /vmlinuz is now a symlink to boot/vmlinuz-6.1.0-29-amd64",
        "I: /initrd.img is now a symlink to boot/initrd.img-6.1.0-29-amd64",
        "/etc/kernel/postinst.d/initramfs-tools:",
        "update-initramfs: Generating /boot/initrd.img-6.1.0-29-amd64",
      ],
    });
    steps.push({ ms: 2600, stage: "Post-install hooks", current: "initramfs-tools", log: ["/etc/kernel/postinst.d/zz-update-grub:", "Generating grub configuration file ..."] });
    steps.push({
      ms: 1200,
      stage: "Post-install hooks",
      current: "grub",
      log: [
        "Found linux image: /boot/vmlinuz-6.1.0-29-amd64",
        "Found initrd image: /boot/initrd.img-6.1.0-29-amd64",
        "Found linux image: /boot/vmlinuz-6.1.0-28-amd64",
        "Found initrd image: /boot/initrd.img-6.1.0-28-amd64",
        "Warning: os-prober will not be executed to detect other bootable partitions.",
        "Adding boot menu entry for UEFI Firmware Settings ...",
        "done",
      ],
    });
  }
  const triggers = ["man-db (2.11.2-2)", "dbus (1.14.10-1~deb12u1)", "mailcap (3.70+nmu1)", `libc-bin (${order.find((u) => u.name === "libc-bin")?.new ?? "2.36-9+deb12u9"})`];
  for (const t of triggers) {
    steps.push({ ms: t.startsWith("man-db") ? 1400 : 350, stage: "Post-install hooks", current: t.split(" ")[0], log: [`Processing triggers for ${t} ...`] });
  }
  if (kernel) steps.push({ ms: 300, stage: "Post-install hooks", current: "needrestart", log: ["", "Pending kernel upgrade!", "Running kernel version:", "  6.1.0-28-amd64", "Diagnostics:", "  The currently running kernel version is not the expected kernel version 6.1.0-29-amd64."] });
  return steps;
}

// ---- runs ----

function finishRun(rec: RunRecord, ups: PkgUpdate[], lines: string[]) {
  const kernel = ups.some((u) => u.name === "linux-image-amd64");
  const end = Date.now();
  const done: RunRecord = {
    ...rec,
    finishedAt: new Date(end).toISOString(),
    ok: true,
    exitStatus: 0,
    upgraded: ups.length + (kernel ? 1 : 0),
    stage: "Done",
    rebootRequired: kernel,
    ...(kernel ? { rebootReason: `Updated packages require a reboot: ${NEW_KERNEL}` } : {}),
    ...(ups.some((u) => u.name === "openssh-server")
      ? { configFiles: [{ path: "/etc/ssh/sshd_config.dpkg-dist", modTime: new Date(end - 8_000).toISOString(), new: true }] }
      : {}),
  };
  state.update((s) => {
    s.pending = [];
    // Show the list as freshly emptied (same time as the run, so the result stays visible).
    s.lastCheck = checkResult([], end, 0);
    s.lastRun = done;
    s.reboot = done.rebootRequired ? { required: true, reason: done.rebootReason } : s.reboot;
    s.logs = { [done.id]: [...lines, "Wargo: upgrade finished with exit status 0"] };
    delete s.inflight;
  });
  recordAudit({ action: "pkg.upgrade.finished", target: done.unit, result: "ok", system: true });
}

// A run interrupted by a page reload finishes "in the background".
{
  const s = state.get();
  if (s.inflight) {
    const ups = s.pending;
    finishRun(s.inflight, ups, plan(ups, ups.some((u) => u.name === "linux-image-amd64")).flatMap((x) => x.log ?? []));
  }
}

function precheck(): Precheck {
  return { lock: { locked: false }, disk: [{ path: "/", free: 686 * GB, low: false }] };
}

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  api.get("/api/pkg/backend", () => json(status())),
  api.get("/api/pkg/updates", () => json(state.get().lastCheck ?? null)),
  api.get("/api/pkg/precheck", () => json(precheck())),

  api.post("/api/pkg/check", async ({ request }) => {
    if (running) return fail(409, "upgrade_running", "An upgrade is in progress");
    if (checking) return fail(409, "check_running", "A check is already running");
    const denied = requireApproval(request, "pkg.refresh", "Refresh package databases");
    if (denied) return denied;
    checking = true;
    refresh("pkg");
    recordAudit({
      action: "pkg.refresh",
      target: "Refresh package databases",
      elevation: "sudo",
      details: { argv: [WARGO, "helper", "pkg-run", "--", "apt-get", "-q", "update"] },
    });
    const ms = 2200 + Math.random() * 1800;
    await delay(ms);
    const res = checkResult(state.get().pending, Date.now(), Math.round(ms * 0.6));
    state.update((s) => {
      s.lastCheck = res;
    });
    checking = false;
    refresh("pkg");
    return json(res);
  }),

  api.post("/api/pkg/upgrade", ({ request }) => {
    if (running || checking) return fail(409, "upgrade_running", "An upgrade or check is already in progress");
    const ups = state.get().pending;
    const target = `Install ${state.get().lastCheck?.updates.length ?? 0} updates with apt`;
    const denied = requireApproval(request, "pkg.upgrade", target);
    if (denied) return denied;

    const start = Date.now();
    const id = runId(start);
    const rec = record(id, ups.length, start, undefined);
    const kernel = ups.some((u) => u.name === "linux-image-amd64");
    const steps: JobStep[] = ups.length
      ? plan(ups, kernel).map((s) => ({ ...s, ms: s.ms * 0.75 }))
      : [
          { ms: 600, stage: "Preparing", current: "Reading package lists", log: ["Reading package lists...", "Building dependency tree...", "Reading state information...", "Calculating upgrade..."] },
          { ms: 300, stage: "Done", current: "Nothing to do", log: ["0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded."] },
        ];
    const lines = steps.flatMap((s) => s.log ?? []);
    const job = startJob({
      kind: "pkg.upgrade",
      title: "System upgrade (apt)",
      steps: [{ ms: 400, stage: "Preparing", current: "Starting" }, ...steps],
      onDone: () => {
        running = null;
        finishRun(rec, ups, lines);
        refresh("pkg");
      },
    });
    rec.jobId = job.id;
    running = { job, rec };
    state.update((s) => {
      s.inflight = rec;
    });
    recordAudit({
      action: "pkg.upgrade",
      target,
      elevation: "sudo",
      details: { argv: ["systemd-run", `--unit=${rec.unit}`, "--collect", "--", WARGO, "helper", "pkg-run", "--", ...UPGRADE_ARGV] },
    });
    refresh("pkg");
    return json(job);
  }),

  api.get("/api/pkg/log", ({ request }) => {
    const s = state.get();
    const rec = running?.rec ?? s.lastRun;
    const id = new URL(request.url).searchParams.get("id");
    if (!rec || (id && id !== rec.id)) return notFound();
    const lines = s.logs[rec.id];
    return lines ? json({ id: rec.id, lines }) : notFound();
  }),

  api.post("/api/system/power", async ({ request }) => {
    const { action } = await body<{ action?: string }>(request.clone());
    if (action !== "reboot" && action !== "poweroff") return badRequest("action must be reboot or poweroff");
    if (running) return fail(409, "upgrade_running", "An upgrade is in progress; wait for it to finish");
    const target = action === "reboot" ? "Reboot the server" : "Power off the server";
    const denied = requireApproval(request, "system.power", target);
    if (denied) return denied;
    const msg = action === "reboot" ? "Rebooting isn't available in the demo. The server keeps running." : "Powering off isn't available in the demo.";
    recordAudit({ action: "system.power", target, elevation: "sudo", result: "error: " + msg, details: { argv: [WARGO, "helper", "power", action] } });
    return fail(501, "not_in_demo", msg);
  }),
];

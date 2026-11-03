// SPDX-License-Identifier: AGPL-3.0-only
// Settings endpoints not owned elsewhere: the audit log (GET /api/audit).
// Appearance is client-side; security, integrations, launcher and about
// live in auth.ts, integrations.ts, launcher.ts and system.ts.
import type { RequestHandler, WebSocketHandler } from "msw";
import { api, json, query } from "../core/http";
import { persisted } from "../core/store";
import { DAY, HOUR, MIN, pick, seeded } from "../core/rand";
import { CONTAINERS, HOME } from "./host";

/** One audit row, as internal/audit Entry. */
export interface AuditEntry {
  id: number;
  ts: string;
  ip: string;
  ua: string;
  action: string;
  target: string;
  result: string;
  elevation?: string;
  details?: unknown;
}

// Who usually signs in (matches the sessions in auth.ts).
const LAPTOP_IP = "192.168.1.42";
const PHONE_IP = "100.101.42.7";
const LAPTOP_UA = "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0";
const PHONE_UA =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1";
const BOT_UAS = [
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
  "python-requests/2.31.0",
  "Go-http-client/1.1",
  "curl/8.5.0",
];
// Documentation-range addresses (RFC 5737), never real hosts.
const BOT_IPS = ["198.51.100.23", "198.51.100.187", "203.0.113.77", "192.0.2.144", "203.0.113.9"];
const BAD_LOGIN = "error: Invalid username or password";
const WARGO = "/usr/local/bin/wargo";

type Draft = Omit<AuditEntry, "id" | "ts"> & { t: number };

/** About 150 entries over the last two weeks, oldest first. */
function generate(): AuditEntry[] {
  const r = seeded(4207);
  const out: Draft[] = [];
  const now = Date.now();
  const add = (t: number, e: Omit<Draft, "t">) => out.push({ t, ...e });
  const laptop = { ip: LAPTOP_IP, ua: LAPTOP_UA };
  const phone = { ip: PHONE_IP, ua: PHONE_UA };
  const ok = "ok";

  const signIn = (t: number, who: typeof laptop) => {
    if (who === phone) {
      add(t, { ...who, action: "auth.passkey_login", target: "homelab.lan", result: ok });
    } else {
      add(t, { ...who, action: "auth.password", target: "demo", result: ok });
      add(t + 9_000 + r() * 12_000, { ...who, action: "auth.second_factor", target: "totp", result: ok });
    }
  };

  const running = CONTAINERS.filter((c) => c.state === "running").map((c) => c.name);
  const torrents = [
    "debian-12.8.0-amd64-netinst.iso",
    "ubuntu-24.04.1-live-server-amd64.iso",
    "archlinux-2024.12.01-x86_64.iso",
    "Fedora-Server-dvd-x86_64-41-1.4.iso",
    "proxmox-ve_8.3-1.iso",
    "Big Buck Bunny (2008) [1080p]",
    "Sintel (2010) [2160p]",
    "kali-linux-2024.4-installer-amd64.iso",
  ];
  const files = [
    { action: "files.upload", target: `${HOME}/Documents/taxes-2024.pdf` },
    { action: "files.mkdir", target: "/DATA/media/movies/Sintel (2010)" },
    { action: "files.move", target: "/DATA/downloads/complete/Sintel (2010) [2160p]" },
    { action: "files.write", target: `${HOME}/stacks/immich/.env` },
    { action: "files.write", target: `${HOME}/stacks/media/compose.yaml` },
    { action: "files.trash", target: "/DATA/downloads/complete/old-sample.mkv" },
    { action: "files.rename", target: `${HOME}/Pictures/IMG_2041.HEIC` },
    { action: "files.copy", target: "/mnt/backup/photos/2024" },
    { action: "files.archive", target: `${HOME}/stacks.tar.gz` },
    { action: "files.extract", target: `${HOME}/Downloads/hass-backup.tar` },
    { action: "files.delete", target: "/DATA/downloads/incomplete/ubuntu-24.04.1.part" },
    { action: "files.create", target: `${HOME}/notes/todo.md` },
    { action: "files.chmod", target: `${HOME}/scripts/backup.sh` },
  ];
  const tiles = ["Jellyfin", "Immich", "Home Assistant", "Grafana", "Paperless", "Uptime Kuma", "Router"];

  for (let d = 13; d >= 0; d--) {
    const day = now - d * DAY;
    // Midnight-ish base for this day, then hour offsets.
    const base = day - (day % DAY) - 1 * HOUR;
    const at = (h: number) => {
      const t = base + h * HOUR + r() * 40 * MIN;
      return Math.min(t, now - (5 + r() * 20) * MIN);
    };

    // Evening session on the laptop most days.
    if (r() < 0.85) {
      let t = at(18 + r() * 3);
      signIn(t, laptop);
      t += 2 * MIN;
      const n = 4 + Math.floor(r() * 7);
      for (let i = 0; i < n; i++) {
        t += (1 + r() * 9) * MIN;
        const k = r();
        if (k < 0.3) {
          const name = pick(running, r);
          add(t, { ...laptop, action: "containers." + pick(["restart", "restart", "stop", "start"], r), target: name, result: ok, details: { id: hex(r, 64) } });
        } else if (k < 0.55) {
          const f = pick(files, r);
          add(t, { ...laptop, ...f, result: r() < 0.06 ? "error: Permission denied" : ok });
        } else if (k < 0.7) {
          const tgt = r() < 0.65 ? "host" : "container:" + pick(running, r);
          const id = hex(r, 12);
          add(t, { ...laptop, action: "term.open", target: tgt, result: ok, details: { id, kind: tgt === "host" ? "host" : "container", tmux: tgt === "host" && r() < 0.5 } });
          t += (2 + r() * 14) * MIN;
          add(t, { ...laptop, action: "term.close", target: tgt, result: ok, details: { id, kind: tgt === "host" ? "host" : "container" } });
        } else if (k < 0.82) {
          const name = pick(torrents, r);
          add(t, { ...laptop, action: "transmission.add", target: name, result: ok, details: { id: 1 + Math.floor(r() * 90), name, duplicate: false } });
        } else if (k < 0.92) {
          const tile = pick(tiles, r);
          const a = pick(["launcher.tile_update", "launcher.tile_update", "launcher.tile_create", "launcher.tile_reorder"], r);
          add(t, {
            ...laptop,
            action: a,
            target: a === "launcher.tile_reorder" ? "" : tile,
            result: ok,
            details: a === "launcher.tile_reorder" ? undefined : { url: `http://192.168.1.20:${8000 + Math.floor(r() * 999)}` },
          });
        } else {
          const pid = 2000 + Math.floor(r() * 60000);
          add(t, { ...laptop, action: "procs.signal", target: `ffmpeg (${pid})`, result: ok, elevation: "sudo", details: { pid, signal: "SIGTERM" } });
        }
      }
    }

    // Quick look from the phone over Tailscale.
    if (r() < 0.6) {
      const t = at(7 + r() * 5);
      signIn(t, phone);
      if (r() < 0.5) add(t + 3 * MIN, { ...phone, action: "transmission.add", target: pick(torrents, r), result: ok });
      if (r() < 0.3) add(t + 5 * MIN, { ...phone, action: "containers.restart", target: pick(running, r), result: ok, details: { id: hex(r, 64) } });
    }

    // Password guessing from the internet now and then.
    if (r() < 0.45) {
      const ip = pick(BOT_IPS, r);
      const ua = pick(BOT_UAS, r);
      let t = at(r() * 22);
      const n = 1 + Math.floor(r() * 4);
      for (let i = 0; i < n; i++) {
        t += (5 + r() * 40) * 1000;
        add(t, { ip, ua, action: "auth.password", target: pick(["admin", "root", "admin", "demo", "user", "pi"], r), result: BAD_LOGIN });
      }
    }
  }

  // A few notable one-offs.
  const plain = (daysAgo: number, h: number) => now - daysAgo * DAY + h * HOUR;
  add(plain(12, 2.1), { ...laptop, action: "auth.password", target: "demo", result: BAD_LOGIN });
  add(plain(11, 1), { ...laptop, action: "pkg.refresh", target: "Refresh package databases", result: ok, elevation: "sudo", details: { argv: [WARGO, "helper", "pkg-run", "--", "apt-get", "-q", "update"] } });
  const runId = "upgrade-prev";
  add(plain(11, 1.05), {
    ...laptop,
    action: "pkg.upgrade",
    target: "Install 19 updates with apt",
    result: ok,
    elevation: "sudo",
    details: { argv: ["systemd-run", `--unit=wargo-${runId}`, "--collect", "--", WARGO, "helper", "pkg-run", "--", "apt-get", "-y", "-q", "full-upgrade"] },
  });
  add(plain(11, 1.12), { ip: "local", ua: "", action: "pkg.upgrade.finished", target: `wargo-${runId}`, result: ok });
  for (const [name, days] of [["sonarr", 9], ["vaultwarden", 6], ["uptime-kuma", 6], ["home-assistant", 3]] as const) {
    add(plain(days, 0.5), { ...laptop, action: "containers.update", target: name, result: ok, details: { id: hex(r, 64), strategy: "compose", job: "j" + hex(r, 6) } });
  }
  add(plain(8, 0.3), { ...laptop, action: "integrations.transmission.update", target: "http://192.168.1.20:9091/transmission/rpc", result: ok, details: { username: "demo", passwordChanged: true } });
  add(plain(8, 0.32), { ...laptop, action: "integrations.prowlarr.import_key", target: "prowlarr", result: ok, details: { container: "prowlarr" } });
  add(plain(5, 0.2), { ...laptop, action: "launcher.settings", target: "sidebar", result: ok, details: { enabled: true } });
  add(plain(4, 0.1), { ...laptop, action: "launcher.icon_fetch", target: "paperless-ngx", result: ok });
  add(plain(2, 0.4), { ...laptop, action: "auth.revoke_sessions", target: "2", result: ok });
  add(plain(2, 0.1), { ...laptop, action: "auth.recovery_regenerate", target: "", result: ok });
  add(plain(1, 0.2), { ...laptop, action: "containers.update", target: "pihole", result: "error: pull pihole/pihole:2024.07.0: toomanyrequests: You have reached your pull rate limit", details: { id: hex(r, 64), strategy: "recreate" } });

  out.sort((a, b) => a.t - b.t);
  return out
    .filter((e) => e.t < now)
    .map(({ t, ...e }, i) => ({ id: 1000 + i * 3, ts: new Date(Math.floor(t / 1000) * 1000).toISOString(), ...e }));
}

function hex(r: () => number, n: number) {
  let s = "";
  for (let i = 0; i < n; i++) s += Math.floor(r() * 16).toString(16);
  return s;
}

const log = persisted<AuditEntry[]>("audit", generate, 2);

/** Adds an entry for something the visitor did (other demo modules may call this). */
export function recordAudit(e: { action: string; target: string; result?: string; elevation?: string; details?: unknown; system?: boolean }) {
  log.update((l) => {
    const last = l[l.length - 1];
    l.push({
      id: (last?.id ?? 1000) + 1,
      ts: new Date().toISOString(),
      ip: e.system ? "local" : LAPTOP_IP,
      ua: e.system ? "" : navigator.userAgent,
      action: e.action,
      target: e.target,
      result: e.result ?? "ok",
      ...(e.elevation ? { elevation: e.elevation } : {}),
      ...(e.details !== undefined ? { details: e.details } : {}),
    });
  });
}

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  api.get("/api/audit", ({ request }) => {
    const q = query(request);
    const limit = Math.min(Math.max(Number(q.get("limit")) || 100, 1), 500);
    const before = Number(q.get("before")) || 0;
    const prefix = q.get("prefix") ?? "";
    const rows: AuditEntry[] = [];
    const all = log.get();
    for (let i = all.length - 1; i >= 0 && rows.length < limit; i--) {
      if (before > 0 && all[i].id >= before) continue;
      if (prefix && !all[i].action.startsWith(prefix)) continue;
      rows.push(all[i]);
    }
    return json(rows);
  }),
];

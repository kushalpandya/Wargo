// SPDX-License-Identifier: AGPL-3.0-only
// Notifications (the bell): Active is built from the demo's own torrents and
// Plex sessions as they are right now; Recent starts with a few made-up
// problems and finds. Clearing and the per-category settings are kept in this
// browser. Push needs a real server, so the demo says it isn't available.
import type { RequestHandler } from "msw";
import type { Active, Category, Item, Mode, Snapshot } from "@/features/notifications/api";
import { api, body, json, notInDemo } from "../core/http";
import { refresh, topic } from "../core/bus";
import { persisted } from "../core/store";
import { HOUR, MIN } from "../core/rand";
import { CONTAINERS } from "./host";
import { currentTorrents } from "./torrents";
import { currentSessions } from "./plex";

const DEFAULTS: Record<Category, Mode> = {
  "download.started": "push",
  "download.finished": "push",
  "stream.started": "push",
  "container.unhealthy": "push",
  "container.failed": "push",
  "updates.found": "push",
  "download.error": "push",
  "disk.full": "push",
};

interface State {
  /** When each seeded item was cleared (ms), by id. */
  cleared: Record<string, number>;
  settings: Record<Category, Mode>;
  /** When the demo first ran in this browser, so seeded times stay put. */
  epoch: number;
}

const state = persisted<State>("notifications", () => ({ cleared: {}, settings: { ...DEFAULTS }, epoch: Date.now() }));

const iso = (ms: number) => new Date(ms).toISOString();

function seeded(): Item[] {
  const t0 = state.get().epoch;
  const item = (id: string, category: Category, title: string, body: string, link: string, ago: number): Item => ({
    id,
    category,
    key: id,
    title,
    body,
    link,
    createdAt: iso(t0 - ago),
    resolvedAt: null,
    clearedAt: null,
  });
  const updates = CONTAINERS.filter((c) => c.update).length;
  const errored = currentTorrents().find((t) => t.error === 2 || t.error === 3);
  return [
    item("n-unhealthy", "container.unhealthy", "jellyfin is unhealthy", "", "/containers/jellyfin", 95 * MIN),
    item("n-failed", "container.failed", "nextcloud-cron stopped with an error", "", "/containers/nextcloud-cron", 3 * HOUR),
    item("n-updates", "updates.found", `${updates + 6} updates available`, `Containers ${updates} · System 6`, "/updates", 2 * HOUR + 14 * MIN),
    ...(errored
      ? [item("n-dlerror", "download.error", "Download error", `${errored.name}: ${errored.errorString}`, "/torrents?tab=downloads", 6 * HOUR)]
      : []),
    item("n-finished", "download.finished", "Download finished", "archlinux-2024.11.01-x86_64.iso", "/torrents?tab=downloads", 2 * 24 * HOUR),
  ];
}

const pct = (n: number) => `${Math.floor(Math.min(1, Math.max(0, n)) * 100)}%`;

function rate(bps: number) {
  const units = ["B/s", "KB/s", "MB/s", "GB/s"];
  let i = 0;
  while (bps >= 1000 && i < units.length - 1) {
    bps /= 1000;
    i++;
  }
  return `${bps >= 10 || i === 0 ? Math.round(bps) : bps.toFixed(1)} ${units[i]}`;
}

function eta(s: number) {
  if (s < 60) return `${s}s left`;
  if (s < 3600) return `${Math.round(s / 60)}m left`;
  return `${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m left`;
}

function active(): Active[] {
  const { settings } = state.get();
  const rows: Active[] = [];
  if (settings["download.started"] !== "off") {
    for (const t of currentTorrents()) {
      if (t.status !== "downloading" || t.error === 2 || t.error === 3) continue;
      const parts = [pct(t.percentDone)];
      if (t.rateDownload > 0) parts.push(rate(t.rateDownload));
      if (t.eta > 0) parts.push(eta(t.eta));
      rows.push({
        id: `dl:${t.hash}`,
        category: "download.started",
        title: t.name,
        detail: parts.join(" · "),
        progress: t.percentDone,
        startedAt: iso(t.addedDate * 1000),
        link: "/torrents?tab=downloads",
      });
    }
  }
  if (settings["stream.started"] !== "off") {
    currentSessions().forEach((p, i) => {
      const what = p.type === "episode" ? `${p.grandparentTitle} S${p.parentIndex} E${p.index}` : `${p.title} (${p.year})`;
      rows.push({
        id: `st:${i}`,
        category: "stream.started",
        title: `${p.user} is watching ${what}`,
        detail: [p.player, p.state[0].toUpperCase() + p.state.slice(1)].join(" · "),
        progress: p.duration ? p.viewOffset / p.duration : 0,
        startedAt: iso(Date.now() - p.viewOffset),
        link: "/",
      });
    });
  }
  return rows;
}

function snapshot(): Snapshot {
  const s = state.get();
  const recent = seeded()
    .filter((i) => !s.cleared[i.id] && s.settings[i.category] !== "off")
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { active: active(), recent, badge: recent.length, settings: s.settings };
}

topic("notifications", { snapshot, tick: snapshot, every: 3000 });

export const handlers: RequestHandler[] = [
  api.get("/api/notifications", () => json(snapshot())),
  api.post("/api/notifications/clear", async ({ request }) => {
    const b = await body<{ id?: string; all?: boolean }>(request);
    const now = Date.now();
    state.update((s) => {
      for (const i of seeded()) if (b.all || i.id === b.id) s.cleared[i.id] = now;
    });
    refresh("notifications");
    return json(snapshot());
  }),
  api.put("/api/notifications/settings", async ({ request }) => {
    const next = await body<Partial<Record<Category, Mode>>>(request);
    state.update((s) => {
      for (const k of Object.keys(DEFAULTS) as Category[]) {
        const m = next[k];
        if (m === "off" || m === "app" || m === "push") s.settings[k] = m;
      }
    });
    refresh("notifications");
    return json(state.get().settings);
  }),
  api.get("/api/notifications/push", () => notInDemo("Push to your phone")),
  api.post("/api/notifications/push", () => notInDemo("Push to your phone")),
  api.del("/api/notifications/push", () => notInDemo("Push to your phone")),
];

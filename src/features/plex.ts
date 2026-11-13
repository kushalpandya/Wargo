// SPDX-License-Identifier: AGPL-3.0-only
// Plex (Settings → Integrations, Home feed): who is watching, what was
// watched and what was added, on a made-up server. Posters are drawn here.
import type { RequestHandler } from "msw";
import { HttpResponse } from "msw";
import { api, fail, query } from "../core/http";
import { json } from "../core/http";

const MIN = 60;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const now = () => Math.floor(Date.now() / 1000);

/** Poster colours per library item (rating key). */
const POSTERS: Record<number, [string, string, string]> = {
  101: ["Severance", "#2b4f6e", "#9cc3d9"],
  102: ["The Bear", "#6e3b2b", "#e7b08c"],
  103: ["Slow Horses", "#3e4a2e", "#c2cf9a"],
  104: ["Andor", "#4a2e2e", "#d9a38c"],
  105: ["Dune: Part Two", "#6b4a1f", "#f0c987"],
  106: ["Past Lives", "#3b3360", "#c7bdf0"],
  107: ["Silo", "#2e3a40", "#a9c2cc"],
  108: ["Perfect Days", "#2f5a4e", "#a8dccb"],
  109: ["Arrival", "#33363d", "#c9ccd6"],
  110: ["Blue Eye Samurai", "#23304f", "#8fa6e0"],
};
const thumb = (key: number) => `/library/metadata/${key}/thumb/1730000000`;

/** Plays in progress, moving forward in real time. */
function sessions() {
  const t = Date.now();
  const ep = 52 * 60_000;
  const movie = 166 * 60_000;
  return [
    {
      type: "episode",
      title: "Woe's Hollow",
      grandparentTitle: "Severance",
      parentIndex: 1,
      index: 7,
      viewOffset: (t / 3) % ep,
      duration: ep,
      user: "alex",
      player: "Living Room TV",
      product: "Plex for Android TV",
      state: "playing",
      local: true,
      decision: "transcode",
      thumb: thumb(101),
    },
    {
      type: "movie",
      title: "Dune: Part Two",
      year: 2024,
      viewOffset: 71 * 60_000,
      duration: movie,
      user: "sam",
      player: "Sam's iPhone",
      product: "Plex for iOS",
      state: "paused",
      local: false,
      decision: "directplay",
      thumb: thumb(105),
    },
  ];
}

function history() {
  const n = now();
  const item = (key: string, o: object, ago: number) => ({ key, at: n - ago, ...o });
  return [
    item("h9", { type: "episode", title: "Half Loop", grandparentTitle: "Severance", parentIndex: 1, index: 3, user: "alex" }, 3 * HOUR),
    item("h8", { type: "episode", title: "In Perpetuity", grandparentTitle: "Severance", parentIndex: 1, index: 2, user: "alex" }, 4 * HOUR),
    item("h7", { type: "episode", title: "Good News About Hell", grandparentTitle: "Severance", parentIndex: 1, index: 1, user: "alex" }, 5 * HOUR),
    item("h6", { type: "movie", title: "Perfect Days", year: 2023, user: "demo", thumb: thumb(108) }, 1 * DAY + 2 * HOUR),
    item("h5", { type: "episode", title: "Fishes", grandparentTitle: "The Bear", parentIndex: 2, index: 6, user: "sam" }, 2 * DAY),
    item("h4", { type: "movie", title: "Arrival", year: 2016, user: "demo", thumb: thumb(109) }, 3 * DAY),
    item("h3", { type: "episode", title: "Failure's Bastards", grandparentTitle: "Slow Horses", parentIndex: 1, index: 1, user: "demo" }, 4 * DAY),
    item("h2", { type: "episode", title: "Kassa", grandparentTitle: "Andor", parentIndex: 1, index: 1, user: "sam" }, 5 * DAY),
  ];
}

function recent() {
  const n = now();
  return [
    { key: "r1", type: "season", title: "Season 2", parentTitle: "Blue Eye Samurai", leafCount: 8, at: n - 6 * HOUR, thumb: thumb(110) },
    { key: "r2", type: "movie", title: "Past Lives", year: 2023, at: n - 1 * DAY, thumb: thumb(106) },
    { key: "r3", type: "episode", title: "The Getaway", grandparentTitle: "Silo", parentIndex: 2, index: 4, at: n - 2 * DAY, thumb: thumb(107) },
    { key: "r4", type: "season", title: "Season 4", parentTitle: "Slow Horses", leafCount: 6, at: n - 3 * DAY, thumb: thumb(103) },
    { key: "r5", type: "movie", title: "Dune: Part Two", year: 2024, at: n - 6 * DAY, thumb: thumb(105) },
  ];
}

const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]!);

/** A simple poster: the title on a two-tone gradient. */
function poster(key: number): string {
  const [title, a, b] = POSTERS[key] ?? ["", "#333", "#999"];
  const words = title.split(" ");
  const lines = words.length > 2 ? [words.slice(0, Math.ceil(words.length / 2)).join(" "), words.slice(Math.ceil(words.length / 2)).join(" ")] : [title];
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 300"><defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${a}"/><stop offset="1" stop-color="${b}"/></linearGradient></defs><rect width="200" height="300" fill="url(#g)"/><circle cx="150" cy="70" r="46" fill="#fff" opacity="0.12"/>${lines
    .map((l, i) => `<text x="100" y="${220 + i * 30}" text-anchor="middle" font-family="sans-serif" font-weight="700" font-size="24" fill="#fff">${esc(l)}</text>`)
    .join("")}</svg>`;
}

/** Plays in progress for the notifications panel (none when Plex isn't set up). */
export function currentSessions() {
  return connected() ? sessions() : [];
}

/** Whether the demo's Plex connection is set up (Settings → Integrations). */
let connected: () => boolean = () => true;
export function setPlexConnected(fn: () => boolean) {
  connected = fn;
}

export const handlers: RequestHandler[] = [
  api.get("/api/plex/activity", () => {
    if (!connected()) return fail(409, "not_configured", "Plex isn't set up");
    return json({ sessions: sessions(), history: history(), recent: recent(), updatedAt: new Date().toISOString() });
  }),
  api.get("/api/plex/thumb", ({ request }) => {
    const m = /^\/library\/metadata\/(\d+)\/(thumb|art)(\/\d+)?$/.exec(query(request).get("path") ?? "");
    if (!m) return fail(400, "bad_request", "invalid path");
    return new HttpResponse(poster(Number(m[1])), { headers: { "Content-Type": "image/svg+xml", "Cache-Control": "private, max-age=86400" } });
  }),
];

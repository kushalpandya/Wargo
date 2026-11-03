// SPDX-License-Identifier: AGPL-3.0-only
// The app launcher: tiles on Home and the Apps page, their icons (bundled
// dashboard-icons or uploads kept in this browser) and "Add from containers".
import type { RequestHandler, WebSocketHandler } from "msw";
import type { Tile } from "@/features/launcher/Tiles";
import type { LauncherSettings, Suggestion } from "@/features/launcher/api";
import { api, badRequest, body, fail, json, notFound } from "../core/http";
import { persisted } from "../core/store";
import { setLauncherCapability } from "./auth";
import { CONTAINERS } from "./host";
import { ICONS, iconResponse } from "./icons";

/** The hostname tiles and suggestions use for the demo server. */
const HOSTNAME = "homelab.local";
const MAX_ICON = 512 * 1024;

// --- icons ---

/** A stable fake sha256 (hex) for a string, so names look like the server's. */
function fakeSha(s: string): string {
  let out = "";
  let h = 0x811c9dc5;
  for (let round = 0; out.length < 64; round++) {
    for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 0x01000193) >>> 0;
    h = Math.imul(h ^ round, 0x01000193) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return out.slice(0, 64);
}

/** Bundled icons: content-addressed file name -> slug. */
const BUNDLED = new Map<string, string>();
const bundledName = (slug: string) => `${fakeSha("dashboard-icons/" + slug)}.svg`;
for (const slug of Object.keys(ICONS)) BUNDLED.set(bundledName(slug), slug);

interface StoredIcon {
  type: string;
  /** base64 */
  data: string;
}
const uploads = persisted<Record<string, StoredIcon>>("launcher.icons", () => ({}));

const ICON_NAME = /^[a-f0-9]{64}\.(png|svg|webp|jpg)$/;
const MIME: Record<string, string> = { png: "image/png", svg: "image/svg+xml", webp: "image/webp", jpg: "image/jpeg" };

function sniff(b: Uint8Array): string {
  if (b.length >= 8 && b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) return "png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "jpg";
  const ascii = (from: number, to: number) => String.fromCharCode(...b.slice(from, to));
  if (b.length >= 12 && ascii(0, 4) === "RIFF" && ascii(8, 12) === "WEBP") return "webp";
  const head = new TextDecoder().decode(b.slice(0, 1024)).trimStart().toLowerCase();
  if ((head.startsWith("<?xml") || head.startsWith("<svg") || head.startsWith("<!--")) && head.includes("<svg")) return "svg";
  return "";
}

async function sha256(b: Uint8Array<ArrayBuffer>): Promise<string> {
  if (crypto?.subtle) {
    const d = new Uint8Array(await crypto.subtle.digest("SHA-256", b));
    return [...d].map((x) => x.toString(16).padStart(2, "0")).join("");
  }
  let s = "";
  for (let i = 0; i < b.length; i += 97) s += String.fromCharCode(b[i]);
  return fakeSha(s + b.length);
}

function toBase64(b: Uint8Array): string {
  let s = "";
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
  return btoa(s);
}

function fromBase64(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const iconExists = (name: string) => BUNDLED.has(name) || !!uploads.get()[name];

// --- settings and tiles ---

const settings = persisted<LauncherSettings>("launcher.settings", () => ({ enabled: true, placement: "overview" }));
setLauncherCapability(() => ({ ...settings.get() }));

type StoredTile = Omit<Tile, "status">;

const url = (port: number, path = "") => `http://${HOSTNAME}:${port}${path}`;

function defaultTiles(): StoredTile[] {
  const t = (name: string, u: string, icon: string | undefined, group: string, health = true): StoredTile => ({
    id: fakeSha("tile/" + name).slice(0, 16),
    name,
    url: u,
    ...(icon ? { icon: bundledName(icon) } : {}),
    group,
    sort: 0,
    newTab: true,
    ...(health ? { healthUrl: u } : {}),
    enabled: true,
  });
  const list = [
    t("Jellyfin", url(8096), "jellyfin", "Media"),
    t("Immich", url(2283), "immich", "Media"),
    t("Nextcloud", url(8080), "nextcloud", "Home"),
    t("Home Assistant", url(8123), "home-assistant", "Home"),
    t("Pi-hole", url(8053), "pi-hole", "Home"),
    t("Router", "http://192.168.1.1", undefined, "Home", false),
    t("Vaultwarden", url(8222), "vaultwarden", "Tools"),
    t("Uptime Kuma", url(3001), "uptime-kuma", "Tools"),
    t("Paperless", url(8010), "paperless-ngx", "Tools"),
  ];
  return list.map((x, i) => ({ ...x, sort: i }));
}

const tiles = persisted<StoredTile[]>("launcher.tiles", defaultTiles);

/** Health check result: a tile is down when it points at a stopped container. */
function health(t: StoredTile): Tile["status"] {
  if (!t.healthUrl) return null;
  try {
    const u = new URL(t.healthUrl);
    const port = Number(u.port || (u.protocol === "https:" ? 443 : 80));
    const local = [HOSTNAME, "homelab", "localhost", "127.0.0.1", "192.168.1.20", "100.84.12.31"].includes(u.hostname);
    if (!local) return "up";
    const c = CONTAINERS.find((x) => x.port === port);
    return c && c.state === "running" ? "up" : "down";
  } catch {
    return "down";
  }
}

const sorted = (list: StoredTile[]) => [...list].sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name));

interface TileBody {
  name?: string;
  url?: string;
  icon?: string;
  group?: string;
  newTab?: boolean;
  healthUrl?: string;
  enabled?: boolean;
}

function validURL(raw: string | undefined, required: boolean): string | Error {
  const s = (raw ?? "").trim();
  if (!s) return required ? new Error("URL is required") : "";
  if (s.length > 2048) return new Error("URL is too long");
  try {
    const u = new URL(s);
    if ((u.protocol !== "http:" && u.protocol !== "https:") || !u.host) throw new Error();
    // Go keeps "http://host" without a trailing slash.
    return /^https?:\/\/[^/]+$/i.test(s) ? s : u.toString();
  } catch {
    return new Error("URL must start with http:// or https://");
  }
}

/** Mirrors the server's tileInput.validate. */
function validate(inp: TileBody): Omit<StoredTile, "id" | "sort"> | Response {
  const name = (inp.name ?? "").trim();
  if (!name || name.length > 64) return badRequest("name is required (max 64 characters)");
  const u = validURL(inp.url, true);
  if (u instanceof Error) return badRequest(u.message);
  const h = validURL(inp.healthUrl, false);
  if (h instanceof Error) return badRequest("health " + h.message);
  const group = (inp.group ?? "").trim();
  if (group.length > 48) return badRequest("group is too long");
  const icon = (inp.icon ?? "").trim();
  if (icon && !ICON_NAME.test(icon)) return badRequest("invalid icon");
  return {
    name,
    url: u,
    ...(icon ? { icon } : {}),
    ...(group ? { group } : {}),
    newTab: inp.newTab ?? true,
    ...(h ? { healthUrl: h } : {}),
    enabled: inp.enabled ?? true,
  };
}

const newTileId = () => [...crypto.getRandomValues(new Uint8Array(8))].map((x) => x.toString(16).padStart(2, "0")).join("");

// "big-bear_jellyfin" -> "Big Bear Jellyfin", like the server.
const prettify = (n: string) =>
  n
    .split(/[-_.]/)
    .filter(Boolean)
    .map((w) => w[0].toUpperCase() + w.slice(1))
    .join(" ");

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  api.get("/api/launcher", () => json(settings.get())),

  api.put("/api/launcher", async ({ request }) => {
    const v = await body<Partial<LauncherSettings>>(request);
    if (v.placement !== "page" && v.placement !== "overview") return badRequest('placement must be "overview" or "page"');
    return json(settings.set({ enabled: !!v.enabled, placement: v.placement }));
  }),

  api.get("/api/launcher/tiles", () => json(sorted(tiles.get()).map((t) => ({ ...t, status: health(t) }) satisfies Tile))),

  api.post("/api/launcher/tiles/reorder", async ({ request }) => {
    const { ids } = await body<{ ids?: string[] }>(request);
    if (!Array.isArray(ids)) return badRequest("invalid JSON body");
    if (ids.length > 1000) return badRequest("too many ids");
    tiles.update((list) => {
      ids.forEach((id, i) => {
        const t = list.find((x) => x.id === id);
        if (t) t.sort = i;
      });
    });
    return json({ ok: true });
  }),

  api.post("/api/launcher/tiles", async ({ request }) => {
    const v = validate(await body<TileBody>(request));
    if (v instanceof Response) return v;
    const list = tiles.get();
    if (list.length >= 500) return badRequest("too many tiles");
    const t: StoredTile = { id: newTileId(), ...v, sort: list.reduce((m, x) => Math.max(m, x.sort), 0) + 1 };
    tiles.set([...list, t]);
    return json({ ...t, status: null } satisfies Tile);
  }),

  api.put("/api/launcher/tiles/:id", async ({ request, params }) => {
    const v = validate(await body<TileBody>(request));
    if (v instanceof Response) return v;
    const cur = tiles.get().find((x) => x.id === params.id);
    if (!cur) return notFound();
    const t: StoredTile = { id: cur.id, ...v, sort: cur.sort };
    tiles.set((list) => list.map((x) => (x.id === cur.id ? t : x)));
    return json({ ...t, status: null } satisfies Tile);
  }),

  api.del("/api/launcher/tiles/:id", ({ params }) => {
    if (!tiles.get().some((x) => x.id === params.id)) return notFound();
    tiles.set((list) => list.filter((x) => x.id !== params.id));
    return json({ ok: true });
  }),

  api.post("/api/launcher/icons", async ({ request }) => {
    let file: FormDataEntryValue | null;
    try {
      file = (await request.formData()).get("file");
    } catch {
      return badRequest("expected multipart/form-data");
    }
    if (!file || typeof file === "string") return badRequest("missing file");
    const b = new Uint8Array(await file.arrayBuffer());
    if (b.length === 0) return badRequest("empty file");
    if (b.length > MAX_ICON) return badRequest("icon is larger than 512 KB");
    const ext = sniff(b);
    if (!ext) return badRequest("unsupported image type (use PNG, SVG, WebP or JPEG)");
    const name = `${await sha256(b)}.${ext}`;
    if (!iconExists(name))
      uploads.update((m) => {
        m[name] = { type: MIME[ext], data: toBase64(b) };
      });
    return json({ icon: name });
  }),

  api.post("/api/launcher/icons/fetch", async ({ request }) => {
    const { slug: raw } = await body<{ slug?: string }>(request);
    const slug = (raw ?? "").trim().toLowerCase();
    if (!/^[a-z0-9-]{1,64}$/.test(slug)) return badRequest("invalid icon name (lowercase letters, digits and dashes)");
    if (!ICONS[slug]) return fail(404, "icon_not_found", `No icon named "${slug}" in dashboard-icons`);
    return json({ icon: bundledName(slug) });
  }),

  api.get("/api/launcher/icons/:name", ({ params }) => {
    const name = String(params.name);
    const slug = BUNDLED.get(name);
    if (slug) return iconResponse(slug);
    const up = ICON_NAME.test(name) ? uploads.get()[name] : undefined;
    if (!up) return notFound();
    const headers: Record<string, string> = { "Content-Type": up.type, "X-Content-Type-Options": "nosniff", "Cache-Control": "private, max-age=31536000, immutable" };
    if (up.type === "image/svg+xml") headers["Content-Security-Policy"] = "default-src 'none'; style-src 'unsafe-inline'; sandbox";
    return new Response(fromBase64(up.data), { headers });
  }),

  api.get("/api/launcher/suggest", () => {
    const existing = new Set(tiles.get().map((t) => t.url));
    const out: Suggestion[] = CONTAINERS.filter((c) => c.state === "running" && c.port)
      .map((c) => {
        const u = url(c.port!);
        return {
          name: prettify(c.name),
          url: u,
          ...(c.icon ? { iconSlug: c.icon } : {}),
          group: "Containers",
          container: c.name,
          image: c.image,
          ports: [c.port!],
          exists: existing.has(u) || existing.has(u + "/"),
        };
      })
      .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
    return json(out);
  }),
];

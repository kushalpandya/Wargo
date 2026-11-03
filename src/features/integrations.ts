// SPDX-License-Identifier: AGPL-3.0-only
// Settings → Integrations: the Prowlarr and Transmission connections.
// Secrets are never stored or returned, only "a key is saved" flags.
import { delay, type RequestHandler, type WebSocketHandler } from "msw";
import type {
  Discovery,
  IntegrationsStatus,
  ProwlarrStatus,
  TestResult,
  TransmissionClientHint,
  TransmissionStatus,
} from "@/features/torrents/api";
import { api, badRequest, body, fail, json, notFound } from "../core/http";
import { persisted } from "../core/store";
import { CONTAINERS, HOME } from "./host";

export const PROWLARR_VERSION = "1.28.2.4885";
export const TRANSMISSION_VERSION = "4.0.6 (38c164933e)";

interface Saved {
  prowlarr: { url: string; hasKey: boolean };
  transmission: { url: string; username: string; hasPassword: boolean };
}

const saved = persisted<Saved>("integrations", () => ({
  prowlarr: { url: "http://127.0.0.1:9696", hasKey: true },
  transmission: { url: "http://127.0.0.1:9091/transmission/rpc", username: "demo", hasPassword: true },
}));

/** Prowlarr's own Transmission download client (on the compose network). */
const CLIENT_HINT: TransmissionClientHint = {
  name: "Transmission",
  host: "transmission",
  port: 9091,
  useSsl: false,
  urlBase: "/transmission/",
  username: "demo",
};

const container = (name: string) => CONTAINERS.find((c) => c.name === name);

/** Hosts that reach the demo server itself. */
const LOCAL_HOSTS = new Set(["127.0.0.1", "localhost", "homelab", "homelab.local", "192.168.1.20", "100.84.12.31", "[::1]"]);

/** Whether a URL points at a running container of that name on its port. */
function reaches(raw: string, name: "prowlarr" | "transmission"): string | null {
  const c = container(name);
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return "invalid URL";
  }
  const port = Number(u.port || (u.protocol === "https:" ? 443 : 80));
  const hostOk = LOCAL_HOSTS.has(u.hostname) || u.hostname === name;
  if (!hostOk) return `dial tcp ${u.hostname}:${port}: i/o timeout`;
  if (!c || c.state !== "running" || port !== c.port) return `dial tcp ${u.hostname}:${port}: connect: connection refused`;
  if (u.protocol === "https:") return "tls: first record does not look like a TLS handshake";
  if (name === "transmission" && !u.pathname.endsWith("/transmission/rpc")) return "Transmission answered 404 Not Found";
  return null;
}

/** Same rules as the server's normalizeURL. */
function normalizeURL(raw: string, rpcPath: string): string | null {
  raw = raw.trim();
  if (!raw) return "";
  if (!raw.includes("://")) raw = "http://" + raw;
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return null;
  }
  if ((u.protocol !== "http:" && u.protocol !== "https:") || !u.host || u.username || u.password) return null;
  u.search = "";
  u.hash = "";
  if (rpcPath) {
    const p = u.pathname.replace(/\/+$/, "");
    u.pathname = p === "" ? rpcPath : p.endsWith("/rpc") ? p : p + "/rpc";
  } else {
    u.pathname = u.pathname.replace(/\/+$/, "");
  }
  return u.toString().replace(/\/$/, "");
}

const badURL = () => badRequest("URL must be an http(s) address like http://127.0.0.1:9696");

export function prowlarrStatus(): ProwlarrStatus {
  const p = saved.get().prowlarr;
  const out: ProwlarrStatus = { url: p.url, hasApiKey: p.hasKey, configured: !!p.url && p.hasKey, available: false, hasTransmissionClient: false };
  if (!out.configured) return out;
  const err = reaches(p.url, "prowlarr");
  if (err) return { ...out, error: err };
  return { ...out, available: true, version: PROWLARR_VERSION, hasTransmissionClient: true, transmissionClient: CLIENT_HINT };
}

export function transmissionStatus(): TransmissionStatus {
  const t = saved.get().transmission;
  const out: TransmissionStatus = { url: t.url, username: t.username, hasPassword: t.hasPassword, configured: !!t.url, available: false };
  if (!out.configured) return out;
  const err = reaches(t.url, "transmission");
  if (err) return { ...out, error: err };
  return { ...out, available: true, version: TRANSMISSION_VERSION };
}

const status = (): IntegrationsStatus => ({ prowlarr: prowlarrStatus(), transmission: transmissionStatus() });

const CONFIG_PATH = `${HOME}/stacks/media/prowlarr/config.xml`;

function discovery(): Discovery {
  const pw = container("prowlarr");
  const tr = container("transmission");
  const d: Discovery = { docker: { available: true }, prowlarr: [], transmission: [] };
  if (pw)
    d.prowlarr.push({
      container: pw.name,
      image: pw.image,
      state: pw.state,
      url: `http://127.0.0.1:${pw.port}`,
      configPath: CONFIG_PATH,
      configReadable: true,
    });
  if (tr) d.transmission.push({ container: tr.name, image: tr.image, state: tr.state, url: `http://127.0.0.1:${tr.port}/transmission/rpc` });
  if (prowlarrStatus().available)
    d.transmissionFromProwlarr = {
      name: CLIENT_HINT.name,
      url: "http://transmission:9091/transmission/rpc",
      host: CLIENT_HINT.host,
      port: CLIENT_HINT.port,
      username: CLIENT_HINT.username,
    };
  return d;
}

interface ProwlarrInput {
  url?: string;
  apiKey?: string | null;
}
interface TransmissionInput {
  url?: string;
  username?: string;
  password?: string | null;
}

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  api.get("/api/integrations", () => json(status())),

  api.get("/api/integrations/discover", async () => json(discovery())),

  api.put("/api/integrations/prowlarr", async ({ request }) => {
    const inp = await body<ProwlarrInput>(request);
    const u = normalizeURL(inp.url ?? "", "");
    if (u === null) return badURL();
    saved.update((s) => {
      s.prowlarr = u ? { url: u, hasKey: s.prowlarr.hasKey || !!inp.apiKey?.trim() } : { url: "", hasKey: false };
    });
    return json(prowlarrStatus());
  }),

  api.put("/api/integrations/transmission", async ({ request }) => {
    const inp = await body<TransmissionInput>(request);
    const u = normalizeURL(inp.url ?? "", "/transmission/rpc");
    if (u === null) return badURL();
    saved.update((s) => {
      s.transmission = u
        ? { url: u, username: (inp.username ?? "").trim(), hasPassword: s.transmission.hasPassword || !!inp.password }
        : { url: "", username: "", hasPassword: false };
    });
    return json(transmissionStatus());
  }),

  api.post("/api/integrations/:name/test", async ({ request, params }) => {
    // A real round trip to the service takes a moment.
    await delay(400 + Math.random() * 500);
    const s = saved.get();
    if (params.name === "prowlarr") {
      const inp = await body<ProwlarrInput>(request);
      const u = inp.url ? normalizeURL(inp.url, "") : s.prowlarr.url;
      if (u === null) return badURL();
      if (!u || !(inp.apiKey || s.prowlarr.hasKey)) return badRequest("URL and API key are required");
      const err = reaches(u, "prowlarr");
      const res: TestResult = err ? { ok: false, error: err, url: u } : { ok: true, version: PROWLARR_VERSION, hasTransmissionClient: true, transmissionClient: CLIENT_HINT, url: u };
      return json(res);
    }
    if (params.name === "transmission") {
      const inp = await body<TransmissionInput>(request);
      const u = inp.url ? normalizeURL(inp.url, "/transmission/rpc") : s.transmission.url;
      if (u === null) return badURL();
      if (!u) return badRequest("URL is required");
      const err = reaches(u, "transmission");
      const res: TestResult = err ? { ok: false, error: err, url: u } : { ok: true, version: TRANSMISSION_VERSION, url: u };
      return json(res);
    }
    return notFound();
  }),

  api.post("/api/integrations/prowlarr/import-key", async ({ request }) => {
    const inp = await body<{ configPath?: string; url?: string }>(request);
    if (inp.configPath !== CONFIG_PATH) return fail(403, "not_detected", "That path is not the /config mount of a detected Prowlarr container");
    const u = normalizeURL(inp.url?.trim() || saved.get().prowlarr.url || "http://127.0.0.1:9696", "");
    if (u === null) return badURL();
    saved.update((s) => {
      s.prowlarr = { url: u, hasKey: true };
    });
    return json(prowlarrStatus());
  }),
];

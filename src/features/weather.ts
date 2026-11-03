// SPDX-License-Identifier: AGPL-3.0-only
// Weather on Home (Settings → Appearance). The real server asks Open-Meteo;
// the demo makes up a plausible day for the chosen place instead.
import type { RequestHandler } from "msw";
import { api, badRequest, body, json, query } from "../core/http";
import { persisted } from "../core/store";

interface Place {
  name: string;
  region?: string;
  country?: string;
  lat: number;
  lon: number;
}
interface Settings {
  enabled: boolean;
  location?: Place | null;
  units: "metric" | "imperial";
}

const PLACES: Place[] = [
  { name: "Berlin", region: "Land Berlin", country: "Germany", lat: 52.52, lon: 13.41 },
  { name: "Bergen", region: "Vestland", country: "Norway", lat: 60.39, lon: 5.32 },
  { name: "Lisbon", region: "Lisbon", country: "Portugal", lat: 38.72, lon: -9.14 },
  { name: "London", region: "England", country: "United Kingdom", lat: 51.51, lon: -0.13 },
  { name: "Montreal", region: "Quebec", country: "Canada", lat: 45.51, lon: -73.59 },
  { name: "Mumbai", region: "Maharashtra", country: "India", lat: 19.07, lon: 72.88 },
  { name: "New York", region: "New York", country: "United States", lat: 40.71, lon: -74.01 },
  { name: "Paris", region: "Île-de-France", country: "France", lat: 48.85, lon: 2.35 },
  { name: "Sydney", region: "New South Wales", country: "Australia", lat: -33.87, lon: 151.21 },
  { name: "Tokyo", region: "Tokyo", country: "Japan", lat: 35.69, lon: 139.69 },
  { name: "Toronto", region: "Ontario", country: "Canada", lat: 43.7, lon: -79.42 },
];
// The demo host's timezone is Europe/Berlin, so that is the guess.
const AUTO = PLACES[0];

const settings = persisted<Settings>("weather.settings", () => ({ enabled: true, location: null, units: "metric" }));

/** A stable made-up reading for a place, drifting slowly through the day. */
function reading(p: Place, imperial: boolean) {
  const h = new Date().getHours() + new Date().getMinutes() / 60;
  const base = 24 - Math.abs(p.lat) * 0.3 + Math.sin(((h - 9) / 24) * 2 * Math.PI) * 4;
  const seed = Math.abs(Math.round(p.lat * 7 + p.lon * 3)) % 4;
  const c = (v: number) => Math.round(imperial ? (v * 9) / 5 + 32 : v);
  return {
    current: {
      temp: c(base),
      feels: c(base - 1.5),
      code: [0, 2, 3, 61][seed],
      isDay: h >= 7 && h < 19,
      humidity: 48 + seed * 9,
      wind: Math.round((9 + seed * 4) * (imperial ? 0.621 : 1)),
    },
    today: { max: c(base + 3), min: c(base - 6) },
  };
}

export const handlers: RequestHandler[] = [
  api.get("/api/weather", () => {
    const s = settings.get();
    if (!s.enabled) return json({ enabled: false, units: s.units });
    const place = s.location ?? AUTO;
    return json({
      enabled: true,
      units: s.units,
      location: place,
      ...(s.location ? {} : { auto: true }),
      ...reading(place, s.units === "imperial"),
      updatedAt: Date.now() - 4 * 60_000,
    });
  }),
  api.get("/api/weather/settings", () => json(settings.get())),
  api.put("/api/weather/settings", async ({ request }) => {
    const v = await body<Settings>(request);
    if (v.location && (typeof v.location.lat !== "number" || typeof v.location.lon !== "number")) return badRequest("invalid location");
    return json(settings.set({ enabled: !!v.enabled, location: v.location?.name ? v.location : null, units: v.units === "imperial" ? "imperial" : "metric" }));
  }),
  api.get("/api/weather/search", ({ request }) => {
    const q = (query(request).get("q") ?? "").trim().toLowerCase();
    if (q.length < 2) return json([]);
    return json(PLACES.filter((p) => p.name.toLowerCase().startsWith(q) || p.country?.toLowerCase().startsWith(q)).slice(0, 8));
  }),
];

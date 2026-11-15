// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useCapabilities } from "@/app/session";
import { setPref } from "@/lib/prefs";
import type { Tile } from "./Tiles";
import { tileSizePref, type TileSize } from "./layout";

export interface TileInput {
  name: string;
  url: string;
  icon?: string;
  group?: string;
  newTab: boolean;
  healthUrl?: string;
  enabled: boolean;
}

export interface Suggestion {
  name: string;
  url: string;
  iconSlug?: string;
  group: string;
  container: string;
  image: string;
  ports: number[];
  exists: boolean;
}

export const tilesKey = ["launcher", "tiles"] as const;

/** How Home shows the app tiles; saved on the server, so every browser and session gets it. */
export interface LauncherSettings {
  tileSize: TileSize;
}

const settingsKey = ["launcher", "settings"] as const;

export function useLauncherSettings() {
  return useQuery({
    queryKey: settingsKey,
    queryFn: async () => {
      const v = await api.get<LauncherSettings>("/api/launcher/settings");
      setPref(tileSizePref, v.tileSize);
      return v;
    },
    staleTime: 5 * 60_000,
  });
}

export function useSaveLauncherSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: LauncherSettings) => api.put<LauncherSettings>("/api/launcher/settings", v),
    onSuccess: (v) => {
      qc.setQueryData(settingsKey, v);
      setPref(tileSizePref, v.tileSize);
    },
  });
}

/**
 * Whether a tile can be shown here: Torrents needs Prowlarr or Transmission
 * (the tile stays in the editor, so its place in the order is kept).
 */
export function useTileAvailable(): (t: Tile) => boolean {
  const { data: caps } = useCapabilities();
  const torrents = !!(caps?.prowlarr?.available || caps?.transmission?.available);
  return (t) => t.builtin !== "torrents" || torrents;
}

export function useTiles() {
  return useQuery({ queryKey: tilesKey, queryFn: () => api.get<Tile[]>("/api/launcher/tiles"), refetchInterval: 60_000 });
}

export async function uploadIcon(file: File): Promise<string> {
  const fd = new FormData();
  fd.append("file", file);
  const r = await api.request<{ icon: string }>("/api/launcher/icons", { method: "POST", raw: fd });
  return r.icon;
}

export async function fetchIcon(slug: string): Promise<string> {
  const r = await api.post<{ icon: string }>("/api/launcher/icons/fetch", { slug });
  return r.icon;
}

/** Guess a dashboard-icons slug from an app name ("Home Assistant" → "home-assistant"). */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
}

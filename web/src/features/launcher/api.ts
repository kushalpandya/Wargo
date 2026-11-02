// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Tile } from "./Tiles";

export interface LauncherSettings {
  enabled: boolean;
  placement: "overview" | "page";
}

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

export function useTiles() {
  return useQuery({ queryKey: tilesKey, queryFn: () => api.get<Tile[]>("/api/launcher/tiles"), refetchInterval: 60_000 });
}

export function useLauncherSettings() {
  return useQuery({ queryKey: ["launcher", "settings"], queryFn: () => api.get<LauncherSettings>("/api/launcher") });
}

export function useSaveLauncherSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: LauncherSettings) => api.put<LauncherSettings>("/api/launcher", v),
    onSuccess: (v) => {
      qc.setQueryData(["launcher", "settings"], v);
      qc.invalidateQueries({ queryKey: ["capabilities"] });
    },
  });
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

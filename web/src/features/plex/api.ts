// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { api, qs } from "@/lib/api";

export interface PlexSession {
  type: string;
  title: string;
  parentTitle?: string;
  grandparentTitle?: string;
  parentIndex?: number;
  index?: number;
  year?: number;
  /** Milliseconds. */
  viewOffset: number;
  duration: number;
  user?: string;
  player?: string;
  product?: string;
  state: "playing" | "paused" | "buffering" | string;
  local: boolean;
  decision: "transcode" | "directstream" | "directplay";
  thumb?: string;
}

export interface PlexItem {
  key: string;
  type: string;
  title: string;
  parentTitle?: string;
  grandparentTitle?: string;
  parentIndex?: number;
  index?: number;
  year?: number;
  leafCount?: number;
  /** viewedAt (history) or addedAt (recent), unix seconds. */
  at: number;
  user?: string;
  thumb?: string;
}

export interface PlexActivity {
  sessions: PlexSession[];
  history: PlexItem[];
  recent: PlexItem[];
  /** Per-section errors when only part of the data could be loaded. */
  errors?: Partial<Record<"sessions" | "history" | "recent", string>>;
  /** Plex web app, when the configured address is not a loopback one. */
  webUrl?: string;
  updatedAt: string;
}

export const plexActivityKey = ["plex", "activity"] as const;

/** GET /api/plex/activity: polled every 15 s while something plays, else every minute. */
export function usePlexActivity({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: plexActivityKey,
    queryFn: () => api.get<PlexActivity>("/api/plex/activity"),
    enabled,
    refetchInterval: (q) => (q.state.data?.sessions.length ? 15_000 : 60_000),
    retry: false,
  });
}

/** Poster proxied (and resized) by Wargo; the browser never sees the token. */
export function plexThumb(path: string, width = 80): string {
  return `/api/plex/thumb${qs({ path, w: width })}`;
}

/** "S2 E3", or "" when the season or episode number is unknown. */
export function plexEpisode(m: { parentIndex?: number; index?: number }): string {
  return m.parentIndex != null && m.index != null ? `S${m.parentIndex} E${m.index}` : "";
}

/** "Severance · S2 E3", "Heat (1995)", "Carrie · Season 1". */
export function plexLabel(m: { type: string; title: string; parentTitle?: string; grandparentTitle?: string; parentIndex?: number; index?: number; year?: number }): string {
  switch (m.type) {
    case "episode":
      return [m.grandparentTitle || m.title, plexEpisode(m)].filter(Boolean).join(" · ");
    case "season":
      return [m.parentTitle, m.title].filter(Boolean).join(" · ");
    case "track":
      return [m.grandparentTitle, m.title].filter(Boolean).join(" · ");
    case "album":
      return [m.parentTitle, m.title].filter(Boolean).join(" · ");
    case "movie":
      return m.year ? `${m.title} (${m.year})` : m.title;
  }
  return m.title || m.grandparentTitle || "Untitled";
}

export const plexDecision: Record<PlexSession["decision"], string> = {
  transcode: "Transcoding",
  directstream: "Direct stream",
  directplay: "Direct play",
};

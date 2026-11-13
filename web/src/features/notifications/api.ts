// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useTopic } from "@/lib/ws";
import { disablePush, enablePush, pushPath, type PushState } from "./push";

export type Category =
  | "download.started"
  | "download.finished"
  | "stream.started"
  | "container.unhealthy"
  | "container.failed"
  | "updates.found"
  | "download.error"
  | "disk.full";

export type Mode = "off" | "app" | "push";

/** Something happening now (a download, a Plex stream); it can't be cleared. */
export interface Active {
  id: string;
  category: Category;
  title: string;
  detail: string;
  /** 0..1 */
  progress: number;
  startedAt: string;
  link: string;
}

/** Something that happened, kept in Recent until cleared or expired. */
export interface Item {
  id: string;
  category: Category;
  key: string;
  title: string;
  body: string;
  link: string;
  createdAt: string;
  resolvedAt: string | null;
  clearedAt: string | null;
}

export interface Snapshot {
  active: Active[];
  recent: Item[];
  badge: number;
  settings: Record<Category, Mode>;
}

const key = ["notifications"] as const;

/** The panel's contents, kept live through the sticky "notifications" topic. */
export function useNotifications() {
  const qc = useQueryClient();
  useTopic<Snapshot>("notifications", (s) => qc.setQueryData(key, s));
  return useQuery({ queryKey: key, queryFn: () => api.get<Snapshot>("/api/notifications"), staleTime: 60_000 });
}

/** Clears one Recent item: mutate(id). */
export function useClearNotification() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.post<Snapshot>("/api/notifications/clear", { id }),
    onSuccess: (s) => qc.setQueryData(key, s),
  });
}

/** Clears every Recent item (Active stays): mutate(). */
export function useClearAll() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<Snapshot>("/api/notifications/clear", { all: true }),
    onSuccess: (s) => qc.setQueryData(key, s),
  });
}

export type Settings = Record<Category, Mode>;

/** Saves every category's mode (the whole map) and keeps the panel's copy in step. */
export function useSaveNotificationSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (s: Settings) => api.put<Settings>("/api/notifications/settings", s),
    onSuccess: (settings) => qc.setQueryData<Snapshot>(key, (s) => (s ? { ...s, settings } : s)),
  });
}

const pushKey = ["notification-push"] as const;

/** Whether this session's device is registered for push, with the server's public key. */
export function usePushState() {
  return useQuery({ queryKey: pushKey, queryFn: () => api.get<PushState>(pushPath) });
}

/** Turns push on (true) or off (false) for this device. Turning on resolves to null when permission wasn't granted. */
export function useSetPush() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (on: boolean) => (on ? enablePush(api) : disablePush(api)),
    onSuccess: (s) => {
      if (s) qc.setQueryData(pushKey, s);
    },
  });
}

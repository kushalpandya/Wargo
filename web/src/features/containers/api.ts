// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useTopic } from "@/lib/ws";
import type { Container, UpdatesStatus } from "./types";

export const containersKey = ["containers"] as const;
export const containerUpdatesKey = ["containers", "updates"] as const;

/** GET /api/containers, kept live through the sticky "containers" topic. */
export function useContainers({ enabled = true, refetchInterval = 30_000 }: { enabled?: boolean; refetchInterval?: number } = {}) {
  const qc = useQueryClient();
  useTopic<Container[]>(enabled ? "containers" : null, (d) => qc.setQueryData(containersKey, d));
  return useQuery({
    queryKey: containersKey,
    queryFn: () => api.get<Container[]>("/api/containers"),
    refetchInterval,
    enabled,
  });
}

/**
 * Image update-check status (GET /api/containers/updates), polled every few
 * minutes and every few seconds while a check runs. No topic subscription:
 * for summaries such as badges and Home.
 */
export function useContainerUpdatesStatus({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: containerUpdatesKey,
    queryFn: () => api.get<UpdatesStatus>("/api/containers/updates"),
    enabled,
    staleTime: 10_000,
    refetchInterval: (q) => (q.state.data?.checking ? 2_000 : 5 * 60_000),
  });
}

/** Like useContainerUpdatesStatus, also refreshed when the "containers" topic changes. */
export function useContainerUpdates({ enabled = true }: { enabled?: boolean } = {}) {
  const qc = useQueryClient();
  useTopic<Container[]>(enabled ? "containers" : null, () => qc.invalidateQueries({ queryKey: containerUpdatesKey, exact: true }));
  return useContainerUpdatesStatus({ enabled });
}

/** Containers whose image has a newer version, from an update-check status. */
export const countAvailable = (s?: UpdatesStatus) => Object.values(s?.results ?? {}).filter((u) => u.updateAvailable).length;

/** POST /api/containers/check-updates; resolves when the check finished. */
export function useCheckContainerUpdates(opts: { onSuccess?: (s: UpdatesStatus) => void; onError?: (e: unknown) => void } = {}) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<UpdatesStatus>("/api/containers/check-updates"),
    onMutate: () => qc.setQueryData<UpdatesStatus>(containerUpdatesKey, (s) => (s ? { ...s, checking: true } : s)),
    onSuccess: (s) => {
      qc.setQueryData(containerUpdatesKey, s);
      qc.invalidateQueries({ queryKey: containersKey, exact: true });
      opts.onSuccess?.(s);
    },
    onError: (e) => {
      qc.invalidateQueries({ queryKey: containerUpdatesKey, exact: true });
      opts.onError?.(e);
    },
  });
}

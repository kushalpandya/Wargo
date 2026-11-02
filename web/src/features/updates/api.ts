// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useTopic } from "@/lib/ws";

export interface PkgUpdate {
  name: string;
  current: string;
  new: string;
  repo: string;
  /** Download size in bytes, 0 when unknown (or already cached). */
  size: number;
  source: "repo" | "aur" | "flatpak";
}

export interface CheckResult {
  at: string;
  durationMs: number;
  updates: PkgUpdate[];
  extras: PkgUpdate[];
  downloadSize: number;
  error?: string;
  /** An upgrade ran after this check; the list may be outdated. */
  stale?: boolean;
}

export interface ConfigFile {
  path: string;
  modTime: string;
  new: boolean;
}

export interface RunRecord {
  id: string;
  unit: string;
  log: string;
  backend: string;
  packages: number;
  startedAt: string;
  finishedAt?: string;
  fake?: boolean;
  jobId?: string;
  ok: boolean;
  error?: string;
  exitStatus?: number;
  upgraded: number;
  /** Stage reached (where it failed, for failed runs). */
  stage?: string;
  rebootRequired: boolean;
  rebootReason?: string;
  configFiles?: ConfigFile[];
}

export interface JobSnapshot {
  id: string;
  kind: string;
  title: string;
  state: "running" | "succeeded" | "failed" | "canceled";
  stage?: string;
  progress: number;
  current?: string;
  error?: string;
  startedAt: string;
  finishedAt?: string;
  result?: Record<string, unknown>;
  logLines: number;
}

export interface PkgStatus {
  distro: string;
  distroId: string;
  backend: string;
  available: boolean;
  extras: string[];
  needsRootToCheck: boolean;
  upgradeCommand?: string;
  checking: boolean;
  lastCheck?: CheckResult;
  running?: JobSnapshot;
  lastRun?: RunRecord;
  reboot: { required: boolean; reason?: string };
  fake?: boolean;
}

export interface Precheck {
  lock: { locked: boolean; path?: string; reason?: string };
  disk: { path: string; free: number; low: boolean }[];
  blocked?: string;
}

export const statusKey = ["pkg", "backend"] as const;

/** Backend status, kept live through the sticky "pkg" topic. */
export function usePkgStatus() {
  const qc = useQueryClient();
  useTopic<PkgStatus>("pkg", (s) => qc.setQueryData(statusKey, s));
  return useQuery({ queryKey: statusKey, queryFn: () => api.get<PkgStatus>("/api/pkg/backend"), staleTime: 60_000 });
}

/** Number of pending repo updates from the last manual check (0 if stale/unknown). */
export function pendingCount(s?: PkgStatus): number {
  const c = s?.lastCheck;
  if (!c || c.stale || c.error) return 0;
  return c.updates.length;
}

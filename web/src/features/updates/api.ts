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
  /** OS packages: repo, aur, flatpak. Homebrew: formula, cask, pinned. */
  source: "repo" | "aur" | "flatpak" | "formula" | "cask" | "pinned";
  /** Homebrew casks whose installer may ask for the account password. */
  needsPassword?: boolean;
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
  /** The upgrade this check ran right after, on its own. */
  afterRun?: string;
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

/** Homebrew status (GET /api/brew/status, sticky "brew" topic). */
export interface BrewStatus {
  supported: boolean;
  available: boolean;
  backend: "brew";
  prefix?: string;
  version?: string;
  /** False when Wargo's user doesn't own Homebrew; problem says why. */
  canUpgrade: boolean;
  problem?: string;
  upgradeCommand?: string;
  checking: boolean;
  /** updates: formulae and casks; extras: pinned formulae (skipped by brew upgrade). */
  lastCheck?: CheckResult;
  running?: JobSnapshot;
  lastRun?: RunRecord;
  fake?: boolean;
}

export interface Precheck {
  lock: { locked: boolean; path?: string; reason?: string };
  disk: { path: string; free: number; low: boolean }[];
  blocked?: string;
}

export const statusKey = ["pkg", "backend"] as const;

export const brewKey = ["brew", "status"] as const;

/** A status kept live through a sticky topic, cached under key. */
function useLiveStatus<T>(key: readonly string[], topic: string, path: string, enabled: boolean) {
  const qc = useQueryClient();
  useTopic<T>(enabled ? topic : null, (s) => qc.setQueryData(key, s));
  return useQuery({ queryKey: key, queryFn: () => api.get<T>(path), enabled, staleTime: 60_000 });
}

/** OS package backend status, kept live through the sticky "pkg" topic. */
export function usePkgStatus({ enabled = true }: { enabled?: boolean } = {}) {
  return useLiveStatus<PkgStatus>(statusKey, "pkg", "/api/pkg/backend", enabled);
}

/** Homebrew status, kept live through the sticky "brew" topic. */
export function useBrewStatus({ enabled = true }: { enabled?: boolean } = {}) {
  return useLiveStatus<BrewStatus>(brewKey, "brew", "/api/brew/status", enabled);
}

/** Number of pending updates from the last check (0 if stale, failed or unknown). */
export function pendingCount(s?: { lastCheck?: CheckResult }): number {
  const c = s?.lastCheck;
  if (!c || c.stale || c.error) return 0;
  return c.updates.length;
}

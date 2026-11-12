// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api";
import { plural } from "@/lib/format";
import { toast } from "@/components/ui/toast";
import { checkContainerUpdates } from "@/features/containers/api";
import { brewKey, statusKey, type BrewStatus, type CheckResult, type PkgStatus } from "./api";

export type CheckSource = "containers" | "system" | "brew";

const NAMES: Record<CheckSource, string> = { containers: "Containers", system: "System", brew: "Homebrew" };

interface Outcome {
  source: CheckSource;
  updates: number;
  /** Items the check couldn't look at (container images). */
  failed?: number;
}

async function checkContainers(qc: QueryClient): Promise<Outcome> {
  const r = Object.values((await checkContainerUpdates(qc)).results);
  return {
    source: "containers",
    updates: r.filter((u) => u.updateAvailable).length,
    failed: r.filter((u) => u.updateAvailable === null).length,
  };
}

async function checkPackages<T extends { checking: boolean; lastCheck?: CheckResult }>(
  qc: QueryClient,
  source: CheckSource,
  key: readonly string[],
  path: string,
): Promise<Outcome> {
  qc.setQueryData<T>(key, (s) => (s ? { ...s, checking: true } : s));
  try {
    const r = await api.post<CheckResult>(path);
    qc.setQueryData<T>(key, (s) => (s ? { ...s, lastCheck: r, checking: false } : s));
    if (r.error) throw new Error(r.error);
    return { source, updates: r.updates.length };
  } finally {
    qc.invalidateQueries({ queryKey: key });
  }
}

/**
 * The Updates page's one "Check for updates": checks every given source at
 * once and sums the results up in a single toast. Each source's own status
 * (and so its tab) updates as its check finishes.
 */
export function useCheckAll() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (sources: CheckSource[]) => {
      const run = (s: CheckSource) =>
        s === "containers"
          ? checkContainers(qc)
          : s === "system"
            ? checkPackages<PkgStatus>(qc, s, statusKey, "/api/pkg/check")
            : checkPackages<BrewStatus>(qc, s, brewKey, "/api/brew/check");
      const settled = await Promise.allSettled(sources.map(run));
      return settled.map((r, i) => ({ source: sources[i], result: r }));
    },
    onSuccess: (results) => {
      const done = results.flatMap((r) => (r.result.status === "fulfilled" ? [r.result.value] : []));
      for (const r of results) {
        if (r.result.status !== "rejected") continue;
        const e = r.result.reason;
        // Closing the approval dialog skips that source without an error.
        if (e instanceof ApiError && e.code === "elevation_canceled") continue;
        toast.error(`${NAMES[r.source]} update check failed`, e);
      }
      if (!done.length) return;
      const total = done.reduce((n, o) => n + o.updates, 0);
      const failed = done.reduce((n, o) => n + (o.failed ?? 0), 0);
      const parts = done.filter((o) => o.updates > 0).map((o) => `${NAMES[o.source]} ${o.updates}`);
      const notes = [parts.length > 1 ? parts.join(", ") : "", failed ? `${failed} ${plural(failed, "image")} could not be checked` : ""]
        .filter(Boolean)
        .join(". ");
      const title =
        total > 0
          ? `${total} ${plural(total, "update")} available${parts.length === 1 ? ` (${NAMES[done.find((o) => o.updates > 0)!.source]})` : ""}`
          : done.length === results.length
            ? "Everything is up to date"
            : `${done.map((o) => NAMES[o.source]).join(" and ")} up to date`;
      toast.ok(title, notes || undefined);
    },
  });
}

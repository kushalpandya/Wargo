// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { containerUpdatesKey } from "@/features/containers/api";
import type { UpdatesStatus } from "@/features/containers/types";
import { pendingCount, usePkgStatus } from "./api";

/**
 * Pending-update count for the sidebar: OS packages from the last check
 * plus containers whose image has a newer version. Renders nothing when
 * there is nothing to report. Never triggers a check.
 */
export function UpdatesBadge({ className }: { className?: string }) {
  const { data } = usePkgStatus();
  const dockerOn = !!useCapabilities().data?.docker?.available;
  // Shares the Updates page's cache; light polling, no topic subscription.
  const ctr = useQuery({
    queryKey: containerUpdatesKey,
    queryFn: () => api.get<UpdatesStatus>("/api/containers/updates"),
    enabled: dockerOn,
    staleTime: 60_000,
    refetchInterval: (q) => (q.state.data?.checking ? 5_000 : 5 * 60_000),
  });
  const running = !!data?.running;
  const pkgN = pendingCount(data);
  const ctrN = dockerOn ? Object.values(ctr.data?.results ?? {}).filter((u) => u.updateAvailable).length : 0;
  const n = pkgN + ctrN;
  if (!running && n === 0) return null;
  const label = running
    ? "Upgrade in progress"
    : [pkgN && `${pkgN} system update${pkgN === 1 ? "" : "s"}`, ctrN && `${ctrN} container update${ctrN === 1 ? "" : "s"}`].filter(Boolean).join(", ");
  return (
    <span
      className={cn(
        "num inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-2xs font-semibold leading-none",
        running ? "bg-frost-soft text-frost" : "bg-accent-soft text-accent",
        className,
      )}
      aria-label={label}
      title={label}
    >
      {running ? "…" : n > 999 ? "999+" : n}
    </span>
  );
}

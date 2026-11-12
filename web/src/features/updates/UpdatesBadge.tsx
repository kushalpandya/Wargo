// SPDX-License-Identifier: AGPL-3.0-only
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { countAvailable, useContainerUpdatesStatus } from "@/features/containers/api";
import { pendingCount, usePkgStatus } from "./api";

/**
 * Pending-update count for the sidebar: OS packages from the last check
 * plus containers whose image has a newer version. Renders nothing when
 * there is nothing to report. Never triggers a check.
 */
export function UpdatesBadge({ className }: { className?: string }) {
  const caps = useCapabilities().data;
  const { data } = usePkgStatus({ enabled: !!caps && caps.pkg?.supported !== false });
  const dockerOn = !!caps?.docker?.available;
  // Shares the Updates page's cache; light polling, no topic subscription.
  const ctr = useContainerUpdatesStatus({ enabled: dockerOn });
  const running = !!data?.running;
  const pkgN = pendingCount(data);
  const ctrN = dockerOn ? countAvailable(ctr.data) : 0;
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

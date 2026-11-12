// SPDX-License-Identifier: AGPL-3.0-only
import { cn } from "@/lib/cn";
import { plural } from "@/lib/format";
import { useCapabilities } from "@/app/session";
import { countAvailable, useContainerUpdatesStatus } from "@/features/containers/api";
import { pendingCount, useBrewStatus, usePkgStatus } from "./api";

/**
 * Pending-update count for the sidebar: OS packages and Homebrew from their
 * last checks plus containers whose image has a newer version. Renders
 * nothing when there is nothing to report. Never triggers a check.
 */
export function UpdatesBadge({ className }: { className?: string }) {
  const caps = useCapabilities().data;
  const pkg = usePkgStatus({
    enabled: !!caps && caps.pkg?.supported !== false,
  }).data;
  const brew = useBrewStatus({ enabled: !!caps?.brew?.available }).data;
  const dockerOn = !!caps?.docker?.available;
  // Shares the Updates page's cache; light polling, no topic subscription.
  const ctr = useContainerUpdatesStatus({ enabled: dockerOn });
  const running = !!pkg?.running || !!brew?.running;
  const pkgN = pendingCount(pkg);
  const brewN = pendingCount(brew);
  const ctrN = dockerOn ? countAvailable(ctr.data) : 0;
  const n = pkgN + brewN + ctrN;
  if (!running && n === 0) return null;
  const label = running
    ? "Update in progress"
    : [
        ctrN && `${ctrN} container ${plural(ctrN, "update")}`,
        pkgN && `${pkgN} system ${plural(pkgN, "update")}`,
        brewN && `${brewN} Homebrew ${plural(brewN, "update")}`,
      ]
        .filter(Boolean)
        .join(", ");
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

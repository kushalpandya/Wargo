// SPDX-License-Identifier: AGPL-3.0-only
import { cn } from "@/lib/cn";
import { pendingCount, usePkgStatus } from "./api";

/**
 * Pending-update count from the last manual check (e.g. for the sidebar).
 * Renders nothing when there is nothing to report. Never triggers a check.
 */
export function UpdatesBadge({ className }: { className?: string }) {
  const { data } = usePkgStatus();
  const running = !!data?.running;
  const n = pendingCount(data);
  if (!running && n === 0) return null;
  return (
    <span
      className={cn(
        "num inline-flex h-4 min-w-4 items-center justify-center rounded-full px-1 text-2xs font-semibold leading-none",
        running ? "bg-frost-soft text-frost" : "bg-accent-soft text-accent",
        className,
      )}
      aria-label={running ? "Upgrade in progress" : `${n} updates available`}
      title={running ? "Upgrade in progress" : `${n} updates available`}
    >
      {running ? "…" : n > 999 ? "999+" : n}
    </span>
  );
}

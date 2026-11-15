// SPDX-License-Identifier: AGPL-3.0-only
import { cn } from "@/lib/cn";
import { Card } from "./card";
import { Meter, Skeleton } from "./misc";

export interface StatItem {
  label: string;
  value: string;
  sub?: string;
  tone?: "warn" | "crit";
  /** 0-100: draws a meter under the value. */
  meter?: number;
}

const toneText = { warn: "text-warn", crit: "text-crit" } as const;

/** Four numbers in one card above a list (System Monitor's Processes and Audit tabs). `items` null shows placeholders. */
export function StatStrip({ items }: { items: StatItem[] | null }) {
  return (
    <Card className="mb-4 grid grid-cols-2 divide-border max-sm:[&>*:nth-child(n+3)]:border-t sm:grid-cols-4 sm:divide-x">
      {items
        ? items.map((it, i) => (
            <div key={it.label} className={cn("min-w-0 px-4 py-3", i % 2 === 1 && "max-sm:border-l max-sm:border-border")}>
              <div className="text-2xs font-medium uppercase tracking-wider text-subtle">{it.label}</div>
              <div className={cn("num mt-0.5 text-xl font-medium leading-tight", it.tone && toneText[it.tone])}>{it.value}</div>
              {/* Cells without a meter keep its space, so all cells are the same height. */}
              {it.meter !== undefined ? (
                <Meter value={it.meter} tone={it.tone ?? "accent"} className="mt-1.5" label={it.label} />
              ) : (
                <div className="mt-1.5 h-1.5" aria-hidden="true" />
              )}
              <div className="mt-1 truncate text-xs text-muted">{it.sub}</div>
            </div>
          ))
        : Array.from({ length: 4 }, (_, i) => (
            // Same lines as a real cell (label, value, meter gap, sub line), so the card keeps its height.
            <div key={i} className="px-4 py-3">
              <Skeleton className="h-3 w-16" />
              <Skeleton className="mt-1.5 h-6 w-20" />
              <div className="mt-1.5 h-1.5" />
              <Skeleton className="mt-1 h-3.5 w-24" />
            </div>
          ))}
    </Card>
  );
}

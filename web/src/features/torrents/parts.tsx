// SPDX-License-Identifier: AGPL-3.0-only
// Pieces shared by the Search and Downloads cards, so both have the same
// shape: a toolbar (input, then controls on the right), a row of chips, the
// list, and a status bar at the bottom.
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

/** Both cards: the same minimum height, so they line up when empty. */
export const torrentCardClass = "flex min-h-[30rem] flex-col";

/** First row: the main input, then controls on the right. */
export const toolbarClass = "flex flex-wrap items-center gap-2 border-b border-border p-3";

/** A labelled row of chips (categories, or which torrents to show). */
export function ChipRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex w-full flex-wrap items-center gap-1.5" role="group" aria-label={label}>
      <span className="mr-1 text-2xs font-semibold uppercase tracking-wider text-subtle">{label}</span>
      {children}
    </div>
  );
}

export function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "inline-flex h-7 items-center gap-1 rounded-full border px-2.5 text-xs font-medium transition-colors @max-3xl:h-8",
        active ? "border-accent/40 bg-accent-soft text-accent" : "border-border bg-surface-2 text-muted hover:border-border-strong hover:text-fg",
      )}
    >
      {children}
    </button>
  );
}

/** The card's bottom line: small live figures, separated by dots. */
export function StatusBar({ children, label }: { children: ReactNode; label: string }) {
  return (
    <div
      role="status"
      aria-label={label}
      className="num mt-auto flex flex-wrap items-center gap-x-3 gap-y-0.5 border-t border-border bg-surface-2/50 px-3 py-1.5 text-2xs text-muted [&_svg]:size-3 [&_svg]:shrink-0"
    >
      {children}
    </div>
  );
}

/** One figure in the status bar, with an optional icon in a colour. */
export function StatusItem({ icon, tone, children }: { icon?: ReactNode; tone?: string; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      {icon && <span className={tone}>{icon}</span>}
      {children}
    </span>
  );
}

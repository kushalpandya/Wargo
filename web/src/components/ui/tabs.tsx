// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { NavLink } from "react-router";
import { cn } from "@/lib/cn";

/**
 * Page tabs backed by routes, so every tab has its own URL (permalinks,
 * back/forward). The active link gets aria-current="page".
 */
export function NavTabs({ label, items, className }: { label: string; items: { to: string; label: ReactNode; end?: boolean }[]; className?: string }) {
  return (
    <nav aria-label={label} className={cn("mb-4 flex gap-1 overflow-x-auto border-b border-border [scrollbar-width:none]", className)}>
      {items.map((i) => (
        <NavLink
          key={i.to}
          to={i.to}
          end={i.end}
          className={({ isActive }) =>
            cn(
              "-mb-px inline-flex h-9 shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-3 text-ui font-medium transition-colors [&_svg]:size-4",
              "focus-visible:rounded-t-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25",
              isActive ? "border-accent text-fg" : "border-transparent text-muted hover:border-border-strong hover:text-fg",
            )
          }
        >
          {i.label}
        </NavLink>
      ))}
    </nav>
  );
}

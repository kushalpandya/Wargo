// SPDX-License-Identifier: AGPL-3.0-only
import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/cn";

/** Pastel tints for card accents (icon chips, tiles). */
export type Tint = "iris" | "mint" | "sky" | "peach" | "rose" | "butter";

/** Background + foreground classes for each tint, for tinted chips and tiles. */
export const tintClasses: Record<Tint, string> = {
  iris: "bg-iris-tint text-iris",
  mint: "bg-mint-tint text-mint",
  sky: "bg-sky-tint text-sky",
  peach: "bg-peach-tint text-peach",
  rose: "bg-rose-tint text-rose",
  butter: "bg-butter-tint text-butter",
};

export function Card({ className, children, ...rest }: HTMLAttributes<HTMLElement>) {
  return (
    <section className={cn("min-w-0 rounded-xl border border-border bg-surface shadow-card", className)} {...rest}>
      {children}
    </section>
  );
}

/** Small rounded square holding an icon, tinted with a pastel colour (or neutral grey). */
export function IconChip({ children, tint = "iris", className }: { children: ReactNode; tint?: Tint | "neutral"; className?: string }) {
  return (
    <span
      className={cn(
        "flex size-8 shrink-0 items-center justify-center rounded-lg [&_svg]:size-4",
        tint === "neutral" ? "bg-surface-3 text-muted" : tintClasses[tint],
        className,
      )}
      aria-hidden="true"
    >
      {children}
    </span>
  );
}

export function CardHeader({
  title,
  icon,
  tint,
  meta,
  actions,
  className,
}: {
  title: ReactNode;
  icon?: ReactNode;
  /** Pastel colour of the icon chip (default: neutral). */
  tint?: Tint;
  meta?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("flex min-h-14 items-center gap-3 border-b border-border/70 px-4 py-2.5", className)}>
      {icon && <IconChip tint={tint ?? "neutral"}>{icon}</IconChip>}
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
        <h2 className="text-md font-semibold tracking-tight">{title}</h2>
        {meta && <span className="num truncate text-xs text-subtle">{meta}</span>}
      </div>
      {actions && <div className="ml-auto flex items-center gap-1">{actions}</div>}
    </header>
  );
}

export function CardBody({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("p-4", className)}>{children}</div>;
}

/** Big number with unit and caption. */
export function Stat({
  label,
  value,
  unit,
  sub,
  tone,
  className,
}: {
  label: ReactNode;
  value: ReactNode;
  unit?: ReactNode;
  sub?: ReactNode;
  tone?: "ok" | "warn" | "crit";
  className?: string;
}) {
  return (
    <div className={cn("min-w-0", className)}>
      <div className="text-xs font-medium text-subtle">{label}</div>
      <div className="mt-0.5 flex items-baseline gap-1">
        <span
          className={cn(
            "num text-2xl font-semibold leading-tight tracking-tight",
            tone === "warn" && "text-warn",
            tone === "crit" && "text-crit",
          )}
        >
          {value}
        </span>
        {unit && <span className="num text-xs text-muted">{unit}</span>}
      </div>
      {sub && <div className="mt-0.5 truncate text-xs text-muted">{sub}</div>}
    </div>
  );
}

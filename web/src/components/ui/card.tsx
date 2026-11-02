// SPDX-License-Identifier: AGPL-3.0-only
import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/cn";

export function Card({ className, children, ...rest }: HTMLAttributes<HTMLElement>) {
  return (
    <section className={cn("min-w-0 rounded-lg border border-border bg-surface shadow-[var(--shadow)]", className)} {...rest}>
      {children}
    </section>
  );
}

export function CardHeader({
  title,
  icon,
  meta,
  actions,
  className,
}: {
  title: ReactNode;
  icon?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("flex min-h-10 items-center gap-2 border-b border-border px-3.5 py-2", className)}>
      {icon && <span className="text-subtle [&_svg]:size-4">{icon}</span>}
      <h2 className="text-[0.8125rem] font-semibold tracking-tight">{title}</h2>
      {meta && <span className="num truncate text-xs text-subtle">{meta}</span>}
      {actions && <div className="ml-auto flex items-center gap-1">{actions}</div>}
    </header>
  );
}

export function CardBody({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("p-3.5", className)}>{children}</div>;
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
      <div className="text-2xs font-medium uppercase tracking-wider text-subtle">{label}</div>
      <div className="mt-0.5 flex items-baseline gap-1">
        <span
          className={cn(
            "num text-xl font-medium leading-tight",
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

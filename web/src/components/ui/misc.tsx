// SPDX-License-Identifier: AGPL-3.0-only
import type { HTMLAttributes, ReactNode } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/cn";
import { useMedia } from "@/lib/hooks";
import { usePageSlots } from "@/app/pageSlots";

export type Tone = "neutral" | "ok" | "warn" | "crit" | "accent" | "frost";

const tones: Record<Tone, string> = {
  neutral: "bg-surface-3 text-muted border-border",
  ok: "bg-ok-soft text-ok border-ok/25",
  warn: "bg-warn-soft text-warn border-warn/25",
  crit: "bg-crit-soft text-crit border-crit/25",
  accent: "bg-accent-soft text-accent border-accent/25",
  frost: "bg-frost-soft text-frost border-frost/25",
};

export function Badge({ tone = "neutral", className, children, ...rest }: HTMLAttributes<HTMLSpanElement> & { tone?: Tone }) {
  return (
    <span
      className={cn(
        "inline-flex h-6 items-center gap-1 whitespace-nowrap rounded-md border px-2 text-2xs font-semibold",
        tones[tone],
        className,
      )}
      {...rest}
    >
      {children}
    </span>
  );
}

const dotTones: Record<Tone, string> = {
  neutral: "bg-subtle",
  ok: "bg-ok",
  warn: "bg-warn",
  crit: "bg-crit",
  accent: "bg-accent",
  frost: "bg-frost",
};

export function StatusDot({ tone = "neutral", pulse, label, className }: { tone?: Tone; pulse?: boolean; label?: string; className?: string }) {
  return (
    <span className={cn("relative inline-flex size-2 shrink-0", className)} role={label ? "img" : undefined} aria-label={label}>
      {pulse && <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", dotTones[tone])} />}
      <span className={cn("relative inline-flex size-2 rounded-full", dotTones[tone])} />
    </span>
  );
}

const meterTones: Record<string, string> = { ok: "bg-ok", warn: "bg-warn", crit: "bg-crit", accent: "bg-accent", frost: "bg-frost" };

/** Thin horizontal bar; value 0..100. */
export function Meter({
  value,
  tone = "accent",
  className,
  label,
  segments,
}: {
  value: number;
  tone?: "ok" | "warn" | "crit" | "accent" | "frost";
  className?: string;
  label?: string;
  /** Optional stacked segments [{value, className}] instead of a single bar. */
  segments?: { value: number; className: string; label?: string }[];
}) {
  const v = Math.max(0, Math.min(100, value || 0));
  return (
    <div
      role="meter"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(v)}
      aria-label={label}
      className={cn("flex h-1.5 w-full overflow-hidden rounded-full bg-surface-3", className)}
    >
      {segments ? (
        segments.map((s, i) => (
          <div key={i} title={s.label} className={cn("h-full transition-[width] duration-500", s.className)} style={{ width: `${Math.max(0, Math.min(100, s.value))}%` }} />
        ))
      ) : (
        <div className={cn("h-full rounded-full transition-[width] duration-500", meterTones[tone])} style={{ width: `${v}%` }} />
      )}
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return (
    <svg className={cn("size-4 animate-spin", className)} viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeOpacity="0.2" strokeWidth="3" />
      <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
    </svg>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded bg-surface-3", className)} aria-hidden="true" />;
}

export function EmptyState({ icon, title, children, action, className }: { icon?: ReactNode; title: string; children?: ReactNode; action?: ReactNode; className?: string }) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-2 px-6 py-12 text-center", className)}>
      {icon && <div className="mb-1 text-subtle [&_svg]:size-7">{icon}</div>}
      <p className="font-medium text-fg">{title}</p>
      {children && <div className="max-w-md text-sm text-muted">{children}</div>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="rounded border border-border bg-surface-2 px-1 py-px text-2xs text-muted">{children}</kbd>;
}

/** The page title as shown in the top bar. */
function BarTitle({ title, className, ...rest }: { title: string; className?: string; "aria-current"?: "page" }) {
  return (
    <h1 className={cn("truncate text-lg font-bold leading-tight tracking-tight", className)} {...rest}>
      {title}
    </h1>
  );
}

/**
 * The page's title, description and main actions. Inside the app shell they
 * go to the top bar (see app/pageSlots); actions stay in the page on phones,
 * where the top bar has no room. Elsewhere it renders as a row in the page.
 */
export function PageHeader({
  title,
  description,
  actions,
  children,
  inline,
  parent,
}: {
  title: string;
  /** Shown before the title as a breadcrumb ("Settings › Appearance"). */
  parent?: { label: string; to: string };
  description?: ReactNode;
  actions?: ReactNode;
  children?: ReactNode;
  /** Description on the title's line ("Title • description") on wide screens, below it on phones. */
  inline?: boolean;
}) {
  const slots = usePageSlots();
  const wide = useMedia("(min-width: 768px)");
  if (!slots.title) {
    return (
      <div className="mb-5 flex flex-wrap items-end justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight">{parent ? `${parent.label} › ${title}` : title}</h1>
          {description && <p className="mt-1 text-sm text-muted">{description}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
        {children}
      </div>
    );
  }
  const actionsInBar = !!actions && wide && !!slots.actions;
  return (
    <>
      {createPortal(
        inline ? (
          <div className="flex min-w-0 flex-col md:flex-row md:items-center md:gap-2.5">
            <BarTitle title={title} className="shrink-0" />
            {description && (
              <div className="flex min-w-0 items-center gap-2.5 text-xs text-muted md:text-sm">
                <span className="text-subtle max-md:hidden" aria-hidden="true">
                  •
                </span>
                {description}
              </div>
            )}
          </div>
        ) : (
          <div className="min-w-0">
            {parent ? (
              <nav aria-label="Breadcrumb" className="min-w-0">
                <ol className="flex min-w-0 items-center gap-1.5 text-lg leading-tight tracking-tight">
                  <li className="shrink-0">
                    <Link to={parent.to} className="-my-2 inline-flex min-h-10 items-center font-semibold text-muted transition-colors hover:text-fg">
                      {parent.label}
                    </Link>
                  </li>
                  <li className="flex min-w-0 items-center gap-1.5">
                    <ChevronRight className="size-4 shrink-0 text-subtle" aria-hidden="true" />
                    <BarTitle title={title} aria-current="page" />
                  </li>
                </ol>
              </nav>
            ) : (
              <BarTitle title={title} />
            )}
            {description && <div className="truncate text-xs text-muted max-sm:hidden">{description}</div>}
          </div>
        ),
        slots.title,
      )}
      {actionsInBar && createPortal(<div className="flex items-center gap-2">{actions}</div>, slots.actions!)}
      {((actions && !actionsInBar) || children) && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          {!actionsInBar && actions}
          {children}
        </div>
      )}
    </>
  );
}

/**
 * Label/value pair used in detail lists. `mono` now only lines numbers up
 * (tabular figures, UI font); the name is kept for existing callers.
 */
export function Field({ label, children, mono }: { label: ReactNode; children: ReactNode; mono?: boolean }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-3 py-1.5">
      <dt className="shrink-0 text-muted">{label}</dt>
      <dd className={cn("min-w-0 truncate text-right", mono && "num text-ui")}>{children}</dd>
    </div>
  );
}

export function ErrorNote({ error, className }: { error: unknown; className?: string }) {
  if (!error) return null;
  const msg = error instanceof Error ? error.message : String(error);
  return (
    <div role="alert" className={cn("rounded-md border border-crit/30 bg-crit-soft px-3 py-2 text-sm text-crit", className)}>
      {msg}
    </div>
  );
}

export function Notice({ tone = "frost", icon, children, className }: { tone?: Tone; icon?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn("flex items-start gap-2.5 rounded-md border px-3 py-2.5 text-sm [&>svg]:mt-0.5 [&>svg]:size-4 [&>svg]:shrink-0", tones[tone], "normal-case tracking-normal", className)}>
      {icon}
      <div className="min-w-0 text-fg">{children}</div>
    </div>
  );
}

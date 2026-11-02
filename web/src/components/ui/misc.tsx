// SPDX-License-Identifier: AGPL-3.0-only
import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/cn";

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
        "inline-flex h-5 items-center gap-1 whitespace-nowrap rounded border px-1.5 text-2xs font-medium uppercase tracking-wide",
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

/** Page title row with optional actions. */
export function PageHeader({ title, description, actions, children }: { title: string; description?: ReactNode; actions?: ReactNode; children?: ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
      <div className="min-w-0">
        <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-0.5 text-sm text-muted">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      {children}
    </div>
  );
}

/** Label/value pair used in detail lists. */
export function Field({ label, children, mono }: { label: ReactNode; children: ReactNode; mono?: boolean }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-3 py-1.5">
      <dt className="shrink-0 text-muted">{label}</dt>
      <dd className={cn("min-w-0 truncate text-right", mono && "num text-[0.8125rem]")}>{children}</dd>
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

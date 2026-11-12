// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Building blocks shared by every tab of the Updates page (containers, OS
 * packages, Homebrew), so each source looks and behaves the same: a toolbar
 * with "Check for updates" and "Update all", a summary card, a confirmation
 * dialog, and the live run panel.
 */
import { useState, type ReactNode } from "react";
import { ChevronRight, Download, RefreshCw, ShieldCheck, TriangleAlert } from "lucide-react";
import { readPref, setPref } from "@/lib/prefs";
import { ApiError } from "@/lib/api";
import { ago, datetime, plural } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card, Stat } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { ErrorNote, Notice, Skeleton, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import type { CheckResult, JobSnapshot, RunRecord } from "./api";

/** toast.error, except when the user closed the approval dialog. */
export function ignoreCanceled(title: string) {
  return (e: unknown) => {
    if (e instanceof ApiError && e.code === "elevation_canceled") return;
    toast.error(title, e);
  };
}

/** Source details on the left, actions on the right. */
export function SourceToolbar({ info, actions }: { info: ReactNode; actions: ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 flex-wrap items-center gap-2 text-sm text-muted">{info}</div>
      <div className="flex flex-wrap items-center gap-2">{actions}</div>
    </div>
  );
}

export function CheckButton({
  checked,
  checking,
  disabled,
  size,
  onClick,
}: {
  checked: boolean;
  checking: boolean;
  disabled?: boolean;
  size?: "sm" | "md";
  onClick: () => void;
}) {
  return (
    <Button
      variant={checked ? "secondary" : "primary"}
      size={size}
      icon={<RefreshCw className={cn(checking && "animate-spin")} />}
      disabled={checking || disabled}
      onClick={onClick}
    >
      {checking ? (
        "Checking…"
      ) : (
        <span>
          Check<span className="max-sm:hidden"> for updates</span>
        </span>
      )}
    </Button>
  );
}

export function UpdateAllButton({ count, disabled, onClick }: { count: number; disabled?: boolean; onClick: () => void }) {
  return (
    <Button variant="primary" icon={<Download />} disabled={disabled} onClick={onClick}>
      Update all <span className="num">({count})</span>
    </Button>
  );
}

/** The "Last checked" cell of a summary card. */
function LastChecked({ at, checking, sub }: { at?: string | number | null; checking: boolean; sub: ReactNode }) {
  return (
    <Stat
      label="Last checked"
      value={
        checking ? (
          <span className="inline-flex items-center gap-1.5 text-sm font-medium text-accent">
            <Spinner /> Checking…
          </span>
        ) : (
          <span title={at ? datetime(at) : undefined}>{at ? ago(at) : "never"}</span>
        )
      }
      sub={sub}
    />
  );
}

/**
 * Summary card: three source-specific stats, then when the last check ran,
 * with a reassurance line underneath.
 */
export function UpdateSummary({
  stats,
  checkedAt,
  checking,
  checkedSub,
  note,
}: {
  stats: ReactNode;
  checkedAt?: string | number | null;
  checking: boolean;
  checkedSub: ReactNode;
  note: ReactNode;
}) {
  return (
    <Card className="mb-4">
      <div className="grid grid-cols-2 gap-x-4 gap-y-3 p-4 sm:grid-cols-4">
        {stats}
        <LastChecked at={checkedAt} checking={checking} sub={checkedSub} />
      </div>
      <div className="flex items-start gap-2 border-t border-border px-4 py-2 text-xs text-muted">
        <ShieldCheck className="mt-px size-3.5 shrink-0 text-frost" />
        <span>{note}</span>
      </div>
    </Card>
  );
}

/** "Pending" stat for a package source's last check. */
export function PendingStat({
  check,
  unit = ["package", "packages"],
  from,
}: {
  check?: CheckResult;
  unit?: [string, string];
  from: string;
}) {
  const n = check?.updates.length ?? 0;
  const ok = !!check && !check.error;
  return (
    <Stat
      label="Pending"
      value={ok ? n : "—"}
      unit={ok ? plural(n, unit[0], unit[1]) : undefined}
      sub={check?.stale ? "outdated, check again" : check ? (n ? from : "up to date") : "not checked yet"}
    />
  );
}

/** One warning or reassurance line in a confirmation dialog. */
export function DialogPoint({ tone = "warn", children }: { tone?: "warn" | "info"; children: ReactNode }) {
  return (
    <li className="flex gap-2">
      {tone === "warn" ? (
        <TriangleAlert className="mt-0.5 size-4 shrink-0 text-warn" />
      ) : (
        <ShieldCheck className="mt-0.5 size-4 shrink-0 text-frost" />
      )}
      <span>{children}</span>
    </li>
  );
}

/** Confirmation before "Update all": the command that runs and what to expect. */
export function UpdateAllDialog({
  open,
  onOpenChange,
  count,
  description,
  commandLabel,
  command,
  points,
  children,
  fake,
  loading,
  disabled,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  count: number;
  description: ReactNode;
  commandLabel: string;
  command?: string;
  points: ReactNode;
  children?: ReactNode;
  fake?: boolean;
  loading: boolean;
  disabled?: boolean;
  onConfirm: () => void;
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Install ${count} ${plural(count, "update")}?`}
      description={description}
      footer={
        <>
          <Button autoFocus onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button variant="primary" icon={<Download />} loading={loading} disabled={disabled} onClick={onConfirm}>
            Update all
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        {command && (
          <div>
            <div className="mb-1 text-2xs font-semibold uppercase tracking-wider text-subtle">{commandLabel}</div>
            <code className="num block break-all rounded-md border border-border bg-surface-2 px-2.5 py-1.5 text-xs">{command}</code>
          </div>
        )}
        <ul className="flex flex-col gap-2 text-muted">{points}</ul>
        {children}
        {fake && <Notice tone="warn">Development mode: this runs a simulated update script. Nothing is installed.</Notice>}
      </div>
    </Dialog>
  );
}

/** A run result the user dismissed stays hidden (per source, per browser). */
export function useDismissedRun(key: string): [string | null, (id: string) => void] {
  const [v, setV] = useState<string | null>(() => readPref(key));
  return [
    v,
    (id) => {
      setPref(key, id);
      setV(id);
    },
  ];
}

/**
 * Whether the run panel shows: while running, and afterwards until it is
 * dismissed or a newer check replaced the result.
 */
export function runVisible(s: { running?: JobSnapshot; lastRun?: RunRecord; lastCheck?: CheckResult }, dismissed: string | null) {
  if (s.running) return true;
  const r = s.lastRun;
  if (!r || r.id === dismissed) return false;
  const c = s.lastCheck;
  // A check after the run hides its result, except the one that ran on its
  // own right after it (to refresh the list).
  const superseded =
    !!r.finishedAt && !!c && !c.stale && c.afterRun !== r.id && new Date(c.at).getTime() > new Date(r.finishedAt).getTime();
  return !superseded;
}

/** A collapsible card (details/summary) for secondary lists; children are <li> rows. */
export function Collapsible({
  title,
  icon,
  count,
  children,
  tone,
}: {
  title: string;
  icon: ReactNode;
  count: number;
  children: ReactNode;
  tone?: "warn" | "ok" | "muted";
}) {
  return (
    <Card className="mt-4">
      <details className="group">
        <summary className="flex min-h-10 cursor-pointer list-none items-center gap-2 px-4 py-2 [&::-webkit-details-marker]:hidden">
          <ChevronRight className="size-4 shrink-0 text-subtle transition-transform group-open:rotate-90" aria-hidden="true" />
          <span className={cn("[&_svg]:size-4", tone === "warn" ? "text-warn" : tone === "muted" ? "text-subtle" : "text-ok")}>{icon}</span>
          <h2 className="text-ui font-semibold tracking-tight">{title}</h2>
          <span className="num text-xs text-subtle">{count}</span>
        </summary>
        <ul className="border-t border-border">{children}</ul>
      </details>
    </Card>
  );
}

/** Failed or outdated check notices shared by package sources. */
export function CheckNotices({ check, running }: { check?: CheckResult; running: boolean }) {
  return (
    <>
      {check?.error && <ErrorNote className="mb-4" error={`Last check failed: ${check.error}`} />}
      {check?.stale && !running && (
        <Notice tone="frost" className="mb-4">
          This list is from before the last update. Check again to see what is still pending.
        </Notice>
      )}
    </>
  );
}

export function TabSkeleton() {
  return (
    <div>
      <Skeleton className="mb-4 h-8 w-48" />
      <Skeleton className="mb-4 h-28 w-full" />
      <Skeleton className="h-64 w-full" />
    </div>
  );
}

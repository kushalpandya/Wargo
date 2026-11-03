// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Check, ChevronRight, CircleAlert, CircleCheck, FileWarning, Power, RotateCw, ScrollText, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useTopic } from "@/lib/ws";
import { datetime, duration } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card, CardHeader } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Badge, Meter, Notice, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import type { JobSnapshot, PkgStatus, RunRecord } from "./api";

export const STAGES = ["Preparing", "Downloading", "Installing", "Post-install hooks", "Done"] as const;
const MAX_LINES = 2000;

/** Reboot with a confirmation dialog (Cancel focused). Elevation is prompted by the API layer. */
export function RebootButton({ size = "sm", variant = "secondary" }: { size?: "xs" | "sm" | "md"; variant?: "primary" | "secondary" }) {
  const [open, setOpen] = useState(false);
  const m = useMutation({
    mutationFn: () => api.post("/api/system/power", { action: "reboot" }),
    onSuccess: () => {
      setOpen(false);
      toast.info("Rebooting", "The server is restarting. This page reconnects when it is back.");
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "elevation_canceled") return;
      toast.error("Reboot failed", e);
    },
  });
  return (
    <>
      <Button size={size} variant={variant} icon={<Power />} onClick={() => setOpen(true)}>
        Reboot
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title="Reboot the server?"
        description="All running containers, terminals and downloads will be interrupted while the host restarts."
        confirmLabel="Reboot now"
        danger
        loading={m.isPending}
        onConfirm={() => m.mutate()}
      />
    </>
  );
}

function Stepper({ stage, state }: { stage?: string; state: "running" | "ok" | "failed" }) {
  const idx = state === "ok" ? STAGES.length - 1 : Math.max(0, STAGES.indexOf((stage ?? "Preparing") as (typeof STAGES)[number]));
  return (
    <ol className="grid grid-cols-5 gap-1.5" aria-label="Upgrade stages">
      {STAGES.map((s, i) => {
        const done = i < idx || (state === "ok" && i === idx);
        const cur = i === idx && state !== "ok";
        const fail = cur && state === "failed";
        return (
          <li key={s} className="min-w-0" aria-current={cur ? "step" : undefined}>
            <div
              className={cn(
                "h-1 rounded-full",
                done ? "bg-ok" : fail ? "bg-crit" : cur ? "bg-accent" : "bg-surface-3",
              )}
            />
            <div
              className={cn(
                "mt-1.5 flex min-w-0 items-center gap-1 text-2xs font-medium uppercase tracking-wide sm:text-xs sm:normal-case sm:tracking-normal",
                done ? "text-fg" : fail ? "text-crit" : cur ? "text-accent" : "text-subtle",
                !cur && "max-sm:sr-only",
              )}
            >
              {done ? (
                <Check className="size-3.5 shrink-0 text-ok" aria-hidden />
              ) : fail ? (
                <X className="size-3.5 shrink-0" aria-hidden />
              ) : cur ? (
                <Spinner className="size-3.5 shrink-0" />
              ) : (
                <span className="num hidden w-3.5 shrink-0 text-center sm:inline">{i + 1}</span>
              )}
              <span className={cn("truncate", cur && "max-sm:whitespace-nowrap max-sm:overflow-visible")}>{s}</span>
              {done && <span className="sr-only">(done)</span>}
            </div>
          </li>
        );
      })}
    </ol>
  );
}

function LogView({ lines, open, onToggle, live }: { lines: string[]; open: boolean; onToggle: () => void; live: boolean }) {
  const ref = useRef<HTMLPreElement>(null);
  const stick = useRef(true);
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines, open]);
  return (
    <div className="border-t border-border">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        aria-controls="pkg-log"
        className="flex min-h-10 w-full items-center gap-2 px-4 py-2 text-left text-ui font-medium hover:bg-surface-2"
      >
        <ChevronRight className={cn("size-3.5 text-subtle transition-transform", open && "rotate-90")} />
        <ScrollText className="size-4 text-subtle" />
        Output log
        <span className="num text-xs font-normal text-subtle">{lines.length} lines</span>
        {live && <span className="ml-auto text-xs font-normal text-muted">live</span>}
      </button>
      {open && (
        <pre
          id="pkg-log"
          ref={ref}
          tabIndex={0}
          aria-label="Package manager output"
          onScroll={(e) => {
            const el = e.currentTarget;
            stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
          }}
          className="num scroll-area max-h-[22rem] min-h-32 overflow-auto whitespace-pre-wrap break-all border-t border-border bg-bg px-4 py-2.5 text-xs leading-relaxed text-muted"
        >
          {lines.length ? lines.join("\n") : "Waiting for output…"}
        </pre>
      )}
    </div>
  );
}

function useElapsed(start?: string, end?: string) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (end || !start) return;
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, [start, end]);
  if (!start) return 0;
  return ((end ? new Date(end).getTime() : now) - new Date(start).getTime()) / 1000;
}

/**
 * Live progress of a running upgrade, or the result of the last one.
 * Works after a page reload: the running job comes from GET /api/pkg/backend.
 */
export function RunPanel({ status, onDismiss }: { status: PkgStatus; onDismiss?: () => void }) {
  const running = status.running;
  const [job, setJob] = useState<JobSnapshot | undefined>(running);
  const [lines, setLines] = useState<string[]>([]);
  const [logOpen, setLogOpen] = useState(!!running);
  const jobId = running?.id ?? job?.id;
  const run: RunRecord | undefined = !running ? status.lastRun : undefined;

  useEffect(() => {
    if (running) setJob((j) => (j?.id === running.id && j.logLines > running.logLines ? j : running));
  }, [running]);

  // Initial log: from the job while it lives in this Wargo process, else from the file.
  const loadedFor = useRef<string | null>(null);
  useEffect(() => {
    const key = running ? `job:${running.id}` : run ? `run:${run.id}` : null;
    if (!key || loadedFor.current === key || (loadedFor.current?.startsWith("job:") && run?.jobId === loadedFor.current.slice(4))) return;
    loadedFor.current = key;
    const p = running
      ? api.get<{ log: string[] }>(`/api/jobs/${running.id}`).then((r) => r.log ?? [])
      : api.get<{ lines: string[] }>(`/api/pkg/log?id=${encodeURIComponent(run!.id)}`).then((r) => r.lines ?? []);
    p.then((l) => setLines((cur) => (l.length >= cur.length ? l.slice(-MAX_LINES) : cur))).catch(() => {});
  }, [running, run]);

  useTopic<JobSnapshot>(jobId ? "jobs" : null, (s) => {
    if (s.id === jobId) setJob(s);
  });
  useTopic<{ id: string; lines: string[] }>(jobId ? "jobs.log" : null, (m) => {
    if (m.id !== jobId) return;
    setLines((cur) => {
      const next = cur.concat((m.lines ?? []).filter((l) => l.trim() !== ""));
      return next.length > MAX_LINES ? next.slice(-MAX_LINES) : next;
    });
  });

  const live = !!running && job?.state !== "succeeded" && job?.state !== "failed";
  const finishedAt = run?.finishedAt ?? job?.finishedAt;
  const elapsed = useElapsed(running?.startedAt ?? run?.startedAt, live ? undefined : finishedAt);
  const fake = status.fake || run?.fake || job?.title.includes("simulated");

  if (!running && !run) return null;

  const frac = live ? Math.max(0, job?.progress ?? 0) : run?.ok ? 1 : Math.max(0, job?.progress ?? 0);
  const ok = run ? run.ok : job?.state === "succeeded";
  const state: "running" | "ok" | "failed" = live ? "running" : ok ? "ok" : "failed";
  const stage = live ? job?.stage : state === "ok" ? "Done" : (run?.stage ?? job?.stage);

  return (
    <Card className="mb-4 overflow-hidden" aria-label="System upgrade">
      <CardHeader
        title={live ? "Installing updates" : ok ? "Upgrade complete" : "Upgrade failed"}
        icon={live ? <Spinner className="text-accent" /> : ok ? <CircleCheck className="text-ok" /> : <CircleAlert className="text-crit" />}
        meta={`${duration(elapsed)}${run?.backend || status.backend ? ` · ${run?.backend ?? status.backend}` : ""}`}
        actions={
          <>
            {fake && <Badge tone="warn">simulated</Badge>}
            {!live && onDismiss && (
              <IconButton size="sm" label="Dismiss result" onClick={onDismiss}>
                <X />
              </IconButton>
            )}
          </>
        }
      />
      <div className="flex flex-col gap-4 p-4">
        {live && (
          <Notice tone="warn">
            Don't reboot or power off the server until this finishes. The upgrade runs as a system service, so closing this page
            or restarting Wargo is safe.
          </Notice>
        )}
        <Stepper stage={stage} state={state} />
        <div>
          <div className="mb-1.5 flex items-baseline gap-3">
            <span className="num text-2xl font-medium leading-none">{Math.round(frac * 100)}%</span>
            <span className="min-w-0 flex-1 truncate text-sm text-muted" aria-live="polite">
              {live ? (
                <>
                  {job?.stage ?? "Starting"}
                  {job?.current ? (
                    <>
                      {" · "}
                      <span className="num text-fg">{job.current}</span>
                    </>
                  ) : null}
                </>
              ) : ok ? (
                `Finished ${datetime(finishedAt)}`
              ) : (
                run?.error ?? job?.error ?? "The upgrade did not finish"
              )}
            </span>
          </div>
          <Meter value={frac * 100} tone={state === "failed" ? "crit" : state === "ok" ? "ok" : "accent"} label="Overall progress" className="h-2" />
        </div>
        {!live && run && <ResultDetails run={run} elapsed={elapsed} />}
      </div>
      <LogView lines={lines} open={logOpen} onToggle={() => setLogOpen((o) => !o)} live={live} />
    </Card>
  );
}

function ResultDetails({ run, elapsed }: { run: RunRecord; elapsed: number }) {
  const files = run.configFiles ?? [];
  const fresh = files.filter((f) => f.new);
  const older = files.filter((f) => !f.new);
  const [showOlder, setShowOlder] = useState(false);
  return (
    <div className="flex flex-col gap-3">
      <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Fact label="Duration" value={duration(elapsed)} />
        <Fact label="Packages" value={run.upgraded > 0 ? String(run.upgraded) : String(run.packages || "—")} />
        <Fact label="Exit status" value={run.exitStatus === undefined ? "—" : String(run.exitStatus)} tone={run.exitStatus ? "crit" : undefined} />
        <Fact label="Reboot" value={run.rebootRequired ? "Required" : "Not needed"} tone={run.rebootRequired ? "warn" : undefined} />
      </dl>
      {run.rebootRequired && (
        <div className="flex flex-wrap items-center gap-3 rounded-md border border-warn/30 bg-warn-soft px-3 py-2.5">
          <RotateCw className="size-4 shrink-0 text-warn" />
          <div className="min-w-0 flex-1 text-sm">
            <span className="font-medium">Reboot required.</span> <span className="text-muted">{run.rebootReason}</span>
          </div>
          <RebootButton />
        </div>
      )}
      {files.length > 0 && (
        <div className="rounded-md border border-border">
          <div className="flex items-center gap-2 border-b border-border px-3 py-2 text-ui font-medium">
            <FileWarning className="size-4 text-warn" />
            Config files to review
            {fresh.length > 0 && <Badge tone="warn">{fresh.length} new</Badge>}
          </div>
          <ul className="text-ui">
            {fresh.map((f) => (
              <li key={f.path} className="num flex items-center gap-2 border-b border-border px-3 py-1.5 last:border-0">
                <span className="min-w-0 flex-1 break-all">{f.path}</span>
                <Badge tone="warn">new</Badge>
              </li>
            ))}
            {fresh.length === 0 && <li className="px-3 py-1.5 text-muted">This upgrade created no new config files.</li>}
          </ul>
          {older.length > 0 && (
            <>
              <button
                type="button"
                onClick={() => setShowOlder((o) => !o)}
                aria-expanded={showOlder}
                className="flex min-h-8 w-full items-center gap-1.5 border-t border-border px-3 py-1.5 text-left text-xs text-muted hover:bg-surface-2 hover:text-fg"
              >
                <ChevronRight className={cn("size-3.5 transition-transform", showOlder && "rotate-90")} />
                {older.length} older file{older.length === 1 ? "" : "s"} still awaiting review
              </button>
              {showOlder && (
                <ul className="border-t border-border text-xs text-muted">
                  {older.map((f) => (
                    <li key={f.path} className="num flex gap-3 px-3 py-1">
                      <span className="min-w-0 flex-1 break-all">{f.path}</span>
                      <span className="shrink-0 text-subtle">{datetime(f.modTime)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
          <p className="border-t border-border px-3 py-2 text-xs text-muted">
            Merge them over SSH
            {run.backend === "pacman" || run.backend === "pamac" ? (
              <>
                , e.g. with <span className="num text-fg">sudo DIFFPROG=vimdiff pacdiff</span>
              </>
            ) : (
              <>
                {" "}by comparing them with <span className="num text-fg">diff</span>
              </>
            )}
            .
          </p>
        </div>
      )}
    </div>
  );
}

function Fact({ label, value, tone }: { label: string; value: string; tone?: "warn" | "crit" }) {
  return (
    <div className="min-w-0 rounded-md border border-border bg-surface-2 px-3 py-2">
      <dt className="text-2xs font-semibold uppercase tracking-wider text-subtle">{label}</dt>
      <dd className={cn("num mt-0.5 truncate text-sm", tone === "warn" && "text-warn", tone === "crit" && "text-crit")}>{value}</dd>
    </div>
  );
}

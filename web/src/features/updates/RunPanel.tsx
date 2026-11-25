// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { useMutation } from "@tanstack/react-query";
import { Check, ChevronRight, CircleAlert, CircleCheck, CircleHelp, FileWarning, Power, RotateCw, ScrollText, Trash2, X } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useTopic } from "@/lib/ws";
import { datetime, duration, listAnd } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card, CardHeader } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Badge, Meter, Notice, OutputBlock, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import type { Diagnosis, JobSnapshot, RunRecord } from "./api";

/** Stepper stages of an OS package upgrade (the backend's stage names). */
export const PKG_STAGES = ["Preparing", "Downloading", "Installing", "Post-install hooks", "Done"] as const;
/** Stepper stages of a Homebrew update. */
export const BREW_STAGES = ["Preparing", "Downloading", "Installing", "Cleaning up", "Done"] as const;
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

type RunState = "running" | "ok" | "failed" | "waiting";

function Stepper({ stages, stage, state }: { stages: readonly string[]; stage?: string; state: RunState }) {
  const idx = state === "ok" ? stages.length - 1 : Math.max(0, stages.indexOf(stage ?? stages[0]));
  return (
    <ol className="grid grid-cols-5 gap-1.5" aria-label="Update stages">
      {stages.map((s, i) => {
        const done = i < idx || (state === "ok" && i === idx);
        const cur = i === idx && state !== "ok";
        const fail = cur && state === "failed";
        const wait = cur && state === "waiting";
        return (
          <li key={s} className="min-w-0" aria-current={cur ? "step" : undefined}>
            <div
              className={cn(
                "h-1 rounded-full",
                done ? "bg-ok" : fail ? "bg-crit" : wait ? "bg-warn" : cur ? "bg-accent" : "bg-surface-3",
              )}
            />
            <div
              className={cn(
                "mt-1.5 flex min-w-0 items-center gap-1 text-2xs font-medium uppercase tracking-wide sm:text-xs sm:normal-case sm:tracking-normal",
                done ? "text-fg" : fail ? "text-crit" : wait ? "text-warn" : cur ? "text-accent" : "text-subtle",
                !cur && "max-sm:sr-only",
              )}
            >
              {done ? (
                <Check className="size-3.5 shrink-0 text-ok" aria-hidden />
              ) : fail ? (
                <X className="size-3.5 shrink-0" aria-hidden />
              ) : wait ? (
                <CircleHelp className="size-3.5 shrink-0" aria-hidden />
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

function LogView({ id, lines, open, onToggle, live }: { id: string; lines: string[]; open: boolean; onToggle: () => void; live: boolean }) {
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
        aria-controls={id}
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
          id={id}
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

/** What RunPanel needs from a source's status (OS packages or Homebrew). */
export interface RunSource {
  /** Short id for element ids ("pkg", "brew"). */
  id: string;
  running?: JobSnapshot;
  lastRun?: RunRecord;
  backend: string;
  fake?: boolean;
  /** GET endpoint returning {lines} for a finished run (?id=). */
  logPath: string;
  stages: readonly string[];
  /** Shown while the update runs. */
  liveNotice: ReactNode;
  /** OS upgrades report reboots and config files. */
  system?: boolean;
  /** Retry a run that failed on package conflicts, removing them (OS upgrades whose package manager allows it). */
  resolveConflicts?: { start: () => void; pending: boolean };
}

/**
 * Live progress of a running update, or the result of the last one.
 * Works after a page reload: the running job comes from the source's status.
 */
export function RunPanel({ source, onDismiss }: { source: RunSource; onDismiss?: () => void }) {
  const running = source.running;
  const [job, setJob] = useState<JobSnapshot | undefined>(running);
  const [lines, setLines] = useState<string[]>([]);
  const [logOpen, setLogOpen] = useState(!!running);
  const jobId = running?.id ?? job?.id;
  const run: RunRecord | undefined = !running ? source.lastRun : undefined;

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
      : api.get<{ lines: string[] }>(`${source.logPath}?id=${encodeURIComponent(run!.id)}`).then((r) => r.lines ?? []);
    p.then((l) => setLines((cur) => (l.length >= cur.length ? l.slice(-MAX_LINES) : cur))).catch(() => {});
  }, [running, run, source.logPath]);

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
  const fake = source.fake || run?.fake || job?.title.includes("simulated");

  if (!running && !run) return null;

  const frac = live ? Math.max(0, job?.progress ?? 0) : run?.ok ? 1 : Math.max(0, job?.progress ?? 0);
  const ok = run ? run.ok : job?.state === "succeeded";
  // A run that stopped on conflicts Wargo can resolve waits for the owner's
  // decision: nothing failed and nothing changed.
  const waiting = !live && !!run && !run.ok && run.diagnosis?.code === "pkg.conflict" && !!source.resolveConflicts;
  const state: RunState = live ? "running" : ok ? "ok" : waiting ? "waiting" : "failed";
  const stage = live ? job?.stage : state === "ok" ? "Done" : (run?.stage ?? job?.stage);

  return (
    <Card className="mb-4 overflow-hidden" aria-label={live ? "Update progress" : "Update result"}>
      <CardHeader
        title={live ? "Installing updates" : ok ? "Updates installed" : waiting ? "Updates need your approval" : "Update failed"}
        icon={
          live ? (
            <Spinner className="text-accent" />
          ) : ok ? (
            <CircleCheck className="text-ok" />
          ) : waiting ? (
            <CircleHelp className="text-warn" />
          ) : (
            <CircleAlert className="text-crit" />
          )
        }
        meta={`${duration(elapsed)}${run?.backend || source.backend ? ` · ${run?.backend ?? source.backend}` : ""}`}
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
        {live && <Notice tone="warn">{source.liveNotice}</Notice>}
        <Stepper stages={source.stages} stage={stage} state={state} />
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
              ) : waiting ? (
                "Stopped before installing"
              ) : (
                run?.diagnosis?.title ?? run?.error ?? job?.error ?? "The update did not finish"
              )}
            </span>
          </div>
          <Meter
            value={frac * 100}
            tone={state === "failed" ? "crit" : state === "waiting" ? "warn" : state === "ok" ? "ok" : "accent"}
            label="Overall progress"
            className="h-2"
          />
        </div>
        {waiting && run ? (
          <ConflictDecision run={run} resolve={source.resolveConflicts!} onDismiss={onDismiss} />
        ) : (
          !live && run && <ResultDetails run={run} elapsed={elapsed} system={!!source.system} />
        )}
      </div>
      <LogView id={`${source.id}-log`} lines={lines} open={logOpen} onToggle={() => setLogOpen((o) => !o)} live={live} />
    </Card>
  );
}

function ResultDetails({ run, elapsed, system }: { run: RunRecord; elapsed: number; system: boolean }) {
  const files = run.configFiles ?? [];
  const fresh = files.filter((f) => f.new);
  const older = files.filter((f) => !f.new);
  const [showOlder, setShowOlder] = useState(false);
  return (
    <div className="flex flex-col gap-3">
      {run.diagnosis && <DiagnosisNote d={run.diagnosis} />}
      {run.removed && run.removed.length > 0 && <RemovedNote run={run} />}
      <dl className={cn("grid gap-3", system ? "grid-cols-2 sm:grid-cols-4" : "grid-cols-3")}>
        <Fact label="Duration" value={duration(elapsed)} />
        <Fact label="Packages" value={run.upgraded > 0 ? String(run.upgraded) : String(run.packages || "—")} />
        <Fact label="Exit status" value={run.exitStatus == null ? "—" : String(run.exitStatus)} tone={run.exitStatus ? "crit" : undefined} />
        {system && <Fact label="Reboot" value={run.rebootRequired ? "Required" : "Not needed"} tone={run.rebootRequired ? "warn" : undefined} />}
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

const withVersion = (name: string, version?: string) => (version ? `${name} ${version}` : name);

/** Why a run failed: the explanation and the log lines it comes from. */
function DiagnosisNote({ d }: { d: Diagnosis }) {
  return (
    <Notice tone="crit" icon={<CircleAlert />}>
      <p className="font-medium">{d.title}</p>
      <p className="mt-0.5 text-muted">{d.summary}</p>
      {d.lines && d.lines.length > 0 && (
        <OutputBlock className="mt-2 max-h-48 bg-surface" label="Package manager output">
          {d.lines.join("\n")}
        </OutputBlock>
      )}
    </Notice>
  );
}

/**
 * The package manager stopped before installing because installed packages
 * conflict with the updates, and it won't remove them unasked. Laid out like
 * Synaptic's and Pamac's summaries: a "To remove" list with the reason for
 * each, then buttons that say what happens (the approval prompt that
 * follows names the removal again).
 */
function ConflictDecision({ run, resolve, onDismiss }: { run: RunRecord; resolve: NonNullable<RunSource["resolveConflicts"]>; onDismiss?: () => void }) {
  const d = run.diagnosis!;
  const [details, setDetails] = useState(false);
  // One row per package to remove, with what it conflicts with.
  const rows = new Map<string, { version?: string; with: string[] }>();
  for (const c of d.conflicts ?? []) {
    if (!c.remove) continue;
    const r = rows.get(c.remove) ?? { version: c.removeVersion, with: [] };
    r.with.push(withVersion(c.package, c.version));
    rows.set(c.remove, r);
  }
  const n = rows.size;
  const cancel = () => {
    onDismiss?.();
    toast.info("Updates not installed", "Nothing was installed or removed. The updates are still listed.");
  };
  return (
    <div className="rounded-md border border-warn/30 bg-warn-soft px-3 py-3">
      <p className="text-sm">
        {n > 0 ? (
          <>
            To install these updates, <span className="num">{run.backend}</span> has to remove {n === 1 ? "a package" : `${n} packages`} that
            conflict{n === 1 ? "s" : ""} with them.
          </>
        ) : (
          <>
            To install these updates, <span className="num">{run.backend}</span> has to remove the installed packages that conflict with them. It
            didn't name them; the details show the conflict.
          </>
        )}
      </p>
      {n > 0 && (
        <div className="mt-2.5">
          <h4 className="text-2xs font-semibold uppercase tracking-wider text-subtle">To remove</h4>
          <ul className="scroll-area mt-1 max-h-52 overflow-auto rounded-md border border-border bg-surface">
            {[...rows].map(([name, r]) => (
              <li key={name} className="flex items-start gap-2.5 border-b border-border px-3 py-2 last:border-0">
                <Trash2 className="mt-0.5 size-4 shrink-0 text-warn" aria-hidden />
                <div className="min-w-0 flex-1">
                  <div className="num break-all text-sm text-fg">
                    {name}
                    {r.version && <span className="ml-1.5 text-muted">{r.version}</span>}
                  </div>
                  <div className="text-xs text-muted [overflow-wrap:anywhere]">
                    Conflicts with <span className="num">{listAnd(r.with)}</span>
                  </div>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}
      <p className="mt-2.5 text-xs text-muted">
        Nothing has changed yet. The newer packages usually take over what {n === 1 ? "it" : "they"} provided. If the package lists changed since,{" "}
        <span className="num">{run.backend}</span> may also remove other conflicting packages; the result shows everything it removed.
      </p>
      <button
        type="button"
        onClick={() => setDetails((o) => !o)}
        aria-expanded={details}
        className="-ml-1.5 mt-1 inline-flex min-h-8 items-center gap-1 rounded px-1.5 text-xs text-muted hover:text-fg"
      >
        <ChevronRight className={cn("size-3.5 transition-transform", details && "rotate-90")} aria-hidden />
        {details ? "Hide details" : "Show details"}
      </button>
      {details && (
        <div className="mt-1">
          {d.lines?.length ? (
            <OutputBlock className="max-h-48 bg-surface" label="Package manager output">
              {d.lines.join("\n")}
            </OutputBlock>
          ) : null}
        </div>
      )}
      {/* Cancel first, then the action (GNOME HIG, and Wargo's own dialogs).
          Equal columns keep the two choices the same size: as wide as the
          longer label, or half the row each on phones. */}
      <div className="mt-3 flex justify-end">
        <div className={cn("grid w-full gap-2 sm:w-auto", onDismiss ? "grid-cols-2" : "grid-cols-1")}>
          {onDismiss && (
            <Button size="sm" variant="raised" onClick={cancel}>
              Cancel
            </Button>
          )}
          <Button size="sm" variant="primary" loading={resolve.pending} onClick={resolve.start}>
            Continue
          </Button>
        </div>
      </div>
    </div>
  );
}

/** What a conflict retry removed, and a warning for anything nobody approved. */
function RemovedNote({ run }: { run: RunRecord }) {
  const extra = run.unexpectedRemovals ?? [];
  return (
    <Notice tone={extra.length ? "warn" : "frost"} icon={<Trash2 />}>
      Removed <span className="num">{listAnd(run.removed!)}</span>.
      {extra.length > 0 && (
        <>
          {" "}
          <span className="font-medium">Not approved: {listAnd(extra)}</span> (a new conflict appeared after the failed run). Reinstall{" "}
          {extra.length === 1 ? "it" : "them"} if you need {extra.length === 1 ? "it" : "them"}.
        </>
      )}
    </Notice>
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

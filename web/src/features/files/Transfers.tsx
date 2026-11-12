// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, ArrowUpFromLine, Check, ChevronDown, ChevronUp, Copy, FolderInput, Pause, Play, RotateCcw, X, PackageOpen, AlertTriangle } from "lucide-react";
import { api } from "@/lib/api";
import { bytes, rate } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useSocketStatus, useTopic } from "@/lib/ws";
import { Button, IconButton } from "@/components/ui/button";
import { Meter } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import type { JobSnapshot } from "./api";
import { cancel, clearFinished, pause, resolveConflict, resume, resumeWith, syncWithServer, useUploads, type UploadItem } from "./uploads";

/** Tracks files.* jobs from the "jobs" topic. */
export function useFileJobs(onFinish: (j: JobSnapshot) => void) {
  const [jobs, setJobs] = useState<Record<string, JobSnapshot>>({});
  const cb = useRef(onFinish);
  cb.current = onFinish;
  const initial = useQuery({ queryKey: ["jobs"], queryFn: () => api.get<JobSnapshot[]>("/api/jobs"), staleTime: 5_000 });
  useEffect(() => {
    if (!initial.data) return;
    setJobs((cur) => {
      const next = { ...cur };
      for (const j of initial.data) if (j.kind.startsWith("files.") && j.state === "running" && !next[j.id]) next[j.id] = j;
      return next;
    });
  }, [initial.data]);
  const seen = useRef<Record<string, JobSnapshot["state"]>>({});
  // Job updates sent while the socket was down are lost. On every (re)connect,
  // refetch the job list and end local running jobs the server no longer reports.
  const qc = useQueryClient();
  const status = useSocketStatus();
  const jobsRef = useRef(jobs);
  jobsRef.current = jobs;
  useEffect(() => {
    if (status !== "open") return;
    let alive = true;
    // Judge only jobs running when the request starts: a job that begins while it
    // is in flight may be missing from the response without being gone.
    const asked = new Set(Object.values(jobsRef.current).filter((j) => j.state === "running").map((j) => j.id));
    qc.fetchQuery({ queryKey: ["jobs"], queryFn: () => api.get<JobSnapshot[]>("/api/jobs"), staleTime: 0 })
      .then((list) => {
        if (!alive) return;
        const byId = new Map(list.map((j) => [j.id, j]));
        // Jobs the server finished while we were away (or no longer reports).
        const done: JobSnapshot[] = [];
        for (const j of Object.values(jobsRef.current)) {
          if (j.state !== "running" || !asked.has(j.id)) continue;
          const now = byId.get(j.id);
          if (now?.state === "running") continue;
          // Missing means the server restarted and the job died: never report success.
          done.push(now ?? { ...j, state: "failed", error: "Interrupted. Check the destination." });
        }
        for (const j of done) {
          seen.current[j.id] = j.state;
          cb.current(j);
        }
        if (done.length) {
          setJobs((cur) => {
            const next = { ...cur };
            for (const j of done) if (next[j.id]?.state === "running") next[j.id] = j;
            return next;
          });
        }
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [status, qc]);
  useTopic<JobSnapshot>("jobs", (j) => {
    if (!j.kind.startsWith("files.")) return;
    const prev = seen.current[j.id];
    seen.current[j.id] = j.state;
    if (j.state !== "running" && prev !== j.state) cb.current(j);
    setJobs((cur) => ({ ...cur, [j.id]: j }));
  });
  const dismiss = (id: string) =>
    setJobs((cur) => {
      const n = { ...cur };
      delete n[id];
      return n;
    });
  const clear = () => setJobs((cur) => Object.fromEntries(Object.entries(cur).filter(([, j]) => j.state === "running")));
  return { jobs: Object.values(jobs).sort((a, b) => b.startedAt.localeCompare(a.startedAt)), dismiss, clear };
}

const jobIcon: Record<string, React.ReactNode> = {
  "files.copy": <Copy />,
  "files.move": <FolderInput />,
  "files.archive": <Archive />,
  "files.extract": <PackageOpen />,
};

function JobRow({ job, onDismiss }: { job: JobSnapshot; onDismiss: () => void }) {
  const running = job.state === "running";
  const pctv = job.progress >= 0 ? job.progress * 100 : undefined;
  return (
    <li className="px-3 py-2.5">
      <div className="flex items-center gap-2">
        <span className={cn("text-subtle [&_svg]:size-3.5", job.state === "failed" && "text-crit", job.state === "succeeded" && "text-ok")}>
          {job.state === "succeeded" ? <Check /> : job.state === "failed" ? <AlertTriangle /> : jobIcon[job.kind]}
        </span>
        <span className="min-w-0 flex-1 truncate text-ui font-medium">{job.title}</span>
        {running ? (
          <IconButton label="Cancel job" size="xs" onClick={() => api.post(`/api/jobs/${job.id}/cancel`).catch((e) => toast.error("Cancel failed", e))}>
            <X />
          </IconButton>
        ) : (
          <IconButton label="Dismiss" size="xs" onClick={onDismiss}>
            <X />
          </IconButton>
        )}
      </div>
      {running && <Meter value={pctv ?? 100} tone={pctv === undefined ? "frost" : "accent"} className={cn("mt-1.5", pctv === undefined && "animate-pulse")} label={`${job.title} progress`} />}
      <div className="num mt-1 truncate text-2xs text-muted">
        {running
          ? `${job.stage ?? "Working"}${pctv !== undefined ? ` · ${Math.round(pctv)}%` : ""}${job.current ? ` · ${job.current}` : ""}`
          : job.state === "failed"
            ? job.error
            : job.state === "canceled"
              ? "Canceled"
              : "Done"}
      </div>
    </li>
  );
}

function UploadRow({ it }: { it: UploadItem }) {
  const pctv = it.size ? (it.received / it.size) * 100 : 100;
  const pick = useRef<HTMLInputElement>(null);
  const eta = it.speed > 0 ? (it.size - it.received) / it.speed : 0;
  return (
    <li className="px-3 py-2.5">
      <div className="flex items-center gap-2">
        <span className={cn("text-subtle [&_svg]:size-3.5", it.state === "done" && "text-ok", (it.state === "error" || it.state === "conflict") && "text-crit")}>
          {it.state === "done" ? <Check /> : it.state === "error" || it.state === "conflict" ? <AlertTriangle /> : <ArrowUpFromLine />}
        </span>
        <span className="min-w-0 flex-1 truncate text-ui font-medium" title={it.name}>
          {it.name}
        </span>
        {it.state === "uploading" && (
          <IconButton label="Pause upload" size="xs" onClick={() => pause(it.key)}>
            <Pause />
          </IconButton>
        )}
        {(it.state === "paused" || it.state === "error") && it.file && (
          <IconButton label="Resume upload" size="xs" onClick={() => resume(it.key)}>
            {it.state === "error" ? <RotateCcw /> : <Play />}
          </IconButton>
        )}
        {it.state === "interrupted" && (
          <>
            <input
              ref={pick}
              type="file"
              className="hidden"
              aria-label={`Select ${it.name} to resume`}
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) resumeWith(it.key, f);
                e.target.value = "";
              }}
            />
            <Button size="xs" onClick={() => pick.current?.click()}>
              Resume…
            </Button>
          </>
        )}
        {it.state !== "done" && it.state !== "canceled" && (
          <IconButton label="Cancel upload" size="xs" onClick={() => cancel(it.key)}>
            <X />
          </IconButton>
        )}
      </div>
      {it.state !== "done" && it.state !== "conflict" && <Meter value={pctv} tone={it.state === "error" ? "crit" : it.state === "uploading" ? "accent" : "frost"} className="mt-1.5" label={`${it.name} upload progress`} />}
      <div className="num mt-1 flex items-center gap-2 text-2xs text-muted">
        {it.state === "conflict" ? (
          <>
            <span className="min-w-0 flex-1 truncate text-crit">{it.error}</span>
            <Button size="xs" onClick={() => resolveConflict(it.key, true)}>
              Replace
            </Button>
            <Button size="xs" variant="ghost" onClick={() => resolveConflict(it.key, false)}>
              Skip
            </Button>
          </>
        ) : (
          <span className="min-w-0 flex-1 truncate">
            {it.state === "done"
              ? `${bytes(it.size)} · uploaded`
              : `${bytes(it.received)} / ${bytes(it.size)}${it.state === "uploading" && it.speed ? ` · ${rate(it.speed)}${eta ? ` · ${Math.ceil(eta)}s left` : ""}` : ""}${
                  it.state === "paused" ? " · paused" : it.state === "interrupted" ? " · interrupted — choose the file again to resume" : it.state === "queued" ? " · waiting" : ""
                }`}
            {it.error && <span className={cn(it.state === "error" ? "text-crit" : "text-warn")}> · {it.error}</span>}
          </span>
        )}
      </div>
    </li>
  );
}

export function TransfersPanel({ jobs, onDismissJob, onClearJobs, raised }: { jobs: JobSnapshot[]; onDismissJob: (id: string) => void; onClearJobs: () => void; raised?: boolean }) {
  const uploads = useUploads();
  const [open, setOpen] = useState(true);
  useEffect(() => void syncWithServer(), []);
  const activeUploads = uploads.filter((u) => u.state === "uploading" || u.state === "queued").length;
  const activeJobs = jobs.filter((j) => j.state === "running").length;
  const total = uploads.length + jobs.length;
  const prevActive = useRef(0);
  useEffect(() => {
    if (activeUploads + activeJobs > prevActive.current) setOpen(true);
    prevActive.current = activeUploads + activeJobs;
  }, [activeUploads, activeJobs]);
  if (total === 0) return null;
  const active = activeUploads + activeJobs;
  const finished = uploads.some((u) => u.state === "done" || u.state === "canceled") || jobs.some((j) => j.state !== "running");
  const waiting = uploads.filter((u) => ["paused", "interrupted", "error", "conflict"].includes(u.state)).length;
  const speed = uploads.reduce((s, u) => s + (u.state === "uploading" ? u.speed : 0), 0);
  return (
    <section
      aria-label="Transfers"
      className={cn(
        "fixed right-3 z-40 w-[min(24rem,calc(100vw-1.5rem))] rounded-lg border border-border-strong bg-surface shadow-xl",
        raised ? "bottom-[calc(env(safe-area-inset-bottom)+4.5rem)]" : "bottom-[calc(env(safe-area-inset-bottom)+0.75rem)]",
      )}
    >
      <header className="flex h-10 items-center gap-2 border-b border-border px-3">
        <h2 className="text-ui font-semibold">Transfers</h2>
        <span className="num text-xs text-muted" aria-live="polite">
          {active ? `${active} active${speed ? ` · ${rate(speed)}` : ""}` : waiting ? `${waiting} paused` : "all done"}
        </span>
        <div className="ml-auto flex items-center gap-1">
          {finished && (
            <Button
              size="xs"
              variant="ghost"
              onClick={() => {
                clearFinished();
                onClearJobs();
              }}
            >
              Clear
            </Button>
          )}
          <IconButton label={open ? "Collapse transfers" : "Expand transfers"} size="sm" onClick={() => setOpen(!open)}>
            {open ? <ChevronDown /> : <ChevronUp />}
          </IconButton>
        </div>
      </header>
      {open && (
        <ul className="scroll-area max-h-[min(50dvh,22rem)] divide-y divide-border">
          {jobs.map((j) => (
            <JobRow key={j.id} job={j} onDismiss={() => onDismissJob(j.id)} />
          ))}
          {uploads.map((u) => (
            <UploadRow key={u.key} it={u} />
          ))}
        </ul>
      )}
    </section>
  );
}

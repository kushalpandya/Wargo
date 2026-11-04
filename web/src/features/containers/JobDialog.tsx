// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, XCircle } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { ErrorNote, Meter, Spinner } from "@/components/ui/misc";
import type { JobSnapshot } from "./types";

/** Shows a job's progress and streaming log until it finishes. */
export function JobDialog({ jobId, title, onClose }: { jobId: string | null; title: string; onClose: () => void }) {
  const q = useQuery({
    queryKey: ["jobs", jobId],
    queryFn: () => api.get<{ job: JobSnapshot; log: string[] }>(`/api/jobs/${jobId}`),
    enabled: !!jobId,
    // Poll while running; stop once it has finished or the job is gone.
    refetchInterval: (query) => (query.state.status === "error" ? false : query.state.data?.job.state === "running" || !query.state.data ? 800 : false),
  });
  const job = q.data?.job;
  const log = q.data?.log ?? [];
  const running = job ? job.state === "running" : !q.error;
  const result = job?.result as { updated?: boolean; message?: string } | undefined;

  return (
    <Dialog
      open={!!jobId}
      onOpenChange={(o) => !o && onClose()}
      title={title}
      description={job?.stage ?? "Starting…"}
      size="lg"
      footer={
        <Button onClick={onClose} variant={running ? "secondary" : "primary"}>
          {running ? "Run in background" : "Close"}
        </Button>
      }
    >
      <div className="space-y-3">
        <div className="flex items-center gap-2.5" role="status" aria-live="polite">
          {running ? (
            <Spinner className="text-accent" />
          ) : job?.state === "succeeded" ? (
            <CheckCircle2 className="size-4 text-ok" />
          ) : (
            <XCircle className="size-4 text-crit" />
          )}
          <span className="text-sm font-medium">
            {running
              ? job?.current || "Working…"
              : job?.state === "succeeded"
                ? result?.updated === false
                  ? "Already up to date"
                  : "Update complete"
                : job?.state === "canceled"
                  ? "Canceled"
                  : "Update failed"}
          </span>
          {running && job && job.progress >= 0 && <span className="num ml-auto text-xs text-muted">{Math.round(job.progress * 100)}%</span>}
        </div>
        {running && <JobMeter job={job} />}
        {q.error && !job && <ErrorNote error={q.error} />}
        {job?.error && <p className="rounded-md border border-crit/30 bg-crit-soft px-3 py-2 text-sm text-crit">{job.error}</p>}
        <JobLog lines={log} />
      </div>
    </Dialog>
  );
}

/** Progress bar for a running job (pulsing while indeterminate). */
export function JobMeter({ job }: { job?: JobSnapshot }) {
  return (
    <Meter
      value={job && job.progress >= 0 ? job.progress * 100 : 100}
      tone="accent"
      label="Progress"
      className={job && job.progress >= 0 ? undefined : "animate-pulse opacity-60"}
    />
  );
}

/** A job's log, kept scrolled to the newest line. */
export function JobLog({ lines, empty = "Waiting for output…" }: { lines: string[]; empty?: string }) {
  const pre = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, [lines.length]);
  return (
    <pre
      ref={pre}
      tabIndex={0}
      aria-label="Job log"
      className="num max-h-72 min-h-32 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-bg p-2.5 text-2xs leading-relaxed text-muted"
    >
      {lines.length ? lines.join("\n") : empty}
    </pre>
  );
}

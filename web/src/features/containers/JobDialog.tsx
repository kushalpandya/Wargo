// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, XCircle } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Meter, Spinner } from "@/components/ui/misc";
import type { JobSnapshot } from "./types";

/** Shows a job's progress and streaming log until it finishes. */
export function JobDialog({ jobId, title, onClose }: { jobId: string | null; title: string; onClose: () => void }) {
  const q = useQuery({
    queryKey: ["jobs", jobId],
    queryFn: () => api.get<{ job: JobSnapshot; log: string[] }>(`/api/jobs/${jobId}`),
    enabled: !!jobId,
    refetchInterval: (query) => (query.state.data?.job.state === "running" || !query.state.data ? 800 : false),
  });
  const job = q.data?.job;
  const log = q.data?.log ?? [];
  const pre = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (pre.current) pre.current.scrollTop = pre.current.scrollHeight;
  }, [log.length]);
  const running = !job || job.state === "running";
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
        {running && (
          <Meter
            value={job && job.progress >= 0 ? job.progress * 100 : 100}
            tone="accent"
            label="Progress"
            className={job && job.progress >= 0 ? undefined : "animate-pulse opacity-60"}
          />
        )}
        {job?.error && <p className="rounded-md border border-crit/30 bg-crit-soft px-3 py-2 text-sm text-crit">{job.error}</p>}
        <pre
          ref={pre}
          tabIndex={0}
          aria-label="Job log"
          className="num max-h-72 min-h-32 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-bg p-2.5 text-[0.75rem] leading-relaxed text-muted"
        >
          {log.length ? log.join("\n") : "Waiting for output…"}
        </pre>
      </div>
    </Dialog>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
// Wargo updating its own container: pull progress, then the restart, then
// the outcome reported by whichever Wargo runs afterwards. The update is
// remembered in sessionStorage so a reload during the restart still shows it.
import { useEffect, useState, useSyncExternalStore } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, XCircle } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/misc";
import { JobLog, JobMeter } from "./JobDialog";
import type { JobSnapshot, SelfUpdateStatus } from "./types";

const KEY = "wargo.selfUpdate";
/** Give up waiting for a result after this long (the server waits 10 min). */
const GIVE_UP_MS = 15 * 60_000;

interface Info {
  jobId: string;
  /** Browser time the update was started (unix ms). */
  startedAt: number;
  fromVersion?: string;
}

const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};
function snapshot(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}
function save(info: Info | null) {
  try {
    if (info) sessionStorage.setItem(KEY, JSON.stringify(info));
    else sessionStorage.removeItem(KEY);
  } catch {
    // Private mode etc.: the dialog still works until a reload.
  }
  listeners.forEach((l) => l());
}

/** Shows the self update dialog for a job started with strategy "self". */
export function startSelfUpdate(jobId: string, fromVersion?: string) {
  save({ jobId, startedAt: Date.now(), fromVersion });
}

/** Mounted once in the app shell. */
export function SelfUpdateDialog() {
  const raw = useSyncExternalStore(subscribe, snapshot, () => null);
  let info: Info | null = null;
  try {
    info = raw ? (JSON.parse(raw) as Info) : null;
  } catch {
    info = null;
  }
  if (!info?.jobId) return null;
  return <Flow key={info.jobId} info={info} />;
}

type Phase =
  | { k: "pulling" }
  | { k: "restarting" }
  | { k: "current" }
  | { k: "done"; version?: string }
  | { k: "failed"; error?: string; log: string[]; from?: string };

function Flow({ info }: { info: Info }) {
  const [phase, setPhase] = useState<Phase>({ k: "pulling" });

  const job = useQuery({
    queryKey: ["jobs", info.jobId],
    queryFn: () => api.get<{ job: JobSnapshot; log: string[] }>(`/api/jobs/${info.jobId}`),
    enabled: phase.k === "pulling",
    retry: false,
    refetchInterval: (q) => (q.state.status === "error" || q.state.data?.job.state !== "running" ? false : 800),
  });

  // Pull phase: follow the job until it hands over (or fails).
  useEffect(() => {
    if (phase.k !== "pulling") return;
    if (job.error) {
      // Gone (404 after a restart) or unreachable: Wargo is restarting.
      setPhase({ k: "restarting" });
      return;
    }
    const j = job.data?.job;
    if (!j || j.state === "running") return;
    const r = j.result as { updated?: boolean; handedOver?: boolean } | undefined;
    if (j.state === "succeeded" && r?.updated === false) setPhase({ k: "current" });
    else if (j.state === "succeeded") setPhase({ k: "restarting" });
    else setPhase({ k: "failed", error: j.error ?? "The update was canceled.", log: job.data?.log ?? [] });
  }, [phase.k, job.data, job.error]);

  // Restart phase: poll for the result; errors are expected while Wargo is down.
  useEffect(() => {
    if (phase.k !== "restarting") return;
    let stop = false;
    let t = 0;
    const tick = async () => {
      try {
        const st = await api.get<SelfUpdateStatus>("/api/containers/self-update");
        const r = st.result;
        if (!stop && r && r.jobId === info.jobId) {
          setPhase(r.ok ? { k: "done", version: r.toVersion } : { k: "failed", error: r.error, log: r.log ?? [], from: r.fromVersion });
          return;
        }
      } catch {
        // Expected while Wargo restarts.
      }
      if (stop) return;
      if (Date.now() - info.startedAt > GIVE_UP_MS) {
        setPhase({ k: "failed", error: "Wargo did not report a result. Check its container on the host.", log: [] });
        return;
      }
      t = window.setTimeout(tick, 2000);
    };
    t = window.setTimeout(tick, 2000);
    return () => {
      stop = true;
      window.clearTimeout(t);
    };
  }, [phase.k, info.jobId, info.startedAt]);

  // Success: reload to get the new version's UI.
  useEffect(() => {
    if (phase.k !== "done") return;
    const t = window.setTimeout(() => {
      save(null);
      window.location.reload();
    }, 1500);
    return () => window.clearTimeout(t);
  }, [phase.k]);

  const closable = phase.k === "failed" || phase.k === "current";
  const close = () => save(null);
  const j = job.data?.job;

  return (
    <Dialog
      open
      onOpenChange={(o) => !o && closable && close()}
      title="Updating Wargo"
      description={
        phase.k === "pulling"
          ? (j?.stage ?? "Starting…")
          : phase.k === "restarting"
            ? "Restarting…"
            : phase.k === "done"
              ? "Reloading the page…"
              : undefined
      }
      size="lg"
      footer={closable ? <Button variant="primary" onClick={close}>Close</Button> : undefined}
    >
      <div className="space-y-3" role="status" aria-live="polite">
        {phase.k === "pulling" && (
          <>
            <div className="flex items-center gap-2.5">
              <Spinner className="text-accent" />
              <span className="text-sm font-medium">{j?.current || "Downloading the new version…"}</span>
              {j && j.progress >= 0 && <span className="num ml-auto text-xs text-muted">{Math.round(j.progress * 100)}%</span>}
            </div>
            <JobMeter job={j} />
            <JobLog lines={job.data?.log ?? []} />
          </>
        )}
        {phase.k === "restarting" && (
          <div className="flex items-start gap-2.5">
            <Spinner className="mt-0.5 shrink-0 text-accent" />
            <p className="text-sm">
              Wargo is restarting on the new version. This page reconnects by itself. If the new version doesn't start properly, the current one comes back. This
              can take a minute.
            </p>
          </div>
        )}
        {phase.k === "current" && (
          <div className="flex items-center gap-2.5">
            <CheckCircle2 className="size-4 text-ok" />
            <span className="text-sm font-medium">Wargo is already up to date.</span>
          </div>
        )}
        {phase.k === "done" && (
          <div className="flex items-center gap-2.5">
            <CheckCircle2 className="size-4 text-ok" />
            <span className="text-sm font-medium">{phase.version ? `Updated to ${phase.version}` : "Updated"}</span>
          </div>
        )}
        {phase.k === "failed" && (
          <>
            <div className="flex items-center gap-2.5">
              <XCircle className="size-4 shrink-0 text-crit" />
              <span className="text-sm font-medium">
                {(phase.from || info.fromVersion) ? `Update failed. Wargo is still on ${phase.from || info.fromVersion}.` : "Update failed. Wargo is still on its current version."}
              </span>
            </div>
            {phase.error && <p className="rounded-md border border-crit/30 bg-crit-soft px-3 py-2 text-sm text-crit">{phase.error}</p>}
            {phase.log.length > 0 && (
              <details>
                <summary className="cursor-pointer text-sm text-muted hover:text-fg">Show log</summary>
                <div className="mt-2">
                  <JobLog lines={phase.log} />
                </div>
              </details>
            )}
          </>
        )}
      </div>
    </Dialog>
  );
}

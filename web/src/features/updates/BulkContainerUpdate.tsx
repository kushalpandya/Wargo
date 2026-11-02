// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowUpCircle, CheckCircle2, CircleDashed, CircleMinus, TriangleAlert, XCircle } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { Meter, Notice, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { ContainerIcon } from "@/features/containers/ContainerIcon";
import { containersKey, containerUpdatesKey } from "@/features/containers/api";
import type { Container, JobSnapshot } from "@/features/containers/types";

type ItemState = "pending" | "running" | "updated" | "current" | "failed" | "skipped";

interface Item {
  c: Container;
  state: ItemState;
  jobId?: string;
  stage?: string;
  progress?: number;
  error?: string;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Runs one container update job to completion, reporting progress. */
async function runOne(c: Container, onProgress: (p: Partial<Item>) => void, alive: () => boolean): Promise<Partial<Item>> {
  let jobId: string;
  try {
    jobId = (await api.post<{ jobId: string }>(`/api/containers/${c.id}/update`, { strategy: "auto" })).jobId;
  } catch (e) {
    if (e instanceof ApiError && e.code === "elevation_canceled") return { state: "skipped", error: "Approval canceled" };
    return { state: "failed", error: e instanceof Error ? e.message : String(e) };
  }
  onProgress({ jobId, state: "running", stage: "Starting…", progress: -1 });
  for (;;) {
    await sleep(800);
    if (!alive()) return { state: "running" };
    let job: JobSnapshot;
    try {
      job = (await api.get<{ job: JobSnapshot }>(`/api/jobs/${jobId}`)).job;
    } catch {
      continue;
    }
    if (job.state === "running") {
      onProgress({ stage: job.current || job.stage, progress: job.progress });
      continue;
    }
    if (job.state === "succeeded") {
      const updated = (job.result as { updated?: boolean } | undefined)?.updated;
      return { state: updated === false ? "current" : "updated", stage: undefined };
    }
    return { state: job.state === "canceled" ? "skipped" : "failed", error: job.error || "Update failed" };
  }
}

const stateLabel: Record<ItemState, string> = {
  pending: "Waiting",
  running: "Updating",
  updated: "Updated",
  current: "Already up to date",
  failed: "Failed",
  skipped: "Skipped",
};

function StateIcon({ s }: { s: ItemState }) {
  switch (s) {
    case "running":
      return <Spinner className="text-accent" />;
    case "updated":
      return <CheckCircle2 className="size-4 text-ok" />;
    case "current":
      return <CheckCircle2 className="size-4 text-muted" />;
    case "failed":
      return <XCircle className="size-4 text-crit" />;
    case "skipped":
      return <CircleMinus className="size-4 text-subtle" />;
    default:
      return <CircleDashed className="size-4 text-subtle" />;
  }
}

/**
 * "Update all": confirms, then updates the containers one at a time via
 * POST /api/containers/{id}/update, showing combined progress.
 */
export function BulkContainerUpdate({ open, containers, onClose }: { open: boolean; containers: Container[]; onClose: () => void }) {
  const qc = useQueryClient();
  const [items, setItems] = useState<Item[] | null>(null);
  const [stopping, setStopping] = useState(false);
  const stopRef = useRef(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => {
    if (!open) {
      setItems(null);
      setStopping(false);
      stopRef.current = false;
    }
  }, [open]);

  const running = !!items && items.some((i) => i.state === "running" || i.state === "pending");
  const done = !!items && !running;

  const start = async () => {
    const list: Item[] = containers.map((c) => ({ c, state: "pending" }));
    setItems(list);
    stopRef.current = false;
    const results: Item[] = [...list];
    for (let i = 0; i < list.length; i++) {
      if (!mounted.current) return;
      if (stopRef.current) {
        for (let j = i; j < list.length; j++) results[j] = { ...results[j], state: "skipped" };
        setItems([...results]);
        break;
      }
      results[i] = { ...results[i], state: "running", stage: "Starting…", progress: -1 };
      setItems([...results]);
      const patch = (p: Partial<Item>) => {
        results[i] = { ...results[i], ...p };
        if (mounted.current) setItems([...results]);
      };
      const r = await runOne(list[i].c, patch, () => mounted.current);
      patch(r);
      qc.invalidateQueries({ queryKey: containersKey, exact: true });
    }
    qc.invalidateQueries({ queryKey: containerUpdatesKey, exact: true });
    const ok = results.filter((r) => r.state === "updated" || r.state === "current").length;
    const failed = results.filter((r) => r.state === "failed").length;
    if (failed) toast.error(`${failed} container update${failed === 1 ? "" : "s"} failed`, `${ok} of ${results.length} succeeded`);
    else toast.ok(`Updated ${ok} container${ok === 1 ? "" : "s"}`);
  };

  const n = items?.length ?? containers.length;
  const finished = items?.filter((i) => !["pending", "running"].includes(i.state)).length ?? 0;
  const current = items?.find((i) => i.state === "running");
  const overall = items ? ((finished + (current && (current.progress ?? -1) >= 0 ? current.progress! : 0)) / n) * 100 : 0;
  const confirmStage = !items;

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        // Keep the dialog while updates run: closing would hide progress.
        if (!o && !running) onClose();
      }}
      title={confirmStage ? `Update ${n} container${n === 1 ? "" : "s"}?` : done ? "Container updates finished" : "Updating containers"}
      description={
        confirmStage
          ? "Each container's image is pulled and the container is recreated with the same configuration, one at a time."
          : `${finished} of ${n} done`
      }
      size="md"
      footer={
        confirmStage ? (
          <>
            <Button autoFocus onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" icon={<ArrowUpCircle />} onClick={start}>
              Update all
            </Button>
          </>
        ) : running ? (
          <Button
            onClick={() => {
              stopRef.current = true;
              setStopping(true);
            }}
            disabled={stopping}
          >
            {stopping ? "Stopping after this one…" : "Stop after current"}
          </Button>
        ) : (
          <Button variant="primary" onClick={onClose}>
            Close
          </Button>
        )
      }
    >
      <div className="space-y-3">
        {confirmStage && (
          <Notice tone="warn" icon={<TriangleAlert />}>
            Containers <span className="font-medium text-fg">restart</span> while they update, so their services are briefly unavailable.
            A container that fails to start is rolled back to its previous version.
          </Notice>
        )}
        {!confirmStage && (
          <div role="status" aria-live="polite">
            <Meter value={done ? 100 : Math.max(overall, 3)} tone={done ? "ok" : "accent"} label="Overall progress" />
          </div>
        )}
        <ul className="divide-y divide-border rounded-md border border-border">
          {(items ?? containers.map((c) => ({ c, state: "pending" as const }))).map((it: Item) => (
            <li key={it.c.id} className="flex min-w-0 items-center gap-2.5 px-2.5 py-2">
              <ContainerIcon c={it.c} size="sm" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{it.c.name}</div>
                <div className={cn("truncate text-xs", it.state === "failed" ? "text-crit" : "text-muted")} title={it.error}>
                  {it.state === "running"
                    ? it.stage || "Working…"
                    : it.error
                      ? it.error
                      : confirmStage
                        ? `${it.c.imageRef}${it.c.tag ? `:${it.c.tag}` : ""}`
                        : stateLabel[it.state]}
                </div>
                {it.state === "running" && (it.progress ?? -1) >= 0 && (
                  <Meter value={(it.progress ?? 0) * 100} tone="accent" label={`${it.c.name} progress`} className="mt-1" />
                )}
              </div>
              {!confirmStage && (
                <span className="flex shrink-0 items-center" aria-label={stateLabel[it.state]} title={stateLabel[it.state]}>
                  <StateIcon s={it.state} />
                </span>
              )}
            </li>
          ))}
        </ul>
      </div>
    </Dialog>
  );
}

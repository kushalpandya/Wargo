// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api";
import { useCapabilities } from "@/app/session";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Segmented } from "@/components/ui/form";
import { toast } from "@/components/ui/toast";
import { JobDialog } from "./JobDialog";
import type { Container } from "./types";

export type Action = "start" | "stop" | "restart" | "update";
type Strategy = "auto" | "recreate" | "compose";

const verbs: Record<Action, string> = { start: "Start", stop: "Stop", restart: "Restart", update: "Update" };
const past: Record<Action, string> = { start: "Started", stop: "Stopped", restart: "Restarted", update: "Updated" };

/** State + dialogs for container actions. Render `element` once. */
export function useContainerActions() {
  const qc = useQueryClient();
  const caps = useCapabilities().data?.docker;
  const [pending, setPending] = useState<{ action: Action; c: Container } | null>(null);
  const [strategy, setStrategy] = useState<Strategy>("auto");
  const [job, setJob] = useState<{ id: string; title: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const mut = useMutation({
    mutationFn: async ({ action, c }: { action: Action; c: Container }) => {
      if (action === "update") return api.post<{ jobId: string }>(`/api/containers/${c.id}/update`, { strategy });
      return api.post(`/api/containers/${c.id}/${action}`);
    },
    onMutate: ({ c }) => setBusy(c.id),
    onSettled: () => setBusy(null),
    onSuccess: (res, { action, c }) => {
      setPending(null);
      if (action === "update") {
        setJob({ id: (res as { jobId: string }).jobId, title: `Update ${c.name}` });
      } else {
        toast.ok(`${past[action]} ${c.name}`);
      }
      qc.invalidateQueries({ queryKey: ["containers"] });
    },
    onError: (e, { action, c }) => {
      setPending(null);
      if (e instanceof ApiError && e.code === "elevation_canceled") return;
      toast.error(`Could not ${action} ${c.name}`, e);
    },
  });

  const request = (action: Action, c: Container) => {
    if (action === "start") return mut.mutate({ action, c });
    setStrategy("auto");
    setPending({ action, c });
  };

  const p = pending;
  const composeOk = !!caps?.compose && !!p?.c.compose;
  const element = (
    <>
      {p && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setPending(null)}
          title={`${verbs[p.action]} ${p.c.name}?`}
          confirmLabel={verbs[p.action]}
          danger={p.action === "stop"}
          loading={mut.isPending}
          onConfirm={() => mut.mutate(p)}
          description={
            p.action === "stop"
              ? "The container stops and its services become unavailable until it is started again."
              : p.action === "restart"
                ? "The container is stopped and started again; its services are briefly unavailable."
                : "Pulls the latest image and, if it changed, recreates the container with the same configuration. It rolls back if the new container fails to start."
          }
        >
          {p.action === "update" && (
            <div className="space-y-2">
              <div className="text-2xs font-medium uppercase tracking-wider text-subtle">Method</div>
              <Segmented<Strategy>
                label="Update method"
                value={strategy}
                onChange={setStrategy}
                options={[
                  { value: "auto", label: "Automatic" },
                  { value: "recreate", label: "Recreate" },
                  ...(composeOk ? [{ value: "compose" as const, label: "Compose" }] : []),
                ]}
              />
              <p className="text-xs text-muted">
                {strategy === "compose"
                  ? `Runs docker compose pull and up -d for service ${p.c.compose?.service}.`
                  : strategy === "recreate"
                    ? "Recreates the container through the Docker API, keeping its config, mounts, networks and labels."
                    : composeOk
                      ? "Uses docker compose when the compose files are readable, otherwise recreates through the Docker API."
                      : "Recreates the container through the Docker API, keeping its config, mounts, networks and labels."}
              </p>
              {p.c.networkUsers?.length ? (
                <p className="text-xs text-warn">
                  {p.c.networkUsers.join(", ")} share{p.c.networkUsers.length === 1 ? "s" : ""} this container's network and will be recreated too.
                </p>
              ) : null}
            </div>
          )}
        </ConfirmDialog>
      )}
      <JobDialog jobId={job?.id ?? null} title={job?.title ?? ""} onClose={() => setJob(null)} />
    </>
  );
  return { request, element, busy };
}

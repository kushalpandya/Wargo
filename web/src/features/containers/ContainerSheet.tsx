// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Tabs } from "radix-ui";
import { ArrowUpCircle, Network as NetworkIcon, Play, RotateCw, Square } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, datetime, duration, pct } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Button } from "@/components/ui/button";
import { Sheet } from "@/components/ui/dialog";
import { Badge, EmptyState, ErrorNote, Field, Meter, Skeleton, StatusDot } from "@/components/ui/misc";
import { containerTone, stateLabel } from "@/features/containers/util";
import { LogsView } from "./LogsView";
import { ContainerIcon } from "./ContainerIcon";
import { parseTime, PortLinks, splitImage } from "./util";
import type { Action } from "./actions";
import type { Container, ContainerDetails, ContainerStats } from "./types";

export type SheetTab = "overview" | "logs" | "env";

function Section({ title, children, meta }: { title: string; children: ReactNode; meta?: ReactNode }) {
  return (
    <section className="border-b border-border px-4 py-3 last:border-0">
      <h3 className="mb-1.5 flex items-center gap-2 text-2xs font-semibold uppercase tracking-wider text-subtle">
        {title}
        {meta && <span className="font-normal normal-case tracking-normal text-subtle">{meta}</span>}
      </h3>
      {children}
    </section>
  );
}

const nsDur = (ns?: number) => (ns ? duration(ns / 1e9) : "—");

function Overview({ d, stats }: { d: ContainerDetails; stats?: ContainerStats }) {
  const [repo, tag] = splitImage(d.image);
  const started = parseTime(d.stateInfo.startedAt);
  const labels = Object.entries(d.labels).sort(([a], [b]) => a.localeCompare(b));
  return (
    <div>
      <div className="grid grid-cols-2 gap-px border-b border-border bg-border sm:grid-cols-4">
        {[
          ["State", d.stateInfo.status + (d.stateInfo.restarting ? " (restarting)" : "")],
          ["Uptime", d.stateInfo.running && started ? duration((Date.now() - started) / 1000) : "—"],
          ["CPU", stats ? pct(stats.cpu, 1) : "—"],
          ["Memory", stats ? bytes(stats.mem) : "—"],
        ].map(([k, v]) => (
          <div key={k} className="min-w-0 bg-surface px-3 py-2.5">
            <div className="text-2xs font-medium uppercase tracking-wider text-subtle">{k}</div>
            <div className="num mt-0.5 truncate text-base font-medium capitalize">{v}</div>
          </div>
        ))}
      </div>
      <Section title="Container">
        <dl className="divide-y divide-border text-sm">
          <Field label="Image" mono>
            <span title={d.image}>
              {repo}
              <span className="text-accent">:{tag}</span>
            </span>
          </Field>
          {d.version && d.version !== tag && <Field label="Version" mono>{d.version}</Field>}
          <Field label="Image ID" mono>{d.imageId.replace("sha256:", "").slice(0, 12)}</Field>
          {d.imageInfo && <Field label="Image size" mono>{bytes(d.imageInfo.size)}</Field>}
          <Field label="Created">{datetime(d.createdAt)}</Field>
          {d.stateInfo.finishedAt && !d.stateInfo.running && (
            <Field label="Exited">
              {datetime(d.stateInfo.finishedAt)} <span className="num text-subtle">(code {d.stateInfo.exitCode})</span>
            </Field>
          )}
          <Field label="Restart policy" mono>
            {d.restartPolicy.name || "no"}
            {d.restartPolicy.maxRetry ? ` (max ${d.restartPolicy.maxRetry})` : ""}
          </Field>
          <Field label="Restarts" mono>{d.stateInfo.restartCount}</Field>
          <Field label="Command" mono>
            <span title={[...(d.entrypoint ?? []), ...(d.cmd ?? [])].join(" ")}>{[...(d.entrypoint ?? []), ...(d.cmd ?? [])].join(" ") || "—"}</span>
          </Field>
          {d.compose && (
            <Field label="Compose">
              {d.compose.project} / {d.compose.service}
            </Field>
          )}
          {d.compose?.workingDir && <Field label="Project dir" mono><span title={d.compose.workingDir}>{d.compose.workingDir}</span></Field>}
          {d.stateInfo.oomKilled && <Field label="OOM killed"><Badge tone="crit">yes</Badge></Field>}
          {d.stateInfo.error && <Field label="Error"><span className="text-crit" title={d.stateInfo.error}>{d.stateInfo.error}</span></Field>}
        </dl>
      </Section>
      <Section title="Ports">
        <PortLinks c={d} max={20} className="py-1" />
        {d.networkContainer && (
          <p className="mt-1.5 text-xs text-muted">Uses the network of {d.networkContainer}; ports above are published by it.</p>
        )}
      </Section>
      <Section title="Networks" meta={d.networkMode}>
        {d.networks.length === 0 ? (
          <p className="text-sm text-muted">
            {d.networkContainer ? `Shares the network namespace of ${d.networkContainer}.` : d.networkMode === "host" ? "Uses the host network." : "No networks."}
          </p>
        ) : (
          <ul className="divide-y divide-border text-sm">
            {d.networks.map((n) => (
              <li key={n.name} className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-0.5 py-1.5">
                <span className="font-medium">{n.name}</span>
                {n.ip && <span className="num text-[0.8125rem] text-muted">{n.ip}{n.prefix ? `/${n.prefix}` : ""}</span>}
                {n.gateway && <span className="num text-xs text-subtle">gw {n.gateway}</span>}
                {!!n.aliases?.length && <span className="truncate text-xs text-subtle">aliases: {n.aliases.join(", ")}</span>}
              </li>
            ))}
          </ul>
        )}
        {!!d.networkUsers?.length && <p className="mt-1.5 text-xs text-muted">Shared with: {d.networkUsers.join(", ")}</p>}
      </Section>
      <Section title="Mounts" meta={String(d.mounts.length)}>
        {d.mounts.length === 0 ? (
          <p className="text-sm text-muted">No mounts.</p>
        ) : (
          <ul className="divide-y divide-border">
            {d.mounts.map((m) => (
              <li key={m.destination} className="py-1.5">
                <div className="flex items-center gap-2">
                  <span className="num min-w-0 flex-1 truncate text-[0.8125rem] font-medium" title={m.destination}>{m.destination}</span>
                  <Badge tone={m.type === "volume" ? "frost" : "neutral"}>{m.type}</Badge>
                  {!m.rw && <Badge tone="warn">ro</Badge>}
                </div>
                <div className="num truncate text-xs text-muted" title={m.source}>
                  {m.name && m.type === "volume" ? `${m.name.length > 24 ? m.name.slice(0, 12) + "…" : m.name} · ` : ""}
                  {m.source}
                </div>
              </li>
            ))}
          </ul>
        )}
      </Section>
      {d.healthInfo && (
        <Section title="Health check" meta={d.healthInfo.status}>
          {d.healthInfo.test && <code className="num block break-all text-xs text-muted">{d.healthInfo.test.filter((t) => t !== "CMD-SHELL" && t !== "CMD").join(" ")}</code>}
          <div className="mt-1 text-xs text-subtle">
            every {nsDur(d.healthInfo.intervalNs)} · timeout {nsDur(d.healthInfo.timeoutNs)} · {d.healthInfo.retries ?? 0} retries · failing streak {d.healthInfo.failingStreak}
          </div>
          <ul className="mt-2 space-y-1.5">
            {d.healthInfo.log
              .slice()
              .reverse()
              .map((l, i) => (
                <li key={i} className="rounded-md border border-border bg-surface-2 px-2.5 py-1.5">
                  <div className="flex items-center gap-2 text-xs">
                    <StatusDot tone={l.exitCode === 0 ? "ok" : "crit"} />
                    <span className="text-muted">{datetime(l.start)}</span>
                    <span className="num ml-auto text-subtle">exit {l.exitCode}</span>
                  </div>
                  {l.output && <pre className="num mt-1 max-h-24 overflow-auto whitespace-pre-wrap break-all text-[0.6875rem] text-muted">{l.output}</pre>}
                </li>
              ))}
          </ul>
        </Section>
      )}
      <Section title="Resources">
        <dl className="divide-y divide-border text-sm">
          <Field label="CPU limit" mono>{d.limits.nanoCpus ? `${(d.limits.nanoCpus / 1e9).toFixed(2)} CPUs` : d.limits.cpusetCpus ? `cpus ${d.limits.cpusetCpus}` : "unlimited"}</Field>
          <Field label="Memory limit" mono>{d.limits.memory ? bytes(d.limits.memory) : "unlimited"}</Field>
          {stats && stats.memLimit > 0 && (
            <div className="py-1.5">
              <dt className="sr-only">Memory use</dt>
              <dd>
                <Meter value={(stats.mem / stats.memLimit) * 100} label="Memory use" />
                <div className="num mt-1 text-xs text-subtle">
                  {bytes(stats.mem)} of {bytes(stats.memLimit)}
                </div>
              </dd>
            </div>
          )}
          <Field label="PIDs limit" mono>{d.limits.pidsLimit > 0 ? d.limits.pidsLimit : "unlimited"}</Field>
          <Field label="Privileged">{d.security.privileged ? <Badge tone="warn">yes</Badge> : "no"}</Field>
          {!!d.security.capAdd?.length && <Field label="Capabilities" mono>{d.security.capAdd.join(", ")}</Field>}
          {d.security.devices > 0 && <Field label="Devices" mono>{d.security.devices}</Field>}
          <Field label="Log driver" mono>{d.logDriver || "—"}</Field>
        </dl>
      </Section>
      <Section title="Labels" meta={String(labels.length)}>
        {labels.length === 0 ? (
          <p className="text-sm text-muted">No labels.</p>
        ) : (
          <details>
            <summary className="flex min-h-8 cursor-pointer items-center text-sm text-frost">Show labels</summary>
            <dl className="mt-1 divide-y divide-border">
              {labels.map(([k, v]) => (
                <div key={k} className="py-1.5">
                  <dt className="num break-all text-xs text-muted">{k}</dt>
                  <dd className="num break-all text-xs">{v || <span className="text-subtle">""</span>}</dd>
                </div>
              ))}
            </dl>
          </details>
        )}
      </Section>
    </div>
  );
}

function EnvTab({ d }: { d: ContainerDetails }) {
  return (
    <div className="px-4 py-3">
      <p className="mb-2 text-xs text-muted">Only variable names are shown; values often contain secrets and never leave the server.</p>
      {d.env.length === 0 ? (
        <EmptyState title="No environment variables" />
      ) : (
        <ul className="grid grid-cols-1 gap-1 sm:grid-cols-2">
          {d.env.map((e) => (
            <li key={e} className="num truncate rounded border border-border bg-surface-2 px-2 py-1 text-xs" title={e}>
              {e}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

const tabCls =
  "inline-flex h-9 items-center border-b-2 border-transparent px-3 text-[0.8125rem] font-medium text-muted hover:text-fg data-[state=active]:border-accent data-[state=active]:text-fg";

export function ContainerSheet({
  id,
  summary,
  tab,
  onTab,
  onClose,
  stats,
  onAction,
  busy,
}: {
  id: string | null;
  summary?: Container;
  tab: SheetTab;
  onTab: (t: SheetTab) => void;
  onClose: () => void;
  stats?: ContainerStats;
  onAction: (a: Action, c: Container) => void;
  busy?: boolean;
}) {
  const q = useQuery({
    queryKey: ["containers", "details", id],
    queryFn: () => api.get<ContainerDetails>(`/api/containers/${id}`),
    enabled: !!id,
    refetchInterval: 10_000,
    retry: false,
  });
  const d = q.data;
  const c: Container | undefined = summary ?? d;
  const running = c?.state === "running" || c?.state === "restarting";
  const notFound = q.error instanceof ApiError && q.error.status === 404;

  return (
    <Sheet
      open={!!id}
      onOpenChange={(o) => !o && onClose()}
      width="max-w-3xl"
      title={
        <span className="flex items-center gap-2">
          {c && <ContainerIcon c={c} size="sm" status={containerTone(c)} statusLabel={stateLabel(c)} />}
          {c?.name ?? id}
        </span>
      }
      description={c ? c.image : undefined}
    >
      {notFound ? (
        <EmptyState title="Container not found">It may have been removed or recreated.</EmptyState>
      ) : q.error && !c ? (
        <div className="p-4">
          <ErrorNote error={q.error} />
        </div>
      ) : (
        <Tabs.Root value={tab} onValueChange={(v) => onTab(v as SheetTab)} className="flex h-full min-h-0 flex-col">
          <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5">
            {c && (
              <>
                {running ? (
                  <>
                    <Button size="sm" icon={<RotateCw />} onClick={() => onAction("restart", c)} disabled={busy}>
                      Restart
                    </Button>
                    <Button size="sm" variant="outline-danger" icon={<Square />} onClick={() => onAction("stop", c)} disabled={busy}>
                      Stop
                    </Button>
                  </>
                ) : (
                  <Button size="sm" icon={<Play />} onClick={() => onAction("start", c)} loading={busy}>
                    Start
                  </Button>
                )}
                <Button size="sm" variant={c.updateAvailable ? "primary" : "secondary"} icon={<ArrowUpCircle />} onClick={() => onAction("update", c)} disabled={busy}>
                  {c.updateAvailable ? "Update available" : "Update"}
                </Button>
                {c.networkContainer && (
                  <span className="inline-flex items-center gap-1 text-xs text-muted">
                    <NetworkIcon className="size-3.5" /> via {c.networkContainer}
                  </span>
                )}
              </>
            )}
          </div>
          <Tabs.List className="flex shrink-0 gap-1 border-b border-border px-2" aria-label="Container sections">
            <Tabs.Trigger value="overview" className={tabCls}>Overview</Tabs.Trigger>
            <Tabs.Trigger value="logs" className={tabCls}>Logs</Tabs.Trigger>
            <Tabs.Trigger value="env" className={tabCls}>
              Env{d ? <span className="num ml-1.5 text-xs text-subtle">{d.env.length}</span> : null}
            </Tabs.Trigger>
          </Tabs.List>
          <Tabs.Content value="overview" className={cn("min-h-0 flex-1 overflow-auto focus-visible:outline-none")}>
            {d ? <Overview d={d} stats={stats} /> : <div className="space-y-3 p-4"><Skeleton className="h-16" /><Skeleton className="h-48" /></div>}
          </Tabs.Content>
          <Tabs.Content value="logs" className="flex min-h-0 flex-1 flex-col focus-visible:outline-none">
            {id && <LogsView id={id} name={c?.name ?? id} className="min-h-0 flex-1" />}
          </Tabs.Content>
          <Tabs.Content value="env" className="min-h-0 flex-1 overflow-auto focus-visible:outline-none">
            {d ? <EnvTab d={d} /> : <Skeleton className="m-4 h-32" />}
          </Tabs.Content>
        </Tabs.Root>
      )}
    </Sheet>
  );
}

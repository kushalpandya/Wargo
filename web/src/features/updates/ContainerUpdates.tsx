// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { Link } from "react-router";
import { ArrowRight, ArrowUpCircle, Boxes, ChevronRight, CircleCheck, RefreshCw, ShieldCheck, TriangleAlert } from "lucide-react";
import { ago, datetime } from "@/lib/format";
import { cn } from "@/lib/cn";
import type { Capabilities } from "@/app/session";
import { Card, CardHeader, Stat } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge, EmptyState, ErrorNote, Notice, Skeleton, Spinner } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { ContainerIcon } from "@/features/containers/ContainerIcon";
import { useContainerActions } from "@/features/containers/actions";
import { useCheckContainerUpdates } from "@/features/containers/api";
import type { Container, UpdateCheck, UpdatesStatus } from "@/features/containers/types";
import { BulkContainerUpdate } from "./BulkContainerUpdate";

/** "6 h", "30 min", "1 day". */
function every(ms: number): string {
  const h = ms / 3_600_000;
  if (h >= 24 && h % 24 === 0) return h === 24 ? "day" : `${h / 24} days`;
  if (h >= 1) return h === 1 ? "hour" : `${Number.isInteger(h) ? h : h.toFixed(1)} h`;
  return `${Math.round(ms / 60_000)} min`;
}

function until(t: number): string {
  const s = Math.max(0, (t - Date.now()) / 1000);
  if (s < 90) return "in a minute";
  if (s < 3600) return `in ${Math.round(s / 60)} min`;
  if (s < 86400 * 2) return `in ${Math.round(s / 3600)} h`;
  return `in ${Math.round(s / 86400)} days`;
}

function Origin({ c }: { c: Container }) {
  return (
    <>
      {c.casaos ? <Badge tone="neutral">CasaOS</Badge> : c.compose ? <Badge tone="frost" title={`Compose project ${c.compose.project}`}>compose</Badge> : null}
    </>
  );
}

function ImageRef({ c, className }: { c: Container; className?: string }) {
  return (
    <span className={cn("num truncate text-xs text-muted", className)} title={c.image}>
      {c.imageRef}
      {c.tag && (
        <>
          <span className="text-subtle">:</span>
          <span className={c.tag === "latest" ? "text-subtle" : "text-fg"}>{c.tag}</span>
        </>
      )}
    </span>
  );
}

const cols = "md:grid md:grid-cols-[minmax(12rem,1.2fr)_minmax(12rem,1.3fr)_minmax(10rem,1fr)_6.5rem] md:items-center md:gap-4";

function StaleRow({ c, u, onUpdate, busy }: { c: Container; u?: UpdateCheck; onUpdate: () => void; busy: boolean }) {
  return (
    <li className={cn("flex items-start gap-3 border-b border-border px-4 py-2.5 last:border-0 hover:bg-surface-2 md:items-center", cols)}>
      <div className="flex min-w-0 flex-1 items-center gap-2.5">
        <ContainerIcon c={c} />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <Link to={`/containers/${c.id}`} className="truncate text-ui font-medium text-fg hover:underline max-md:-my-2 max-md:py-2">
              {c.name}
            </Link>
            <Origin c={c} />
          </div>
          {/* Small screens: version + image below the name. */}
          <div className="md:hidden">
            <div className="num flex min-w-0 items-center gap-1 text-xs">
              <span className="truncate text-muted" title={c.version}>{c.version}</span>
              <ArrowRight className="size-3 shrink-0 text-subtle" aria-label="to" />
              <span className="shrink-0 text-accent">new image</span>
            </div>
            <div className="flex min-w-0">
              <ImageRef c={c} />
            </div>
          </div>
        </div>
        <Button size="sm" variant="primary" icon={<ArrowUpCircle />} onClick={onUpdate} disabled={busy} className="shrink-0 md:hidden" aria-label={`Update ${c.name}`}>
          Update
        </Button>
      </div>
      <div className="num hidden min-w-0 items-center gap-1.5 text-xs md:flex">
        <span className="truncate text-fg" title={c.version}>{c.version}</span>
        <ArrowRight className="size-3 shrink-0 text-subtle" aria-label="to" />
        <span className="shrink-0 text-accent" title={u?.remoteDigest ? `Registry digest ${u.remoteDigest}` : undefined}>
          new image available
        </span>
      </div>
      <div className="hidden min-w-0 md:flex">
        <ImageRef c={c} />
      </div>
      <div className="hidden justify-end md:flex">
        <Button size="sm" icon={<ArrowUpCircle />} onClick={onUpdate} disabled={busy} aria-label={`Update ${c.name}`}>
          Update
        </Button>
      </div>
    </li>
  );
}

function ListHeader() {
  return (
    <div aria-hidden="true" className={cn("hidden border-b border-border bg-surface-2 px-4 py-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle", cols)}>
      <span>Container</span>
      <span>Current → available</span>
      <span>Image</span>
      <span className="sr-only">Actions</span>
    </div>
  );
}

/** Collapsible list (details/summary) of other containers. */
function Collapsible({ title, icon, count, children, tone }: { title: string; icon: React.ReactNode; count: number; children: React.ReactNode; tone?: "warn" }) {
  return (
    <Card className="mt-4">
      <details className="group">
        <summary className="flex min-h-10 cursor-pointer list-none items-center gap-2 px-4 py-2 [&::-webkit-details-marker]:hidden">
          <ChevronRight className="size-4 shrink-0 text-subtle transition-transform group-open:rotate-90" aria-hidden="true" />
          <span className={cn("[&_svg]:size-4", tone === "warn" ? "text-warn" : "text-ok")}>{icon}</span>
          <h2 className="text-ui font-semibold tracking-tight">{title}</h2>
          <span className="num text-xs text-subtle">{count}</span>
        </summary>
        <ul className="border-t border-border">{children}</ul>
      </details>
    </Card>
  );
}

function Summary({ st, checking, stale, current, errors, total }: { st?: UpdatesStatus; checking: boolean; stale: number; current: number; errors: number; total: number }) {
  const checked = !!st?.checkedAt;
  return (
    <Card className="mb-4">
      <div className="grid grid-cols-2 gap-x-4 gap-y-3 p-4 sm:grid-cols-4">
        <Stat
          label="Updates"
          value={checked ? stale : "—"}
          unit={checked ? (stale === 1 ? "container" : "containers") : undefined}
          sub={checked ? (stale ? "newer images found" : "nothing to update") : "not checked yet"}
        />
        <Stat label="Up to date" value={checked ? current : "—"} unit={checked ? `of ${total}` : undefined} sub={checked ? "containers checked" : " "} />
        <Stat
          label="Not checked"
          value={checked ? errors : "—"}
          tone={errors ? "warn" : undefined}
          sub={checked ? (errors ? "local, pinned or unreachable" : "every image checked") : " "}
        />
        <div className="min-w-0">
          <div className="text-2xs font-medium uppercase tracking-wider text-subtle">Last checked</div>
          <div className="mt-0.5 flex items-baseline gap-1">
            {checking ? (
              <span className="inline-flex items-center gap-1.5 text-sm text-accent">
                <Spinner /> Checking…
              </span>
            ) : (
              <span className="num text-xl font-medium leading-tight" title={st?.checkedAt ? datetime(st.checkedAt) : undefined}>
                {st?.checkedAt ? ago(st.checkedAt) : "never"}
              </span>
            )}
          </div>
          <div className="mt-0.5 truncate text-xs text-muted">
            {st?.intervalMs ? (st.nextCheckAt ? `next check ${until(st.nextCheckAt)}` : `every ${every(st.intervalMs)}`) : "automatic checks off"}
          </div>
        </div>
      </div>
      <div className="flex items-start gap-2 border-t border-border px-4 py-2 text-xs text-muted">
        <ShieldCheck className="mt-px size-3.5 shrink-0 text-frost" />
        <span>
          {st?.intervalMs
            ? `Wargo compares image digests with the registry every ${every(st.intervalMs)} without pulling anything.`
            : "Background checks are disabled (docker.update_check_interval = 0)."}{" "}
          Containers are only updated when you click Update.
        </span>
      </div>
    </Card>
  );
}

/** The "Containers" tab of the Updates page. */
export function ContainerUpdates({
  docker,
  list,
  listError,
  updates,
  updatesError,
}: {
  docker?: Capabilities["docker"];
  list?: Container[];
  listError: unknown;
  updates?: UpdatesStatus;
  updatesError: unknown;
}) {
  const actions = useContainerActions();
  const [bulk, setBulk] = useState(false);
  const check = useCheckContainerUpdates({
    onSuccess: (s) => {
      const r = Object.values(s.results);
      const n = r.filter((u) => u.updateAvailable).length;
      const failed = r.filter((u) => u.updateAvailable === null).length;
      toast.ok(n ? `${n} container update${n === 1 ? "" : "s"} available` : "All containers are up to date", failed ? `${failed} image(s) could not be checked` : undefined);
    },
    onError: (e) => toast.error("Update check failed", e),
  });

  if (docker && !docker.available) {
    return (
      <Card>
        <EmptyState icon={<Boxes />} title="Docker is not available">
          {docker.error ?? "Wargo could not reach the Docker daemon."}
        </EmptyState>
      </Card>
    );
  }
  if (listError) return <ErrorNote error={listError} />;
  if (!list) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-28 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const checking = check.isPending || !!updates?.checking;
  const res = updates?.results ?? {};
  const stale = list.filter((c) => c.updateAvailable).sort((a, b) => a.name.localeCompare(b.name));
  const current = list.filter((c) => c.updateAvailable === false).sort((a, b) => a.name.localeCompare(b.name));
  const failed = list.filter((c) => res[c.id]?.error && c.updateAvailable !== false).sort((a, b) => a.name.localeCompare(b.name));
  const unchecked = list.filter((c) => !res[c.id] && c.updateAvailable == null);
  const checked = !!updates?.checkedAt;

  const checkBtn = (
    <Button
      variant={checked ? "secondary" : "primary"}
      icon={<RefreshCw className={cn(checking && "animate-spin")} />}
      disabled={checking}
      onClick={() => check.mutate()}
    >
      {checking ? "Checking…" : "Check now"}
    </Button>
  );

  return (
    <div>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2 text-sm text-muted">
          <span>{docker?.version ? `Docker ${docker.version}` : "Docker"}</span>
          <span className="num">
            · {list.length} container{list.length === 1 ? "" : "s"}
          </span>
          {docker?.casaos && <Badge tone="neutral">CasaOS</Badge>}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {checkBtn}
          {stale.some((c) => !c.self) && (
            <Button variant="primary" icon={<ArrowUpCircle />} onClick={() => setBulk(true)} disabled={checking}>
              Update all <span className="num">({stale.filter((c) => !c.self).length})</span>
            </Button>
          )}
        </div>
      </div>

      <Summary st={updates} checking={checking} stale={stale.length} current={current.length} errors={failed.length} total={list.length} />

      {updatesError ? <ErrorNote className="mb-4" error={updatesError} /> : null}
      {updates?.error && <ErrorNote className="mb-4" error={`Last check failed: ${updates.error}`} />}

      {!checked && stale.length === 0 ? (
        <Card>
          <EmptyState icon={<Boxes />} title={checking ? "Checking images…" : "No update check yet"} action={checking ? undefined : checkBtn}>
            {updates?.intervalMs && updates.nextCheckAt
              ? `The first automatic check runs ${until(updates.nextCheckAt)}. `
              : ""}
            Wargo asks each image's registry for its current digest; nothing is pulled.
          </EmptyState>
        </Card>
      ) : stale.length === 0 ? (
        <Card>
          <EmptyState icon={<CircleCheck className="text-ok" />} title="All containers are up to date">
            {current.length} of {list.length} containers checked {updates?.checkedAt ? ago(updates.checkedAt) : ""}
            {failed.length ? `; ${failed.length} could not be checked.` : "."}
          </EmptyState>
        </Card>
      ) : (
        <Card>
          <CardHeader title="Image updates" icon={<ArrowUpCircle />} meta={`${stale.length}`} />
          <ListHeader />
          <ul>
            {stale.map((c) => (
              <StaleRow key={c.id} c={c} u={res[c.id]} onUpdate={() => actions.request("update", c)} busy={actions.busy === c.id} />
            ))}
          </ul>
          {stale.some((c) => res[c.id]?.error) && (
            <div className="flex items-start gap-2 border-t border-border px-4 py-2 text-xs text-muted">
              <TriangleAlert className="mt-px size-3.5 shrink-0 text-warn" />
              <span>Some results are from an earlier check because the latest check failed for that image.</span>
            </div>
          )}
        </Card>
      )}

      {current.length > 0 && (stale.length > 0 || failed.length > 0) && (
        <Collapsible title="Up to date" icon={<CircleCheck />} count={current.length}>
          {current.map((c) => (
            <li key={c.id} className="flex min-w-0 items-center gap-2.5 border-b border-border px-4 py-2 last:border-0">
              <ContainerIcon c={c} size="sm" />
              <span className="min-w-0 flex-1 truncate text-ui font-medium">{c.name}</span>
              <span className="num max-w-[45%] truncate text-xs text-muted" title={c.image}>
                {c.version}
              </span>
            </li>
          ))}
        </Collapsible>
      )}

      {failed.length > 0 && (
        <Collapsible title="Could not be checked" icon={<TriangleAlert />} count={failed.length} tone="warn">
          {failed.map((c) => (
            <li key={c.id} className="flex min-w-0 items-start gap-2.5 border-b border-border px-4 py-2 last:border-0">
              <ContainerIcon c={c} size="sm" className="mt-0.5" />
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 items-baseline gap-2">
                  <span className="truncate text-ui font-medium">{c.name}</span>
                  <ImageRef c={c} />
                </div>
                <div className="text-xs text-warn">{res[c.id]?.error}</div>
              </div>
            </li>
          ))}
        </Collapsible>
      )}

      {checked && unchecked.length > 0 && (
        <Notice tone="frost" className="mt-4">
          {unchecked.length} container{unchecked.length === 1 ? " was" : "s were"} created or recreated since the last check:{" "}
          {unchecked.map((c) => c.name).join(", ")}.
        </Notice>
      )}

      {/* Wargo updates itself on its own (it restarts), never as part of a batch. */}
      <BulkContainerUpdate open={bulk} containers={stale.filter((c) => !c.self)} onClose={() => setBulk(false)} />
      {actions.element}
    </div>
  );
}

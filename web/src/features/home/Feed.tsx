// SPDX-License-Identifier: AGPL-3.0-only
import { memo, useMemo, type ReactNode } from "react";
import { Link } from "react-router";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowDownToLine,
  CheckCircle2,
  CircleAlert,
  FilePen,
  FilePlus2,
  FileX2,
  FolderInput,
  PackageCheck,
  PanelRightClose,
  ScrollText,
  Sparkles,
} from "lucide-react";
import { IconButton } from "@/components/ui/button";
import { api, qs } from "@/lib/api";
import { ago, rate } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { Card, CardHeader } from "@/components/ui/card";
import { Meter, Skeleton } from "@/components/ui/misc";
import { countAvailable, useContainers, useContainerUpdatesStatus } from "@/features/containers/api";
import { pendingCount, usePkgStatus } from "@/features/updates/api";
import { useTorrents } from "@/features/torrents/api";
import type { AuditEntry } from "@/features/settings/Audit";
import type { EventsResponse } from "@/features/system/Events";

/*
 * Home's feed: what deserves a look right now, from data Wargo already has.
 * It reuses the queries (and caches) of the pages it summarises, polled
 * gently (1 to 5 minutes), and adds no background work on the server.
 */

const fileVerb: Record<string, [string, ReactNode]> = {
  "files.upload": ["Uploaded", <FilePlus2 key="i" />],
  "files.create": ["Created", <FilePlus2 key="i" />],
  "files.mkdir": ["New folder", <FilePlus2 key="i" />],
  "files.write": ["Edited", <FilePen key="i" />],
  "files.rename": ["Renamed", <FilePen key="i" />],
  "files.move": ["Moved", <FolderInput key="i" />],
  "files.copy": ["Copied", <FolderInput key="i" />],
  "files.extract": ["Extracted", <FolderInput key="i" />],
  "files.archive": ["Compressed", <FolderInput key="i" />],
  "files.trash": ["Moved to trash", <FileX2 key="i" />],
  "files.delete": ["Deleted", <FileX2 key="i" />],
};

// Stopped containers exit with 137 or 143 (SIGKILL, SIGTERM): not a failure.
const exitedBadly = /^Exited \((?!(?:0|137|143)\))\d+\)/;

const baseName = (p: string) => p.replace(/\/+$/, "").split("/").pop() || p;

function Section({ title, icon, to, children }: { title: string; icon: ReactNode; to?: string; children: ReactNode }) {
  return (
    <section className="border-b border-border/70 last:border-0">
      <div className="flex items-center gap-2 px-4 pb-1 pt-3 text-xs font-semibold text-subtle [&_svg]:size-3.5">
        {icon}
        <h3>{title}</h3>
        {to && (
          <Link to={to} className="-my-1.5 ml-auto inline-flex min-h-7 items-center rounded px-1.5 font-medium text-accent hover:underline">
            View
          </Link>
        )}
      </div>
      <ul className="pb-2">{children}</ul>
    </section>
  );
}

function Row({ to, icon, tone, title, detail, aside }: { to: string; icon?: ReactNode; tone?: "crit" | "warn" | "ok" | "muted"; title: ReactNode; detail?: ReactNode; aside?: ReactNode }) {
  return (
    <li>
      <Link to={to} className="flex min-w-0 items-start gap-2.5 px-4 py-1.5 transition-colors hover:bg-surface-2 focus-visible:bg-surface-2 focus-visible:outline-none">
        {icon && (
          <span
            className={cn(
              "mt-0.5 shrink-0 [&_svg]:size-4",
              tone === "crit" ? "text-crit" : tone === "warn" ? "text-warn" : tone === "ok" ? "text-ok" : "text-subtle",
            )}
            aria-hidden="true"
          >
            {icon}
          </span>
        )}
        <span className="min-w-0 flex-1">
          <span className="block truncate text-ui text-fg">{title}</span>
          {detail && <span className="block truncate text-xs text-muted">{detail}</span>}
        </span>
        {aside && <span className="num shrink-0 pt-0.5 text-xs text-subtle">{aside}</span>}
      </Link>
    </li>
  );
}

export const Feed = memo(function Feed({ onCollapse }: { onCollapse?: () => void }) {
  const { data: caps } = useCapabilities();
  const docker = !!caps?.docker?.available;
  const torrentsOn = !!caps?.transmission?.available;

  const containers = useContainers({ enabled: docker, refetchInterval: 60_000 });
  const ctrUpdates = useContainerUpdatesStatus({ enabled: docker });
  const pkg = usePkgStatus({ enabled: !!caps?.pkg?.available });
  const torrents = useTorrents({ enabled: torrentsOn, refetchInterval: 60_000 });
  const files = useQuery({
    queryKey: ["audit", "files"],
    queryFn: () => api.get<AuditEntry[]>(`/api/audit${qs({ prefix: "files.", limit: 40 })}`),
    refetchInterval: 60_000,
  });
  const events = useQuery({
    queryKey: ["system", "events", "24h", "error"],
    queryFn: () => api.get<EventsResponse>(`/api/system/events${qs({ range: "24h", level: "error" })}`),
    staleTime: 2 * 60_000,
    refetchInterval: 5 * 60_000,
  });

  const attention = useMemo(
    () =>
      (containers.data ?? []).filter(
        (c) => c.health === "unhealthy" || c.state === "dead" || (c.state === "exited" && exitedBadly.test(c.status)),
      ),
    [containers.data],
  );
  const pkgN = pendingCount(pkg.data);
  const ctrN = countAvailable(ctrUpdates.data);
  const list = torrents.data?.torrents ?? [];
  const active = list.filter((t) => t.status === "downloading" || t.status === "checking" || t.status === "download-wait").slice(0, 3);
  const broken = list.filter((t) => t.error > 0).slice(0, 2);
  const twoDays = Date.now() / 1000 - 2 * 86400;
  const finished = list.filter((t) => t.percentDone >= 1 && t.addedDate > twoDays && t.error === 0).slice(0, 3);
  const recentFiles = (files.data ?? []).filter((e) => e.result === "ok" && fileVerb[e.action]).slice(0, 6);
  const topEvents = (events.data?.events ?? []).slice(0, 3);

  const sources: [string, UseQueryResult][] = [
    ["Containers", containers],
    ["Updates", ctrUpdates],
    ["Updates", pkg],
    ["Downloads", torrents],
    ["Recent files", files],
    ["System events", events],
  ];
  // isLoading is false for disabled queries, so this waits only for what can load.
  const loading = sources.some(([, q]) => q.isLoading);
  const failed = [...new Set(sources.filter(([, q]) => q.isError).map(([name]) => name))];
  const quiet = !attention.length && !broken.length && !pkgN && !ctrN && !active.length && !finished.length && !recentFiles.length && !topEvents.length;

  return (
    <Card className="flex h-full min-h-0 flex-col">
      <CardHeader
        title="Feed"
        icon={<Sparkles />}
        tint="iris"
        meta="what deserves a look"
        actions={
          onCollapse && (
            <IconButton label="Collapse the feed" size="sm" onClick={onCollapse}>
              <PanelRightClose />
            </IconButton>
          )
        }
      />
      <div className="scroll-area min-h-0 flex-1">
        {loading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 5 }, (_, i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : quiet && !failed.length ? (
          <div className="flex flex-col items-center gap-2 px-6 py-12 text-center">
            <CheckCircle2 className="size-7 text-ok" aria-hidden="true" />
            <p className="font-medium">All quiet</p>
            <p className="text-sm text-muted">Nothing needs your attention right now.</p>
          </div>
        ) : (
          <>
            {(attention.length > 0 || broken.length > 0) && (
              <Section title="Needs attention" icon={<AlertTriangle />}>
                {attention.map((c) => (
                  <Row
                    key={c.id}
                    to={`/containers/${c.id}`}
                    icon={<CircleAlert />}
                    tone={c.health === "unhealthy" ? "warn" : "crit"}
                    title={c.name}
                    detail={c.health === "unhealthy" ? "Health check failing" : c.status}
                  />
                ))}
                {broken.map((t) => (
                  <Row key={t.id} to="/torrents?tab=downloads" icon={<CircleAlert />} tone="crit" title={t.name} detail={t.errorString || "Torrent error"} />
                ))}
              </Section>
            )}
            {(pkgN > 0 || ctrN > 0) && (
              <Section title="Updates" icon={<PackageCheck />} to="/updates">
                {pkgN > 0 && <Row to="/updates?tab=system" icon={<PackageCheck />} tone="ok" title={`${pkgN} system update${pkgN === 1 ? "" : "s"}`} detail="From the last check" />}
                {ctrN > 0 && <Row to="/updates?tab=containers" icon={<PackageCheck />} tone="ok" title={`${ctrN} container image${ctrN === 1 ? "" : "s"}`} detail="Newer versions available" />}
              </Section>
            )}
            {(active.length > 0 || finished.length > 0) && (
              <Section title="Downloads" icon={<ArrowDownToLine />} to="/torrents?tab=downloads">
                {active.map((t) => (
                  <li key={t.id} className="px-4 py-1.5">
                    <Link to="/torrents?tab=downloads" className="block min-w-0 rounded-sm focus-visible:outline-offset-2">
                      <div className="flex items-baseline gap-2">
                        <span className="min-w-0 flex-1 truncate text-ui">{t.name}</span>
                        <span className="num shrink-0 text-xs text-subtle">{Math.round(t.percentDone * 100)}%</span>
                      </div>
                      <Meter value={t.percentDone * 100} tone="frost" className="mt-1" label={`${t.name} progress`} />
                      <div className="num mt-0.5 text-2xs text-muted">{t.status === "downloading" ? `${rate(t.rateDownload)} · ${t.peersConnected} peers` : "Waiting"}</div>
                    </Link>
                  </li>
                ))}
                {finished.map((t) => (
                  <Row key={t.id} to="/torrents?tab=downloads" icon={<CheckCircle2 />} tone="ok" title={t.name} detail="Downloaded" aside={`Added ${ago(t.addedDate * 1000)}`} />
                ))}
              </Section>
            )}
            {recentFiles.length > 0 && (
              <Section title="Recent files" icon={<FilePen />} to="/files">
                {recentFiles.map((e) => {
                  const [verb, icon] = fileVerb[e.action];
                  return <Row key={e.id} to="/files" icon={icon} title={baseName(e.target)} detail={`${verb} · ${e.target}`} aside={ago(e.ts)} />;
                })}
              </Section>
            )}
            {topEvents.length > 0 && (
              <Section title="System events" icon={<ScrollText />} to="/system/events?level=error">
                {topEvents.map((e) => (
                  <Row
                    key={e.key}
                    to="/system/events?level=error"
                    icon={<CircleAlert />}
                    tone="crit"
                    title={e.message}
                    detail={`${e.origin}${e.count > 1 ? ` · ${e.count.toLocaleString()} times` : ""}`}
                    aside={ago(e.last)}
                  />
                ))}
              </Section>
            )}
            {failed.length > 0 && (
              <p className="flex items-center gap-2 px-4 py-3 text-xs text-muted">
                <CircleAlert className="size-3.5 shrink-0 text-warn" aria-hidden="true" />
                Couldn't load: {failed.join(", ")}
              </p>
            )}
          </>
        )}
      </div>
    </Card>
  );
});

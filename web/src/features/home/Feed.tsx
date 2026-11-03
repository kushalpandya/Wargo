// SPDX-License-Identifier: AGPL-3.0-only
import { memo, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowDownToLine,
  CheckCircle2,
  CircleAlert,
  Clapperboard,
  FilePen,
  FilePlus2,
  FileX2,
  FolderInput,
  Music,
  PackageCheck,
  PanelRightClose,
  Pause,
  Play,
  ScrollText,
  Sparkles,
  Tv,
} from "lucide-react";
import { IconButton } from "@/components/ui/button";
import { api, qs } from "@/lib/api";
import { ago, rate } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { Card, CardHeader, IconChip, type Tint } from "@/components/ui/card";
import { Meter, Skeleton } from "@/components/ui/misc";
import { countAvailable, useContainers, useContainerUpdatesStatus } from "@/features/containers/api";
import { pendingCount, usePkgStatus } from "@/features/updates/api";
import { useTorrents } from "@/features/torrents/api";
import { plexDecision, plexEpisode, plexLabel, plexThumb, usePlexActivity, type PlexItem, type PlexSession } from "@/features/plex/api";
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

/**
 * A top-level feed section: its own panel with a tinted icon and title, so
 * sections stand apart. Children are rows, or SubSections.
 */
function Section({ title, icon, tint, to, children }: { title: string; icon: ReactNode; tint: Tint; to?: string; children: ReactNode }) {
  return (
    <section className="overflow-hidden rounded-lg border border-border/70 bg-surface-2/40">
      <div className="flex items-center gap-2.5 px-3 pb-1 pt-2.5">
        <IconChip tint={tint} className="size-6 rounded-md [&_svg]:size-3.5">
          {icon}
        </IconChip>
        <h3 className="text-ui font-semibold">{title}</h3>
        {to && (
          <Link to={to} className="-my-1.5 ml-auto inline-flex min-h-7 items-center rounded px-1.5 text-xs font-medium text-accent hover:underline">
            View
          </Link>
        )}
      </div>
      <div className="flex flex-col gap-1 pb-2">{children}</div>
    </section>
  );
}

/**
 * Rows of a section, indented under its title with a guide line below the
 * section icon; with a title, a labelled group inside it (Plex: Now playing…).
 */
function SubSection({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <div className="ml-6 border-l border-border/80 pl-2.5">
      {title && <h4 className="px-3 pb-0.5 pt-2 text-2xs font-semibold uppercase tracking-wider text-subtle">{title}</h4>}
      <ul>{children}</ul>
    </div>
  );
}

const rowClass = "flex min-w-0 items-start gap-2.5 px-3 py-1.5";
const linkClass = "transition-colors hover:bg-surface-3/70 focus-visible:bg-surface-3/70 focus-visible:outline-none";

/** A feed row's target: a page in Wargo (to), another site in a new tab (href), or nothing. */
function Target({ to, href, className, children }: { to?: string; href?: string; className: string; children: ReactNode }) {
  if (to)
    return (
      <Link to={to} className={className}>
        {children}
      </Link>
    );
  if (href)
    return (
      <a href={href} target="_blank" rel="noreferrer" className={className}>
        {children}
        <span className="sr-only"> (opens in a new tab)</span>
      </a>
    );
  return <div className={className}>{children}</div>;
}

function Row({
  to,
  href,
  icon,
  tone,
  title,
  detail,
  aside,
}: {
  to?: string;
  href?: string;
  icon?: ReactNode;
  tone?: "crit" | "warn" | "ok" | "muted";
  title: ReactNode;
  detail?: ReactNode;
  aside?: ReactNode;
}) {
  return (
    <li>
      <Target to={to} href={href} className={cn(rowClass, (to || href) && linkClass)}>
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
      </Target>
    </li>
  );
}

/** Small poster from Plex (proxied by Wargo), or an icon when there is none. */
function Poster({ path, fallback }: { path?: string; fallback: ReactNode }) {
  // Remember which path failed, so a new poster for the same row gets a fresh try.
  const [failed, setFailed] = useState<string>();
  if (!path || failed === path) return fallback;
  return (
    <img
      src={plexThumb(path, 64)}
      alt=""
      loading="lazy"
      decoding="async"
      width={20}
      height={30}
      onError={() => setFailed(path)}
      className="h-[30px] w-5 rounded-sm bg-surface-3 object-cover"
    />
  );
}

const plexIcon = (type: string) => (type === "track" || type === "album" || type === "artist" ? <Music /> : type === "movie" ? <Clapperboard /> : <Tv />);

function sessionTitle(s: PlexSession) {
  const verb = s.type === "track" ? "listening to" : "watching";
  const what = plexLabel(s);
  return s.user ? `${s.user} is ${verb} ${what}` : `${verb[0].toUpperCase()}${verb.slice(1)} ${what}`;
}

function PlexStream({ s, href }: { s: PlexSession; href?: string }) {
  const pct = s.duration > 0 ? Math.min(100, (s.viewOffset / s.duration) * 100) : 0;
  const paused = s.state === "paused";
  const title = sessionTitle(s);
  const sub = [s.player || s.product, plexDecision[s.decision] ?? s.decision, paused ? "Paused" : s.state === "buffering" ? "Buffering" : ""].filter(Boolean).join(" · ");
  return (
    <li>
      <Target href={href} className={cn(rowClass, href && linkClass)}>
        <span className={cn("mt-0.5 shrink-0 [&_svg]:size-4", paused ? "text-subtle" : "text-ok")} aria-hidden="true">
          <Poster path={s.thumb} fallback={paused ? <Pause /> : <Play />} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline gap-2">
            <span className="min-w-0 flex-1 truncate text-ui text-fg">{title}</span>
            <span className="num shrink-0 text-xs text-subtle">{Math.round(pct)}%</span>
          </span>
          <Meter value={pct} tone={paused ? "frost" : "accent"} className="mt-1" label={`${title} progress`} />
          <span className="mt-0.5 block truncate text-2xs text-muted">{sub}</span>
        </span>
      </Target>
    </li>
  );
}

/** Recently watched, with back-to-back episodes of one show (same viewer) as one entry. */
interface Watched {
  item: PlexItem;
  /** Episodes in this entry, newest first. */
  episodes: PlexItem[];
}

function groupWatched(history: PlexItem[]): Watched[] {
  const out: Watched[] = [];
  for (const h of history) {
    const prev = out[out.length - 1];
    if (prev && h.type === "episode" && prev.item.type === "episode" && h.grandparentTitle && h.grandparentTitle === prev.item.grandparentTitle && h.user === prev.item.user) {
      prev.episodes.push(h);
    } else {
      out.push({ item: h, episodes: [h] });
    }
  }
  return out;
}

type Numbered = PlexItem & { parentIndex: number; index: number };
const byEpisode = (a: Numbered, b: Numbered) => a.parentIndex - b.parentIndex || a.index - b.index;

/** First and last episode of an entry by season and number (not watch order); undefined without numbers. */
function episodeRange(episodes: PlexItem[]): [Numbered, Numbered] | undefined {
  const numbered = episodes.filter((e): e is Numbered => e.parentIndex != null && e.index != null).sort(byEpisode);
  return numbered.length ? [numbered[0], numbered[numbered.length - 1]] : undefined;
}

/** Distinct episodes in an entry: rewatching one episode counts once. */
const distinctEpisodes = (episodes: PlexItem[]) => new Set(episodes.map((e) => plexEpisode(e) || e.key)).size;

function watchedTitle({ item, episodes }: Watched) {
  if (episodes.length < 2) return plexLabel(item);
  const range = episodeRange(episodes);
  if (!range) return item.grandparentTitle || plexLabel(item);
  const [first, last] = range;
  if (byEpisode(first, last) === 0) return [item.grandparentTitle, plexEpisode(first)].filter(Boolean).join(" · ");
  // "S1 E1 to E3" within a season, "S1 E9 to S2 E1" across seasons.
  const to = first.parentIndex === last.parentIndex ? `E${last.index}` : plexEpisode(last);
  return [item.grandparentTitle, `${plexEpisode(first)} to ${to}`].filter(Boolean).join(" · ");
}

function watchedDetail({ item: h, episodes }: Watched) {
  const n = distinctEpisodes(episodes);
  const title = h.type === "episode" && h.title && h.title !== h.grandparentTitle ? h.title : "";
  const what = n > 1 ? `${n} episodes` : episodes.length > 1 ? [title, `${episodes.length} plays`].filter(Boolean).join(" · ") : title;
  return [what, h.user ? `watched by ${h.user}` : "watched"].filter(Boolean).join(" · ");
}

function addedDetail(r: PlexItem) {
  switch (r.type) {
    case "movie":
      return "Movie";
    case "season":
      return r.leafCount ? `New season · ${r.leafCount} episode${r.leafCount === 1 ? "" : "s"}` : "New season";
    case "show":
      return "New show";
    case "episode":
      return r.title ? `Episode · ${r.title}` : "Episode";
    case "album":
      return "Album";
    case "track":
      return "Track";
  }
  return r.type;
}

export const Feed = memo(function Feed({ onCollapse }: { onCollapse?: () => void }) {
  const { data: caps } = useCapabilities();
  const docker = !!caps?.docker?.available;
  const torrentsOn = !!caps?.transmission?.available;
  const plexOn = !!caps?.plex?.available;

  const containers = useContainers({ enabled: docker, refetchInterval: 60_000 });
  const ctrUpdates = useContainerUpdatesStatus({ enabled: docker });
  const pkg = usePkgStatus({ enabled: !!caps?.pkg?.available });
  const torrents = useTorrents({ enabled: torrentsOn, refetchInterval: 60_000 });
  const plex = usePlexActivity({ enabled: plexOn });
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
  const plexData = plexOn ? plex.data : undefined;
  const streams = plexData?.sessions ?? [];
  const watched = groupWatched(plexData?.history ?? []).slice(0, 3);
  const added = (plexData?.recent ?? []).slice(0, 4);
  const plexLink = plexData?.webUrl;

  const sources: [string, UseQueryResult][] = [
    ["Containers", containers],
    ["Updates", ctrUpdates],
    ["Updates", pkg],
    ["Downloads", torrents],
    ["Recent files", files],
    ["System events", events],
  ];
  // isLoading is false for disabled queries, so this waits only for what can load.
  // Plex is left out: a slow server should not hold the whole feed, so its
  // section simply appears once its data arrives.
  const loading = sources.some(([, q]) => q.isLoading);
  const failed = [...new Set(sources.filter(([, q]) => q.isError).map(([name]) => name))];
  // A disabled query keeps its last isError, so Plex counts only while it is on.
  // A partial Plex answer (one section failed) still counts as a failure.
  if (plexOn && (plex.isError || (plexData?.errors && Object.keys(plexData.errors).length))) failed.push("Plex");
  const quiet =
    !attention.length &&
    !broken.length &&
    !pkgN &&
    !ctrN &&
    !active.length &&
    !finished.length &&
    !streams.length &&
    !watched.length &&
    !added.length &&
    !recentFiles.length &&
    !topEvents.length &&
    // Plex still loading: don't call it quiet before its section can show.
    !(plexOn && plex.isLoading);

  return (
    <Card className="flex h-full min-h-0 flex-col">
      <CardHeader
        title="Feed"
        icon={<Sparkles />}
        tint="iris"
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
          <div className="flex flex-col gap-2.5 p-3">
            {(attention.length > 0 || broken.length > 0) && (
              <Section title="Needs attention" icon={<AlertTriangle />} tint="rose">
                <SubSection>
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
                </SubSection>
              </Section>
            )}
            {(pkgN > 0 || ctrN > 0) && (
              <Section title="Updates" icon={<PackageCheck />} tint="mint" to="/updates">
                <SubSection>
                  {pkgN > 0 && <Row to="/updates?tab=system" icon={<PackageCheck />} tone="ok" title={`${pkgN} system update${pkgN === 1 ? "" : "s"}`} detail="From the last check" />}
                  {ctrN > 0 && <Row to="/updates?tab=containers" icon={<PackageCheck />} tone="ok" title={`${ctrN} container image${ctrN === 1 ? "" : "s"}`} detail="Newer versions available" />}
                </SubSection>
              </Section>
            )}
            {(streams.length > 0 || watched.length > 0 || added.length > 0) && (
              <Section title="Plex" icon={<Clapperboard />} tint="butter">
                {streams.length > 0 && (
                  <SubSection title="Now playing">
                    {streams.map((s) => (
                      <PlexStream key={`${s.user ?? ""}|${plexLabel(s)}|${s.player || s.product || ""}`} s={s} href={plexLink} />
                    ))}
                  </SubSection>
                )}
                {watched.length > 0 && (
                  <SubSection title="Recently watched">
                    {watched.map((w) => (
                      <Row
                        key={`${w.item.key}-${w.item.at}`}
                        href={plexLink}
                        icon={<Poster path={w.item.thumb} fallback={plexIcon(w.item.type)} />}
                        title={watchedTitle(w)}
                        detail={watchedDetail(w)}
                        aside={ago(w.item.at * 1000)}
                      />
                    ))}
                  </SubSection>
                )}
                {added.length > 0 && (
                  <SubSection title="Recently added">
                    {added.map((r) => (
                      <Row key={`${r.key}-${r.at}`} href={plexLink} icon={<Poster path={r.thumb} fallback={plexIcon(r.type)} />} title={plexLabel(r)} detail={addedDetail(r)} aside={ago(r.at * 1000)} />
                    ))}
                  </SubSection>
                )}
              </Section>
            )}
            {(active.length > 0 || finished.length > 0) && (
              <Section title="Downloads" icon={<ArrowDownToLine />} tint="sky" to="/torrents?tab=downloads">
                <SubSection>
                  {active.map((t) => (
                    <li key={t.id} className="px-3 py-1.5">
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
                </SubSection>
              </Section>
            )}
            {recentFiles.length > 0 && (
              <Section title="Recent files" icon={<FilePen />} tint="iris" to="/files">
                <SubSection>
                  {recentFiles.map((e) => {
                    const [verb, icon] = fileVerb[e.action];
                    return <Row key={e.id} to="/files" icon={icon} title={baseName(e.target)} detail={`${verb} · ${e.target}`} aside={ago(e.ts)} />;
                  })}
                </SubSection>
              </Section>
            )}
            {topEvents.length > 0 && (
              <Section title="System events" icon={<ScrollText />} tint="peach" to="/system/events?level=error">
                <SubSection>
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
                </SubSection>
              </Section>
            )}
            {failed.length > 0 && (
              <p className="flex items-center gap-2 px-1 py-1 text-xs text-muted">
                <CircleAlert className="size-3.5 shrink-0 text-warn" aria-hidden="true" />
                Couldn't load: {failed.join(", ")}
              </p>
            )}
          </div>
        )}
      </div>
    </Card>
  );
});

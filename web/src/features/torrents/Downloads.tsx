// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { ArrowDown, ArrowUp, Download, HardDrive, Pause, Play, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { useTopic } from "@/lib/ws";
import { bytes, duration, rate } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { Card } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { SearchInput, Segmented, Select } from "@/components/ui/form";
import { Badge, EmptyState, ErrorNote, Meter, PageHeader, Skeleton, Spinner, type Tone } from "@/components/ui/misc";
import { SortTh, Table, Td, Th, THead, Tr, useSort } from "@/components/ui/table";
import { toast } from "@/components/ui/toast";
import { errorMessage, type Torrent, type TorrentStatus, type TransmissionPayload } from "./api";

type Filter = "all" | "downloading" | "seeding" | "paused" | "errors";
type SortKey = "name" | "status" | "progress" | "down" | "up" | "eta" | "size" | "ratio" | "added";

const FILTERS: [Filter, string][] = [
  ["all", "All"],
  ["downloading", "Downloading"],
  ["seeding", "Seeding"],
  ["paused", "Paused"],
  ["errors", "Errors"],
];

const statusLabel: Record<TorrentStatus, string> = {
  stopped: "Paused",
  "check-wait": "Queued check",
  checking: "Checking",
  "download-wait": "Queued",
  downloading: "Downloading",
  "seed-wait": "Queued seed",
  seeding: "Seeding",
};

function statusTone(t: Torrent): Tone {
  if (t.error) return "crit";
  switch (t.status) {
    case "downloading":
      return "frost";
    case "seeding":
      return "ok";
    case "checking":
    case "check-wait":
    case "download-wait":
    case "seed-wait":
      return "warn";
    default:
      return "neutral";
  }
}

function matches(t: Torrent, f: Filter) {
  switch (f) {
    case "downloading":
      return ["downloading", "download-wait", "checking", "check-wait"].includes(t.status);
    case "seeding":
      return t.status === "seeding" || t.status === "seed-wait";
    case "paused":
      return t.status === "stopped";
    case "errors":
      return t.error !== 0;
    default:
      return true;
  }
}

function eta(t: Torrent) {
  if (t.status === "stopped") return "—";
  if (t.percentDone >= 1) return "Done";
  if (t.eta < 0) return "∞";
  return duration(t.eta);
}

function ratio(r: number) {
  if (r < 0) return "—";
  return r.toFixed(2);
}

const meterTone = (t: Torrent) => (t.error ? "crit" : t.percentDone >= 1 ? "ok" : t.status === "stopped" ? "accent" : "frost");

export default function Downloads({ embedded }: { embedded?: boolean } = {}) {
  const qc = useQueryClient();
  const caps = useCapabilities().data;
  const initial = useQuery({
    queryKey: ["transmission", "torrents"],
    queryFn: () => api.get<TransmissionPayload>("/api/transmission/torrents"),
    enabled: caps?.transmission?.configured !== false,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [live, setLive] = useState<TransmissionPayload | null>(null);
  useTopic<TransmissionPayload>("transmission", setLive);
  const data = live && (!initial.data || live.at >= initial.data.at) ? live : initial.data;

  const [filter, setFilter] = useState<Filter>("all");
  const [q, setQ] = useState("");
  const [pending, setPending] = useState<Record<number, string | undefined>>({});
  const [removing, setRemoving] = useState<Torrent | null>(null);

  const torrents = data?.torrents;
  const counts = useMemo(() => {
    const c: Record<Filter, number> = {
      all: 0,
      downloading: 0,
      seeding: 0,
      paused: 0,
      errors: 0,
    };
    for (const t of torrents ?? []) for (const f of Object.keys(c) as Filter[]) if (matches(t, f)) c[f]++;
    return c;
  }, [torrents]);

  const visible = useMemo(() => {
    const words = q.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return (torrents ?? []).filter((t) => matches(t, filter) && words.every((w) => t.name.toLowerCase().includes(w)));
  }, [torrents, filter, q]);

  const { sorted, sort, toggle, setSort } = useSort<Torrent, SortKey>(
    visible,
    { key: "added", dir: "desc" },
    {
      name: (t) => t.name,
      status: (t) => (t.error ? "0" : t.status),
      progress: (t) => t.percentDone,
      down: (t) => t.rateDownload,
      up: (t) => t.rateUpload,
      eta: (t) => (t.eta < 0 ? Number.MAX_SAFE_INTEGER : t.eta),
      size: (t) => t.sizeWhenDone,
      ratio: (t) => t.uploadRatio,
      added: (t) => t.addedDate,
    },
  );

  const act = async (t: Torrent, kind: "start" | "stop" | "remove" | "remove-data") => {
    setPending((p) => ({ ...p, [t.id]: kind }));
    try {
      if (kind === "start" || kind === "stop") {
        await api.post(`/api/transmission/torrents/${t.id}/${kind}`);
        toast.ok(kind === "start" ? "Resumed" : "Paused", t.name);
      } else {
        await api.del(`/api/transmission/torrents/${t.id}?deleteData=${kind === "remove-data"}`);
        toast.ok(kind === "remove-data" ? "Removed torrent and data" : "Removed torrent", t.name);
        setRemoving(null);
      }
      if (!live) qc.invalidateQueries({ queryKey: ["transmission", "torrents"] });
    } catch (e) {
      toast.error("Action failed", errorMessage(e));
    } finally {
      setPending((p) => ({ ...p, [t.id]: undefined }));
    }
  };

  const stats = data?.stats;
  const notReady = caps && !caps.transmission?.configured;

  return (
    <div className="@container">
      {!embedded && (
        <PageHeader
          title="Downloads"
          description={`Live view of Transmission${caps?.transmission?.version ? ` ${caps.transmission.version.split(" ")[0]}` : ""}.`}
        />
      )}

      {notReady ? (
        <Card>
          <EmptyState
            icon={<Download />}
            title="Transmission is not connected"
            action={
              <Button asChild variant="primary">
                <Link to="/settings/integrations">Open settings</Link>
              </Button>
            }
          >
            Connect Wargo to Transmission in Settings → Integrations to see and manage downloads here.
          </EmptyState>
        </Card>
      ) : (
        <>
          <Card className="mb-4 grid grid-cols-2 @2xl:grid-cols-4 @2xl:divide-x @2xl:divide-border">
            <StatCell label="Download" icon={<ArrowDown />} value={stats ? rate(stats.downloadSpeed) : undefined} tone="text-frost" />
            <StatCell
              label="Upload"
              icon={<ArrowUp />}
              value={stats ? rate(stats.uploadSpeed) : undefined}
              tone="text-ok"
              className="@max-2xl:border-l @max-2xl:border-border"
            />
            <StatCell
              label="Active"
              value={stats ? String(stats.activeTorrentCount) : undefined}
              sub={stats ? `${stats.pausedTorrentCount} paused · ${torrents?.length ?? 0} total` : undefined}
              className="@max-2xl:border-t @max-2xl:border-border"
            />
            <StatCell
              label="Free space"
              icon={<HardDrive />}
              value={stats ? (stats.freeSpace !== undefined ? bytes(stats.freeSpace) : "—") : undefined}
              className="@max-2xl:border-l @max-2xl:border-t @max-2xl:border-border"
            />
          </Card>

          {data?.error && <ErrorNote error={`Transmission: ${data.error}`} className="mb-4" />}

          <Card>
            <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2.5">
              <div className="flex items-center gap-1.5 @2xl:hidden">
                <label htmlFor="filter-torrents" className="text-2xs font-semibold uppercase tracking-wider text-subtle">
                  Show
                </label>
                <Select id="filter-torrents" className="w-auto" value={filter} onChange={(e) => setFilter(e.target.value as Filter)}>
                  {FILTERS.map(([v, l]) => (
                    <option key={v} value={v}>
                      {l} ({counts[v]})
                    </option>
                  ))}
                </Select>
              </div>
              <div className="@max-2xl:hidden">
                <Segmented<Filter>
                  label="Filter torrents"
                  value={filter}
                  onChange={setFilter}
                  options={FILTERS.map(([v, l]) => ({
                    value: v,
                    label: (
                      <>
                        {l}
                        <span className={cn("num text-2xs", v === "errors" && counts.errors ? "text-crit" : "text-subtle")}>{counts[v]}</span>
                      </>
                    ),
                  }))}
                />
              </div>
              <SearchInput
                value={q}
                onChange={setQ}
                placeholder="Filter by name…"
                label="Filter torrents by name"
                className="min-w-0 flex-1 basis-44 @max-2xl:order-last @max-2xl:basis-full @5xl:max-w-xs"
              />
              <div className="flex items-center gap-1.5 @3xl:hidden">
                <label htmlFor="sort-torrents" className="text-2xs font-semibold uppercase tracking-wider text-subtle">
                  Sort
                </label>
                <Select
                  id="sort-torrents"
                  className="w-auto"
                  value={`${sort.key}:${sort.dir}`}
                  onChange={(e) => {
                    const [key, dir] = e.target.value.split(":") as [SortKey, "asc" | "desc"];
                    setSort({ key, dir });
                  }}
                >
                  <option value="added:desc">Recently added</option>
                  <option value="name:asc">Name</option>
                  <option value="progress:desc">Progress</option>
                  <option value="down:desc">Download speed</option>
                  <option value="up:desc">Upload speed</option>
                  <option value="size:desc">Size</option>
                  <option value="ratio:desc">Ratio</option>
                </Select>
              </div>
            </div>

            {initial.isLoading && !live ? (
              <div className="space-y-2 p-3.5">
                {Array.from({ length: 5 }, (_, i) => (
                  <Skeleton key={i} className="h-8" />
                ))}
              </div>
            ) : initial.error && !live ? (
              <div className="p-3.5">
                <ErrorNote error={initial.error} />
              </div>
            ) : sorted.length === 0 ? (
              <EmptyState icon={<Download />} title={torrents?.length ? "Nothing matches" : "No torrents"}>
                {torrents?.length ? "Try another filter." : "Queue something from Torrent search and it will appear here."}
              </EmptyState>
            ) : (
              <>
                <div className="@max-3xl:hidden">
                  <Table className="table-fixed">
                    <THead>
                      <tr>
                        <SortTh k="name" sort={sort} onSort={toggle} defaultDir="asc">
                          Name
                        </SortTh>
                        <SortTh k="status" sort={sort} onSort={toggle} defaultDir="asc" className="w-32">
                          Status
                        </SortTh>
                        <SortTh k="progress" sort={sort} onSort={toggle} className="w-40 @7xl:w-48">
                          Progress
                        </SortTh>
                        <SortTh k="down" sort={sort} onSort={toggle} align="right" className="w-24">
                          ↓ Rate
                        </SortTh>
                        <SortTh k="up" sort={sort} onSort={toggle} align="right" className="w-24 @max-5xl:hidden">
                          ↑ Rate
                        </SortTh>
                        <SortTh k="eta" sort={sort} onSort={toggle} defaultDir="asc" align="right" className="w-20 @max-7xl:hidden">
                          ETA
                        </SortTh>
                        <SortTh k="size" sort={sort} onSort={toggle} align="right" className="w-24 @max-5xl:hidden">
                          Size
                        </SortTh>
                        <SortTh k="ratio" sort={sort} onSort={toggle} align="right" className="w-20 @max-7xl:hidden">
                          Ratio
                        </SortTh>
                        <Th className="w-[4.75rem]">
                          <span className="sr-only">Actions</span>
                        </Th>
                      </tr>
                    </THead>
                    <tbody>
                      {sorted.map((t) => (
                        <TorrentRow key={t.id} t={t} pending={pending[t.id]} onAct={act} onRemove={() => setRemoving(t)} />
                      ))}
                    </tbody>
                  </Table>
                </div>
                <ul className="@3xl:hidden" aria-label="Torrents">
                  {sorted.map((t) => (
                    <TorrentCard key={t.id} t={t} pending={pending[t.id]} onAct={act} onRemove={() => setRemoving(t)} />
                  ))}
                </ul>
              </>
            )}
          </Card>
        </>
      )}

      <RemoveDialog
        torrent={removing}
        pending={removing ? pending[removing.id] : undefined}
        onClose={() => setRemoving(null)}
        onRemove={(withData) => removing && act(removing, withData ? "remove-data" : "remove")}
      />
    </div>
  );
}

function StatCell({
  label,
  value,
  sub,
  icon,
  tone,
  className,
}: {
  label: string;
  value?: string;
  sub?: string;
  icon?: React.ReactNode;
  tone?: string;
  className?: string;
}) {
  return (
    <div className={cn("min-w-0 px-3.5 py-3", className)}>
      <div className="flex items-center gap-1.5 text-2xs font-medium uppercase tracking-wider text-subtle [&_svg]:size-3.5">
        {icon && <span className={tone}>{icon}</span>}
        {label}
      </div>
      {value === undefined ? <Skeleton className="mt-1.5 h-6 w-20" /> : <div className="num mt-0.5 truncate text-xl font-medium leading-tight">{value}</div>}
      {sub && <div className="mt-0.5 truncate text-xs text-muted">{sub}</div>}
    </div>
  );
}

interface RowProps {
  t: Torrent;
  pending?: string;
  onAct: (t: Torrent, kind: "start" | "stop") => void;
  onRemove: () => void;
}

function StatusBadge({ t }: { t: Torrent }) {
  return <Badge tone={statusTone(t)}>{t.error ? "Error" : statusLabel[t.status]}</Badge>;
}

function Progress({ t }: { t: Torrent }) {
  const p = t.percentDone * 100;
  return (
    <div className="flex items-center gap-2">
      <Meter value={p} tone={meterTone(t)} className="flex-1" label={`${t.name}: ${p.toFixed(1)}% done`} />
      <span className="num w-12 shrink-0 text-right text-xs text-muted">{t.percentDone >= 1 ? "100%" : `${(Math.floor(p * 10) / 10).toFixed(1)}%`}</span>
    </div>
  );
}

function RowActions({ t, pending, onAct, onRemove, size = "sm" }: RowProps & { size?: "sm" | "md" }) {
  const paused = t.status === "stopped";
  return (
    <div className="flex justify-end gap-0.5">
      <IconButton
        size={size}
        label={paused ? `Resume ${t.name}` : `Pause ${t.name}`}
        title={paused ? "Resume" : "Pause"}
        disabled={!!pending}
        onClick={() => onAct(t, paused ? "start" : "stop")}
      >
        {pending === "start" || pending === "stop" ? <Spinner /> : paused ? <Play /> : <Pause />}
      </IconButton>
      <IconButton size={size} label={`Remove ${t.name}`} title="Remove…" disabled={!!pending} onClick={onRemove} className="hover:text-crit">
        <Trash2 />
      </IconButton>
    </div>
  );
}

function TorrentRow(props: RowProps) {
  const { t } = props;
  return (
    <Tr>
      <Td className="py-1.5">
        <div className="truncate font-medium" title={t.name}>
          {t.name}
        </div>
        {t.error ? (
          <div className="truncate text-xs text-crit" title={t.errorString}>
            {t.errorString || "Error"}
          </div>
        ) : (
          <div className="num truncate text-2xs text-subtle">
            {t.peersConnected} peer{t.peersConnected === 1 ? "" : "s"} · {t.downloadDir}
          </div>
        )}
      </Td>
      <Td>
        <StatusBadge t={t} />
      </Td>
      <Td>
        <Progress t={t} />
      </Td>
      <Td className={cn("num text-right", t.rateDownload ? "text-frost" : "text-subtle")}>{t.rateDownload ? rate(t.rateDownload) : "—"}</Td>
      <Td className={cn("num text-right @max-5xl:hidden", t.rateUpload ? "text-ok" : "text-subtle")}>{t.rateUpload ? rate(t.rateUpload) : "—"}</Td>
      <Td className="num text-right text-muted @max-7xl:hidden">{eta(t)}</Td>
      <Td className="num text-right text-muted @max-5xl:hidden">{bytes(t.sizeWhenDone)}</Td>
      <Td className="num text-right text-muted @max-7xl:hidden">{ratio(t.uploadRatio)}</Td>
      <Td className="py-1">
        <RowActions {...props} />
      </Td>
    </Tr>
  );
}

function TorrentCard(props: RowProps) {
  const { t } = props;
  return (
    <li className="border-b border-border px-3.5 py-3">
      <div className="flex items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="break-words text-sm font-medium leading-snug [overflow-wrap:anywhere]">{t.name}</div>
          <div className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-xs">
            <StatusBadge t={t} />
            <span className="num text-muted">{bytes(t.sizeWhenDone)}</span>
            <span className="num text-muted">ETA {eta(t)}</span>
            <span className="num text-muted">Ratio {ratio(t.uploadRatio)}</span>
          </div>
        </div>
        <RowActions {...props} size="md" />
      </div>
      <div className="mt-2">
        <Progress t={t} />
      </div>
      <div className="num mt-1 flex gap-3 text-xs">
        <span className={t.rateDownload ? "text-frost" : "text-subtle"}>↓ {rate(t.rateDownload)}</span>
        <span className={t.rateUpload ? "text-ok" : "text-subtle"}>↑ {rate(t.rateUpload)}</span>
        <span className="text-subtle">{t.peersConnected} peers</span>
      </div>
      {t.error !== 0 && <p className="mt-1 break-words text-xs text-crit">{t.errorString || "Error"}</p>}
    </li>
  );
}

function RemoveDialog({
  torrent,
  pending,
  onClose,
  onRemove,
}: {
  torrent: Torrent | null;
  pending?: string;
  onClose: () => void;
  onRemove: (withData: boolean) => void;
}) {
  return (
    <Dialog
      open={!!torrent}
      onOpenChange={(o) => !o && onClose()}
      size="sm"
      title="Remove torrent?"
      description="Removing the torrent stops it and takes it off Transmission's list. Removing with data also deletes the downloaded files from disk."
      footer={
        <>
          <Button autoFocus onClick={onClose}>
            Cancel
          </Button>
          <Button loading={pending === "remove"} disabled={!!pending} onClick={() => onRemove(false)}>
            Remove torrent
          </Button>
          <Button variant="danger" loading={pending === "remove-data"} disabled={!!pending} onClick={() => onRemove(true)}>
            Remove with data
          </Button>
        </>
      }
    >
      {torrent && (
        <div className="rounded-md border border-border bg-surface-2 px-3 py-2">
          <p className="break-words text-sm font-medium [overflow-wrap:anywhere]">{torrent.name}</p>
          <p className="num mt-0.5 text-xs text-muted">
            {bytes(torrent.sizeWhenDone)} · {(torrent.percentDone * 100).toFixed(1)}% · {torrent.downloadDir}
          </p>
        </div>
      )}
    </Dialog>
  );
}

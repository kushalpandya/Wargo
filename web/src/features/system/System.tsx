// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo } from "react";
import { Navigate, useParams } from "react-router";
import { ArrowDown, ArrowUp, Activity, HardDrive, ListTree, Network, ScrollText } from "lucide-react";
import { useIsDark } from "@/lib/theme";
import { NavTabs } from "@/components/ui/tabs";
import { bytes, level, pct, rate } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card, CardBody, CardHeader, Stat } from "@/components/ui/card";
import { Meter, PageHeader, Skeleton } from "@/components/ui/misc";
import { TimeChart } from "@/components/charts/TimeChart";
import { useMetrics, usePublicIP, useSeries, type Point, type Snapshot } from "@/features/system/useMetrics";
import { StorageCard } from "@/features/system/Storage";
import { SensorsCard } from "@/features/system/Sensors";
import { ProcessesPanel } from "@/features/processes/Processes";
import { SummaryTiles } from "./Tiles";
import { EventsPanel } from "./Events";
import { LinkIcon, linkDetails, linkName } from "./LinkLabel";
import { useCapabilities } from "@/app/session";

const toneOf = (p: number) => level(p);

/** Distinct, theme-aware colour for core i of n. */
function coreColor(i: number, n: number, dark: boolean) {
  const h = Math.round((i * 360) / Math.max(n, 1) + 25) % 360;
  // Same lightness and chroma for every core (OKLCH), so none stands out.
  return dark ? `oklch(0.8 0.1 ${h})` : `oklch(0.58 0.13 ${h})`;
}

function CpuMemoryCard({ snap: s, points }: { snap: Snapshot | null; points: Point[] }) {
  const dark = useIsDark();
  const n = s?.cpu.perCore.length ?? points.at(-1)?.cores?.length ?? 0;
  const coreData = useMemo(() => {
    const xs = points.map((p) => p.t / 1000);
    // Samples from before an upgrade have no per-core values: gaps.
    const ys = Array.from({ length: n }, (_, i) => points.map((p) => p.cores?.[i] ?? null));
    return [xs, ...ys] as unknown as [number[], ...number[][]];
  }, [points, n]);
  const coreSeries = useMemo(() => Array.from({ length: n }, (_, i) => ({ label: `Core ${i}`, color: coreColor(i, n, dark), width: 1.25 })), [n, dark]);
  const hasSwap = !!s?.memory.swapTotal;
  const memData = useSeries(points, hasSwap ? ["mem", "swap"] : ["mem"]);

  return (
    <Card>
      <CardHeader title="CPU & memory" icon={<Activity />} tint="iris" meta={s ? `${pct(s.cpu.total)} · ${n} threads · last 10 min` : "last 10 min"} />
      <CardBody>
        {n > 0 ? (
          <TimeChart label="Usage of each processor core over the last 10 minutes" data={coreData} series={coreSeries} height={190} yMax={100} yFormat={(v) => `${v}%`} />
        ) : (
          <Skeleton className="h-[190px]" />
        )}
        {s && (
          <ul aria-label="Per-core usage" className="mt-3 grid grid-cols-[repeat(auto-fill,minmax(4.25rem,1fr))] gap-1.5">
            {s.cpu.perCore.map((v, i) => {
              const t = toneOf(v);
              return (
                <li key={i} aria-label={`Core ${i}: ${Math.round(v)}%`} className="flex items-center gap-1.5 rounded border border-border bg-surface-2 px-1.5 py-0.5 text-2xs">
                  <span className="size-2 shrink-0 rounded-full" style={{ background: coreColor(i, n, dark) }} aria-hidden="true" />
                  <span className="num text-subtle" aria-hidden="true">
                    {i}
                  </span>
                  <span className={cn("num ml-auto", t === "crit" ? "text-crit" : t === "warn" ? "text-warn" : "text-fg")} aria-hidden="true">
                    {Math.round(v)}%
                  </span>
                </li>
              );
            })}
          </ul>
        )}
        <div className="mt-3 grid grid-cols-3 gap-3 border-t border-border pt-3">
          <Stat label="Load 1m" value={s?.cpu.load[0].toFixed(2) ?? "–"} />
          <Stat label="Load 5m" value={s?.cpu.load[1].toFixed(2) ?? "–"} />
          <Stat label="Load 15m" value={s?.cpu.load[2].toFixed(2) ?? "–"} />
        </div>
        <div className="mt-3 border-t border-border pt-3">
          <div className="mb-1.5 flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs">
            <span className="flex items-center gap-3 text-muted">
              <span className="inline-flex items-center gap-1.5">
                <span className="h-1 w-3.5 rounded-full bg-mint" /> Memory
              </span>
              {hasSwap && (
                <span className="inline-flex items-center gap-1.5">
                  <span className="h-1 w-3.5 rounded-full bg-peach" /> Swap
                </span>
              )}
            </span>
            <span className="num truncate">{s ? `${bytes(s.memory.used)} used · ${bytes(s.memory.cached)} cache · ${bytes(s.memory.available)} free` : "–"}</span>
          </div>
          <TimeChart
            label="Memory and swap usage over the last 10 minutes"
            data={memData}
            height={80}
            yMax={100}
            series={[{ label: "Memory", color: "--mint", fill: true }, ...(hasSwap ? [{ label: "Swap", color: "--peach", dash: true }] : [])]}
            yFormat={(v) => `${v}%`}
          />
          {s && (
            <Meter
              value={0}
              label="Memory composition"
              className="mt-2 h-2"
              segments={[
                { value: (s.memory.used / s.memory.total) * 100, className: "bg-mint", label: "Used" },
                { value: ((s.memory.cached + s.memory.buffers) / s.memory.total) * 100, className: "bg-mint/35", label: "Cache" },
              ]}
            />
          )}
        </div>
      </CardBody>
    </Card>
  );
}

function NetworkCard({ snap, points }: { snap: Snapshot | null; points: Point[] }) {
  const data = useSeries(points, ["rx", "tx"]);
  const ifs = (snap?.net ?? []).filter((n) => n.rxTotal + n.txTotal > 0);
  const pub = usePublicIP();
  const primary = snap?.net?.find((n) => n.name === snap.primaryIf);
  // First IPv4 address of the interface carrying the default route.
  const lan = (primary?.addrs?.find((a) => !a.includes(":")) ?? primary?.addrs?.[0])?.replace(/\/\d+$/, "");
  return (
    <Card>
      <CardHeader title="Network" icon={<Network />} tint="sky" meta={snap ? `↓ ${rate(snap.netRx)}  ↑ ${rate(snap.netTx)}` : undefined} />
      <CardBody className="pb-2">
        <TimeChart
          label="Network throughput"
          data={data}
          height={150}
          series={[
            { label: "Download", color: "--sky", fill: true },
            { label: "Upload", color: "--iris" },
          ]}
          yFormat={(v) => bytes(v, 0)}
        />
        <div className="mt-2 flex items-center gap-4 text-xs text-muted">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-1 w-3.5 rounded-full bg-sky" /> Download
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-1 w-3.5 rounded-full bg-iris" /> Upload
          </span>
        </div>
      </CardBody>
      <dl className="grid grid-cols-2 gap-3 border-t border-border px-4 py-2.5 text-xs">
        <div className="min-w-0">
          <dt className="text-subtle">LAN address</dt>
          <dd className="num truncate text-fg" title={primary?.addrs?.join(", ")}>
            {lan ?? "–"}
          </dd>
        </div>
        <div className="min-w-0">
          <dt className="text-subtle">Public IP</dt>
          <dd
            className={cn("num truncate", pub.data?.ip ? "text-fg" : "text-subtle")}
            title={pub.data?.ip ? `Looked up via ${pub.data.source}, refreshed every 30 minutes` : pub.data?.error}
          >
            {pub.data?.ip ?? (pub.isLoading ? "Looking up…" : "Unavailable")}
          </dd>
        </div>
      </dl>
      <div className="border-t border-border">
        {ifs.map((n) => {
          const name = linkName(n.link);
          const details = [linkDetails(n.link), n.addrs?.[0]?.replace(/\/\d+$/, "")].filter(Boolean).join(" · ");
          return (
            <div key={n.name} className="flex items-center gap-3 border-b border-border px-4 py-2 text-xs last:border-0">
              <LinkIcon link={n.link} className={cn("size-4", n.link?.up === false ? "text-subtle" : n.name === snap?.primaryIf ? "text-frost" : "text-muted")} />
              <div className="min-w-0 flex-1">
                <div className="flex min-w-0 items-baseline gap-1.5">
                  <span className="truncate font-medium text-fg">{name ?? n.name}</span>
                  {name && <span className="num shrink-0 text-subtle">{n.name}</span>}
                </div>
                {details && (
                  <div className="num truncate text-subtle" title={n.addrs?.join(", ")}>
                    {details}
                  </div>
                )}
              </div>
              <span className="num inline-flex w-24 shrink-0 items-center justify-end gap-1 text-frost">
                <ArrowDown className="size-3" />
                {rate(n.rxRate)}
              </span>
              <span className="num inline-flex w-24 shrink-0 items-center justify-end gap-1 text-accent">
                <ArrowUp className="size-3" />
                {rate(n.txRate)}
              </span>
            </div>
          );
        })}
      </div>
    </Card>
  );
}

function DiskIOCard({ snap, points }: { snap: Snapshot | null; points: Point[] }) {
  const data = useSeries(points, ["dr", "dw"]);
  return (
    <Card>
      <CardHeader title="Disk activity" icon={<HardDrive />} tint="peach" meta={snap ? `R ${rate(snap.diskRead)}  W ${rate(snap.diskWrite)}` : undefined} />
      <CardBody className="pb-2">
        <TimeChart
          label="Disk throughput"
          data={data}
          height={150}
          series={[
            { label: "Read", color: "--mint", fill: true },
            { label: "Write", color: "--peach" },
          ]}
          yFormat={(v) => bytes(v, 0)}
        />
        <div className="mt-2 flex items-center gap-4 text-xs text-muted">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-1 w-3.5 rounded-full bg-mint" /> Read
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-1 w-3.5 rounded-full bg-peach" /> Write
          </span>
        </div>
      </CardBody>
      <div className="border-t border-border">
        {(snap?.disks ?? []).map((d) => (
          <div key={d.name} className="flex items-center gap-3 border-b border-border px-4 py-2 text-xs last:border-0">
            <span className="num w-20 truncate font-medium">{d.name}</span>
            <div className="flex flex-1 items-center gap-2">
              <Meter value={d.busy} tone={toneOf(d.busy)} label={`${d.name} busy`} className="max-w-32" />
              <span className="num w-9 text-right text-subtle">{pct(d.busy)}</span>
            </div>
            <span className="num w-20 text-right text-ok">R {rate(d.readRate)}</span>
            <span className="num w-20 text-right text-warn">W {rate(d.writeRate)}</span>
          </div>
        ))}
      </div>
    </Card>
  );
}

function ActivityMonitor() {
  const { latest: s, points } = useMetrics();
  const tempSeries = useSeries(points, ["temp"]);
  // No hwmon (macOS): no Sensors card, the CPU card takes the full row.
  const sensors = useCapabilities().data?.metrics?.sensors !== false;
  return (
    <>
      <SummaryTiles snap={s} points={points} />
      <div className="mt-3 grid grid-cols-1 gap-3 lg:grid-cols-2">
        <div className={cn("min-w-0", !sensors && "lg:col-span-2")}>
          <CpuMemoryCard snap={s} points={points} />
        </div>
        {sensors && (
          // On wide screens the CPU card sets the row height; sensors scroll within it.
          <div className="min-w-0 lg:relative">
            <SensorsCard snap={s} series={tempSeries} className="lg:absolute lg:inset-0" />
          </div>
        )}
      </div>
      <div className="mt-3 grid grid-cols-1 gap-3 lg:grid-cols-2">
        <NetworkCard snap={s} points={points} />
        <DiskIOCard snap={s} points={points} />
      </div>
      <div className="mt-3">
        <StorageCard />
      </div>
    </>
  );
}

export default function System() {
  const { "*": sub } = useParams();
  // No journal (macOS): no Events tab, and its URL falls back to the overview.
  const events = useCapabilities().data?.events?.supported !== false;
  if (sub && sub !== "processes" && !(sub === "events" && events)) return <Navigate to="/system" replace />;
  const tab = sub || "activity";
  return (
    <div className="min-w-0">
      <PageHeader
        title="System"
        description={
          tab === "processes"
            ? "Everything running on this host."
            : tab === "events"
              ? "Recent errors and warnings from the system, the kernel, containers and Wargo."
              : "Live resource usage, hardware sensors, network and storage."
        }
      />
      <NavTabs
        label="System sections"
        items={[
          { to: "/system", end: true, label: <><Activity /> Activity Monitor</> },
          { to: "/system/processes", label: <><ListTree /> Processes</> },
          ...(events ? [{ to: "/system/events", label: <><ScrollText /> Events</> }] : []),
        ]}
      />
      {tab === "processes" ? <ProcessesPanel embedded /> : tab === "events" ? <EventsPanel /> : <ActivityMonitor />}
    </div>
  );
}

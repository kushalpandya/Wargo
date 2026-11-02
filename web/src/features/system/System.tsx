// SPDX-License-Identifier: AGPL-3.0-only
import { ArrowDown, ArrowUp, Activity, Cpu, HardDrive, Network } from "lucide-react";
import { bytes, level, pct, rate } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card, CardBody, CardHeader, Stat } from "@/components/ui/card";
import { Meter, PageHeader, Skeleton } from "@/components/ui/misc";
import { TimeChart } from "@/components/charts/TimeChart";
import { useMetrics, useSeries, type Point, type Snapshot } from "@/features/system/useMetrics";
import { StorageCard } from "@/features/system/Storage";
import { SensorsCard } from "@/features/system/Sensors";
import { ProcessesPanel } from "@/features/processes/Processes";
import { SummaryTiles } from "./Tiles";
import { LinkIcon, linkDetails, linkName } from "./LinkLabel";

const toneOf = (p: number) => level(p);

function CoreGrid({ cores }: { cores: number[] }) {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(2.75rem,1fr))] gap-1.5" role="list" aria-label="Per-core usage">
      {cores.map((v, i) => {
        const t = toneOf(v);
        return (
          <div
            key={i}
            role="listitem"
            aria-label={`Core ${i}: ${Math.round(v)}%`}
            className="relative overflow-hidden rounded border border-border bg-surface-2 px-1.5 py-1"
          >
            <div
              className={cn("absolute inset-x-0 bottom-0 transition-[height] duration-500", t === "crit" ? "bg-crit/25" : t === "warn" ? "bg-warn/25" : "bg-accent/20")}
              style={{ height: `${Math.max(2, v)}%` }}
            />
            <div className="relative text-2xs text-muted">{i}</div>
            <div className="num relative text-xs">{Math.round(v)}</div>
          </div>
        );
      })}
    </div>
  );
}

function NetworkCard({ snap, points }: { snap: Snapshot | null; points: Point[] }) {
  const data = useSeries(points, ["rx", "tx"]);
  const ifs = (snap?.net ?? []).filter((n) => n.rxTotal + n.txTotal > 0);
  return (
    <Card>
      <CardHeader title="Network" icon={<Network />} meta={snap ? `↓ ${rate(snap.netRx)}  ↑ ${rate(snap.netTx)}` : undefined} />
      <CardBody className="pb-2">
        <TimeChart
          label="Network throughput"
          data={data}
          height={150}
          series={[
            { label: "Download", color: "--frost", fill: true },
            { label: "Upload", color: "--accent" },
          ]}
          yFormat={(v) => bytes(v, 0)}
        />
        <div className="mt-2 flex items-center gap-4 text-xs text-muted">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-0.5 w-3 rounded bg-frost" /> Download
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-0.5 w-3 rounded bg-accent" /> Upload
          </span>
        </div>
      </CardBody>
      <div className="border-t border-border">
        {ifs.map((n) => {
          const name = linkName(n.link);
          const details = [linkDetails(n.link), n.addrs?.[0]?.replace(/\/\d+$/, "")].filter(Boolean).join(" · ");
          return (
            <div key={n.name} className="flex items-center gap-3 border-b border-border px-3.5 py-2 text-xs last:border-0">
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
      <CardHeader title="Disk activity" icon={<HardDrive />} meta={snap ? `R ${rate(snap.diskRead)}  W ${rate(snap.diskWrite)}` : undefined} />
      <CardBody className="pb-2">
        <TimeChart
          label="Disk throughput"
          data={data}
          height={150}
          series={[
            { label: "Read", color: "--ok", fill: true },
            { label: "Write", color: "--warn" },
          ]}
          yFormat={(v) => bytes(v, 0)}
        />
        <div className="mt-2 flex items-center gap-4 text-xs text-muted">
          <span className="inline-flex items-center gap-1.5">
            <span className="h-0.5 w-3 rounded bg-ok" /> Read
          </span>
          <span className="inline-flex items-center gap-1.5">
            <span className="h-0.5 w-3 rounded bg-warn" /> Write
          </span>
        </div>
      </CardBody>
      <div className="border-t border-border">
        {(snap?.disks ?? []).map((d) => (
          <div key={d.name} className="flex items-center gap-3 border-b border-border px-3.5 py-2 text-xs last:border-0">
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

export default function System() {
  const { latest: s, points } = useMetrics();
  const cpuMem = useSeries(points, ["cpu", "mem"]);
  const tempSeries = useSeries(points, ["temp"]);

  return (
    <div className="mx-auto max-w-[1600px]">
      <PageHeader title="System" description="Live resource usage, hardware sensors, storage and processes." />
      <SummaryTiles snap={s} points={points} />

      <div className="mt-3 grid grid-cols-1 gap-3 xl:grid-cols-3">
        <Card className="xl:col-span-2">
          <CardHeader
            title="CPU & memory"
            icon={<Activity />}
            meta="last 10 min"
            actions={
              <div className="flex items-center gap-3 text-xs text-muted">
                <span className="inline-flex items-center gap-1.5">
                  <span className="h-0.5 w-3 rounded bg-accent" /> CPU
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <span className="h-0.5 w-3 rounded bg-frost" /> Memory
                </span>
              </div>
            }
          />
          <CardBody>
            <TimeChart
              label="CPU and memory usage over time"
              data={cpuMem}
              height={210}
              yMax={100}
              series={[
                { label: "CPU", color: "--accent", fill: true },
                { label: "Memory", color: "--frost" },
              ]}
              yFormat={(v) => `${v}%`}
            />
          </CardBody>
        </Card>
        <Card>
          <CardHeader title="Cores" icon={<Cpu />} meta={s ? `${s.cpu.perCore.length} threads` : undefined} />
          <CardBody>
            {s ? <CoreGrid cores={s.cpu.perCore} /> : <Skeleton className="h-28" />}
            <div className="mt-4 grid grid-cols-3 gap-3 border-t border-border pt-3">
              <Stat label="Load 1m" value={s?.cpu.load[0].toFixed(2) ?? "–"} />
              <Stat label="Load 5m" value={s?.cpu.load[1].toFixed(2) ?? "–"} />
              <Stat label="Load 15m" value={s?.cpu.load[2].toFixed(2) ?? "–"} />
            </div>
            <div className="mt-3 border-t border-border pt-3">
              <div className="mb-1.5 flex justify-between gap-2 text-xs">
                <span className="text-muted">Memory</span>
                <span className="num truncate">
                  {s ? `${bytes(s.memory.used)} used · ${bytes(s.memory.cached)} cache · ${bytes(s.memory.available)} free` : "–"}
                </span>
              </div>
              {s && (
                <Meter
                  value={0}
                  label="Memory composition"
                  className="h-2"
                  segments={[
                    { value: (s.memory.used / s.memory.total) * 100, className: "bg-frost", label: "Used" },
                    { value: ((s.memory.cached + s.memory.buffers) / s.memory.total) * 100, className: "bg-frost/30", label: "Cache" },
                  ]}
                />
              )}
            </div>
          </CardBody>
        </Card>
      </div>

      <div className="mt-3 grid grid-cols-1 gap-3 lg:grid-cols-2">
        <NetworkCard snap={s} points={points} />
        <DiskIOCard snap={s} points={points} />
      </div>

      <div className="mt-3 grid grid-cols-1 gap-3 xl:grid-cols-3">
        <div className="xl:col-span-2">
          <StorageCard />
        </div>
        <SensorsCard snap={s} series={tempSeries} />
      </div>

      <section aria-labelledby="processes-heading">
        <ProcessesPanel embedded />
      </section>
    </div>
  );
}

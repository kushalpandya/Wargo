// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { Activity, ArrowDown, ArrowUp, Cpu, HardDrive, MemoryStick, Network, Thermometer } from "lucide-react";
import { api } from "@/lib/api";
import { bytes, duration, level, pct, rate, temp } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card, CardBody, CardHeader, Stat } from "@/components/ui/card";
import { Meter, Skeleton } from "@/components/ui/misc";
import { Ring, Sparkline } from "@/components/charts/Sparkline";
import { TimeChart } from "@/components/charts/TimeChart";
import { useCapabilities } from "@/app/session";
import { useMetrics, useSeries, type Point, type Snapshot, type SystemInfo } from "./useMetrics";
import { StorageCard } from "./Storage";
import { SensorsCard } from "./Sensors";
import { ContainersSummary } from "./ContainersSummary";
import { LauncherStrip } from "./LauncherStrip";

const toneOf = (p: number) => level(p);

function last<T>(a: T[], n: number) {
  return a.slice(Math.max(0, a.length - n));
}

function HostStrip({ info, snap }: { info?: SystemInfo; snap: Snapshot | null }) {
  const items: [string, string | undefined][] = [
    ["Host", info?.hostname],
    ["OS", info?.os],
    ["Kernel", info?.kernel],
    ["CPU", info ? `${info.cpuModel} · ${info.cores}C/${info.threads}T` : undefined],
    ["Uptime", snap ? duration(snap.uptime) : undefined],
    ["Processes", snap ? String(snap.procs) : undefined],
  ];
  return (
    <div className="mb-4 flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs">
      {items.map(([k, v]) => (
        <div key={k} className="flex min-w-0 items-baseline gap-1.5">
          <span className="text-subtle">{k}</span>
          {v ? <span className="num max-w-[22rem] truncate text-fg">{v}</span> : <Skeleton className="h-3 w-16" />}
        </div>
      ))}
    </div>
  );
}

function KpiCard({
  icon,
  title,
  value,
  unit,
  sub,
  ring,
  ringTone,
  spark,
  sparkMax,
  sparkColor = "var(--accent)",
  extra,
}: {
  icon: React.ReactNode;
  title: string;
  value: string;
  unit?: string;
  sub?: React.ReactNode;
  ring?: number;
  ringTone?: "ok" | "warn" | "crit" | "accent" | "frost";
  spark: number[];
  sparkMax?: number;
  sparkColor?: string;
  extra?: React.ReactNode;
}) {
  return (
    <Card className="flex flex-col">
      <div className="flex items-start gap-3 p-3.5 pb-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5 text-2xs font-semibold uppercase tracking-wider text-subtle [&_svg]:size-3.5">
            {icon}
            {title}
          </div>
          <div className="mt-1.5 flex items-baseline gap-1">
            <span className="num text-2xl font-medium leading-none">{value}</span>
            {unit && <span className="num text-xs text-muted">{unit}</span>}
          </div>
          {sub && <div className="mt-1.5 truncate text-xs text-muted">{sub}</div>}
        </div>
        {ring !== undefined && <Ring value={ring} size={44} stroke={4} tone={ringTone} label={`${title} ${Math.round(ring)}%`} />}
      </div>
      {extra}
      <div className="mt-auto px-0.5 pb-0.5">
        <Sparkline values={spark} max={sparkMax} stroke={sparkColor} height={32} />
      </div>
    </Card>
  );
}

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
            <div className="relative text-2xs text-subtle">{i}</div>
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
        {ifs.map((n) => (
          <div key={n.name} className="flex items-center gap-3 border-b border-border px-3.5 py-2 text-xs last:border-0">
            <span className="num w-28 truncate font-medium text-fg" title={n.name}>
              {n.name}
            </span>
            <span className="num hidden min-w-0 flex-1 truncate text-subtle sm:block" title={n.addrs?.join(", ")}>
              {n.addrs?.[0]?.replace(/\/\d+$/, "") ?? ""}
            </span>
            <span className="num ml-auto inline-flex w-24 items-center justify-end gap-1 text-frost">
              <ArrowDown className="size-3" />
              {rate(n.rxRate)}
            </span>
            <span className="num inline-flex w-24 items-center justify-end gap-1 text-accent">
              <ArrowUp className="size-3" />
              {rate(n.txRate)}
            </span>
          </div>
        ))}
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

export default function Overview() {
  const { latest: s, points } = useMetrics();
  const { data: info } = useQuery({ queryKey: ["system", "info"], queryFn: () => api.get<SystemInfo>("/api/system/info"), staleTime: Infinity });
  const { data: caps } = useCapabilities();
  const cpuMem = useSeries(points, ["cpu", "mem"]);
  const tempSeries = useSeries(points, ["temp"]);

  const recent = useMemo(() => last(points, 90), [points]);
  const memPct = s?.memory.usedPct ?? 0;
  const swapPct = s?.memory.swapTotal ? (s.memory.swapUsed / s.memory.swapTotal) * 100 : 0;
  const maxTemp = useMemo(() => Math.max(0, ...(s?.temps ?? []).filter((t) => t.kind !== "core").map((t) => t.value)), [s]);
  const tempTone = s ? (s.cpuTemp >= 90 ? "crit" : s.cpuTemp >= 80 ? "warn" : "ok") : "ok";
  const hasTemp = !!s?.cpuTemp;

  return (
    <div className="mx-auto max-w-[1600px]">
      <div className="mb-1 flex items-baseline justify-between gap-3">
        <h1 className="text-lg font-semibold tracking-tight">Overview</h1>
      </div>
      <HostStrip info={info} snap={s} />

      {caps?.launcher?.enabled && caps.launcher.placement === "overview" && <LauncherStrip />}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <KpiCard
          icon={<Cpu />}
          title="CPU"
          value={s ? s.cpu.total.toFixed(0) : "–"}
          unit="%"
          ring={s?.cpu.total ?? 0}
          ringTone={toneOf(s?.cpu.total ?? 0) === "ok" ? "accent" : toneOf(s?.cpu.total ?? 0)}
          sub={s ? `Load ${s.cpu.load.map((l) => l.toFixed(2)).join(" · ")} · ${(s.cpu.freqMHz / 1000).toFixed(2)} GHz` : undefined}
          spark={recent.map((p) => p.cpu)}
          sparkMax={100}
        />
        <KpiCard
          icon={<MemoryStick />}
          title="Memory"
          value={s ? bytes(s.memory.used).split(" ")[0] : "–"}
          unit={s ? `${bytes(s.memory.used).split(" ")[1]} / ${bytes(s.memory.total)}` : undefined}
          ring={memPct}
          ringTone={toneOf(memPct) === "ok" ? "frost" : toneOf(memPct)}
          sub={
            s ? (
              <>
                {bytes(s.memory.cached)} cached · {s.memory.swapTotal ? `swap ${pct(swapPct)}` : "no swap"}
              </>
            ) : undefined
          }
          spark={recent.map((p) => p.mem)}
          sparkMax={100}
          sparkColor="var(--frost)"
        />
        <KpiCard
          icon={<Thermometer />}
          title="CPU temperature"
          value={hasTemp ? String(Math.round(s!.cpuTemp)) : "–"}
          unit="°C"
          sub={s ? (hasTemp ? `Hottest sensor ${temp(maxTemp)}` : "No CPU sensor found") : undefined}
          ring={hasTemp ? s!.cpuTemp : undefined}
          ringTone={tempTone}
          spark={recent.map((p) => p.temp)}
          sparkMax={100}
          sparkColor={tempTone === "ok" ? "var(--ok)" : `var(--${tempTone})`}
        />
        <KpiCard
          icon={<Activity />}
          title="Network"
          value={s ? rate(s.netRx).split(" ")[0] : "–"}
          unit={s ? `${rate(s.netRx).split(" ")[1]} down` : undefined}
          sub={s ? `↑ ${rate(s.netTx)} up` : undefined}
          spark={recent.map((p) => p.rx + p.tx)}
          sparkColor="var(--frost)"
        />
      </div>

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
              <div className="mb-1.5 flex justify-between text-xs">
                <span className="text-muted">Memory</span>
                <span className="num">
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

      {caps?.docker?.available && (
        <div className="mt-3">
          <ContainersSummary />
        </div>
      )}
      {!caps?.docker?.available && caps?.docker && (
        <p className="mt-3 text-xs text-subtle">
          Docker isn't reachable{caps.docker.error ? `: ${caps.docker.error}` : ""}. <Link className="underline" to="/containers">Details</Link>
        </p>
      )}
    </div>
  );
}

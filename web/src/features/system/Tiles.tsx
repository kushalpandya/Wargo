// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo, type ReactNode } from "react";
import { Link } from "react-router";
import { Cpu, HardDrive, MemoryStick, Network, Thermometer } from "lucide-react";
import { bytes, level, pct, rate } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Ring, Sparkline } from "@/components/charts/Sparkline";
import type { Point, Snapshot } from "@/features/system/useMetrics";
import { useStorage, type Filesystem } from "@/features/system/Storage";
import { LinkIcon, linkDetails, linkName } from "./LinkLabel";

type Pastel = "iris" | "mint" | "sky" | "peach";
type Tone = "ok" | "warn" | "crit" | Pastel;

/** Ring colour: the tile's pastel until usage reaches warn/crit levels. */
const ringTone = (p: number, base: Pastel, warn = 75, crit = 90): Tone => {
  const l = level(p, warn, crit);
  return l === "ok" ? base : l;
};

export function tempTone(c: number): "ok" | "warn" | "crit" {
  return c >= 90 ? "crit" : c >= 80 ? "warn" : "ok";
}

/** Splits "12.3 MB/s" into ["12.3", "MB/s"]. */
function split(s: string): [string, string] {
  const i = s.indexOf(" ");
  return i < 0 ? [s, ""] : [s.slice(0, i), s.slice(i + 1)];
}

const tileBg: Record<Pastel, string> = { iris: "bg-iris-tint", mint: "bg-mint-tint", sky: "bg-sky-tint", peach: "bg-peach-tint" };
const tileInk: Record<Pastel, string> = { iris: "text-iris", mint: "text-mint", sky: "text-sky", peach: "text-peach" };

function Tile({
  icon,
  title,
  value,
  unit,
  sub,
  ring,
  tone,
  tint,
  corner,
  spark,
  sparkMax,
  to,
}: {
  icon: ReactNode;
  title: string;
  value: string;
  unit?: string;
  sub?: ReactNode;
  ring?: number;
  tone?: Tone;
  /** The tile's pastel colour (background tint, icon, sparkline). */
  tint: Pastel;
  corner?: ReactNode;
  spark: number[];
  sparkMax?: number;
  to?: string;
}) {
  const body = (
    <section
      className={cn(
        "relative flex h-full min-w-0 flex-col overflow-hidden rounded-xl border border-border/60 shadow-card",
        tileBg[tint],
        to && "transition-[transform,box-shadow] duration-200 hover:-translate-y-0.5 hover:shadow-pop",
      )}
    >
      <div className="flex items-start gap-3 p-3 pb-1 sm:p-4 sm:pb-1">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-ui font-semibold">
            <span className={cn("flex size-7 shrink-0 items-center justify-center rounded-lg bg-surface/80 shadow-sm [&_svg]:size-4", tileInk[tint])} aria-hidden="true">
              {icon}
            </span>
            <h2>{title}</h2>
            {corner && <span className="ml-auto min-w-0 font-medium max-sm:hidden">{corner}</span>}
          </div>
          <div className="mt-3 flex min-w-0 items-baseline gap-1.5">
            <span className="num text-3xl font-bold leading-none tracking-tight sm:text-4xl">{value}</span>
            {unit && <span className="num truncate text-xs font-medium text-muted sm:text-ui">{unit}</span>}
          </div>
          {sub && <div className="mt-2 truncate text-xs text-muted">{sub}</div>}
        </div>
        {ring !== undefined && (
          <div className="max-sm:hidden">
            <Ring value={ring} size={54} stroke={6} tone={tone ?? tint} label={`${title} ${Math.round(ring)}%`} />
          </div>
        )}
      </div>
      <div className="mt-auto pt-2">
        <Sparkline values={spark} max={sparkMax} stroke={`var(--${tint})`} height={44} />
      </div>
    </section>
  );
  return to ? (
    <Link to={to} className="block h-full rounded-xl focus-visible:outline-offset-2" title="Open System">
      {body}
    </Link>
  ) : (
    body
  );
}

/** Mounted filesystems summed per physical device (bind mounts counted once). */
export function diskTotals(fs: Filesystem[] | undefined) {
  const seen = new Set<string>();
  let used = 0;
  let total = 0;
  for (const f of fs ?? []) {
    if (seen.has(f.device)) continue;
    seen.add(f.device);
    used += f.used;
    total += f.total;
  }
  return { used, total, pct: total ? (used / total) * 100 : 0, count: seen.size };
}

/** CPU (with temperature), memory, network and disks — shared by Home and System. */
export function SummaryTiles({ snap: s, points, link }: { snap: Snapshot | null; points: Point[]; link?: boolean }) {
  const recent = useMemo(() => points.slice(-90), [points]);
  const storage = useStorage();
  const disk = diskTotals(storage.data?.filesystems);
  const cpu = s?.cpu.total ?? 0;
  const mem = s?.memory.usedPct ?? 0;
  const hasTemp = !!s?.cpuTemp;
  const tt = hasTemp ? tempTone(s!.cpuTemp) : "ok";
  const to = link ? "/system" : undefined;
  const primary = s?.net?.find((n) => n.name === s.primaryIf) ?? s?.net?.find((n) => n.link?.up && n.link.kind !== "bridge");
  const [rxV, rxU] = split(rate(s?.netRx ?? 0));
  const [memV, memU] = split(bytes(s?.memory.used ?? 0));

  return (
    <div className="grid grid-cols-2 gap-3 sm:gap-4 xl:grid-cols-4">
      <Tile
        to={to}
        icon={<Cpu />}
        title="CPU"
        value={s ? cpu.toFixed(0) : "–"}
        unit="%"
        ring={cpu}
        tint="iris"
        tone={ringTone(cpu, "iris")}
        corner={
          hasTemp ? (
            <span
              className={cn("num inline-flex items-center gap-0.5 text-xs font-medium", tt === "crit" ? "text-crit" : tt === "warn" ? "text-warn" : "text-muted")}
              title="CPU package temperature"
            >
              <Thermometer className="size-3.5" aria-hidden="true" />
              {Math.round(s!.cpuTemp)}°C
            </span>
          ) : undefined
        }
        sub={s ? `Load ${s.cpu.load.map((l) => l.toFixed(2)).join(" · ")} · ${(s.cpu.freqMHz / 1000).toFixed(2)} GHz` : undefined}
        spark={recent.map((p) => p.cpu)}
        sparkMax={100}
      />
      <Tile
        to={to}
        icon={<MemoryStick />}
        title="Memory"
        value={s ? memV : "–"}
        unit={s ? `${memU} / ${bytes(s.memory.total)}` : undefined}
        ring={mem}
        tint="mint"
        tone={ringTone(mem, "mint")}
        sub={s ? `${bytes(s.memory.cached)} cached · ${s.memory.swapTotal ? `swap ${pct((s.memory.swapUsed / s.memory.swapTotal) * 100)}` : "no swap"}` : undefined}
        spark={recent.map((p) => p.mem)}
        sparkMax={100}
      />
      <Tile
        to={to}
        icon={<Network />}
        title="Network"
        corner={
          primary?.link && linkName(primary.link) ? (
            <span className="inline-flex min-w-0 max-w-36 items-center gap-1 text-xs font-medium text-muted" title={[linkName(primary.link), linkDetails(primary.link)].filter(Boolean).join(" · ")}>
              <LinkIcon link={primary.link} />
              <span className="truncate">{linkName(primary.link)}</span>
            </span>
          ) : undefined
        }
        value={s ? rxV : "–"}
        unit={s ? `${rxU} down` : undefined}
        sub={s ? `↑ ${rate(s.netTx)} up` : undefined}
        tint="sky"
        spark={recent.map((p) => p.rx + p.tx)}
      />
      <Tile
        to={to}
        icon={<HardDrive />}
        title="Disks"
        value={storage.data ? disk.pct.toFixed(0) : "–"}
        unit={storage.data ? `% · ${bytes(disk.used)} / ${bytes(disk.total)}` : undefined}
        ring={storage.data ? disk.pct : undefined}
        tint="peach"
        tone={ringTone(disk.pct, "peach", 85, 95)}
        sub={s ? `R ${rate(s.diskRead)} · W ${rate(s.diskWrite)}` : undefined}
        spark={recent.map((p) => p.dr + p.dw)}
      />
    </div>
  );
}

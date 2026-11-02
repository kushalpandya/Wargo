// SPDX-License-Identifier: AGPL-3.0-only
import { useMemo, useState } from "react";
import { Fan, Thermometer } from "lucide-react";
import { cn } from "@/lib/cn";
import { temp } from "@/lib/format";
import { Card, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { TimeChart } from "@/components/charts/TimeChart";
import type { Snapshot, Temp } from "./useMetrics";

const kindLabel: Record<Temp["kind"], string> = {
  cpu: "Processor",
  core: "Cores",
  gpu: "Graphics",
  disk: "Drives",
  board: "Motherboard",
  wifi: "Wireless",
  other: "Other",
};

function tone(t: Temp) {
  const crit = t.crit ?? 95;
  const high = t.high && t.high < crit ? t.high : crit - 10;
  return t.value >= crit ? "crit" : t.value >= high ? "warn" : "ok";
}

function TempRow({ t }: { t: Temp }) {
  const tt = tone(t);
  const max = t.crit ?? 100;
  return (
    <div className="flex items-center gap-3 py-1.5 text-xs">
      <span className="min-w-0 flex-1 truncate text-muted" title={`${t.chip} · ${t.label}`}>
        {t.label}
      </span>
      <div className="h-1 w-20 overflow-hidden rounded-full bg-surface-3" aria-hidden="true">
        <div className={cn("h-full rounded-full", tt === "crit" ? "bg-crit" : tt === "warn" ? "bg-warn" : "bg-ok")} style={{ width: `${Math.min(100, (t.value / max) * 100)}%` }} />
      </div>
      <span className={cn("num w-11 text-right", tt === "crit" ? "text-crit" : tt === "warn" ? "text-warn" : "text-fg")}>{temp(t.value)}</span>
    </div>
  );
}

export function SensorsCard({ snap, series }: { snap: Snapshot | null; series: [number[], ...number[][]] }) {
  const [showCores, setShowCores] = useState(false);
  const groups = useMemo(() => {
    const g = new Map<Temp["kind"], Temp[]>();
    for (const t of snap?.temps ?? []) {
      if (!g.has(t.kind)) g.set(t.kind, []);
      g.get(t.kind)!.push(t);
    }
    return [...g.entries()];
  }, [snap]);

  return (
    <Card className="h-full">
      <CardHeader title="Sensors" icon={<Thermometer />} meta={snap?.temps ? `${snap.temps.length} readings` : undefined} />
      <div className="px-3.5 pt-3">
        <TimeChart label="CPU temperature over time" data={series} height={90} series={[{ label: "CPU", color: "--ok", fill: true }]} yFormat={(v) => `${v}°`} />
      </div>
      {!snap && <Skeleton className="m-3.5 h-32" />}
      <div className="px-3.5 pb-3">
        {groups.map(([kind, list]) => {
          if (kind === "core") {
            return (
              <div key={kind} className="border-t border-border pt-2 first:border-0">
                <button
                  type="button"
                  onClick={() => setShowCores((v) => !v)}
                  aria-expanded={showCores}
                  className="flex w-full items-center justify-between py-1 text-2xs font-semibold uppercase tracking-wider text-subtle hover:text-fg"
                >
                  <span>
                    {kindLabel[kind]} ({list.length})
                  </span>
                  <span className="num normal-case tracking-normal">
                    max {temp(Math.max(...list.map((t) => t.value)))} {showCores ? "▴" : "▾"}
                  </span>
                </button>
                {showCores && (
                  <div className="grid grid-cols-2 gap-x-4">
                    {list.map((t) => (
                      <TempRow key={t.chip + t.label} t={t} />
                    ))}
                  </div>
                )}
              </div>
            );
          }
          return (
            <div key={kind} className="mt-2 border-t border-border pt-2">
              <div className="py-1 text-2xs font-semibold uppercase tracking-wider text-subtle">{kindLabel[kind]}</div>
              {list.map((t) => (
                <TempRow key={t.chip + t.label} t={t} />
              ))}
            </div>
          );
        })}
        {snap?.fans && snap.fans.length > 0 && (
          <div className="mt-2 border-t border-border pt-2">
            <div className="py-1 text-2xs font-semibold uppercase tracking-wider text-subtle">Fans</div>
            {snap.fans.map((f) => (
              <div key={f.chip + f.label} className="flex items-center gap-2 py-1.5 text-xs">
                <Fan className="size-3.5 text-subtle" />
                <span className="flex-1 text-muted">{f.label}</span>
                <span className="num">{f.rpm.toLocaleString()} rpm</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </Card>
  );
}

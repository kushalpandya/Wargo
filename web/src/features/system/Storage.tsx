// SPDX-License-Identifier: AGPL-3.0-only
import { memo } from "react";
import { useQuery } from "@tanstack/react-query";
import { HardDrive, Thermometer } from "lucide-react";
import { api } from "@/lib/api";
import { bytes, level, pct, temp } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Card, CardHeader } from "@/components/ui/card";
import { Badge, Meter, Skeleton } from "@/components/ui/misc";

export interface Filesystem {
  device: string;
  mount: string;
  fstype: string;
  total: number;
  used: number;
  free: number;
  usedPct: number;
  inodesPct: number;
  readOnly: boolean;
  label?: string;
  partition?: string;
}

export interface Drive {
  name: string;
  model: string;
  serial?: string;
  size: number;
  rotational: boolean;
  removable: boolean;
  transport: string;
  temp?: number;
  partitions: { name: string; size: number; mounts?: string[] }[];
  used: number;
  total: number;
}

export function useStorage() {
  return useQuery({
    queryKey: ["system", "storage"],
    queryFn: () => api.get<{ drives: Drive[]; filesystems: Filesystem[] }>("/api/system/storage"),
    refetchInterval: 30_000,
  });
}

function driveKind(d: Drive) {
  if (d.transport === "nvme") return "NVMe SSD";
  if (d.transport === "usb") return d.rotational ? "USB HDD" : "USB drive";
  if (d.transport === "mmc") return "SD / eMMC";
  if (d.transport === "virtio") return "Virtual disk";
  return d.rotational ? "HDD" : "SATA SSD";
}

// Memoised: parents re-render on every 1 s metrics tick.
export const StorageCard = memo(function StorageCard() {
  const { data, isLoading } = useStorage();
  const fsByPart = new Map((data?.filesystems ?? []).map((f) => [f.partition, f]));

  return (
    <Card className="h-full">
      <CardHeader
        title="Storage"
        icon={<HardDrive />}
        meta={data ? `${data.drives.length} drive${data.drives.length === 1 ? "" : "s"} · ${data.filesystems.length} mounted` : undefined}
      />
      {isLoading && (
        <div className="space-y-3 p-3.5">
          <Skeleton className="h-16" />
          <Skeleton className="h-16" />
        </div>
      )}
      <ul className="divide-y divide-border">
        {data?.drives.map((d) => {
          const usedPct = d.total ? (d.used / d.total) * 100 : 0;
          const mounted = d.partitions.map((p) => fsByPart.get(p.name)).filter(Boolean) as Filesystem[];
          const direct = fsByPart.get(d.name);
          if (direct) mounted.push(direct);
          return (
            <li key={d.name} className="px-3.5 py-3">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-medium">{d.model || d.name}</span>
                <Badge>{driveKind(d)}</Badge>
                {d.removable && <Badge tone="frost">Removable</Badge>}
                <span className="num text-xs text-subtle">
                  /dev/{d.name} · {bytes(d.size)}
                </span>
                {d.temp ? (
                  <span className="num ml-auto inline-flex items-center gap-1 text-xs text-muted">
                    <Thermometer className="size-3" />
                    {temp(d.temp)}
                  </span>
                ) : null}
              </div>
              {d.total > 0 ? (
                <div className="mt-2 flex items-center gap-3">
                  <Meter value={usedPct} tone={level(usedPct, 80, 92) === "ok" ? "accent" : level(usedPct, 80, 92)} className="h-2" label={`${d.name} used`} />
                  <span className="num w-44 shrink-0 text-right text-xs text-muted">
                    <span className="text-fg">{pct(usedPct)}</span> · {bytes(d.used)} of {bytes(d.total)}
                  </span>
                </div>
              ) : (
                <p className="mt-1.5 text-xs text-subtle">No mounted filesystems</p>
              )}
              {mounted.length > 0 && (
                <div className="mt-2.5 grid gap-1.5">
                  {mounted.map((f) => (
                    <div key={f.mount} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 rounded-md bg-surface-2 px-2.5 py-1.5 text-xs sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)_auto]">
                      <div className="flex min-w-0 items-center gap-1.5">
                        <span className="num truncate font-medium text-fg" title={f.mount}>
                          {f.mount}
                        </span>
                        {f.readOnly && <Badge tone="warn">ro</Badge>}
                      </div>
                      <div className="order-3 col-span-2 flex items-center gap-2 sm:order-none sm:col-span-1">
                        <Meter value={f.usedPct} tone={level(f.usedPct, 80, 92) === "ok" ? "frost" : level(f.usedPct, 80, 92)} label={`${f.mount} used`} />
                      </div>
                      <span className={cn("num whitespace-nowrap text-right text-subtle")}>
                        <span className="text-fg">{pct(f.usedPct)}</span> · {bytes(f.free)} free · {f.fstype}
                        {f.label ? ` · ${f.label}` : ""}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </li>
          );
        })}
      </ul>
    </Card>
  );
});

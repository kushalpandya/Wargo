// SPDX-License-Identifier: AGPL-3.0-only
import { memo, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { HardDrive, Thermometer } from "lucide-react";
import { api } from "@/lib/api";
import { bytes, level, pct, temp } from "@/lib/format";
import { healthFacts, type DriveHealth } from "./health";
import { cn } from "@/lib/cn";
import { Card, CardHeader } from "@/components/ui/card";
import { Badge, Meter, Skeleton, StatusDot, type Tone } from "@/components/ui/misc";
import { Tooltip } from "@/components/ui/menu";

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
  partitions: { name: string; size: number; label?: string; mounts?: string[] }[];
  used: number;
  total: number;
  /** Absent for virtual disks and memory cards, and from older servers. */
  health?: DriveHealth;
  /** "on" when the SSD supports TRIM and something runs it, "off" when nothing does. */
  trim?: "on" | "off";
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
  if (d.transport === "other") return d.rotational ? "HDD" : "SSD";
  return d.rotational ? "HDD" : "SATA SSD";
}

const healthBadge: Record<DriveHealth["status"], { label: string; tone: Tone; text: string }> = {
  ok: { label: "Healthy", tone: "ok", text: "text-subtle" },
  warn: { label: "Warning", tone: "warn", text: "text-warn" },
  fail: { label: "Failing", tone: "crit", text: "text-crit" },
  unknown: { label: "Health unknown", tone: "neutral", text: "text-subtle" },
};

/** A badge whose explanation shows in a tooltip (hover or keyboard focus). */
function TipBadge({ tip, tone, className, children }: { tip?: string; tone?: Tone; className?: string; children: React.ReactNode }) {
  return (
    <Tooltip content={tip}>
      <span className={cn("inline-flex", tip && "cursor-help")} tabIndex={tip ? 0 : undefined}>
        <Badge tone={tone} className={className}>
          {children}
        </Badge>
      </span>
    </Tooltip>
  );
}

function DriveBadges({ d }: { d: Drive }) {
  const h = d.health;
  const b = h && (healthBadge[h.status] ?? healthBadge.unknown);
  return (
    <>
      {h && b && (
        <TipBadge tone={b.tone} tip={[h.reason, ...(h.notes ?? [])].filter(Boolean).join(", ") || undefined}>
          {b.label}
        </TipBadge>
      )}
      {d.trim && (
        <TipBadge
          className="gap-1.5"
          tip={d.trim === "on" ? "TRIM is on: the drive supports it and it runs" : "TRIM is off: the drive supports it, but no periodic trim or discard mount runs it"}
        >
          <StatusDot tone={d.trim === "on" ? "ok" : "warn"} />
          {d.trim === "on" ? "TRIM" : "TRIM off"}
        </TipBadge>
      )}
    </>
  );
}

function HealthFacts({ h }: { h: DriveHealth }) {
  const facts = healthFacts(h);
  if (!facts.length) return null;
  return <div className={cn("num mt-0.5 text-xs", (healthBadge[h.status] ?? healthBadge.unknown).text)}>{facts.join(" · ")}</div>;
}

interface PartRow {
  name: string;
  size: number;
  label?: string;
  /** Mounted filesystems on this partition (several for bind mounts or btrfs subvolumes). */
  fs: Filesystem[];
}

const usageTone = (p: number, base: "accent" | "frost") => (level(p, 80, 92) === "ok" ? base : level(p, 80, 92));

function ColumnTitle({ id, children }: { id: string; children: React.ReactNode }) {
  return (
    <h3 id={id} className="flex h-9 items-center border-b border-border px-4 text-2xs font-semibold uppercase tracking-wider text-subtle">
      {children}
    </h3>
  );
}

function PartitionRow({ p }: { p: PartRow }) {
  const f = p.fs[0];
  const label = p.label || p.fs.find((x) => x.label)?.label;
  return (
    <li className="px-4 py-2 text-xs">
      <div className="flex min-w-0 items-baseline gap-2">
        {/* The filesystem label when there is one, else the device name. */}
        <span className={cn("shrink-0 font-medium text-fg", !label && "num")} title={label ? `/dev/${p.name}` : undefined}>
          {label || p.name}
        </span>
        <span className="num min-w-0 flex-1 truncate text-muted" title={p.fs.map((x) => x.mount).join(", ")}>
          {f ? p.fs.map((x) => x.mount).join(", ") : "Not mounted"}
        </span>
        {f?.readOnly && <Badge tone="warn">ro</Badge>}
        <span className="num shrink-0 text-subtle">{bytes(f?.total || p.size)}</span>
      </div>
      {f && (
        <div className="mt-1.5 flex items-center gap-2">
          <Meter value={f.usedPct} tone={usageTone(f.usedPct, "frost")} label={`${f.mount} used`} />
          <span className="num shrink-0 whitespace-nowrap text-right text-subtle">
            <span className="text-fg">{pct(f.usedPct)}</span> · {bytes(f.free)} free · {f.fstype}
          </span>
        </div>
      )}
    </li>
  );
}

// Memoised: parents re-render on every 1 s metrics tick.
export const StorageCard = memo(function StorageCard() {
  const { data, isLoading } = useStorage();
  const [hover, setHover] = useState<string | null>(null);

  const { groups, other } = useMemo(() => {
    const byPart = new Map<string, Filesystem[]>();
    for (const f of data?.filesystems ?? []) {
      if (!f.partition) continue;
      if (!byPart.has(f.partition)) byPart.set(f.partition, []);
      byPart.get(f.partition)!.push(f);
    }
    // Device names the drive list knows. Only filesystems on other devices
    // (macOS APFS volumes such as disk3s1s1) are matched by mount point,
    // so an over-mount on Linux still groups by partition alone.
    const known = new Set<string>();
    for (const d of data?.drives ?? []) {
      known.add(d.name);
      for (const p of d.partitions ?? []) known.add(p.name);
    }
    const claimed = new Set<string>();
    const claimedFs = new Set<Filesystem>();
    const groups = (data?.drives ?? []).map((d) => {
      const parts: PartRow[] = (d.partitions ?? []).map((p) => {
        claimed.add(p.name);
        // A partition's filesystems: those on it, plus those it lists by
        // mount point (macOS APFS volumes live on a container device).
        const fs = [...(byPart.get(p.name) ?? [])];
        for (const f of data?.filesystems ?? []) {
          if (!fs.includes(f) && !(f.partition && known.has(f.partition)) && p.mounts?.includes(f.mount)) fs.push(f);
        }
        for (const f of fs) claimedFs.add(f);
        return { name: p.name, size: p.size, label: p.label, fs };
      });
      // A filesystem directly on the whole drive (no partition table).
      if (byPart.has(d.name)) {
        claimed.add(d.name);
        parts.push({ name: d.name, size: d.size, fs: byPart.get(d.name)! });
      }
      return { drive: d, parts };
    });
    // Mounted filesystems on devices outside the drive list (LVM, RAID, …).
    const rest = new Map<string, PartRow>();
    for (const f of data?.filesystems ?? []) {
      const key = f.partition || f.device;
      if ((f.partition && claimed.has(f.partition)) || claimedFs.has(f)) continue;
      if (!rest.has(key)) rest.set(key, { name: key.replace(/^\/dev\//, ""), size: f.total, fs: [] });
      rest.get(key)!.fs.push(f);
    }
    return { groups, other: [...rest.values()] };
  }, [data]);

  return (
    <Card>
      <CardHeader
        title="Storage"
        icon={<HardDrive />}
        tint="butter"
        meta={data ? `${data.drives?.length ?? 0} drive${data.drives?.length === 1 ? "" : "s"} · ${data.filesystems?.length ?? 0} mounted` : undefined}
      />
      {isLoading ? (
        <div className="grid gap-3 p-4 lg:grid-cols-2">
          <Skeleton className="h-24" />
          <Skeleton className="h-24" />
        </div>
      ) : (
        <div className="grid divide-border max-lg:divide-y lg:grid-cols-2 lg:divide-x">
          <section aria-labelledby="storage-drives" className="min-w-0 lg:max-h-[26rem] lg:overflow-y-auto">
            <ColumnTitle id="storage-drives">Drives</ColumnTitle>
            <ul className="divide-y divide-border">
              {groups.map(({ drive: d, parts }) => {
                const usedPct = d.total ? (d.used / d.total) * 100 : 0;
                return (
                  <li
                    key={d.name}
                    className={cn("px-4 py-2.5 transition-colors", hover === d.name && "bg-surface-2")}
                    onMouseEnter={() => setHover(d.name)}
                    onMouseLeave={() => setHover(null)}
                  >
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <span className="min-w-0 truncate font-medium">{d.model || d.name}</span>
                      <Badge>{driveKind(d)}</Badge>
                      {d.removable && <Badge tone="frost">Removable</Badge>}
                      <DriveBadges d={d} />
                      {d.temp ? (
                        <span className="num ml-auto inline-flex items-center gap-1 text-xs text-muted">
                          <Thermometer className="size-3" />
                          {temp(d.temp)}
                        </span>
                      ) : null}
                    </div>
                    <div className="num mt-0.5 text-xs text-subtle">
                      /dev/{d.name} · {bytes(d.size)} · {parts.length} partition{parts.length === 1 ? "" : "s"}
                    </div>
                    {d.health && <HealthFacts h={d.health} />}
                    {d.total > 0 ? (
                      <div className="mt-2 flex items-center gap-3">
                        <Meter value={usedPct} tone={usageTone(usedPct, "accent")} className="h-2" label={`${d.name} used`} />
                        <span className="num shrink-0 text-right text-xs text-muted">
                          <span className="text-fg">{pct(usedPct)}</span> · {bytes(d.used)} of {bytes(d.total)}
                        </span>
                      </div>
                    ) : (
                      <p className="mt-1.5 text-xs text-subtle">No mounted filesystems</p>
                    )}
                  </li>
                );
              })}
            </ul>
          </section>
          <section aria-labelledby="storage-parts" className="min-w-0 lg:max-h-[26rem] lg:overflow-y-auto">
            <ColumnTitle id="storage-parts">Partitions</ColumnTitle>
            {[...groups.map((g) => ({ key: g.drive.name, title: `${g.drive.model || g.drive.name} · /dev/${g.drive.name}`, parts: g.parts })), ...(other.length ? [{ key: "_other", title: "Other mounted devices", parts: other }] : [])]
              .filter((g) => g.parts.length > 0)
              .map((g) => (
                <div key={g.key} className={cn("border-t border-border transition-colors first-of-type:border-t-0", hover === g.key && "bg-surface-2")}>
                  <div className="num truncate border-b border-border bg-surface-2/50 px-4 py-1 text-2xs text-subtle">{g.title}</div>
                  <ul className="divide-y divide-border">
                    {g.parts.map((p) => (
                      <PartitionRow key={p.name} p={p} />
                    ))}
                  </ul>
                </div>
              ))}
          </section>
        </div>
      )}
    </Card>
  );
});

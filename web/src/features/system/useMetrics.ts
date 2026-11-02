// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { useTopic } from "@/lib/ws";

export interface Temp {
  chip: string;
  label: string;
  value: number;
  high?: number;
  crit?: number;
  kind: "cpu" | "core" | "disk" | "gpu" | "board" | "wifi" | "other";
}

/** What a network interface is connected to. */
export interface Link {
  kind: "wifi" | "ethernet" | "vpn" | "bridge" | "other";
  up: boolean;
  ssid?: string;
  signalDbm?: number;
  band?: string;
  rateMbps?: number;
  connection?: string;
}

export interface Snapshot {
  ts: number;
  cpu: { total: number; perCore: number[]; load: [number, number, number]; freqMHz: number };
  memory: { total: number; used: number; available: number; cached: number; buffers: number; swapTotal: number; swapUsed: number; usedPct: number };
  temps: Temp[] | null;
  fans: { chip: string; label: string; rpm: number }[] | null;
  net: { name: string; rxRate: number; txRate: number; rxTotal: number; txTotal: number; addrs?: string[]; link?: Link }[] | null;
  /** Interface carrying the default route. */
  primaryIf?: string;
  netRx: number;
  netTx: number;
  disks: { name: string; readRate: number; writeRate: number; busy: number }[] | null;
  diskRead: number;
  diskWrite: number;
  uptime: number;
  procs: number;
  cpuTemp: number;
}

export interface Point {
  t: number;
  cpu: number;
  mem: number;
  swap: number;
  rx: number;
  tx: number;
  dr: number;
  dw: number;
  temp: number;
  load: number;
  /** Per-core usage (absent in samples from older servers). */
  cores?: number[];
}

export interface SystemInfo {
  hostname: string;
  os: string;
  osId: string;
  kernel: string;
  arch: string;
  cpuModel: string;
  cores: number;
  threads: number;
  memTotal: number;
  bootTime: number;
  virtualization?: string;
}

const MAX = 600;

/** Live snapshot + rolling history (seeded from the server's buffer). */
export function useMetrics() {
  const [latest, setLatest] = useState<Snapshot | null>(null);
  const [points, setPoints] = useState<Point[]>([]);
  const seeded = useRef(false);

  const hist = useQuery({
    queryKey: ["system", "history"],
    queryFn: () => api.get<{ interval: number; points: Point[] }>("/api/system/metrics/history"),
    staleTime: Infinity,
    gcTime: 0,
  });

  useEffect(() => {
    if (hist.data && !seeded.current) {
      seeded.current = true;
      setPoints((cur) => {
        const last = hist.data.points.at(-1)?.t ?? 0;
        return [...hist.data.points, ...cur.filter((p) => p.t > last)].slice(-MAX);
      });
    }
  }, [hist.data]);

  useTopic<Snapshot>("metrics", (s) => {
    setLatest(s);
    const swap = s.memory.swapTotal ? (s.memory.swapUsed / s.memory.swapTotal) * 100 : 0;
    const p: Point = {
      t: s.ts,
      cpu: s.cpu.total,
      mem: s.memory.usedPct,
      swap,
      rx: s.netRx,
      tx: s.netTx,
      dr: s.diskRead,
      dw: s.diskWrite,
      temp: s.cpuTemp,
      load: s.cpu.load[0],
      cores: s.cpu.perCore.map((v) => Math.round(v * 10) / 10),
    };
    setPoints((cur) => (cur.length && cur[cur.length - 1].t >= p.t ? cur : [...cur.slice(-(MAX - 1)), p]));
  });

  return { latest, points };
}

/** Converts points to uPlot aligned data for selected keys. */
export function useSeries(points: Point[], keys: (keyof Point)[]) {
  return useMemo(() => {
    const xs = points.map((p) => p.t / 1000);
    return [xs, ...keys.map((k) => points.map((p) => p[k] as number))] as [number[], ...number[][]];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [points]);
}

// SPDX-License-Identifier: AGPL-3.0-only

/** One row of GET /api/procs. cpu is percent of one core (top-style). */
export interface Proc {
  pid: number;
  ppid: number;
  name: string;
  user: string;
  uid: number;
  state: string;
  cpu: number;
  memRSS: number;
  memPct: number;
  threads: number;
  startTime: number;
  command: string;
  nice: number;
  kernel?: boolean;
}

/** Payload of GET /api/procs and the "procs" topic. */
export interface ProcList {
  procs: Proc[];
  uid: number;
  self: number;
  totalMem: number;
  cpus: number;
  sampledAt: number;
}

export interface ProcDetails extends Proc {
  cmdline: string;
  args?: string[];
  exe?: string;
  cwd?: string;
  envCount?: number;
  fdCount?: number;
  cgroup?: string;
  containerId?: string;
  containerName?: string;
  children: number[];
  mem: { rss: number; vms: number; shared: number; swap: number };
  io?: { read: number; write: number };
  status: { realUid: number; volCtxSwitches: number; invCtxSwitches: number; parentName?: string };
  readable: boolean;
}

export type SignalName = "TERM" | "KILL" | "HUP" | "INT" | "STOP" | "CONT" | "USR1" | "USR2";

export const stateNames: Record<string, string> = {
  R: "Running",
  S: "Sleeping",
  D: "Disk wait",
  Z: "Zombie",
  T: "Stopped",
  t: "Tracing",
  I: "Idle",
  X: "Dead",
  P: "Parked",
  W: "Paging",
};

/** Highlight thresholds: cpu is percent of one core, mem percent of RAM. */
export const cpuTone = (v: number): "warn" | "crit" | undefined => (v >= 90 ? "crit" : v >= 40 ? "warn" : undefined);
export const memTone = (v: number): "warn" | "crit" | undefined => (v >= 20 ? "crit" : v >= 8 ? "warn" : undefined);

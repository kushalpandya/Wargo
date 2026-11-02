// SPDX-License-Identifier: AGPL-3.0-only

export interface Port {
  ip?: string;
  private: number;
  public?: number;
  type: string;
}

/** Shape returned by GET /api/containers (also the sticky "containers" topic). */
export interface Container {
  /** Short (12 char) id. */
  id: string;
  fullId: string;
  name: string;
  image: string;
  imageId: string;
  command?: string;
  state: "running" | "exited" | "paused" | "restarting" | "created" | "dead" | "removing";
  status: string;
  health?: "healthy" | "unhealthy" | "starting";
  /** Unix ms. */
  created: number;
  startedAt?: string;
  ports: Port[];
  networkMode: string;
  /** For network_mode "container:<id>", the name of that container. */
  networkContainer?: string;
  networkContainerId?: string;
  /** Ports published by the network container. */
  networkPorts?: Port[];
  /** Containers that share this container's network namespace. */
  networkUsers?: string[];
  compose?: { project: string; service: string; workingDir?: string; configFiles?: string };
  casaos?: boolean;
  /** Wargo's own container (container mode). */
  self?: boolean;
  /** null = not checked yet. */
  updateAvailable?: boolean | null;
  /** When the image was last checked for updates (unix ms); absent = never. */
  updateCheckedAt?: number;
  /**
   * Best-effort human version of the image: OCI version label, linuxserver
   * build_version, "version" label, a non-"latest" tag, else short image id.
   */
  version: string;
  /** Image without tag/digest, e.g. "lscr.io/linuxserver/plex". */
  imageRef: string;
  /** Image tag ("latest" by default; "" for digest-pinned or id-only images). */
  tag: string;
  /**
   * Wargo URL of the container's logo (GET /api/containers/{id}/icon), present
   * when a candidate exists. It can still 404: fall back to a letter avatar.
   */
  icon?: string;
}

/** Live stats published on the "containers.stats" topic: short id -> stats. */
export interface ContainerStats {
  /** Percent of total host CPU capacity. */
  cpu: number;
  mem: number;
  memLimit: number;
  /** Bytes per second. */
  netRx: number;
  netTx: number;
}

export interface UpdateCheck {
  image: string;
  /** null when the check failed (see error). A failed re-check keeps the previous answer and sets error. */
  updateAvailable: boolean | null;
  localDigest?: string;
  remoteDigest?: string;
  error?: string;
  /** Unix ms. */
  checkedAt: number;
}

/** GET /api/containers/updates (and the answer of POST /api/containers/check-updates). */
export interface UpdatesStatus {
  /** When the last full check finished (unix ms), null if never. */
  checkedAt: number | null;
  /** A check (manual or background) is running. */
  checking: boolean;
  /** Next background check (unix ms); absent when background checks are disabled. */
  nextCheckAt?: number;
  /** Background check interval in ms (0 = disabled). */
  intervalMs: number;
  /** Short container id -> result for its image. */
  results: Record<string, UpdateCheck>;
  /** The last check could not run at all (e.g. Docker unreachable). */
  error?: string;
}

export interface Mount {
  type: string;
  name?: string;
  source: string;
  destination: string;
  mode?: string;
  rw: boolean;
}

export interface Network {
  name: string;
  ip?: string;
  prefix?: number;
  ipv6?: string;
  gateway?: string;
  mac?: string;
  aliases?: string[];
  dnsNames?: string[];
}

/** GET /api/containers/{id}. Env holds variable names only (never values). */
export interface ContainerDetails extends Container {
  createdAt: string;
  path: string;
  args: string[] | null;
  entrypoint: string[] | null;
  cmd: string[] | null;
  workingDir?: string;
  user?: string;
  hostname?: string;
  tty: boolean;
  env: string[];
  stateInfo: {
    status: string;
    running: boolean;
    paused: boolean;
    restarting: boolean;
    oomKilled: boolean;
    pid: number;
    exitCode: number;
    error?: string;
    startedAt?: string;
    finishedAt?: string;
    restartCount: number;
  };
  mounts: Mount[];
  networks: Network[];
  restartPolicy: { name: string; maxRetry: number };
  labels: Record<string, string>;
  limits: { nanoCpus: number; cpuShares: number; cpusetCpus?: string; memory: number; memorySwap: number; pidsLimit: number };
  healthInfo?: {
    status: string;
    failingStreak: number;
    test?: string[];
    intervalNs?: number;
    timeoutNs?: number;
    retries?: number;
    log: { start: string; end: string; exitCode: number; output: string }[];
  };
  imageInfo?: { id: string; tags: string[] | null; digests: string[] | null; created: string; size: number; architecture: string; os: string };
  security: { privileged: boolean; capAdd?: string[]; devices: number };
  logDriver: string;
  platform?: string;
}

export interface LogLine {
  ts?: string;
  stream: "out" | "err";
  text: string;
}

/** Job snapshot from /api/jobs and the "jobs" topic. */
export interface JobSnapshot {
  id: string;
  kind: string;
  title: string;
  state: "running" | "succeeded" | "failed" | "canceled";
  stage?: string;
  progress: number;
  current?: string;
  error?: string;
  startedAt: string;
  finishedAt?: string;
  result?: Record<string, unknown>;
  logLines: number;
}

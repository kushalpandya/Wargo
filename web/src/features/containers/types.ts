// SPDX-License-Identifier: AGPL-3.0-only

/** Shape returned by GET /api/containers. */
export interface Container {
  id: string;
  name: string;
  image: string;
  imageId: string;
  state: "running" | "exited" | "paused" | "restarting" | "created" | "dead" | "removing";
  status: string;
  health?: "healthy" | "unhealthy" | "starting";
  created: number;
  startedAt?: string;
  ports: { ip?: string; private: number; public?: number; type: string }[];
  networkMode: string;
  /** For network_mode "container:<id>", the name of that container. */
  networkContainer?: string;
  compose?: { project: string; service: string; workingDir?: string; configFiles?: string };
  casaos?: boolean;
  /** null = not checked yet. */
  updateAvailable?: boolean | null;
}

/** Live stats published on the "containers.stats" topic: id -> stats. */
export interface ContainerStats {
  cpu: number;
  mem: number;
  memLimit: number;
  netRx: number;
  netTx: number;
}

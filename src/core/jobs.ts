// SPDX-License-Identifier: AGPL-3.0-only
// Fake background jobs (file copies, archives, OS upgrades, image pulls),
// shaped like Wargo's job manager: GET /api/jobs, /api/jobs/{id}, cancel,
// and progress on the "jobs" and "jobs.log" topics.
import { api, json, notFound } from "./http";
import { publish, topic } from "./bus";
import { newId } from "./store";

export type JobState = "running" | "succeeded" | "failed" | "canceled";

export interface JobSnapshot {
  id: string;
  kind: string;
  title: string;
  state: JobState;
  stage?: string;
  /** 0..1, or -1 while it can't be measured. */
  progress: number;
  current?: string;
  error?: string;
  startedAt: string;
  finishedAt?: string;
  result?: Record<string, unknown>;
  logLines: number;
}

export interface JobStep {
  /** How long this step takes, in ms. */
  ms: number;
  stage?: string;
  current?: string;
  /** Log lines written at the start of the step. */
  log?: string[];
}

export interface JobSpec {
  kind: string;
  title: string;
  steps: JobStep[];
  /** Set to make the job fail at the end with this error. */
  fail?: string;
  result?: Record<string, unknown>;
  /** Runs when the job succeeds (apply the change to the demo state here). */
  onDone?: () => void;
}

const jobs = new Map<string, { s: JobSnapshot; log: string[]; canceled: boolean }>();

topic("jobs", {});
topic("jobs.log", {});

const emit = (id: string) => publish("jobs", { ...jobs.get(id)!.s });

/** Starts a job and returns its first snapshot. */
export function startJob(spec: JobSpec): JobSnapshot {
  const id = newId("j");
  const j = {
    s: { id, kind: spec.kind, title: spec.title, state: "running", progress: 0, startedAt: new Date().toISOString(), logLines: 0 } as JobSnapshot,
    log: [] as string[],
    canceled: false,
  };
  jobs.set(id, j);
  const total = spec.steps.reduce((a, s) => a + s.ms, 0) || 1;
  let done = 0;
  const run = async () => {
    for (const step of spec.steps) {
      if (j.canceled) return;
      if (step.stage) j.s.stage = step.stage;
      if (step.current) j.s.current = step.current;
      if (step.log?.length) {
        j.log.push(...step.log);
        j.s.logLines = j.log.length;
        publish("jobs.log", { id, lines: step.log });
      }
      // Progress moves smoothly through the step.
      const slices = Math.max(1, Math.round(step.ms / 250));
      for (let i = 0; i < slices; i++) {
        if (j.canceled) return;
        await new Promise((r) => setTimeout(r, step.ms / slices));
        done += step.ms / slices;
        j.s.progress = Math.min(0.99, done / total);
        emit(id);
      }
    }
    if (j.canceled) return;
    j.s.finishedAt = new Date().toISOString();
    if (spec.fail) {
      j.s.state = "failed";
      j.s.error = spec.fail;
    } else {
      j.s.state = "succeeded";
      j.s.progress = 1;
      j.s.result = spec.result;
      spec.onDone?.();
    }
    emit(id);
  };
  setTimeout(run, 30);
  return { ...j.s };
}

export const jobHandlers = [
  api.get("/api/jobs", () => json([...jobs.values()].map((j) => j.s).reverse())),
  api.get("/api/jobs/:id", ({ params }) => {
    const j = jobs.get(String(params.id));
    return j ? json({ job: j.s, log: j.log }) : notFound();
  }),
  api.post("/api/jobs/:id/cancel", ({ params }) => {
    const j = jobs.get(String(params.id));
    if (!j) return notFound();
    if (j.s.state !== "running") return json({ canceled: false });
    j.canceled = true;
    j.s.state = "canceled";
    j.s.finishedAt = new Date().toISOString();
    emit(j.s.id);
    return json({ canceled: true });
  }),
];

// SPDX-License-Identifier: AGPL-3.0-only
// Resumable, chunked uploads. Chunks (8 MiB by default) stay far below
// Cloudflare's 100 MB request limit; the server tracks the received offset
// so an upload can resume after a network error or a page reload (the user
// re-selects the same file and it continues where it stopped).
import { useSyncExternalStore } from "react";
import { api, ApiError, csrf } from "@/lib/api";
import { joinPath, type RootId } from "./api";

export type UploadState = "queued" | "uploading" | "paused" | "done" | "error" | "canceled" | "conflict" | "interrupted";

export interface UploadItem {
  key: string;
  root: RootId;
  dir: string;
  /** Relative name; may contain "/" for folder uploads. */
  name: string;
  size: number;
  lastModified: number;
  file?: File;
  uploadId?: string;
  received: number;
  state: UploadState;
  error?: string;
  overwrite: boolean;
  /** Bytes per second (smoothed). */
  speed: number;
  startedAt?: number;
}

const STORAGE_KEY = "wargo.files.uploads";
const CONCURRENCY = 2;

let items: UploadItem[] = [];
const listeners = new Set<() => void>();
const controllers = new Map<string, XMLHttpRequest | AbortController>();
const doneCallbacks = new Set<(it: UploadItem) => void>();

function emit() {
  items = [...items];
  listeners.forEach((l) => l());
  persist();
}

function persist() {
  const keep = items
    .filter((i) => i.uploadId && !["done", "canceled"].includes(i.state))
    .map(({ key, root, dir, name, size, lastModified, uploadId, received, overwrite }) => ({ key, root, dir, name, size, lastModified, uploadId, received, overwrite }));
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(keep));
  } catch {
    /* quota */
  }
}

function restore() {
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "[]") as Partial<UploadItem>[];
    items = (Array.isArray(raw) ? raw : [])
      .filter((r) => r && typeof r.key === "string" && typeof r.name === "string" && typeof r.dir === "string" && typeof r.uploadId === "string" && Number.isFinite(r.size))
      .map((r) => ({ ...(r as UploadItem), root: String(r.root ?? "0"), received: Number(r.received) || 0, state: "interrupted", speed: 0 }));
  } catch {
    items = [];
  }
}
restore();

/** Drops restored entries the server no longer knows about. */
export async function syncWithServer() {
  if (!items.some((i) => i.state === "interrupted")) return;
  try {
    const srv = await api.get<{ id: string; received: number }[]>("/api/files/uploads");
    const map = new Map(srv.map((s) => [s.id, s.received]));
    items = items.filter((i) => i.state !== "interrupted" || map.has(i.uploadId!));
    items.forEach((i) => {
      if (i.state === "interrupted") i.received = map.get(i.uploadId!) ?? i.received;
    });
    emit();
  } catch {
    /* offline */
  }
}

function update(key: string, patch: Partial<UploadItem>) {
  items = items.map((i) => (i.key === key ? { ...i, ...patch } : i));
  emit();
}

function get(key: string) {
  return items.find((i) => i.key === key);
}

export function useUploads(): UploadItem[] {
  return useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => items,
  );
}

export function onUploadDone(cb: (it: UploadItem) => void) {
  doneCallbacks.add(cb);
  return () => {
    doneCallbacks.delete(cb);
  };
}

let seq = 0;

export interface PendingFile {
  file: File;
  /** Path relative to the target dir ("a/b/c.txt" for folder uploads). */
  rel: string;
}

export function enqueue(root: RootId, dir: string, files: PendingFile[]) {
  for (const { file, rel } of files) {
    // Re-selecting a file for an interrupted upload resumes it.
    const resumable = items.find((i) => i.state === "interrupted" && i.root === root && i.dir === dir && i.name === rel && i.size === file.size);
    if (resumable) {
      update(resumable.key, { file, state: "queued", error: undefined });
      continue;
    }
    items.push({
      key: `u${Date.now()}-${++seq}`,
      root,
      dir,
      name: rel,
      size: file.size,
      lastModified: file.lastModified,
      file,
      received: 0,
      state: "queued",
      overwrite: false,
      speed: 0,
    });
  }
  emit();
  pump();
}

/** Attach files picked by the user to interrupted uploads (by name+size). */
export function resumeWith(key: string, file: File) {
  const it = get(key);
  if (!it) return;
  if (file.size !== it.size || !it.name.endsWith(file.name)) {
    update(key, { error: "That is not the same file (name or size differ)" });
    return;
  }
  update(key, { file, state: "queued", error: undefined });
  pump();
}

function pump() {
  const active = items.filter((i) => i.state === "uploading").length;
  const next = items.filter((i) => i.state === "queued").slice(0, Math.max(0, CONCURRENCY - active));
  next.forEach((i) => void run(i.key));
}

export function pause(key: string) {
  const c = controllers.get(key);
  update(key, { state: "paused", speed: 0 });
  c?.abort();
}

export function resume(key: string) {
  const it = get(key);
  if (!it) return;
  if (!it.file) return;
  update(key, { state: "queued", error: undefined });
  pump();
}

export function cancel(key: string) {
  const it = get(key);
  if (!it) return;
  controllers.get(key)?.abort();
  update(key, { state: "canceled", speed: 0 });
  if (it.uploadId) api.del(`/api/files/upload/${encodeURIComponent(it.uploadId)}`).catch(() => {});
  window.setTimeout(() => {
    items = items.filter((i) => i.key !== key);
    emit();
  }, 600);
}

export function resolveConflict(key: string, overwrite: boolean) {
  if (!overwrite) {
    cancel(key);
    return;
  }
  update(key, { overwrite: true, state: "queued", error: undefined });
  pump();
}

export function clearFinished() {
  items = items.filter((i) => !["done", "canceled"].includes(i.state));
  emit();
}

async function sha256Hex(buf: ArrayBuffer): Promise<string | null> {
  if (!globalThis.crypto?.subtle) return null;
  const d = await crypto.subtle.digest("SHA-256", buf);
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

class ChunkError extends Error {
  status: number;
  code: string;
  data?: { offset?: number };
  constructor(status: number, code: string, message: string, data?: { offset?: number }) {
    super(message);
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

/** PUTs one chunk with XHR so we get upload progress events. */
function putChunk(key: string, id: string, offset: number, body: ArrayBuffer, hash: string | null, onProgress: (n: number) => void): Promise<number> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    controllers.set(key, xhr);
    xhr.open("PUT", `/api/files/upload/${encodeURIComponent(id)}?offset=${offset}`);
    xhr.setRequestHeader("X-Wargo-CSRF", csrf());
    xhr.setRequestHeader("Content-Type", "application/octet-stream");
    if (hash) xhr.setRequestHeader("X-Chunk-Sha256", hash);
    xhr.upload.onprogress = (e) => onProgress(e.loaded);
    xhr.onload = () => {
      let j: { received?: number; code?: string; message?: string; data?: { offset?: number } } = {};
      try {
        j = JSON.parse(xhr.responseText);
      } catch {
        /* non-JSON */
      }
      if (xhr.status === 200) resolve(j.received ?? offset + body.byteLength);
      else reject(new ChunkError(xhr.status, j.code ?? "error", j.message ?? `HTTP ${xhr.status}`, j.data));
    };
    xhr.onerror = () => reject(new ChunkError(0, "network", "Network error"));
    xhr.onabort = () => reject(new ChunkError(0, "aborted", "Aborted"));
    xhr.send(body);
  });
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function run(key: string) {
  let it = get(key);
  if (!it?.file) return;
  update(key, { state: "uploading", startedAt: Date.now(), speed: 0 });
  const file = it.file;
  let failures = 0;
  try {
    const init = await api.post<{ uploadId: string; chunkSize: number; received: number }>("/api/files/upload/init", {
      root: it.root,
      dir: it.dir,
      name: it.name,
      size: it.size,
      overwrite: it.overwrite,
    });
    let offset = init.received;
    update(key, { uploadId: init.uploadId, received: offset });
    let lastT = performance.now();
    let lastB = offset;
    let speed = 0;
    while (offset < it.size) {
      it = get(key);
      if (!it || it.state !== "uploading") return; // paused / canceled
      const end = Math.min(it.size, offset + init.chunkSize);
      const buf = await file.slice(offset, end).arrayBuffer();
      const hash = await sha256Hex(buf);
      try {
        const base = offset;
        offset = await putChunk(key, init.uploadId, offset, buf, hash, (n) => {
          const now = performance.now();
          const dt = (now - lastT) / 1000;
          if (dt > 0.25) {
            const inst = (base + n - lastB) / dt;
            speed = speed ? speed * 0.6 + inst * 0.4 : inst;
            lastT = now;
            lastB = base + n;
          }
          const cur = get(key);
          if (cur?.state === "uploading") update(key, { received: base + n, speed });
        });
        failures = 0;
        update(key, { received: offset });
      } catch (e) {
        const ce = e as ChunkError;
        if (ce.code === "aborted") return;
        if (ce.code === "offset_mismatch" && typeof ce.data?.offset === "number") {
          offset = ce.data.offset;
          continue;
        }
        if (ce.code === "upload_not_found") throw ce;
        if (ce.status >= 400 && ce.status < 500 && ce.code !== "checksum_mismatch" && ce.code !== "upload_interrupted") throw ce;
        if (++failures > 6) throw ce;
        update(key, { error: `Retrying… (${ce.message})` });
        await sleep(Math.min(15000, 500 * 2 ** failures));
        update(key, { error: undefined });
      }
    }
    await api.post(`/api/files/upload/${encodeURIComponent(init.uploadId)}/complete`);
    update(key, { state: "done", received: it.size, speed: 0, file: undefined, error: undefined });
    const done = get(key);
    if (done) doneCallbacks.forEach((cb) => cb(done));
  } catch (e) {
    const err = e as ApiError | ChunkError;
    if (err.code === "exists") update(key, { state: "conflict", error: `${joinPath(it!.dir, it!.name)} already exists`, speed: 0 });
    else update(key, { state: "error", error: err.message, speed: 0 });
  } finally {
    controllers.delete(key);
    pump();
  }
}

// ---- collecting files from inputs and drag & drop ----------------------------

export function fromInput(list: FileList | null): PendingFile[] {
  if (!list) return [];
  return [...list].map((f) => ({ file: f, rel: (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name }));
}

type FsEntry = {
  isFile: boolean;
  isDirectory: boolean;
  name: string;
  fullPath: string;
  file?: (cb: (f: File) => void, err: (e: unknown) => void) => void;
  createReader?: () => { readEntries: (cb: (e: FsEntry[]) => void, err: (e: unknown) => void) => void };
};

async function walk(entry: FsEntry, prefix: string, out: PendingFile[]) {
  if (entry.isFile && entry.file) {
    const f = await new Promise<File>((res, rej) => entry.file!(res, rej));
    out.push({ file: f, rel: prefix + f.name });
  } else if (entry.isDirectory && entry.createReader) {
    const reader = entry.createReader();
    for (;;) {
      const batch = await new Promise<FsEntry[]>((res, rej) => reader.readEntries(res, rej));
      if (!batch.length) break;
      for (const e of batch) await walk(e, prefix + entry.name + "/", out);
    }
  }
}

/** Expands a drop (files and folders) into relative paths. */
export async function fromDrop(dt: DataTransfer): Promise<PendingFile[]> {
  const out: PendingFile[] = [];
  const entries = [...dt.items]
    .filter((i) => i.kind === "file")
    .map((i) => (i as DataTransferItem & { webkitGetAsEntry?: () => FsEntry | null }).webkitGetAsEntry?.() ?? null);
  if (entries.length && entries.every(Boolean)) {
    for (const e of entries) await walk(e!, "", out);
    return out;
  }
  return [...dt.files].map((f) => ({ file: f, rel: f.name }));
}

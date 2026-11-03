// SPDX-License-Identifier: AGPL-3.0-only
// Shared pieces of the file handlers: roots, entries, names, sizes and
// the bytes behind a file.
import type { Entry, Root } from "@/features/files/api";
import { requireApproval } from "../../core/elevation";
import { fail } from "../../core/http";
import { fromB64, renderImage } from "./media";
import * as V from "./vfs";

// ---- errors & approval ------------------------------------------------------------

/** Thrown to answer with a ready response (the approval prompt). */
export class Reply extends Error {
  constructor(public res: Response) {
    super("reply");
  }
}

type Info = { request: Request; params: Record<string, unknown> };

/** Wraps a handler so FsError/Reply become responses. */
export const h =
  (fn: (info: Info) => Response | Promise<Response>) =>
  async (info: Info): Promise<Response> => {
    try {
      return await fn(info);
    } catch (e) {
      if (e instanceof Reply) return e.res;
      if (e instanceof V.FsError) return fail(e.status, e.code, e.message, e.data);
      if (e instanceof SyntaxError) return fail(400, "bad_request", `invalid JSON body: ${e.message}`);
      throw e;
    }
  };

/**
 * Needs root for op on abs: asks for approval (throws the 403 prompt) or
 * returns true once the request carries it.
 */
export function approve(request: Request, op: string, abs: string): true {
  const r = requireApproval(request, "files.op", `${op} ${abs}`);
  if (r) throw new Reply(r);
  return true;
}

// ---- roots ---------------------------------------------------------------------------

export function lookupRoot(v: string | null | undefined): V.RootRef {
  if (!v) return V.PLACES[0];
  const byId = [...V.PLACES, ...V.DRIVES].find((r) => r.id === v);
  if (byId) return byId;
  if (v.startsWith("m-")) throw new V.FsError(404, "unknown_root", "That drive is no longer mounted");
  const byName = V.PLACES.find((r) => r.name === v);
  if (byName) return byName;
  throw new V.FsError(404, "unknown_root", `Unknown root "${v}"`);
}

export function rootByPath(p: string): V.RootRef | undefined {
  return V.PLACES.find((r) => r.path === p) ?? V.DRIVES.find((r) => r.path === p);
}

const mountFor = (p: string) => V.MOUNTS.find((m) => m.path === V.mountOf(p))!;

export function rootStats(hidden: string[]): Root[] {
  const places: Root[] = V.PLACES.map((r) => ({ id: r.id, kind: "place", name: r.name, path: r.path, ...V.usage(mountFor(r.path)) }));
  const drives: Root[] = V.MOUNTS.map((m) => ({
    id: m.id,
    kind: "drive",
    name: m.label,
    path: m.path,
    device: m.device,
    fstype: m.fstype,
    readOnly: false,
    removable: false,
    network: false,
    ...V.usage(m),
    ...(hidden.includes(m.path) ? { hidden: true } : {}),
  }));
  return [...places, ...drives];
}

export const freeAt = (abs: string) => V.usage(mountFor(abs)).free;

// ---- entries -----------------------------------------------------------------------------

export const iso = (ms: number) => new Date(ms).toISOString();

export function entry(rootPath: string, abs: string, n: V.Node, name: string): Entry {
  const a = V.access(n);
  const e: Entry = {
    name,
    type: n.t === "d" ? "dir" : n.t === "l" ? "symlink" : "file",
    size: n.t === "d" ? 0 : (n.size ?? 0),
    mode: V.lsMode(n),
    octal: V.octal(n.mode),
    mtime: iso(n.mtime),
    owner: V.userName(n.uid),
    group: V.groupName(n.gid),
    uid: n.uid,
    gid: n.gid,
    readable: a.r,
    writable: a.w,
  };
  if (n.t === "l") {
    e.target = n.target;
    try {
      const t = V.resolve(rootPath, V.relOf(rootPath, abs), true);
      if (!t.node) {
        e.targetType = "broken";
        e.readable = e.writable = false;
      } else {
        e.targetType = t.node.t === "d" ? "dir" : "file";
        if (t.node.t === "f") {
          e.size = t.node.size ?? 0;
          e.mime = V.mimeOf(name) || undefined;
        }
        const ta = V.access(t.node);
        e.readable = ta.r;
        e.writable = ta.w;
      }
    } catch (err) {
      e.targetType = err instanceof V.FsError && err.code === "outside_root" ? "outside" : "broken";
      e.readable = e.writable = false;
    }
  }
  if (n.t === "f") {
    const m = V.mimeOf(name);
    if (m) e.mime = m;
  }
  return e;
}

export const isDirEntry = (e: Entry) => e.type === "dir" || e.targetType === "dir";

export function sortEntries(list: Entry[]) {
  return list.sort((a, b) => {
    const da = isDirEntry(a);
    const db = isDirEntry(b);
    if (da !== db) return da ? -1 : 1;
    const la = a.name.toLowerCase();
    const lb = b.name.toLowerCase();
    if (la !== lb) return la < lb ? -1 : 1;
    return a.name < b.name ? -1 : a.name > b.name ? 1 : 0;
  });
}

// ---- names -----------------------------------------------------------------------------

export function validName(name: string) {
  if (!name || name === "." || name === "..") throw V.errBad("invalid name");
  if (/[/\0]/.test(name)) throw V.errBad('name must not contain "/"');
  if (new TextEncoder().encode(name).length > 255) throw V.errBad("name is too long");
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f\x7f\x80-\x9f]/.test(name)) throw V.errBad("name must not contain control characters");
}

export function cleanPaths(list: unknown): string[] {
  if (!Array.isArray(list) || list.length === 0) throw V.errBad("no paths given");
  if (list.length > 10000) throw V.errBad("too many paths");
  const out: string[] = [];
  for (const p of list) {
    const c = V.clean(String(p));
    if (c === ".") throw V.errBad("the root itself cannot be used here");
    if (!out.includes(c)) out.push(c);
  }
  return out;
}

export function splitExt(name: string): [string, string] {
  const low = name.toLowerCase();
  for (const e of [".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst"]) {
    if (low.endsWith(e) && name.length > e.length) return [name.slice(0, -e.length), name.slice(-e.length)];
  }
  const i = name.lastIndexOf(".");
  return i <= 0 ? [name, ""] : [name.slice(0, i), name.slice(i)];
}

/** name, or "name (2).ext"... so it doesn't exist in dirAbs. */
export function uniqueName(dirAbs: string, name: string): string {
  const nodes = V.tree();
  if (!nodes[V.joinAbs(dirAbs, name)]) return name;
  const [stem, ext] = splitExt(name);
  for (let i = 2; i < 10000; i++) {
    const n = `${stem} (${i})${ext}`;
    if (!nodes[V.joinAbs(dirAbs, n)]) return n;
  }
  return `${stem}-${Date.now()}${ext}`;
}

// ---- tree operations ----------------------------------------------------------------------

export interface Measure {
  bytes: number;
  files: number;
  /** Largest file below (path relative to the item's parent). */
  biggest: string;
}

export function measure(absList: string[]): Measure {
  const nodes = V.tree();
  let bytes = 0;
  let files = 0;
  let big = 0;
  let biggest = "";
  for (const abs of absList) {
    const parent = V.parentAbs(abs);
    for (const k of V.subtree(abs)) {
      const n = nodes[k];
      if (n.t !== "f") continue;
      files++;
      bytes += n.size ?? 0;
      if ((n.size ?? 0) >= big) {
        big = n.size ?? 0;
        biggest = V.relOf(parent, k);
      }
    }
  }
  return { bytes, files, biggest };
}

export function moveTree(from: string, to: string) {
  const nodes = V.tree();
  const keys = V.subtree(from);
  const moved = keys.map((k) => [to + k.slice(from.length), nodes[k]] as const);
  for (const k of keys) delete nodes[k];
  for (const [k, n] of moved) nodes[k] = n;
}

export function copyTree(from: string, to: string, uid = V.ME.uid, gid = V.ME.gid) {
  const nodes = V.tree();
  for (const k of V.subtree(from)) {
    nodes[to + k.slice(from.length)] = { ...structuredClone(nodes[k]), uid, gid };
  }
}

export function removeTree(abs: string) {
  const nodes = V.tree();
  for (const k of V.subtree(abs)) {
    const g = nodes[k].gen;
    if (g?.startsWith("mem:") && !Object.values(nodes).some((n) => n !== nodes[k] && n.gen === g)) V.memBlobs.delete(g.slice(4));
    delete nodes[k];
  }
}

/** Creates missing folders down to abs (owned by the demo user). */
export function mkdirAll(abs: string, uid = V.ME.uid, gid = V.ME.gid) {
  const nodes = V.tree();
  if (nodes[abs]) return;
  mkdirAll(V.parentAbs(abs), uid, gid);
  nodes[abs] = { t: "d", mode: 0o755, mtime: Date.now(), uid, gid };
}

/** Removing abs needs a writable parent, and every non-empty folder below writable too. */
export function canRemove(abs: string): boolean {
  const nodes = V.tree();
  const parent = nodes[V.parentAbs(abs)];
  if (!parent || !V.access(parent).w) return false;
  for (const k of V.subtree(abs)) {
    const n = nodes[k];
    if (n.t === "d" && V.childrenOf(k).length > 0 && !V.access(n).w) return false;
  }
  return true;
}

// ---- bytes ------------------------------------------------------------------------------------

/** The bytes behind a file, or null when the demo only has its metadata. */
export async function bytesOf(n: V.Node, name: string): Promise<Uint8Array<ArrayBuffer> | null> {
  if (n.text !== undefined) return new Uint8Array(new TextEncoder().encode(n.text));
  const g = n.gen;
  if (g?.startsWith("img:")) {
    const low = name.toLowerCase();
    const type = low.endsWith(".png") ? "image/png" : low.endsWith(".webp") ? "image/webp" : "image/jpeg";
    return new Uint8Array(await (await renderImage(g, type)).arrayBuffer());
  }
  if (g?.startsWith("b64:")) return fromB64(g.slice(4));
  if (g?.startsWith("mem:")) {
    const b = V.memBlobs.get(g.slice(4));
    return b ? new Uint8Array(b) : null;
  }
  if ((n.size ?? 0) === 0) return new Uint8Array(0);
  return null;
}

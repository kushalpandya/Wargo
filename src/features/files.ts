// SPDX-License-Identifier: AGPL-3.0-only
// The file manager: a virtual filesystem (Home, DATA, the system drive and
// the backup drive) behind every /api/files/* endpoint, with permissions,
// approval prompts for root-owned paths, trash, background copy / archive
// jobs, bookmarks and resumable uploads.
import { HttpResponse, type RequestHandler, type WebSocketHandler } from "msw";
import type { Bookmark, FileContent, Listing, Locations } from "@/features/files/api";
import { api, body, fail, json, noContent, notInDemo, query } from "../core/http";
import { startJob, type JobStep } from "../core/jobs";
import { persisted } from "../core/store";
import { HOME } from "./host";
import * as V from "./files/vfs";
import {
  approve, bytesOf, canRemove, cleanPaths, copyTree, entry, freeAt, h, iso, isDirEntry, lookupRoot, measure, mkdirAll,
  moveTree, removeTree, rootByPath, rootStats, sortEntries, splitExt, uniqueName, validName,
} from "./files/helpers";
import { makeZip, toB64, type ZipItem } from "./files/media";

const MAX_EDIT = 4 << 20;
const CHUNK = 8 << 20;
const TRASH = `${HOME}/.local/share/wargo/trash`;
const MAX_BOOKMARKS = 100;

// ---- bookmarks & hidden drives ------------------------------------------------------

interface StoredBookmark {
  id: string;
  name: string;
  root: string;
  path: string;
}

const bmId = () => Array.from({ length: 16 }, () => Math.floor(Math.random() * 16).toString(16)).join("");

const marks = persisted("files.bookmarks", () => ({
  list: [
    { id: bmId(), name: "Home", root: "0", path: "." },
    { id: bmId(), name: "DATA", root: "1", path: "." },
    { id: bmId(), name: "docker", root: "0", path: "docker" },
    { id: bmId(), name: "Movies", root: "1", path: "Media/Movies" },
    { id: bmId(), name: "Photos", root: "1", path: "Photos" },
    { id: bmId(), name: "Logs", root: "m-root", path: "var/log" },
  ] as StoredBookmark[],
  hidden: [] as string[],
}));

function bookmarkAbs(b: StoredBookmark) {
  const r = [...V.PLACES, ...V.DRIVES].find((x) => x.id === b.root);
  return r ? V.absOf(r.path, b.path) : "";
}

function view(b: StoredBookmark): Bookmark {
  const abs = bookmarkAbs(b);
  const v: Bookmark = { ...b, abs, kind: b.root.startsWith("m-") ? "drive" : "place", available: !!abs };
  if (!abs) v.reason = "Location is no longer configured";
  if (abs === HOME) v.home = true;
  return v;
}

/** A folder inside a drive, re-expressed in the most specific place containing it. */
function preferPlace(ref: V.RootRef, rel: string): [V.RootRef, string] {
  if (ref.kind !== "drive") return [ref, rel];
  const abs = V.absOf(ref.path, rel);
  const best = V.PLACES.filter((p) => V.insideAbs(abs, p.path)).sort((a, b) => b.path.length - a.path.length)[0];
  if (!best) return [ref, rel];
  const r = V.relOf(best.path, abs);
  try {
    const st = V.resolve(best.path, r);
    if (st.node?.t === "d") return [best, r];
  } catch {
    /* keep the drive */
  }
  return [ref, rel];
}

function cleanBookmarkName(name: string) {
  const n = name.trim();
  if (!n) throw V.errBad("Bookmark name must not be empty");
  if ([...n].length > 64) throw V.errBad("Bookmark name must be at most 64 characters");
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f\x7f]/.test(n)) throw V.errBad("Bookmark name must not contain control characters");
  return n;
}

const bookmarkHandlers = [
  api.get("/api/files/roots", h(() => json(rootStats(marks.get().hidden)))),

  api.get(
    "/api/files/locations",
    h(() => {
      const roots = rootStats(marks.get().hidden);
      return json<Locations>({
        roots,
        bookmarks: marks.get().list.map(view),
        hiddenDrives: roots.filter((r) => r.hidden).length,
        maxBookmarks: MAX_BOOKMARKS,
        home: HOME,
      });
    }),
  ),

  api.get("/api/files/bookmarks", h(() => json(marks.get().list.map(view)))),

  api.post(
    "/api/files/bookmarks",
    h(async ({ request }) => {
      const req = await body<{ root?: string; path?: string; name?: string }>(request);
      if (!req.root) throw V.errBad("root is required");
      let ref = lookupRoot(req.root);
      let rel = V.clean(req.path);
      const st = V.resolve(ref.path, rel);
      if (!st.node) throw V.errNotFound(rel);
      if (st.node.t !== "d") throw new V.FsError(400, "not_dir", "Only folders can be bookmarked");
      [ref, rel] = preferPlace(ref, rel);
      const name = cleanBookmarkName(req.name?.trim() ? req.name : rel === "." ? ref.name : V.baseRel(rel).slice(0, 64));
      const b: StoredBookmark = { id: bmId(), name, root: ref.id, path: rel };
      const abs = bookmarkAbs(b);
      const dup = marks.get().list.find((x) => bookmarkAbs(x) === abs);
      if (dup) return fail(409, "exists", `This folder is already bookmarked as "${dup.name}"`, { id: dup.id });
      if (marks.get().list.length >= MAX_BOOKMARKS) return fail(409, "limit", `You can have at most ${MAX_BOOKMARKS} bookmarks`);
      marks.update((m) => void m.list.push(b));
      return json(view(b));
    }),
  ),

  api.post(
    "/api/files/bookmarks/reorder",
    h(async ({ request }) => {
      const { ids } = await body<{ ids?: string[] }>(request);
      const list = marks.get().list;
      const stale = () => fail(409, "stale", "The bookmark list changed; reload and try again");
      if (!Array.isArray(ids) || ids.length !== list.length) return stale();
      const out: StoredBookmark[] = [];
      for (const id of ids) {
        const b = list.find((x) => x.id === id);
        if (!b || out.includes(b)) return stale();
        out.push(b);
      }
      marks.update((m) => void (m.list = out));
      return json(out.map(view));
    }),
  ),

  api.put(
    "/api/files/bookmarks/:id",
    h(async ({ request, params }) => {
      const { name } = await body<{ name?: string }>(request);
      const n = cleanBookmarkName(name ?? "");
      const b = marks.get().list.find((x) => x.id === params.id);
      if (!b) return fail(404, "not_found", "No such bookmark");
      marks.update(() => void (b.name = n));
      return json(view(b));
    }),
  ),

  api.del(
    "/api/files/bookmarks/:id",
    h(({ params }) => {
      const b = marks.get().list.find((x) => x.id === params.id);
      if (!b) return fail(404, "not_found", "No such bookmark");
      marks.update((m) => void (m.list = m.list.filter((x) => x !== b)));
      return json({ deleted: b.id });
    }),
  ),

  api.put(
    "/api/files/drives/:id/hidden",
    h(async ({ request, params }) => {
      const { hidden } = await body<{ hidden?: boolean }>(request);
      if (typeof hidden !== "boolean") throw V.errBad("hidden is required");
      const d = V.DRIVES.find((x) => x.id === params.id);
      if (!d) return fail(404, "unknown_root", "Not a drive id");
      marks.update((m) => {
        m.hidden = m.hidden.filter((p) => p !== d.path);
        if (hidden) m.hidden.push(d.path);
      });
      return json({ path: d.path, hidden });
    }),
  ),
];

// ---- browsing ------------------------------------------------------------------------------

const browseHandlers = [
  api.get(
    "/api/files/list",
    h(({ request }) => {
      const q = query(request);
      const ref = lookupRoot(q.get("root"));
      const p = V.clean(q.get("path"));
      const hidden = q.get("hidden") === "1" || q.get("hidden") === "true";
      const dirsOnly = q.get("dirsOnly") === "1";
      const st = V.resolve(ref.path, p);
      if (!st.node) throw V.errNotFound(p);
      if (st.node.t !== "d") throw V.errNotDir(p);
      const a = V.access(st.node);
      if (!a.r || !a.x) throw V.errPerm(p);
      const nodes = V.tree();
      const entries = V.childrenOf(st.abs)
        .filter((n) => hidden || !n.startsWith("."))
        .map((n) => entry(ref.path, V.joinAbs(st.abs, n), nodes[V.joinAbs(st.abs, n)], n))
        .filter((e) => !dirsOnly || isDirEntry(e));
      return json<Listing>({ path: p, parent: p === "." ? null : V.parentRel(p), entries: sortEntries(entries), truncated: false });
    }),
  ),

  api.get(
    "/api/files/stat",
    h(({ request }) => {
      const q = query(request);
      const ref = lookupRoot(q.get("root"));
      const p = V.clean(q.get("path"));
      const st = V.mustLstat(ref.path, p);
      return json({ path: p, abs: V.absOf(ref.path, p), entry: entry(ref.path, st.abs, st.node, p === "." ? ref.name : V.baseRel(p)) });
    }),
  ),

  api.get(
    "/api/files/size",
    h(async ({ request }) => {
      const q = query(request);
      const ref = lookupRoot(q.get("root"));
      const p = V.clean(q.get("path"));
      const st = V.mustLstat(ref.path, p);
      const m = measure([st.abs]);
      return json({ size: m.bytes, files: m.files, partial: false });
    }),
  ),
];

// ---- content (editor) -----------------------------------------------------------------------

function textOf(n: V.Node): string | null {
  if (n.text !== undefined) return n.text;
  if (!n.gen && (n.size ?? 0) === 0) return "";
  return null;
}

const contentHandlers = [
  api.get(
    "/api/files/content",
    h(({ request }) => {
      const q = query(request);
      const ref = lookupRoot(q.get("root"));
      const p = V.clean(q.get("path"));
      const st = V.mustStat(ref.path, p);
      if (st.node.t !== "f") throw V.errBad("not a regular file");
      const size = st.node.size ?? 0;
      if (size > MAX_EDIT) return fail(413, "too_large", "File is too large to edit", { size, max: MAX_EDIT });
      if (!V.access(st.node).r) approve(request, "read", st.abs);
      const text = textOf(st.node);
      if (text === null) return fail(415, "binary", "This looks like a binary file");
      return json<FileContent>({ content: text, mtime: iso(st.node.mtime), size, encoding: "utf-8", writable: V.access(st.node).w, mode: V.octal(st.node.mode) });
    }),
  ),

  api.put(
    "/api/files/content",
    h(async ({ request }) => {
      const req = await body<{ root?: string; path?: string; content?: string; mtime?: string | null; encoding?: string; create?: boolean }>(request);
      const content = req.content ?? "";
      const size = req.encoding === "latin1" ? content.length : new TextEncoder().encode(content).length;
      if (size > MAX_EDIT) return fail(413, "too_large", "Content exceeds the editor size limit");
      const ref = lookupRoot(req.root);
      const p = V.clean(req.path);
      if (p === ".") throw V.errBad("path required");
      if (req.create) validName(V.baseRel(p));
      const st = V.resolve(ref.path, p);
      const nodes = V.tree();
      let asRoot = false;
      if (st.node) {
        if (req.create) throw V.errExists(p);
        if (st.node.t !== "f") throw V.errBad("not a regular file");
        if (!req.mtime || Date.parse(req.mtime) !== st.node.mtime) {
          return fail(409, "conflict", "The file was changed on disk since it was opened", { mtime: iso(st.node.mtime), size: st.node.size ?? 0 });
        }
        if (!V.access(st.node).w) asRoot = approve(request, "write", st.abs);
        const n = st.node;
        n.text = content;
        n.size = size;
        n.mtime = Date.now();
        delete n.gen;
        delete n.arc;
      } else {
        if (!req.create) throw V.errNotFound(p);
        const parent = nodes[V.parentAbs(st.abs)];
        if (!parent) throw V.errNotFound(p);
        if (!V.access(parent).w) asRoot = approve(request, "write", st.abs);
        nodes[st.abs] = { t: "f", text: content, size, mode: 0o644, mtime: Date.now(), uid: asRoot ? 0 : V.ME.uid, gid: asRoot ? 0 : V.ME.gid };
      }
      V.save();
      const n = nodes[st.abs];
      return json({ mtime: iso(n.mtime), size: n.size ?? 0 });
    }),
  ),
];

// ---- downloads -------------------------------------------------------------------------------

function inlineType(name: string) {
  const m = V.mimeOf(name);
  if (m === "image/svg+xml" || m === "text/html" || m === "text/xml") return "text/plain; charset=utf-8";
  if (m.startsWith("image/") || m.startsWith("video/") || m.startsWith("audio/") || m === "application/pdf") return m;
  if (m.startsWith("text/") || m === "application/json") return "text/plain; charset=utf-8";
  return "";
}

const disposition = (kind: string, name: string) => `${kind}; filename*=UTF-8''${encodeURIComponent(name)}`;

async function zipResponse(ref: V.RootRef, absList: string[], name: string) {
  const nodes = V.tree();
  const items: ZipItem[] = [];
  let total = 0;
  for (const abs of absList) {
    const base = abs === ref.path ? abs : V.parentAbs(abs);
    for (const k of V.subtree(abs).sort()) {
      const n = nodes[k];
      const rel = V.relOf(base, k);
      if (rel === ".") continue;
      if (n.t === "d") items.push({ name: rel, dir: true, mtime: n.mtime, mode: n.mode });
      else if (n.t === "f") {
        if (!V.access(n).r) throw V.errPerm(V.relOf(ref.path, k));
        const data = await bytesOf(n, V.baseAbs(k));
        if (!data) return notInDemo("Zipping media and other large files");
        total += data.length;
        if (total > 64 * V.MB) return notInDemo("Zipping media and other large files");
        items.push({ name: rel, data, mtime: n.mtime, mode: n.mode });
      }
    }
  }
  return new HttpResponse(makeZip(items), {
    headers: { "Content-Type": "application/zip", "Content-Disposition": disposition("attachment", name), "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store" },
  });
}

async function downloadZip(request: Request) {
  let req: { root?: string; paths?: string[]; name?: string };
  if (request.method === "GET") {
    const q = query(request);
    req = { root: q.get("root") ?? "", paths: q.getAll("path"), name: q.get("name") ?? "" };
  } else if ((request.headers.get("Content-Type") ?? "").startsWith("application/json")) {
    req = await body(request);
  } else {
    const f = new URLSearchParams(await request.text());
    req = { root: f.get("root") ?? "", paths: f.getAll("paths"), name: f.get("name") ?? "" };
  }
  const ref = lookupRoot(req.root);
  if (!req.paths?.length) throw V.errBad("no paths given");
  const paths = [...new Set(req.paths.map((p) => V.clean(p)))];
  const abs = paths.map((p) => V.mustLstat(ref.path, p).abs);
  let name = req.name ?? "";
  try {
    validName(name);
  } catch {
    name = paths.length === 1 && paths[0] !== "." ? V.baseRel(paths[0]) : V.parentRel(paths[0]) !== "." ? V.baseRel(V.parentRel(paths[0])) : ref.name;
  }
  if (!name.toLowerCase().endsWith(".zip")) name += ".zip";
  return zipResponse(ref, abs, name);
}

const downloadHandlers = [
  api.get(
    "/api/files/download",
    h(async ({ request }) => {
      const q = query(request);
      const ref = lookupRoot(q.get("root"));
      const p = V.clean(q.get("path"));
      const st = V.mustStat(ref.path, p);
      if (st.node.t === "d") return zipResponse(ref, [st.abs], `${p === "." ? ref.name : V.baseRel(p)}.zip`);
      if (st.node.t !== "f") throw V.errBad("not a regular file");
      if (!V.access(st.node).r) throw V.errPerm(p);
      const name = p === "." ? ref.name : V.baseRel(p);
      const data = await bytesOf(st.node, name);
      if (!data) return notInDemo("Downloading this file");
      const headers: Record<string, string> = { "X-Content-Type-Options": "nosniff", "Cache-Control": "private, no-cache", "Last-Modified": new Date(st.node.mtime).toUTCString() };
      const inline = q.get("inline") === "1" ? inlineType(name) : "";
      if (inline) {
        headers["Content-Type"] = inline;
        headers["Content-Disposition"] = disposition("inline", name);
      } else {
        const m = V.mimeOf(name);
        headers["Content-Type"] = !m || inlineType(name) === "text/plain; charset=utf-8" ? "application/octet-stream" : m;
        headers["Content-Disposition"] = disposition("attachment", name);
      }
      return new HttpResponse(data, { headers });
    }),
  ),
  api.get("/api/files/download-zip", h(({ request }) => downloadZip(request))),
  api.post("/api/files/download-zip", h(({ request }) => downloadZip(request))),
];

// ---- mkdir / rename / move / copy -------------------------------------------------------------

/** Rough speed of a copy between two mounts (bytes/s). */
function speed(a: string, b: string) {
  const ma = V.mountOf(a);
  const mb = V.mountOf(b);
  if (ma === "/" && mb === "/") return 1400 * V.MB;
  if (ma === mb) return 190 * V.MB;
  return 165 * V.MB;
}

const clampMs = (ms: number, lo: number, hi: number) => Math.round(Math.min(hi, Math.max(lo, ms)));

interface Pair {
  src: string;
  dst: string;
  dstRel: string;
}

function describe(pairs: Pair[]) {
  return pairs.length === 1 ? V.baseAbs(pairs[0].src) : `${pairs.length} items`;
}

interface TransferReq {
  root?: string;
  destRoot?: string;
  paths?: string[];
  dest?: string;
  onConflict?: string;
}

function plan(req: TransferReq, isCopy: boolean) {
  const sref = lookupRoot(req.root);
  const dref = req.destRoot ? lookupRoot(req.destRoot) : sref;
  const paths = cleanPaths(req.paths);
  const dest = V.clean(req.dest);
  const d = V.resolve(dref.path, dest);
  if (!d.node) throw V.errNotFound(dest);
  if (d.node.t !== "d") throw new V.FsError(400, "not_dir", "Destination is not a folder");
  const nodes = V.tree();
  const pairs: Pair[] = [];
  const conflicts: string[] = [];
  for (const p of paths) {
    const s = V.mustLstat(sref.path, p);
    if (V.insideAbs(d.abs, s.abs)) throw new V.FsError(400, "into_self", "Cannot copy or move a folder into itself");
    const name = V.baseAbs(s.abs);
    let dst = V.joinAbs(d.abs, name);
    if (V.parentAbs(s.abs) === d.abs) {
      if (!isCopy) continue;
      dst = V.joinAbs(d.abs, uniqueName(d.abs, name));
    } else if (nodes[dst]) {
      if (req.onConflict === "rename") dst = V.joinAbs(d.abs, uniqueName(d.abs, name));
      else if (req.onConflict === "skip") continue;
      else {
        conflicts.push(V.relOf(dref.path, dst));
        continue;
      }
    }
    pairs.push({ src: s.abs, dst, dstRel: V.relOf(dref.path, dst) });
  }
  if (conflicts.length) throw new V.FsError(409, "exists", `${conflicts.length} item(s) already exist in the destination`, { conflicts });
  return { sref, dref, pairs, destAbs: d.abs };
}

/** Background copy or move between filesystems. */
function transferJob(kind: "files.copy" | "files.move", pairs: Pair[], sref: V.RootRef, dref: V.RootRef, destAbs: string) {
  const move = kind === "files.move";
  const m = measure(pairs.map((x) => x.src));
  const nodes = V.tree();
  const denied = !V.access(nodes[destAbs]).w ? destAbs : move ? pairs.find((x) => !canRemove(x.src))?.src : undefined;
  const steps: JobStep[] = [{ ms: 500, stage: "Scanning" }];
  for (const x of pairs) {
    const one = measure([x.src]);
    const ms = clampMs((one.bytes / speed(x.src, x.dst)) * 1000, 900, 14000 / pairs.length + 600);
    const parts = Math.max(1, Math.min(4, Math.round(ms / 1500)));
    for (let k = 0; k < parts; k++) {
      steps.push({ ms: ms / parts, stage: move ? "Moving" : "Copying", current: V.relOf(sref.path, V.joinAbs(V.parentAbs(x.src), one.biggest || V.baseAbs(x.src))) });
    }
    steps.push({ ms: 50, log: [`${V.relOf(sref.path, x.src)} → ${x.dstRel}`] });
  }
  const job = startJob({
    kind,
    title: `${move ? "Move" : "Copy"} ${describe(pairs)}`,
    steps: denied ? steps.slice(0, 2) : steps,
    fail: denied ? `open ${denied}: permission denied` : undefined,
    result: { bytes: m.bytes, files: m.files, root: sref.id, destRoot: dref.id },
    onDone: () => {
      const t = V.tree();
      if (!t[destAbs]) return;
      for (const x of pairs) {
        if (!t[x.src] || t[x.dst]) continue;
        copyTree(x.src, x.dst);
        if (move) removeTree(x.src);
      }
      V.save();
    },
  });
  return job.id;
}

const opHandlers = [
  api.post(
    "/api/files/mkdir",
    h(async ({ request }) => {
      const req = await body<{ root?: string; path?: string }>(request);
      const ref = lookupRoot(req.root);
      const p = V.clean(req.path);
      if (p === ".") throw V.errBad("path required");
      validName(V.baseRel(p));
      const st = V.lstat(ref.path, p);
      if (st.node) throw V.errExists(p);
      const parent = V.tree()[V.parentAbs(st.abs)];
      if (!parent) throw V.errNotFound(p);
      const asRoot = !V.access(parent).w && approve(request, "mkdir", st.abs);
      V.tree()[st.abs] = { t: "d", mode: 0o755, mtime: Date.now(), uid: asRoot ? 0 : V.ME.uid, gid: asRoot ? 0 : V.ME.gid };
      V.save();
      return json({ path: p });
    }),
  ),

  api.post(
    "/api/files/rename",
    h(async ({ request }) => {
      const req = await body<{ root?: string; path?: string; name?: string }>(request);
      validName(req.name ?? "");
      const ref = lookupRoot(req.root);
      const p = V.clean(req.path);
      if (p === ".") throw V.errBad("cannot rename the root");
      const dst = V.joinRel(V.parentRel(p), req.name!);
      if (dst === p) return json({ path: dst });
      const s = V.mustLstat(ref.path, p);
      const dAbs = V.joinAbs(V.parentAbs(s.abs), req.name!);
      if (V.tree()[dAbs]) throw V.errExists(dst);
      if (!V.access(V.tree()[V.parentAbs(s.abs)]).w) approve(request, "move", s.abs);
      moveTree(s.abs, dAbs);
      V.save();
      return json({ path: dst });
    }),
  ),

  api.post(
    "/api/files/move",
    h(async ({ request }) => {
      const req = await body<TransferReq>(request);
      const { sref, dref, pairs, destAbs } = plan(req, false);
      const nodes = V.tree();
      const cross: Pair[] = [];
      let moved = 0;
      for (const x of pairs) {
        if (V.mountOf(x.src) !== V.mountOf(destAbs)) {
          cross.push(x);
          continue;
        }
        const ok = V.access(nodes[V.parentAbs(x.src)]).w && V.access(nodes[destAbs]).w;
        if (!ok) {
          if (pairs.length !== 1) throw V.errPerm(V.relOf(sref.path, x.src));
          approve(request, "move", sref.path === dref.path ? x.src : `${x.src} → ${x.dst}`);
        }
        moveTree(x.src, x.dst);
        moved++;
      }
      V.save();
      const out: { moved: number; jobId?: string } = { moved };
      if (cross.length) out.jobId = transferJob("files.move", cross, sref, dref, destAbs);
      return json(out);
    }),
  ),

  api.post(
    "/api/files/copy",
    h(async ({ request }) => {
      const req = await body<TransferReq>(request);
      const { sref, dref, pairs, destAbs } = plan(req, true);
      if (!pairs.length) return json({ copied: 0 });
      const m = measure(pairs.map((x) => x.src));
      if (m.bytes > 32 * V.MB || m.files > 500) return json({ copied: 0, jobId: transferJob("files.copy", pairs, sref, dref, destAbs) });
      let asRoot = false;
      const nodes = V.tree();
      for (const x of pairs) {
        const readable = V.subtree(x.src).every((k) => V.access(nodes[k]).r);
        if (!readable || !V.access(nodes[destAbs]).w) {
          if (pairs.length !== 1) throw V.errPerm(V.relOf(sref.path, x.src));
          asRoot = approve(request, "copy", sref.path === dref.path ? x.src : `${x.src} → ${x.dst}`);
        }
      }
      for (const x of pairs) copyTree(x.src, x.dst, asRoot ? 0 : V.ME.uid, asRoot ? 0 : V.ME.gid);
      V.save();
      return json({ copied: pairs.length, paths: pairs.map((x) => x.dstRel) });
    }),
  ),
];

// ---- delete / trash ---------------------------------------------------------------------------

function stamp() {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
}

const deleteHandlers = [
  api.post(
    "/api/files/delete",
    h(async ({ request }) => {
      const req = await body<{ root?: string; paths?: string[]; trash?: boolean }>(request);
      const ref = lookupRoot(req.root);
      const paths = cleanPaths(req.paths);
      const items = paths.map((p) => ({ p, abs: V.mustLstat(ref.path, p).abs }));
      const nodes = V.tree();
      if (req.trash) {
        const other = items.filter((x) => V.mountOf(x.abs) !== V.mountOf(TRASH)).map((x) => x.p);
        if (other.length) return fail(409, "trash_unavailable", "The trash is on a different filesystem. Delete permanently instead?", { paths: other });
        mkdirAll(TRASH);
        nodes[TRASH].mode = 0o700;
        const s = stamp();
        const trashed: string[] = [];
        for (const x of items) {
          if (V.insideAbs(TRASH, x.abs)) throw new V.FsError(400, "into_self", "Cannot copy or move a folder into itself");
          if (!V.access(nodes[V.parentAbs(x.abs)]).w) throw V.errPerm(x.p);
          let name = `${s}-${V.baseAbs(x.abs)}`;
          for (let i = 2; nodes[V.joinAbs(TRASH, name)]; i++) name = `${s}-${i}-${V.baseAbs(x.abs)}`;
          moveTree(x.abs, V.joinAbs(TRASH, name));
          trashed.push(name);
        }
        V.save();
        return json({ deleted: trashed.length, trashed });
      }
      let n = 0;
      for (const x of items) {
        if (!canRemove(x.abs)) {
          if (items.length !== 1) throw V.errPerm(x.p);
          approve(request, "remove", x.abs);
        }
        removeTree(x.abs);
        n++;
      }
      V.save();
      return json({ deleted: n });
    }),
  ),
];

// ---- chmod / chown ------------------------------------------------------------------------------

function parseMode(s: string | undefined) {
  const v = (s ?? "").trim();
  if (!/^[0-7]{1,4}$/.test(v)) throw V.errBad(`invalid mode "${v}"`);
  return parseInt(v, 8);
}

/** The node a chmod/chown applies to (symlinks are followed when given directly). */
function targets(ref: V.RootRef, p: string, recursive: boolean): string[] {
  const st = V.mustLstat(ref.path, p);
  if (!recursive) {
    if (st.node.t === "l") {
      const t = V.mustStat(ref.path, p);
      return [t.abs];
    }
    return [st.abs];
  }
  const nodes = V.tree();
  return V.subtree(st.abs).filter((k) => nodes[k].t !== "l");
}

const permHandlers = [
  api.post(
    "/api/files/chmod",
    h(async ({ request }) => {
      const req = await body<{ root?: string; paths?: string[]; mode?: string; dirMode?: string; recursive?: boolean }>(request);
      const mode = parseMode(req.mode);
      const dirMode = req.dirMode ? parseMode(req.dirMode) : mode;
      const ref = lookupRoot(req.root);
      const paths = cleanPaths(req.paths);
      const nodes = V.tree();
      for (const p of paths) {
        const list = targets(ref, p, !!req.recursive);
        if (list.some((k) => nodes[k].uid !== V.ME.uid)) {
          if (paths.length !== 1 || req.recursive) throw V.errPerm(p);
          approve(request, "chmod", list[0]);
        }
        for (const k of list) nodes[k].mode = nodes[k].t === "d" ? dirMode : mode;
      }
      V.save();
      return json({ changed: paths.length });
    }),
  ),

  api.post(
    "/api/files/chown",
    h(async ({ request }) => {
      const req = await body<{ root?: string; paths?: string[]; owner?: string; recursive?: boolean }>(request);
      const owner = (req.owner ?? "").trim();
      if (!owner || /[ /\0]/.test(owner)) throw V.errBad("owner must be user[:group]");
      const [u, g = ""] = owner.split(/:(.*)/s);
      const uid = u ? V.lookupUser(u) : -1;
      if (uid === undefined) throw V.errBad(`unknown user "${u}"`);
      const gid = g ? V.lookupGroup(g) : -1;
      if (gid === undefined) throw V.errBad(`unknown group "${g}"`);
      const ref = lookupRoot(req.root);
      const paths = cleanPaths(req.paths);
      const nodes = V.tree();
      for (const p of paths) {
        const list = targets(ref, p, !!req.recursive);
        const allowed = list.every((k) => {
          const n = nodes[k];
          return n.uid === V.ME.uid && (uid === -1 || uid === n.uid) && (gid === -1 || V.ME.groups.has(gid));
        });
        if (!allowed) {
          if (paths.length !== 1) throw V.errPerm(p);
          if (req.recursive) throw new V.FsError(403, "permission_denied", "Changing ownership recursively needs root; change one item at a time or use a root terminal");
          approve(request, "chown", list[0]);
        }
        for (const k of list) {
          if (uid !== -1) nodes[k].uid = uid;
          if (gid !== -1) nodes[k].gid = gid;
        }
      }
      V.save();
      return json({ changed: paths.length });
    }),
  ),
];

// ---- archive / extract ----------------------------------------------------------------------------

const PRECOMPRESSED = /\.(zip|gz|tgz|xz|bz2|zst|7z|rar|jpe?g|png|gif|webp|avif|heic|mp4|mkv|webm|mov|m4v|avi|mp3|flac|ogg|opus|m4a|aac|pdf|iso|deb)$/i;

const archiveHandlers = [
  api.post(
    "/api/files/archive",
    h(async ({ request }) => {
      const req = await body<{ root?: string; paths?: string[]; dest?: string; format?: string }>(request);
      const fmtIn = req.format || "zip";
      const ext = ({ zip: ".zip", "tar.gz": ".tar.gz", tgz: ".tar.gz", tar: ".tar" } as Record<string, string>)[fmtIn];
      if (!ext) throw V.errBad("format must be zip, tar or tar.gz");
      const ref = lookupRoot(req.root);
      const paths = cleanPaths(req.paths);
      const items = paths.map((p) => ({ p, abs: V.mustLstat(ref.path, p).abs }));
      let dest = V.clean(req.dest);
      if (!req.dest) {
        const dir = V.parentRel(paths[0]);
        const dirAbs = V.parentAbs(items[0].abs);
        dest = V.joinRel(dir, uniqueName(dirAbs, (paths.length === 1 ? V.baseRel(paths[0]) : "Archive") + ext));
      }
      try {
        if (dest === ".") throw new Error();
        validName(V.baseRel(dest));
      } catch {
        throw V.errBad("invalid archive name");
      }
      const d = V.lstat(ref.path, dest);
      if (d.node) throw V.errExists(dest);
      const parent = V.tree()[V.parentAbs(d.abs)];
      if (!parent || parent.t !== "d") throw new V.FsError(400, "not_dir", "Destination folder does not exist");
      for (const x of items) if (V.insideAbs(d.abs, x.abs)) throw V.errBad("the archive cannot be created inside a folder being archived");

      const format = ext.slice(1) as "zip" | "tar" | "tar.gz";
      const nodes = V.tree();
      const members: [string, V.Node][] = [];
      let packed = 0;
      for (const x of items) {
        const base = V.parentAbs(x.abs);
        for (const k of V.subtree(x.abs).sort()) {
          const n = nodes[k];
          if (format === "zip" && n.t === "l") continue;
          members.push([V.relOf(base, k), structuredClone(n)]);
          if (n.t === "f") packed += PRECOMPRESSED.test(k) || format === "tar" ? (n.size ?? 0) : Math.round((n.size ?? 0) * 0.32);
          packed += format === "zip" ? 76 + k.length * 2 : 512;
        }
      }
      const m = measure(items.map((x) => x.abs));
      const total = clampMs((m.bytes / (110 * V.MB)) * 1000, 1200, 12000);
      const steps: JobStep[] = [{ ms: 400, stage: "Scanning" }];
      const parts = Math.max(2, Math.round(total / 1500));
      for (let k = 0; k < parts; k++) {
        const cur = members.filter(([, n]) => n.t === "f")[Math.floor((k / parts) * members.length)]?.[0];
        steps.push({ ms: total / parts, stage: "Compressing", current: cur ? V.joinRel(V.relOf(ref.path, V.parentAbs(items[0].abs)), cur) : undefined });
      }
      const denied = !V.access(parent).w;
      const job = startJob({
        kind: "files.archive",
        title: `Compress ${V.baseRel(dest)}`,
        steps: denied ? steps.slice(0, 1) : steps,
        fail: denied ? `open ${V.joinRel(V.parentRel(dest), `.${V.baseRel(dest)}.partial`)}: permission denied` : undefined,
        result: { path: dest, size: packed, files: m.files, bytes: m.bytes, root: ref.id },
        onDone: () => {
          const t = V.tree();
          if (!t[V.parentAbs(d.abs)] || t[d.abs]) return;
          t[d.abs] = { t: "f", size: packed, mode: 0o644, mtime: Date.now(), uid: V.ME.uid, gid: V.ME.gid, arc: { fmt: format, members } };
          V.save();
        },
      });
      return json({ jobId: job.id, path: dest });
    }),
  ),

  api.post(
    "/api/files/extract",
    h(async ({ request }) => {
      const req = await body<{ root?: string; path?: string; dest?: string }>(request);
      const ref = lookupRoot(req.root);
      const p = V.clean(req.path);
      const st = V.mustStat(ref.path, p);
      if (st.node.t === "d") throw new V.FsError(400, "is_dir", `Is a folder: ${p}`);
      if (!V.access(st.node).r) throw V.errPerm(p);
      const arc = st.node.arc ?? (st.node.text === undefined && /\.zip$/i.test(p) ? { fmt: "zip" as const, members: [] } : undefined);
      if (!arc || !["zip", "tar", "tar.gz"].includes(arc.fmt)) return fail(415, "unsupported_archive", "Only zip, tar and tar.gz archives can be extracted");
      let dest = V.clean(req.dest);
      if (!req.dest) dest = V.joinRel(V.parentRel(p), uniqueName(V.parentAbs(st.abs), splitExt(V.baseRel(p))[0]));
      const d = V.resolve(ref.path, dest);
      if (d.node && d.node.t !== "d") throw new V.FsError(400, "not_dir", "Destination is not a folder");
      const parent = V.tree()[V.parentAbs(d.abs)];
      if (!parent || parent.t !== "d") throw new V.FsError(400, "not_dir", "Destination's parent folder does not exist");
      const members = arc.members ?? [];
      const bytes = members.reduce((s, [, n]) => s + (n.t === "f" ? (n.size ?? 0) : 0), 0);
      const total = clampMs((bytes / (160 * V.MB)) * 1000, 1000, 10000);
      const parts = Math.max(2, Math.round(total / 1500));
      const steps: JobStep[] = Array.from({ length: parts }, () => ({ ms: total / parts, stage: "Extracting" }));
      const denied = !V.access(d.node ?? parent).w;
      const result: Record<string, unknown> = { dest, files: 0, bytes: 0, skipped: 0, root: ref.id };
      const job = startJob({
        kind: "files.extract",
        title: `Extract ${V.baseRel(p)}`,
        steps: denied ? steps.slice(0, 1) : steps,
        fail: denied ? `mkdir ${dest}: permission denied` : undefined,
        result,
        onDone: () => {
          const t = V.tree();
          if (!t[V.parentAbs(d.abs)]) return;
          mkdirAll(d.abs);
          let files = 0;
          let written = 0;
          let skipped = 0;
          for (const [rel, n] of members) {
            const target = V.joinAbs(d.abs, V.clean(rel));
            if (n.t === "d") mkdirAll(target);
            else if (n.t === "l" || t[target]) skipped++;
            else {
              mkdirAll(V.parentAbs(target));
              t[target] = { ...structuredClone(n), uid: V.ME.uid, gid: V.ME.gid };
              files++;
              written += n.size ?? 0;
            }
          }
          Object.assign(result, { files, bytes: written, skipped });
          V.save();
        },
      });
      return json({ jobId: job.id, dest });
    }),
  ),
];

// ---- uploads ------------------------------------------------------------------------------------------

interface Upload {
  id: string;
  rootPath: string;
  dir: string;
  name: string;
  size: number;
  overwrite: boolean;
  created: string;
}

/** Upload metadata survives reloads; the bytes only live in memory. */
const uploads = persisted("files.uploads", () => [] as Upload[]);
const received = new Map<string, { n: number; chunks: Uint8Array[] }>();
const recv = (id: string) => received.get(id)?.n ?? 0;
const KEEP_BYTES = 64 * V.MB;

const uploadId = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");

function uploadTarget(dir: string, name: string) {
  if (name.replace(/\\/g, "/").split("/").includes("..")) throw V.errBad("invalid name");
  const rel = V.clean(name);
  if (rel === ".") throw V.errBad("name required");
  rel.split("/").forEach(validName);
  return V.joinRel(dir, rel);
}

function getUpload(id: unknown) {
  if (typeof id !== "string" || !/^[0-9a-f]{32}$/.test(id)) throw new V.FsError(404, "not_found", "Not found");
  const up = uploads.get().find((u) => u.id === id);
  if (!up) throw new V.FsError(404, "upload_not_found", "Upload not found or expired");
  return up;
}

async function chunk({ request, params }: { request: Request; params: Record<string, unknown> }) {
  const up = getUpload(params.id);
  const offset = Number(query(request).get("offset"));
  if (!Number.isInteger(offset) || offset < 0) throw V.errBad("offset required");
  const cur = recv(up.id);
  if (offset !== cur) return fail(409, "offset_mismatch", "Upload offset mismatch", { offset: cur });
  const buf = new Uint8Array(await request.arrayBuffer());
  if (buf.length > CHUNK + (1 << 20)) return fail(413, "too_large", "Chunk too large");
  if (offset + buf.length > up.size) throw V.errBad("upload exceeds the declared size");
  const want = request.headers.get("X-Chunk-Sha256")?.toLowerCase();
  if (want && crypto.subtle) {
    const d = new Uint8Array(await crypto.subtle.digest("SHA-256", buf));
    const got = Array.from(d, (b) => b.toString(16).padStart(2, "0")).join("");
    if (got !== want) return fail(422, "checksum_mismatch", "Chunk checksum mismatch");
  }
  const r = received.get(up.id) ?? { n: 0, chunks: [] };
  r.n += buf.length;
  if (up.size <= KEEP_BYTES) r.chunks.push(buf);
  received.set(up.id, r);
  return json({ received: r.n });
}

function isText(b: Uint8Array) {
  if (b.subarray(0, 8192).includes(0)) return null;
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(b);
  } catch {
    return null;
  }
}

function dropUpload(id: string) {
  received.delete(id);
  uploads.set((l) => l.filter((u) => u.id !== id));
}

const uploadHandlers = [
  api.post(
    "/api/files/upload/init",
    h(async ({ request }) => {
      const req = await body<{ root?: string; dir?: string; name?: string; size?: number; overwrite?: boolean }>(request);
      const size = Number(req.size ?? 0);
      if (!(size >= 0)) throw V.errBad("invalid size");
      const ref = lookupRoot(req.root);
      const dir = V.clean(req.dir);
      const target = uploadTarget(dir, req.name ?? "");
      const d = V.resolve(ref.path, dir);
      if (!d.node) throw V.errNotFound(dir);
      if (d.node.t !== "d") throw V.errNotDir(dir);
      const t = V.resolve(ref.path, target, false);
      if (t.node) {
        if (t.node.t === "d") return fail(409, "is_dir", `A folder with this name exists: ${target}`);
        if (!req.overwrite) return fail(409, "exists", `Already exists: ${target}`, { path: target });
      }
      const name = dir === "." ? target : target.slice(dir.length + 1);
      const same = uploads.get().find((u) => u.rootPath === ref.path && u.dir === dir && u.name === name && u.size === size);
      if (same) {
        uploads.update(() => void (same.overwrite = !!req.overwrite));
        return json({ uploadId: same.id, chunkSize: CHUNK, received: recv(same.id), size, resumed: true });
      }
      if (uploads.get().length >= 1000) return fail(429, "too_many_uploads", "Too many unfinished uploads; finish or cancel some first");
      if (size > freeAt(d.abs)) return fail(507, "no_space", `Not enough free space in ${ref.name}`);
      const up: Upload = { id: uploadId(), rootPath: ref.path, dir, name, size, overwrite: !!req.overwrite, created: new Date().toISOString() };
      uploads.update((l) => void l.push(up));
      received.set(up.id, { n: 0, chunks: [] });
      return json({ uploadId: up.id, chunkSize: CHUNK, received: 0, size, resumed: false });
    }),
  ),

  api.put("/api/files/upload/:id", h(chunk)),
  api.post(
    "/api/files/upload/:id/complete",
    h(async ({ params }) => {
      const up = getUpload(params.id);
      const cur = recv(up.id);
      if (cur !== up.size) return fail(409, "offset_mismatch", "Upload offset mismatch", { offset: cur });
      const ref = rootByPath(up.rootPath);
      if (!ref) {
        dropUpload(up.id);
        return fail(404, "unknown_root", "The upload's root is no longer configured");
      }
      const target = V.joinRel(up.dir, up.name);
      const nodes = V.tree();
      // Folder uploads create the folders on the way.
      const dirAbs = V.resolve(ref.path, up.dir).abs;
      const abs = V.joinAbs(dirAbs, up.dir === "." ? target : target.slice(up.dir.length + 1));
      let probe = V.parentAbs(abs);
      while (!nodes[probe]) probe = V.parentAbs(probe);
      if (!V.access(nodes[probe]).w) throw V.errPerm(target);
      mkdirAll(V.parentAbs(abs));
      const old = nodes[abs];
      if (old?.t === "d") throw new V.FsError(400, "is_dir", `Is a folder: ${target}`);
      if (old && !up.overwrite) throw V.errExists(target);
      if (!V.access(nodes[V.parentAbs(abs)]).w) throw V.errPerm(target);

      const node: V.Node = { t: "f", size: up.size, mode: 0o644, mtime: Date.now(), uid: V.ME.uid, gid: V.ME.gid };
      const r = received.get(up.id);
      if (r && up.size <= KEEP_BYTES) {
        const all = new Uint8Array(up.size);
        let o = 0;
        for (const c of r.chunks) {
          all.set(c, o);
          o += c.length;
        }
        const text = up.size <= 1 << 20 ? isText(all) : null;
        if (text !== null) node.text = text;
        else if (up.size <= 256 << 10) node.gen = `b64:${toB64(all)}`;
        else {
          const key = up.id.slice(0, 12);
          V.memBlobs.set(key, all);
          node.gen = `mem:${key}`;
        }
      }
      if (/\.zip$/i.test(up.name) && !node.text) node.arc = { fmt: "zip", members: [] };
      if (old) removeTree(abs);
      nodes[abs] = node;
      V.save();
      dropUpload(up.id);
      return json({ path: target, size: up.size });
    }),
  ),
  api.post("/api/files/upload/:id", h(chunk)),
  api.del(
    "/api/files/upload/:id",
    h(({ params }) => {
      const up = getUpload(params.id);
      dropUpload(up.id);
      return noContent();
    }),
  ),
  api.get(
    "/api/files/uploads",
    h(() =>
      json(
        uploads
          .get()
          .map((u) => {
            const ref = rootByPath(u.rootPath);
            return ref ? { id: u.id, root: ref.id, dir: u.dir, name: u.name, size: u.size, received: recv(u.id), created: u.created } : null;
          })
          .filter(Boolean),
      ),
    ),
  ),
];

export const handlers: (RequestHandler | WebSocketHandler)[] = [
  ...bookmarkHandlers,
  ...browseHandlers,
  ...contentHandlers,
  ...downloadHandlers,
  ...opHandlers,
  ...deleteHandlers,
  ...permHandlers,
  ...archiveHandlers,
  ...uploadHandlers,
];

// SPDX-License-Identifier: AGPL-3.0-only
// The demo's virtual filesystem: one namespace of absolute paths shared by
// every root (so "/" contains /home/demo and /DATA), with owners, modes,
// symlinks and mounts. Kept in localStorage so changes survive reloads.
import { persisted } from "../../core/store";
import { GB, MB, TB } from "../../core/rand";
import { HOME } from "../host";
import { seedTree } from "./seed";

export interface ArchiveInfo {
  /** Sniffed format. Only zip, tar and tar.gz can be extracted. */
  fmt: "zip" | "tar" | "tar.gz" | "tar.zst" | "tar.xz" | "7z";
  /** Members as [relative path, node]; folders end without "/". */
  members?: [string, Node][];
}

export interface Node {
  t: "f" | "d" | "l";
  /** Files only (folders report 0). */
  size?: number;
  /** Permission bits incl. setuid/setgid/sticky (0o7777). */
  mode: number;
  /** Unix ms. */
  mtime: number;
  uid: number;
  gid: number;
  /** Symlink target. */
  target?: string;
  /** Text content. */
  text?: string;
  /**
   * Generated content: "img:<seed>:<style>:<w>x<h>",
   * "b64:<data>" (small uploads), "mem:<id>" (uploads kept in memory).
   */
  gen?: string;
  arc?: ArchiveInfo;
}

export type Tree = Record<string, Node>;

// ---- users & identity -------------------------------------------------------

export const USERS: Record<number, string> = {
  0: "root", 1: "daemon", 2: "bin", 3: "sys", 33: "www-data", 34: "backup", 42: "_apt", 100: "messagebus",
  101: "sshd", 997: "systemd-timesync", 998: "systemd-network", 1000: "demo", 65534: "nobody",
};
export const GROUPS: Record<number, string> = {
  0: "root", 1: "daemon", 2: "bin", 3: "sys", 4: "adm", 5: "tty", 6: "disk", 27: "sudo", 33: "www-data", 34: "backup",
  42: "shadow", 50: "staff", 103: "ssl-cert", 100: "users", 989: "docker", 999: "systemd-journal", 1000: "demo", 65534: "nogroup",
};
export const ME = { uid: 1000, gid: 1000, groups: new Set([1000, 4, 27, 100, 989]) };

export const userName = (uid: number) => USERS[uid] ?? String(uid);
export const groupName = (gid: number) => GROUPS[gid] ?? String(gid);

export function lookupUser(s: string): number | undefined {
  if (/^\d+$/.test(s)) return Number(s);
  const e = Object.entries(USERS).find(([, n]) => n === s);
  return e ? Number(e[0]) : undefined;
}
export function lookupGroup(s: string): number | undefined {
  if (/^\d+$/.test(s)) return Number(s);
  const e = Object.entries(GROUPS).find(([, n]) => n === s);
  return e ? Number(e[0]) : undefined;
}

/** access(2) from the mode bits, as the demo user (or root when elevated). */
export function access(n: Node, asRoot = false): { r: boolean; w: boolean; x: boolean } {
  if (asRoot) return { r: true, w: true, x: true };
  const m = n.mode;
  const sh = n.uid === ME.uid ? 6 : ME.groups.has(n.gid) ? 3 : 0;
  return { r: ((m >> sh) & 4) !== 0, w: ((m >> sh) & 2) !== 0, x: ((m >> sh) & 1) !== 0 };
}

// ---- mounts -----------------------------------------------------------------

export interface Mount {
  id: string;
  label: string;
  path: string;
  device: string;
  fstype: string;
  total: number;
  used: number;
  free: number;
}

/** Same numbers as the System page's filesystems. */
export const MOUNTS: Mount[] = [
  { id: "m-root", label: "System", path: "/", device: "/dev/nvme0n1p2", fstype: "ext4", total: 915 * GB, used: 182 * GB, free: 686 * GB },
  { id: "m-data", label: "DATA", path: "/DATA", device: "/dev/sda1", fstype: "ext4", total: 7.2 * TB, used: 4.9 * TB, free: 2.3 * TB },
  { id: "m-backup", label: "Backup", path: "/mnt/backup", device: "/dev/sdb1", fstype: "btrfs", total: 7.2 * TB, used: 3.1 * TB, free: 4.1 * TB },
];
/** Not offered as a drive, but its own filesystem. */
const EFI = { path: "/boot/efi" };

export function mountOf(abs: string): string {
  let best = "/";
  for (const p of [...MOUNTS.map((m) => m.path), EFI.path]) {
    if (p !== "/" && (abs === p || abs.startsWith(p + "/")) && p.length > best.length) best = p;
  }
  return best;
}

// ---- roots ------------------------------------------------------------------

export interface RootRef {
  id: string;
  name: string;
  path: string;
  kind: "place" | "drive";
}

export const PLACES: RootRef[] = [
  { id: "0", name: "Home", path: HOME, kind: "place" },
  { id: "1", name: "DATA", path: "/DATA", kind: "place" },
];
export const DRIVES: RootRef[] = MOUNTS.map((m) => ({ id: m.id, name: m.label, path: m.path, kind: "drive" }));

// ---- state ------------------------------------------------------------------

interface State {
  nodes: Tree;
  /** Bytes per mount when the demo started (usage moves with changes). */
  base: Record<string, number>;
}

function sums(nodes: Tree) {
  const out: Record<string, number> = {};
  for (const [p, n] of Object.entries(nodes)) {
    if (n.t !== "f") continue;
    const m = mountOf(p);
    out[m] = (out[m] ?? 0) + (n.size ?? 0);
  }
  return out;
}

const store = persisted<State>(
  "files.fs",
  () => {
    const nodes = seedTree();
    return { nodes, base: sums(nodes) };
  },
  2,
);

export const tree = () => store.get().nodes;
/** Saves after a change. */
export const save = () => store.update(() => {});

export function usage(m: Mount) {
  const delta = (sums(tree())[m.path] ?? 0) - (store.get().base[m.path] ?? 0);
  const used = Math.max(0, m.used + delta);
  return { total: m.total, used, free: Math.max(0, m.free - delta) };
}

/** Uploaded binaries too big for localStorage live here for the session. */
export const memBlobs = new Map<string, Uint8Array>();

// ---- paths ------------------------------------------------------------------

/** Like fsops.Clean: root-relative, no leading slash, "." for the root. */
export function clean(p: string | null | undefined): string {
  const parts: string[] = [];
  for (const s of (p ?? "").replace(/\\/g, "/").split("/")) {
    if (!s || s === ".") continue;
    if (s === "..") parts.pop();
    else parts.push(s);
  }
  return parts.length ? parts.join("/") : ".";
}

export const joinAbs = (dir: string, name: string) => (dir === "/" ? `/${name}` : `${dir}/${name}`);
export const absOf = (rootPath: string, rel: string) => (rel === "." ? rootPath : joinAbs(rootPath, rel));
export const parentAbs = (abs: string) => {
  const i = abs.lastIndexOf("/");
  return i <= 0 ? "/" : abs.slice(0, i);
};
export const baseAbs = (abs: string) => abs.slice(abs.lastIndexOf("/") + 1);
export const insideAbs = (p: string, dir: string) => dir === "/" || p === dir || p.startsWith(dir + "/");
export function relOf(rootPath: string, abs: string): string {
  if (abs === rootPath) return ".";
  return rootPath === "/" ? abs.slice(1) : abs.slice(rootPath.length + 1);
}

export const parentRel = (p: string) => {
  const i = p.lastIndexOf("/");
  return i < 0 ? "." : p.slice(0, i);
};
export const baseRel = (p: string) => p.slice(p.lastIndexOf("/") + 1);
export const joinRel = (dir: string, name: string) => clean(dir === "." ? name : `${dir}/${name}`);

/** Direct children of a folder (names). */
export function childrenOf(abs: string): string[] {
  const pre = abs === "/" ? "/" : abs + "/";
  const out: string[] = [];
  for (const k of Object.keys(tree())) {
    if (k.length > pre.length && k.startsWith(pre) && !k.slice(pre.length).includes("/")) out.push(k.slice(pre.length));
  }
  return out;
}

/** The path and everything below it. */
export function subtree(abs: string): string[] {
  const pre = abs === "/" ? "/" : abs + "/";
  return Object.keys(tree()).filter((k) => k === abs || k.startsWith(pre));
}

// ---- errors -----------------------------------------------------------------

export class FsError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public data?: unknown,
  ) {
    super(message);
  }
}

const msg = (what: string, rel: string) => (rel === "" || rel === "." ? what : `${what}: ${rel}`);
export const errNotFound = (rel: string) => new FsError(404, "not_found", msg("No such file or folder", rel));
export const errExists = (rel: string) => new FsError(409, "exists", msg("Already exists", rel));
export const errPerm = (rel: string) => new FsError(403, "permission_denied", msg("Permission denied", rel));
export const errOutside = (rel: string) => new FsError(403, "outside_root", msg("Path leads outside the root", rel));
export const errNotDir = (rel: string) => new FsError(400, "not_dir", msg("Not a folder", rel));
export const errBad = (m: string) => new FsError(400, "bad_request", m);

// ---- resolution -------------------------------------------------------------

export interface Resolved {
  /** Real absolute path (symlinks followed). */
  abs: string;
  node?: Node;
}

/**
 * Resolves rel inside rootPath, following symlinks (the last one only when
 * followLast) and never leaving the root. A missing last component gives
 * node undefined; a missing folder on the way throws not_found.
 */
export function resolve(rootPath: string, rel: string, followLast = true, asRoot = false, depth = 0): Resolved {
  if (depth > 40) throw new FsError(400, "fs_error", msg("too many levels of symbolic links", rel));
  const nodes = tree();
  const parts = rel === "." ? [] : rel.split("/");
  let cur = rootPath;
  for (let i = 0; i < parts.length; i++) {
    const dirNode = nodes[cur];
    if (!dirNode) throw errNotFound(rel);
    if (dirNode.t !== "d") throw errNotDir(rel);
    if (!access(dirNode, asRoot).x) throw errPerm(rel);
    const next = joinAbs(cur, parts[i]);
    const n = nodes[next];
    const last = i === parts.length - 1;
    if (!n) {
      if (last) return { abs: next };
      throw errNotFound(rel);
    }
    if (n.t === "l" && (!last || followLast)) {
      const t = n.target ?? "";
      const targetAbs = t.startsWith("/") ? clean(t) : clean(relOf("/", cur) === "." ? t : `${relOf("/", cur)}/${t}`);
      const ta = targetAbs === "." ? "/" : "/" + targetAbs;
      if (!insideAbs(ta, rootPath)) throw errOutside(rel);
      const rest = parts.slice(i + 1).join("/");
      const nr = relOf(rootPath, ta);
      return resolve(rootPath, rest ? joinRel(nr, rest) : nr, followLast, asRoot, depth + 1);
    }
    cur = next;
  }
  return { abs: cur, node: nodes[cur] };
}

/** Lstat: the node itself (symlinks not followed), parent folders resolved. */
export function lstat(rootPath: string, rel: string, asRoot = false): Resolved {
  if (rel === ".") return { abs: rootPath, node: tree()[rootPath] };
  const parent = resolve(rootPath, parentRel(rel), true, asRoot);
  if (!parent.node) throw errNotFound(rel);
  if (parent.node.t !== "d") throw errNotDir(rel);
  if (!access(parent.node, asRoot).x) throw errPerm(rel);
  const abs = joinAbs(parent.abs, baseRel(rel));
  return { abs, node: tree()[abs] };
}

/** Like lstat but throws not_found when missing. */
export function mustLstat(rootPath: string, rel: string, asRoot = false) {
  const r = lstat(rootPath, rel, asRoot);
  if (!r.node) throw errNotFound(rel);
  return r as Required<Resolved>;
}

export function mustStat(rootPath: string, rel: string, asRoot = false) {
  const r = resolve(rootPath, rel, true, asRoot);
  if (!r.node) throw errNotFound(rel);
  return r as Required<Resolved>;
}

// ---- mime & modes -----------------------------------------------------------

const MIME: Record<string, string> = {
  md: "text/markdown", go: "text/x-go", py: "text/x-python", ts: "text/x-typescript", toml: "text/x-toml", yaml: "text/yaml",
  yml: "text/yaml", sh: "text/x-shellscript", conf: "text/plain", ini: "text/plain", cfg: "text/plain", log: "text/plain",
  env: "text/plain", txt: "text/plain", csv: "text/csv", json: "application/json", service: "text/plain", timer: "text/plain",
  mkv: "video/x-matroska", webm: "video/webm", mp4: "video/mp4", m4v: "video/mp4", mov: "video/quicktime", mp3: "audio/mpeg",
  flac: "audio/flac", ogg: "audio/ogg", opus: "audio/ogg", wav: "audio/wav", m4a: "audio/mp4", webp: "image/webp",
  avif: "image/avif", heic: "image/heic", svg: "image/svg+xml", pdf: "application/pdf", zip: "application/zip",
  gz: "application/gzip", tgz: "application/gzip", tar: "application/x-tar", "7z": "application/x-7z-compressed",
  rar: "application/vnd.rar", xz: "application/x-xz", zst: "application/zstd", iso: "application/x-iso9660-image",
  torrent: "application/x-bittorrent", png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif",
  ico: "image/x-icon", bmp: "image/bmp", html: "text/html", css: "text/css", js: "text/javascript", xml: "text/xml",
  srt: "text/plain", vtt: "text/vtt", nfo: "text/plain", deb: "application/vnd.debian.binary-package", ttf: "font/ttf",
  otf: "font/otf", woff2: "font/woff2", gpg: "application/pgp-encrypted", db: "application/vnd.sqlite3",
};

export function mimeOf(name: string): string {
  const i = name.lastIndexOf(".");
  if (i < 0) return "";
  return MIME[name.slice(i + 1).toLowerCase()] ?? "";
}

export function lsMode(n: Node): string {
  const b = ["-", "-", "-", "-", "-", "-", "-", "-", "-", "-"];
  b[0] = n.t === "d" ? "d" : n.t === "l" ? "l" : "-";
  const rwx = "rwxrwxrwx";
  for (let i = 0; i < 9; i++) if (n.mode & (1 << (8 - i))) b[i + 1] = rwx[i];
  const special = (i: number, set: boolean, lower: string, upper: string) => {
    if (set) b[i] = b[i] === "x" ? lower : upper;
  };
  special(3, (n.mode & 0o4000) !== 0, "s", "S");
  special(6, (n.mode & 0o2000) !== 0, "s", "S");
  special(9, (n.mode & 0o1000) !== 0, "t", "T");
  return b.join("");
}

export const octal = (mode: number) => (mode & 0o7777).toString(8).padStart(4, "0");

/** A file whose bytes the demo can produce (text, generated or uploaded). */
export const hasBytes = (n: Node) => n.t === "f" && (n.text !== undefined || !!n.gen);

export { GB, MB, TB };

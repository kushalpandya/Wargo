// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, qs } from "@/lib/api";

/** Root ids: "0", "1"… for configured places, "m-<base64url>" for drives. */
export type RootId = string;

export interface Root {
  id: RootId;
  kind: "place" | "drive";
  name: string;
  path: string;
  total?: number;
  free?: number;
  used?: number;
  error?: string;
  readOnly?: boolean;
  /** Drives only. */
  device?: string;
  fstype?: string;
  removable?: boolean;
  network?: boolean;
  /** Drives the user hid from the sidebar (still valid roots). */
  hidden?: boolean;
}

/** A bookmarked folder: root + path inside it. */
export interface Bookmark {
  id: string;
  name: string;
  root: RootId;
  path: string;
  /** Absolute host path (used to match the current folder). */
  abs: string;
  /** Kind of the root it lives in. */
  kind: "place" | "drive";
  /** Points at the admin's home folder. */
  home?: boolean;
  available: boolean;
  /** Why it's unavailable ("Drive not mounted"). */
  reason?: string;
}

export interface Locations {
  /** Every root: configured places, then detected drives (with hidden flags). */
  roots: Root[];
  bookmarks: Bookmark[];
  hiddenDrives: number;
  maxBookmarks: number;
  home?: string;
}

export type EntryType = "file" | "dir" | "symlink" | "other";

export interface Entry {
  name: string;
  type: EntryType;
  size: number;
  mode: string;
  octal: string;
  mtime: string;
  owner: string;
  group: string;
  uid: number;
  gid: number;
  target?: string;
  targetType?: "file" | "dir" | "other" | "broken" | "outside";
  mime?: string;
  readable: boolean;
  writable: boolean;
}

export interface Listing {
  path: string;
  parent: string | null;
  entries: Entry[];
  truncated: boolean;
}

export interface FileContent {
  content: string;
  mtime: string;
  size: number;
  encoding: "utf-8" | "latin1";
  writable: boolean;
  mode: string;
}

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
}

// ---- paths -----------------------------------------------------------------

/** Root-relative path helpers; "." is the root. */
export function joinPath(dir: string, name: string): string {
  if (!dir || dir === ".") return name;
  return `${dir}/${name}`;
}

export function parentPath(p: string): string {
  const i = p.lastIndexOf("/");
  return i < 0 ? "." : p.slice(0, i);
}

export function baseName(p: string): string {
  const i = p.lastIndexOf("/");
  return i < 0 ? p : p.slice(i + 1);
}

export function segments(p: string): string[] {
  return !p || p === "." ? [] : p.split("/").filter(Boolean);
}

export function absPath(root: Root | undefined, p: string): string {
  if (!root) return p;
  if (!p || p === ".") return root.path;
  return `${root.path.replace(/\/$/, "")}/${p}`;
}

/** Route for a folder: /files/<root>/<encoded segments>. */
export function routeFor(root: RootId, p: string): string {
  const segs = segments(p).map(encodeURIComponent);
  return `/files/${encodeURIComponent(root)}${segs.length ? "/" + segs.join("/") : ""}`;
}

const decode = (s: string) => {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
};

/** Parses /files/<root>/<path...> (each segment URI-encoded). */
export function parseRoute(pathname: string): { root: RootId | null; path: string } {
  const rest = pathname.replace(/^\/files\/?/, "");
  const parts = rest.split("/").filter(Boolean);
  if (parts.length === 0) return { root: null, path: "." };
  const segs = parts.slice(1).map(decode).filter((s) => s !== "." && s !== "..");
  return { root: decode(parts[0]), path: segs.length ? segs.join("/") : "." };
}

/** A pane's location. */
export interface Loc {
  root: RootId;
  path: string;
}

/** Encodes an extra pane for the ?pane= query parameter: "<root>:<path>". */
export function encodePane(l: Loc): string {
  return `${l.root}:${l.path === "." ? "" : l.path}`;
}

export function decodePane(v: string): Loc | null {
  const i = v.indexOf(":");
  if (i <= 0) return null;
  const segs = segments(v.slice(i + 1)).filter((s) => s !== "." && s !== "..");
  return { root: v.slice(0, i), path: segs.length ? segs.join("/") : "." };
}

export function downloadUrl(root: RootId, p: string, inline = false): string {
  return `/api/files/download${qs({ root, path: p, inline: inline ? 1 : undefined })}`;
}

export function zipUrl(root: RootId, paths: string[], name?: string): string {
  const q = new URLSearchParams({ root: String(root) });
  paths.forEach((p) => q.append("path", p));
  if (name) q.set("name", name);
  return `/api/files/download-zip?${q}`;
}

/** Triggers a native browser download (streams; nothing buffered in JS). */
export function startDownload(url: string) {
  const a = document.createElement("a");
  a.href = url;
  a.rel = "noopener";
  a.download = "";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// ---- queries ---------------------------------------------------------------

export const locationsKey = ["files", "locations"] as const;

/** Roots, drives and bookmarks in one call (shared by every pane). */
export function useLocations() {
  return useQuery({
    queryKey: locationsKey,
    queryFn: () => api.get<Locations>("/api/files/locations"),
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
}

export function listKey(root: RootId | null, p: string, hidden: boolean) {
  return ["files", "list", root, p, hidden] as const;
}

export function useListing(root: RootId | null, p: string, hidden: boolean) {
  return useQuery({
    queryKey: listKey(root, p, hidden),
    queryFn: ({ signal }) => api.get<Listing>(`/api/files/list${qs({ root: root ?? "", path: p, hidden: hidden ? 1 : undefined })}`, signal),
    enabled: root !== null,
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[2] === root ? prev : undefined),
    retry: false,
  });
}

/** Invalidates listings (and root usage) after a mutation. */
export function useRefreshFiles() {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: ["files", "list"] });
    qc.invalidateQueries({ queryKey: locationsKey });
  };
}

export const bookmarkApi = {
  add: (root: RootId, path: string, name?: string) => api.post<Bookmark>("/api/files/bookmarks", { root, path, name }),
  rename: (id: string, name: string) => api.put<Bookmark>(`/api/files/bookmarks/${encodeURIComponent(id)}`, { name }),
  remove: (id: string) => api.del(`/api/files/bookmarks/${encodeURIComponent(id)}`),
  reorder: (ids: string[]) => api.post<Bookmark[]>("/api/files/bookmarks/reorder", { ids }),
  setDriveHidden: (id: RootId, hidden: boolean) => api.put(`/api/files/drives/${encodeURIComponent(id)}/hidden`, { hidden }),
};

export const fileApi = {
  mkdir: (root: RootId, path: string) => api.post<{ path: string }>("/api/files/mkdir", { root, path }),
  rename: (root: RootId, path: string, name: string) => api.post<{ path: string }>("/api/files/rename", { root, path, name }),
  move: (root: RootId, paths: string[], dest: string, onConflict?: string, destRoot?: RootId) =>
    api.post<{ moved: number; jobId?: string }>("/api/files/move", { root, destRoot: destRoot === root ? undefined : destRoot, paths, dest, onConflict }),
  copy: (root: RootId, paths: string[], dest: string, onConflict?: string, destRoot?: RootId) =>
    api.post<{ copied: number; jobId?: string }>("/api/files/copy", { root, destRoot: destRoot === root ? undefined : destRoot, paths, dest, onConflict }),
  remove: (root: RootId, paths: string[], trash: boolean) => api.post<{ deleted: number }>("/api/files/delete", { root, paths, trash }),
  chmod: (root: RootId, paths: string[], mode: string, recursive: boolean, dirMode?: string) => api.post("/api/files/chmod", { root, paths, mode, recursive, dirMode }),
  chown: (root: RootId, paths: string[], owner: string, recursive: boolean) => api.post("/api/files/chown", { root, paths, owner, recursive }),
  archive: (root: RootId, paths: string[], dest: string, format: string) => api.post<{ jobId: string; path: string }>("/api/files/archive", { root, paths, dest, format }),
  extract: (root: RootId, path: string, dest: string) => api.post<{ jobId: string; dest: string }>("/api/files/extract", { root, path, dest }),
  size: (root: RootId, path: string) => api.get<{ size: number; files: number; partial: boolean }>(`/api/files/size${qs({ root, path })}`),
  content: (root: RootId, path: string) => api.get<FileContent>(`/api/files/content${qs({ root, path })}`),
  save: (root: RootId, path: string, content: string, mtime: string | null, encoding?: string, create?: boolean) =>
    api.put<{ mtime: string; size: number }>("/api/files/content", { root, path, content, mtime, encoding, create }),
};

const MAC_LINKED = /^\/(etc|tmp|var)(\/|$)/;

/** True when the server host is macOS (from the cached system info, no new endpoint). */
export function useIsMac(): boolean {
  const info = useQuery({ queryKey: ["system", "info"], queryFn: () => api.get<{ osId: string }>("/api/system/info"), staleTime: Infinity });
  return info.data?.osId === "macos";
}

/**
 * On macOS /etc, /tmp and /var are links into /private, which file actions
 * refuse to follow. Returns a (stable) function that adds a short pointer to
 * such errors, using the absolute path the server attaches as `abs`.
 */
export function useMacHint(): (err: unknown) => unknown {
  const isMac = useIsMac();
  return useCallback(
    (err) => {
      if (!isMac || !(err instanceof ApiError) || err.code !== "symlink_in_path") return err;
      const abs = (err.data as { abs?: string } | undefined)?.abs;
      if (!abs || !MAC_LINKED.test(abs)) return err;
      const top = abs.split("/")[1];
      return new ApiError(err.status, err.code, `${err.message}. On macOS, /etc, /tmp and /var are links into /private. Open /private/${top} instead.`, err.data);
    },
    [isMac],
  );
}

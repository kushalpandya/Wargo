// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, qs } from "@/lib/api";

export interface Root {
  id: number;
  name: string;
  path: string;
  total?: number;
  free?: number;
  error?: string;
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
export function routeFor(root: number, p: string): string {
  const segs = segments(p).map(encodeURIComponent);
  return `/files/${root}${segs.length ? "/" + segs.join("/") : ""}`;
}

/** Parses /files/<root>/<path...> (each segment URI-encoded). */
export function parseRoute(pathname: string): { root: number | null; path: string } {
  const rest = pathname.replace(/^\/files\/?/, "");
  const parts = rest.split("/").filter(Boolean);
  if (parts.length === 0) return { root: null, path: "." };
  const root = Number(parts[0]);
  const segs = parts.slice(1).map((s) => {
    try {
      return decodeURIComponent(s);
    } catch {
      return s;
    }
  });
  return { root: Number.isFinite(root) ? root : null, path: segs.length ? segs.join("/") : "." };
}

export function downloadUrl(root: number, p: string, inline = false): string {
  return `/api/files/download${qs({ root, path: p, inline: inline ? 1 : undefined })}`;
}

export function zipUrl(root: number, paths: string[], name?: string): string {
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

export function useRoots() {
  return useQuery({
    queryKey: ["files", "roots"],
    queryFn: () => api.get<Root[]>("/api/files/roots"),
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
}

export function listKey(root: number | null, p: string, hidden: boolean) {
  return ["files", "list", root, p, hidden] as const;
}

export function useListing(root: number | null, p: string, hidden: boolean) {
  return useQuery({
    queryKey: listKey(root, p, hidden),
    queryFn: ({ signal }) => api.get<Listing>(`/api/files/list${qs({ root: root ?? 0, path: p, hidden: hidden ? 1 : undefined })}`, signal),
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
    qc.invalidateQueries({ queryKey: ["files", "roots"] });
  };
}

export const fileApi = {
  mkdir: (root: number, path: string) => api.post<{ path: string }>("/api/files/mkdir", { root: String(root), path }),
  rename: (root: number, path: string, name: string) => api.post<{ path: string }>("/api/files/rename", { root: String(root), path, name }),
  move: (root: number, paths: string[], dest: string, onConflict?: string) =>
    api.post<{ moved: number; jobId?: string }>("/api/files/move", { root: String(root), paths, dest, onConflict }),
  copy: (root: number, paths: string[], dest: string, onConflict?: string) =>
    api.post<{ copied: number; jobId?: string }>("/api/files/copy", { root: String(root), paths, dest, onConflict }),
  remove: (root: number, paths: string[], trash: boolean) => api.post<{ deleted: number }>("/api/files/delete", { root: String(root), paths, trash }),
  chmod: (root: number, paths: string[], mode: string, recursive: boolean, dirMode?: string) =>
    api.post("/api/files/chmod", { root: String(root), paths, mode, recursive, dirMode }),
  chown: (root: number, paths: string[], owner: string, recursive: boolean) => api.post("/api/files/chown", { root: String(root), paths, owner, recursive }),
  archive: (root: number, paths: string[], dest: string, format: string) =>
    api.post<{ jobId: string; path: string }>("/api/files/archive", { root: String(root), paths, dest, format }),
  extract: (root: number, path: string, dest: string) => api.post<{ jobId: string; dest: string }>("/api/files/extract", { root: String(root), path, dest }),
  size: (root: number, path: string) => api.get<{ size: number; files: number; partial: boolean }>(`/api/files/size${qs({ root, path })}`),
  content: (root: number, path: string) => api.get<FileContent>(`/api/files/content${qs({ root, path })}`),
  save: (root: number, path: string, content: string, mtime: string | null, encoding?: string, create?: boolean) =>
    api.put<{ mtime: string; size: number }>("/api/files/content", { root: String(root), path, content, mtime, encoding, create }),
};

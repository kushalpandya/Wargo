// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent, type MouseEvent } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import { ContextMenu as CM } from "radix-ui";
import {
  Archive,
  ArrowUpFromLine,
  ChevronDown,
  ChevronRight,
  ClipboardCopy,
  Copy,
  CopyPlus,
  CornerLeftUp,
  Download,
  Eye,
  EyeOff,
  FilePlus2,
  FolderInput,
  FolderOpen,
  FolderPlus,
  FolderUp,
  HardDrive,
  Info,
  LayoutGrid,
  List,
  MoreHorizontal,
  PackageOpen,
  Pencil,
  RefreshCw,
  ShieldCheck,
  SquareCheck,
  Trash2,
  Upload,
  UserCog,
  X,
} from "lucide-react";
import { ApiError } from "@/lib/api";
import { bytes, level } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useCapabilities } from "@/app/session";
import { Card } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { SearchInput } from "@/components/ui/form";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "@/components/ui/menu";
import { EmptyState, ErrorNote, Meter, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import {
  absPath,
  baseName,
  downloadUrl,
  fileApi,
  joinPath,
  parentPath,
  parseRoute,
  routeFor,
  segments,
  startDownload,
  useListing,
  useRefreshFiles,
  useRoots,
  zipUrl,
  type Entry,
  type JobSnapshot,
  type Root,
} from "./api";
import { isExtractable, kindOf } from "./kinds";
import { ActionMenu, ActionSheet, ContextMenuContent, type ActionGroups } from "./actions";
import { Listing, type Sort, type SortKey } from "./Listing";
import { ChmodDialog, ChownDialog, CompressDialog, ConflictDialog, DeleteDialog, ExtractDialog, FolderPicker, NameDialog, PropertiesSheet } from "./dialogs";
import { Preview, canPreview } from "./Preview";
import { TransfersPanel, useFileJobs } from "./Transfers";
import { enqueue, fromDrop, fromInput, onUploadDone } from "./uploads";

function useMedia(q: string) {
  const [m, setM] = useState(() => window.matchMedia(q).matches);
  useEffect(() => {
    const mq = window.matchMedia(q);
    const h = () => setM(mq.matches);
    mq.addEventListener("change", h);
    return () => mq.removeEventListener("change", h);
  }, [q]);
  return m;
}

function usePersisted<T extends string | boolean>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    const raw = localStorage.getItem(key);
    if (raw === null) return initial;
    return (typeof initial === "boolean" ? raw === "1" : raw) as T;
  });
  const set = (x: T) => {
    setV(x);
    localStorage.setItem(key, typeof x === "boolean" ? (x ? "1" : "0") : String(x));
  };
  return [v, set];
}

const sortFns: Record<SortKey, (a: Entry, b: Entry) => number> = {
  name: (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }),
  size: (a, b) => a.size - b.size,
  mtime: (a, b) => a.mtime.localeCompare(b.mtime),
  type: (a, b) => kindOf(a).localeCompare(kindOf(b)) || a.name.localeCompare(b.name),
  owner: (a, b) => a.owner.localeCompare(b.owner),
  mode: (a, b) => a.octal.localeCompare(b.octal),
};

const isDirEntry = (e: Entry) => e.type === "dir" || e.targetType === "dir";

type DialogState =
  | { kind: "mkdir" | "newfile" }
  | { kind: "rename"; entry: Entry }
  | { kind: "delete"; paths: string[] }
  | { kind: "chmod" | "chown" | "compress"; entries: Entry[] }
  | { kind: "extract"; entry: Entry }
  | { kind: "copy" | "move"; paths: string[] }
  | { kind: "props"; entry: Entry }
  | null;

// ---- roots -------------------------------------------------------------------

function RootUsage({ r }: { r: Root }) {
  if (!r.total) return null;
  const used = r.total - (r.free ?? 0);
  const p = (used / r.total) * 100;
  return (
    <>
      <Meter value={p} tone={level(p, 80, 92)} className="mt-1.5 h-1" label={`${r.name} usage`} />
      <div className="num mt-1 text-2xs text-muted">
        {bytes(r.free)} free of {bytes(r.total)}
      </div>
    </>
  );
}

function RootsSidebar({ roots, current, onPick }: { roots: Root[]; current: number; onPick: (id: number) => void }) {
  return (
    <aside className="hidden w-56 shrink-0 lg:block">
      <Card>
        <div className="border-b border-border px-3.5 py-2 text-2xs font-semibold uppercase tracking-wider text-subtle">Locations</div>
        <nav aria-label="File roots" className="p-1.5">
          {roots.map((r) => (
            <button
              key={r.id}
              type="button"
              onClick={() => onPick(r.id)}
              aria-current={r.id === current ? "page" : undefined}
              className={cn("relative block w-full rounded-md px-2.5 py-2 text-left transition-colors", r.id === current ? "bg-surface-3" : "hover:bg-surface-2")}
            >
              {r.id === current && <span className="absolute inset-y-2 left-0 w-0.5 rounded-full bg-accent" aria-hidden="true" />}
              <div className="flex items-center gap-2">
                <HardDrive className={cn("size-4 shrink-0", r.id === current ? "text-accent" : "text-subtle")} />
                <span className="truncate text-[0.8125rem] font-medium">{r.name}</span>
              </div>
              <div className="num mt-0.5 truncate pl-6 text-2xs text-subtle" title={r.path}>
                {r.path}
              </div>
              <div className="pl-6">
                <RootUsage r={r} />
              </div>
            </button>
          ))}
        </nav>
      </Card>
    </aside>
  );
}

// ---- breadcrumbs -------------------------------------------------------------

function Breadcrumbs({ roots, root, path, onNav, onRoot }: { roots: Root[]; root?: Root; path: string; onNav: (p: string) => void; onRoot: (id: number) => void }) {
  const segs = segments(path);
  const ref = useRef<HTMLOListElement>(null);
  useEffect(() => {
    ref.current?.scrollTo({ left: ref.current.scrollWidth });
  }, [path]);
  const rootBtn = (
    <button type="button" onClick={() => onNav(".")} className="flex h-8 shrink-0 items-center gap-1.5 rounded-md px-2 font-medium hover:bg-surface-3">
      <HardDrive className="size-4 text-accent" />
      {root?.name ?? "…"}
    </button>
  );
  return (
    <nav aria-label="Breadcrumb" className="min-w-0 flex-1">
      <ol ref={ref} className="flex min-w-0 items-center overflow-x-auto text-[0.8125rem] [scrollbar-width:none]">
        <li className="flex shrink-0 items-center">
          {roots.length > 1 ? (
            <div className="flex items-center">
              {rootBtn}
              <Menu
                align="start"
                trigger={
                  <button type="button" aria-label="Switch location" className="-ml-1 flex h-8 w-6 items-center justify-center rounded-md text-subtle hover:bg-surface-3 hover:text-fg lg:hidden">
                    <ChevronDown className="size-3.5" />
                  </button>
                }
              >
                <MenuLabel>Locations</MenuLabel>
                {roots.map((r) => (
                  <MenuItem key={r.id} icon={<HardDrive />} onSelect={() => onRoot(r.id)} shortcut={r.id === root?.id ? "✓" : r.free !== undefined ? `${bytes(r.free)} free` : undefined}>
                    {r.name}
                  </MenuItem>
                ))}
              </Menu>
            </div>
          ) : (
            rootBtn
          )}
        </li>
        {segs.map((s, i) => {
          const last = i === segs.length - 1;
          return (
            <li key={i} className="flex shrink-0 items-center">
              <ChevronRight className="size-3.5 shrink-0 text-subtle" aria-hidden="true" />
              {last ? (
                <span aria-current="page" className="max-w-[16rem] truncate px-2 font-medium">
                  {s}
                </span>
              ) : (
                <button type="button" onClick={() => onNav(segs.slice(0, i + 1).join("/"))} className="h-8 max-w-[12rem] truncate rounded-md px-2 text-muted hover:bg-surface-3 hover:text-fg">
                  {s}
                </button>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

// ---- page --------------------------------------------------------------------

export default function FilesPage() {
  const loc = useLocation();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();
  const { root: routeRoot, path } = parseRoute(loc.pathname);
  const roots = useRoots();
  const caps = useCapabilities();
  const capRoots = caps.data?.files?.roots;
  const rootList: Root[] = roots.data ?? (capRoots ?? []).map((r, i) => ({ id: i, name: r.name, path: r.path }));
  const root = routeRoot ?? 0;
  const rootInfo = rootList.find((r) => r.id === root);
  const trashAvailable = caps.data?.files?.trash !== false;

  const compact = useMedia("(max-width: 767px)");
  const coarse = useMedia("(pointer: coarse)");
  const [hidden, setHidden] = usePersisted<boolean>("wargo.files.hidden", false);
  const [view, setView] = usePersisted<"list" | "grid">("wargo.files.view", "list");
  const [sort, setSort] = useState<Sort>({ key: "name", dir: "asc" });
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [focus, setFocus] = useState(-1);
  const anchor = useRef(-1);
  const [dialog, setDialog] = useState<DialogState>(null);
  const [sheet, setSheet] = useState<{ title: string; subtitle?: string; groups: ActionGroups } | null>(null);
  const [conflict, setConflict] = useState<{ count: number; names: string[]; resolve: (c: "rename" | "skip" | null) => void } | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const dragDepth = useRef(0);
  const listRef = useRef<HTMLDivElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const refresh = useRefreshFiles();
  const previewName = params.get("view");

  useEffect(() => {
    if (routeRoot === null && rootList.length) nav(routeFor(0, "."), { replace: true });
  }, [routeRoot, rootList.length, nav]);

  const listing = useListing(rootList.length ? root : null, path, hidden);

  // Reset per-folder state.
  useEffect(() => {
    setSelected(new Set());
    setFocus(-1);
    anchor.current = -1;
    setFilter("");
  }, [root, path]);

  useEffect(() => {
    folderInput.current?.setAttribute("webkitdirectory", "");
  }, []);

  useEffect(
    () =>
      onUploadDone(() => {
        refresh();
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  const onJobFinish = useCallback(
    (j: JobSnapshot) => {
      refresh();
      const verb: Record<string, string> = { "files.copy": "Copy", "files.move": "Move", "files.archive": "Archive", "files.extract": "Extraction" };
      if (j.state === "succeeded") {
        const skipped = Number(j.result?.skipped ?? 0);
        toast.ok(`${verb[j.kind] ?? "Job"} finished`, skipped ? `${j.title} · ${skipped} entries skipped` : j.title);
      } else if (j.state === "failed") toast.error(`${verb[j.kind] ?? "Job"} failed`, j.error);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
  const { jobs, dismiss, clear } = useFileJobs(onJobFinish);

  const entries = useMemo(() => {
    const list = listing.data?.entries ?? [];
    const q = filter.trim().toLowerCase();
    const filtered = q ? list.filter((e) => e.name.toLowerCase().includes(q)) : list;
    const fn = sortFns[sort.key];
    const m = sort.dir === "asc" ? 1 : -1;
    return [...filtered].sort((a, b) => {
      const d = Number(isDirEntry(b)) - Number(isDirEntry(a));
      if (d) return d;
      return fn(a, b) * m || sortFns.name(a, b);
    });
  }, [listing.data, filter, sort]);

  const selEntries = useMemo(() => entries.filter((e) => selected.has(e.name)), [entries, selected]);
  const previewables = useMemo(() => entries.filter(canPreview), [entries]);

  const go = (p: string) => nav(routeFor(root, p));
  const pickRoot = (id: number) => nav(routeFor(id, "."));
  const pathOf = (e: Entry) => joinPath(path, e.name);

  const open = (e: Entry) => {
    if (e.targetType === "outside") return toast.error("Link points outside this root", `${e.name} → ${e.target}`);
    if (e.targetType === "broken") return toast.error("Broken link", `${e.name} → ${e.target}`);
    if (isDirEntry(e)) return go(pathOf(e));
    if (kindOf(e) === "archive") {
      if (isExtractable(e.name)) return setDialog({ kind: "extract", entry: e });
      return startDownload(downloadUrl(root, pathOf(e)));
    }
    if (canPreview(e)) {
      const p = new URLSearchParams(params);
      p.set("view", e.name);
      setParams(p);
      return;
    }
    startDownload(downloadUrl(root, pathOf(e)));
  };

  const closePreview = () => {
    const p = new URLSearchParams(params);
    p.delete("view");
    setParams(p, { replace: true });
    window.setTimeout(() => listRef.current?.focus(), 0);
  };

  // ---- mutations with conflict handling ----
  const withConflict = async <R,>(fn: (oc?: string) => Promise<R>): Promise<R | null> => {
    try {
      return await fn();
    } catch (e) {
      if (e instanceof ApiError && e.code === "exists" && (e.data as { conflicts?: string[] })?.conflicts) {
        const names = (e.data as { conflicts: string[] }).conflicts;
        const choice = await new Promise<"rename" | "skip" | null>((resolve) => setConflict({ count: names.length, names, resolve }));
        setConflict(null);
        if (!choice) return null;
        return fn(choice);
      }
      throw e;
    }
  };

  const transfer = async (kind: "copy" | "move", paths: string[], dest: string) => {
    const r = await withConflict<{ jobId?: string }>((oc) => (kind === "copy" ? fileApi.copy(root, paths, dest, oc) : fileApi.move(root, paths, dest, oc)));
    if (!r) return;
    if (r.jobId) toast.info(kind === "copy" ? "Copying in the background" : "Moving in the background");
    else toast.ok(kind === "copy" ? "Copied" : "Moved", `${paths.length} item${paths.length === 1 ? "" : "s"} → ${dest === "." ? rootInfo?.name : dest}`);
    setSelected(new Set());
    refresh();
  };

  const copyPath = (p: string) =>
    navigator.clipboard?.writeText(p).then(
      () => toast.ok("Path copied"),
      () => toast.error("Clipboard unavailable"),
    );

  // ---- actions ----
  const folderGroups = (): ActionGroups => [
    [
      { key: "mkdir", label: "New folder", icon: <FolderPlus />, run: () => setDialog({ kind: "mkdir" }) },
      { key: "newfile", label: "New text file", icon: <FilePlus2 />, run: () => setDialog({ kind: "newfile" }) },
      { key: "upload", label: "Upload files", icon: <Upload />, run: () => fileInput.current?.click() },
      { key: "uploaddir", label: "Upload folder", icon: <FolderUp />, run: () => folderInput.current?.click() },
    ],
    [
      { key: "selall", label: "Select all", icon: <SquareCheck />, shortcut: "Ctrl+A", disabled: !entries.length, run: () => setSelected(new Set(entries.map((e) => e.name))) },
      { key: "dl", label: "Download folder as zip", icon: <Download />, run: () => startDownload(downloadUrl(root, path)) },
      { key: "refresh", label: "Refresh", icon: <RefreshCw />, run: () => refresh() },
      { key: "copypath", label: "Copy path", icon: <ClipboardCopy />, run: () => copyPath(absPath(rootInfo, path)) },
    ],
  ];

  const itemGroups = (sel: Entry[]): ActionGroups => {
    if (!sel.length) return folderGroups();
    const one = sel.length === 1 ? sel[0] : null;
    const paths = sel.map(pathOf);
    return [
      [
        ...(one
          ? [
              {
                key: "open",
                label: isDirEntry(one) ? "Open" : kindOf(one) === "text" || kindOf(one) === "code" ? "Open in editor" : "Open",
                icon: <FolderOpen />,
                shortcut: "Enter",
                disabled: one.targetType === "outside" || one.targetType === "broken",
                run: () => open(one),
              },
            ]
          : []),
        {
          key: "download",
          label: one && !isDirEntry(one) ? "Download" : "Download as zip",
          icon: <Download />,
          run: () => startDownload(one ? downloadUrl(root, pathOf(one)) : zipUrl(root, paths)),
        },
      ],
      [
        ...(one ? [{ key: "rename", label: "Rename", icon: <Pencil />, shortcut: "F2", run: () => setDialog({ kind: "rename", entry: one }) }] : []),
        { key: "copy", label: "Copy to…", icon: <Copy />, run: () => setDialog({ kind: "copy", paths }) },
        { key: "move", label: "Move to…", icon: <FolderInput />, run: () => setDialog({ kind: "move", paths }) },
        { key: "dup", label: "Duplicate", icon: <CopyPlus />, run: () => void transfer("copy", paths, path).catch((e) => toast.error("Duplicate failed", e)) },
      ],
      [
        { key: "compress", label: "Compress…", icon: <Archive />, run: () => setDialog({ kind: "compress", entries: sel }) },
        ...(one && isExtractable(one.name) ? [{ key: "extract", label: "Extract…", icon: <PackageOpen />, run: () => setDialog({ kind: "extract", entry: one }) }] : []),
      ],
      [
        { key: "chmod", label: "Permissions…", icon: <ShieldCheck />, run: () => setDialog({ kind: "chmod", entries: sel }) },
        { key: "chown", label: "Change owner…", icon: <UserCog />, run: () => setDialog({ kind: "chown", entries: sel }) },
        ...(one
          ? [
              { key: "props", label: "Properties", icon: <Info />, run: () => setDialog({ kind: "props", entry: one }) },
              { key: "copypath", label: "Copy path", icon: <ClipboardCopy />, run: () => copyPath(absPath(rootInfo, pathOf(one))) },
            ]
          : []),
      ],
      [{ key: "delete", label: "Delete", icon: <Trash2 />, shortcut: "Del", danger: true, run: () => setDialog({ kind: "delete", paths }) }],
    ];
  };

  // ---- selection ----
  const selectOnly = (i: number) => {
    const e = entries[i];
    if (!e) return;
    setSelected(new Set([e.name]));
    setFocus(i);
    anchor.current = i;
  };
  const toggle = (i: number) => {
    const e = entries[i];
    if (!e) return;
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(e.name)) n.delete(e.name);
      else n.add(e.name);
      return n;
    });
    setFocus(i);
    anchor.current = i;
  };
  const range = (i: number, additive: boolean) => {
    const a = anchor.current >= 0 ? anchor.current : i;
    const [lo, hi] = a < i ? [a, i] : [i, a];
    setSelected((s) => {
      const n = additive ? new Set(s) : new Set<string>();
      for (let k = lo; k <= hi; k++) n.add(entries[k].name);
      return n;
    });
    setFocus(i);
  };
  const onItemClick = (i: number, e: MouseEvent) => {
    listRef.current?.focus({ preventScroll: true });
    if (e.shiftKey) range(i, e.ctrlKey || e.metaKey);
    else if (e.ctrlKey || e.metaKey) toggle(i);
    else selectOnly(i);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>, cols: number) => {
    if (e.target !== e.currentTarget) return;
    const n = entries.length;
    const mod = e.ctrlKey || e.metaKey;
    const move = (to: number) => {
      e.preventDefault();
      const i = Math.max(0, Math.min(n - 1, to));
      if (n === 0) return;
      if (e.shiftKey) range(i, false);
      else if (mod) setFocus(i);
      else selectOnly(i);
    };
    const cur = focus < 0 ? -1 : focus;
    switch (e.key) {
      case "ArrowDown":
        return move(cur < 0 ? 0 : cur + cols);
      case "ArrowUp":
        return move(cur < 0 ? 0 : cur - cols);
      case "ArrowRight":
        if (view === "grid") return move(cur + 1);
        return;
      case "ArrowLeft":
        if (view === "grid") return move(cur - 1);
        return;
      case "Home":
        return move(0);
      case "End":
        return move(n - 1);
      case "PageDown":
        return move(cur + 10 * cols);
      case "PageUp":
        return move(cur - 10 * cols);
      case "Enter":
        if (entries[cur]) {
          e.preventDefault();
          open(entries[cur]);
        }
        return;
      case "Backspace":
        if (path !== ".") {
          e.preventDefault();
          go(parentPath(path));
        }
        return;
      case "Delete":
        if (selEntries.length) {
          e.preventDefault();
          setDialog({ kind: "delete", paths: selEntries.map(pathOf) });
        }
        return;
      case "F2": {
        const t = selEntries.length === 1 ? selEntries[0] : entries[cur];
        if (t) {
          e.preventDefault();
          setDialog({ kind: "rename", entry: t });
        }
        return;
      }
      case " ":
        if (cur >= 0) {
          e.preventDefault();
          toggle(cur);
        }
        return;
      case "Escape":
        if (selected.size) {
          e.preventDefault();
          setSelected(new Set());
        }
        return;
      case "a":
      case "A":
        if (mod) {
          e.preventDefault();
          setSelected(new Set(entries.map((x) => x.name)));
        }
        return;
    }
  };

  const onContext = (i: number | null) => {
    if (i === null) {
      setSelected(new Set());
      return;
    }
    const e = entries[i];
    if (e && !selected.has(e.name)) selectOnly(i);
  };

  const onMore = (i: number, anchorEl: HTMLElement) => {
    const e = entries[i];
    if (!e) return;
    const sel = selected.has(e.name) && selected.size > 1 ? selEntries : [e];
    if (!selected.has(e.name)) selectOnly(i);
    if (compact || coarse) {
      setSheet({ title: sel.length === 1 ? e.name : `${sel.length} items`, subtitle: sel.length === 1 ? `${e.mode} · ${e.owner}` : undefined, groups: itemGroups(sel) });
    } else {
      // Open the row's context menu at the button.
      const r = anchorEl.getBoundingClientRect();
      anchorEl.dispatchEvent(new window.MouseEvent("contextmenu", { bubbles: true, clientX: r.left, clientY: r.bottom }));
    }
  };

  // ---- drag & drop upload ----
  const hasFiles = (e: DragEvent) => [...e.dataTransfer.types].includes("Files");
  const dropHandlers = {
    onDragEnter: (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      dragDepth.current++;
      setDragOver(true);
    },
    onDragOver: (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
    },
    onDragLeave: (e: DragEvent) => {
      if (!hasFiles(e)) return;
      dragDepth.current = Math.max(0, dragDepth.current - 1);
      if (!dragDepth.current) setDragOver(false);
    },
    onDrop: async (e: DragEvent) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      dragDepth.current = 0;
      setDragOver(false);
      const files = await fromDrop(e.dataTransfer);
      if (files.length) enqueue(root, path, files);
    },
  };

  // ---- render ----
  const err = listing.error as ApiError | null;
  const dirs = entries.filter(isDirEntry).length;
  const files = entries.length - dirs;
  const totalSize = entries.reduce((s, e) => s + (isDirEntry(e) ? 0 : e.size), 0);
  const selSize = selEntries.reduce((s, e) => s + (isDirEntry(e) ? 0 : e.size), 0);
  const closeDialog = (o: boolean) => !o && setDialog(null);
  const doneAndRefresh = () => {
    setSelected(new Set());
    refresh();
  };
  const selGroups = itemGroups(selEntries);

  const sortMenu = (
    <>
      <MenuLabel>Sort by</MenuLabel>
      {(
        [
          ["name", "Name"],
          ["size", "Size"],
          ["mtime", "Modified"],
          ["type", "Type"],
        ] as [SortKey, string][]
      ).map(([k, l]) => (
        <MenuItem key={k} onSelect={() => setSort((s) => ({ key: k, dir: s.key === k && s.dir === "asc" ? "desc" : s.key === k ? "asc" : k === "name" || k === "type" ? "asc" : "desc" }))} shortcut={sort.key === k ? (sort.dir === "asc" ? "↑" : "↓") : undefined}>
          {l}
        </MenuItem>
      ))}
    </>
  );

  if (rootList.length === 0 && !roots.isLoading && !caps.isLoading) {
    return (
      <Card>
        <EmptyState icon={<HardDrive />} title="No file roots configured">
          Add <code className="num">[[files.roots]]</code> entries to the Wargo config to browse files.
        </EmptyState>
      </Card>
    );
  }

  return (
    <div className="flex h-[calc(100dvh-5rem)] gap-4 sm:h-[calc(100dvh-5.5rem)]">
      <RootsSidebar roots={rootList} current={root} onPick={pickRoot} />
      <Card className="relative flex min-w-0 flex-1 flex-col overflow-hidden" {...dropHandlers}>
        <input ref={fileInput} type="file" multiple className="hidden" aria-label="Upload files" onChange={(e) => (enqueue(root, path, fromInput(e.target.files)), (e.target.value = ""))} />
        <input ref={folderInput} type="file" multiple className="hidden" aria-label="Upload folder" onChange={(e) => (enqueue(root, path, fromInput(e.target.files)), (e.target.value = ""))} />

        {/* Path bar */}
        <div className="flex h-11 items-center gap-1 border-b border-border px-1.5 sm:px-2">
          <IconButton label="Up one level" size="md" disabled={path === "."} onClick={() => go(parentPath(path))}>
            <CornerLeftUp />
          </IconButton>
          <Breadcrumbs roots={rootList} root={rootInfo} path={path} onNav={go} onRoot={pickRoot} />
          <IconButton label="Refresh" onClick={() => refresh()} className="max-sm:hidden">
            <RefreshCw className={cn(listing.isFetching && "animate-spin")} />
          </IconButton>
        </div>

        {/* Toolbar */}
        <div className="flex min-h-11 flex-wrap items-center gap-1.5 border-b border-border px-2 py-1.5">
          {selected.size > 0 && !compact ? (
            <div className="flex min-w-0 flex-1 items-center gap-1" role="toolbar" aria-label="Selection actions">
              <IconButton label="Clear selection" size="sm" onClick={() => setSelected(new Set())}>
                <X />
              </IconButton>
              <span className="num mr-2 whitespace-nowrap text-[0.8125rem] font-medium" aria-live="polite">
                {selected.size} selected{selSize ? <span className="text-muted"> · {bytes(selSize)}</span> : null}
              </span>
              <Button size="sm" variant="ghost" icon={<Download />} onClick={() => selGroups[0].find((a) => a.key === "download")?.run()}>
                Download
              </Button>
              <Button size="sm" variant="ghost" icon={<Copy />} onClick={() => setDialog({ kind: "copy", paths: selEntries.map(pathOf) })} className="max-lg:hidden">
                Copy to
              </Button>
              <Button size="sm" variant="ghost" icon={<FolderInput />} onClick={() => setDialog({ kind: "move", paths: selEntries.map(pathOf) })} className="max-lg:hidden">
                Move to
              </Button>
              <Button size="sm" variant="ghost" icon={<Trash2 />} className="text-crit hover:text-crit" onClick={() => setDialog({ kind: "delete", paths: selEntries.map(pathOf) })}>
                Delete
              </Button>
              <ActionMenu
                title={selected.size === 1 ? selEntries[0]?.name : `${selected.size} items`}
                groups={selGroups}
                trigger={
                  <IconButton label="More actions" size="sm">
                    <MoreHorizontal />
                  </IconButton>
                }
              />
            </div>
          ) : (
            <div className="flex items-center gap-1">
              <Button size="sm" variant="primary" icon={<Upload />} onClick={() => fileInput.current?.click()} className="max-sm:hidden">
                Upload
              </Button>
              <IconButton label="Upload files" size="sm" variant="primary" onClick={() => fileInput.current?.click()} className="sm:hidden">
                <ArrowUpFromLine />
              </IconButton>
              <Menu
                align="start"
                trigger={
                  <Button size="sm" icon={<FolderPlus />}>
                    <span className="max-sm:sr-only">New</span>
                    <ChevronDown className="text-subtle" />
                  </Button>
                }
              >
                <MenuItem icon={<FolderPlus />} onSelect={() => setDialog({ kind: "mkdir" })}>
                  New folder
                </MenuItem>
                <MenuItem icon={<FilePlus2 />} onSelect={() => setDialog({ kind: "newfile" })}>
                  New text file
                </MenuItem>
                <MenuSeparator />
                <MenuItem icon={<Upload />} onSelect={() => fileInput.current?.click()}>
                  Upload files
                </MenuItem>
                <MenuItem icon={<FolderUp />} onSelect={() => folderInput.current?.click()}>
                  Upload folder
                </MenuItem>
              </Menu>
            </div>
          )}
          <div className={cn("ml-auto flex items-center gap-1", selected.size > 0 && !compact && "max-md:hidden")}>
            <SearchInput value={filter} onChange={setFilter} placeholder="Filter" label="Filter this folder" className="w-28 sm:w-44" />
            <Menu
              trigger={
                <IconButton label="View options" size="sm" className="md:hidden">
                  <MoreHorizontal />
                </IconButton>
              }
            >
              {sortMenu}
              <MenuSeparator />
              <MenuItem icon={view === "list" ? <LayoutGrid /> : <List />} onSelect={() => setView(view === "list" ? "grid" : "list")}>
                {view === "list" ? "Grid view" : "List view"}
              </MenuItem>
              <MenuItem icon={hidden ? <EyeOff /> : <Eye />} onSelect={() => setHidden(!hidden)}>
                {hidden ? "Hide hidden files" : "Show hidden files"}
              </MenuItem>
              <MenuItem icon={<RefreshCw />} onSelect={() => refresh()}>
                Refresh
              </MenuItem>
            </Menu>
            <div className="flex items-center gap-1 max-md:hidden">
              {view === "grid" && (
                <Menu
                  trigger={
                    <Button size="sm" variant="ghost">
                      Sort <ChevronDown className="text-subtle" />
                    </Button>
                  }
                >
                  {sortMenu}
                </Menu>
              )}
              <IconButton label={hidden ? "Hide hidden files" : "Show hidden files"} size="sm" onClick={() => setHidden(!hidden)} aria-pressed={hidden} className={cn(hidden && "text-accent")}>
                {hidden ? <Eye /> : <EyeOff />}
              </IconButton>
              <div role="radiogroup" aria-label="View" className="inline-flex rounded-md border border-border bg-surface-2 p-0.5">
                {(
                  [
                    ["list", <List key="l" />, "List view"],
                    ["grid", <LayoutGrid key="g" />, "Grid view"],
                  ] as const
                ).map(([v, icon, label]) => (
                  <button
                    key={v}
                    type="button"
                    role="radio"
                    aria-checked={view === v}
                    aria-label={label}
                    title={label}
                    onClick={() => setView(v)}
                    className={cn("flex h-6 w-7 items-center justify-center rounded-[5px] [&_svg]:size-3.5", view === v ? "bg-surface text-fg shadow-sm ring-1 ring-border" : "text-muted hover:text-fg")}
                  >
                    {icon}
                  </button>
                ))}
              </div>
            </div>
          </div>
        </div>

        {/* Listing */}
        <CM.Root modal={false}>
          <CM.Trigger asChild disabled={coarse}>
            <div
              className={cn("scroll-area relative min-h-0 flex-1", compact && selected.size > 0 && "pb-28")}
              onContextMenu={(e) => {
                if (!(e.target as HTMLElement).closest("[data-row]")) onContext(null);
              }}
              onClick={(e) => {
                if (e.target === e.currentTarget && !coarse) setSelected(new Set());
              }}
            >
              {listing.isLoading && (
                <div className="space-y-2 p-3">
                  {Array.from({ length: 8 }, (_, i) => (
                    <Skeleton key={i} className="h-7" />
                  ))}
                </div>
              )}
              {err && (
                <div className="p-4">
                  {err.code === "not_found" ? (
                    <EmptyState icon={<FolderOpen />} title="Folder not found" action={<Button onClick={() => go(".")}>Go to {rootInfo?.name}</Button>}>
                      {path} doesn't exist in {rootInfo?.name}.
                    </EmptyState>
                  ) : err.code === "outside_root" ? (
                    <EmptyState icon={<ShieldCheck />} title="Outside this root" action={<Button onClick={() => go(parentPath(path))}>Go back</Button>}>
                      This link points outside {rootInfo?.name} and can't be opened here.
                    </EmptyState>
                  ) : (
                    <ErrorNote error={err} />
                  )}
                </div>
              )}
              {listing.data && entries.length === 0 && (
                <EmptyState
                  icon={filter ? <FolderOpen /> : <Upload />}
                  title={filter ? "No matches" : "This folder is empty"}
                  action={
                    !filter && (
                      <Button icon={<Upload />} onClick={() => fileInput.current?.click()}>
                        Upload files
                      </Button>
                    )
                  }
                >
                  {filter ? `Nothing here matches “${filter}”.` : "Drop files here or use Upload."}
                </EmptyState>
              )}
              {listing.data && entries.length > 0 && (
                <Listing
                  ref={listRef}
                  entries={entries}
                  view={view}
                  root={root}
                  dir={path}
                  selected={selected}
                  focus={focus}
                  sort={sort}
                  onSort={(k) => setSort((s) => (s.key === k ? { key: k, dir: s.dir === "asc" ? "desc" : "asc" } : { key: k, dir: k === "name" || k === "owner" || k === "mode" ? "asc" : "desc" }))}
                  coarse={coarse}
                  compact={compact && view === "list"}
                  onItemClick={onItemClick}
                  onItemOpen={(i) => entries[i] && open(entries[i])}
                  onToggle={toggle}
                  onLongPress={toggle}
                  onMore={onMore}
                  onContext={onContext}
                  onKeyDown={onKeyDown}
                  onSelectAll={(on) => setSelected(on ? new Set(entries.map((e) => e.name)) : new Set())}
                  label={`Files in ${baseName(path) === "." ? rootInfo?.name : baseName(path)}`}
                />
              )}
              {listing.data?.truncated && <div className="px-3 py-2 text-center text-xs text-warn">Only the first 20,000 entries are shown.</div>}
            </div>
          </CM.Trigger>
          <ContextMenuContent title={selEntries.length === 1 ? selEntries[0].name : selEntries.length ? `${selEntries.length} items` : undefined} groups={selGroups} />
        </CM.Root>

        {/* Status bar */}
        <div className="num flex h-8 shrink-0 items-center gap-3 border-t border-border px-3 text-2xs text-muted">
          <span className="truncate">
            {dirs} folder{dirs === 1 ? "" : "s"} · {files} file{files === 1 ? "" : "s"}
            {totalSize ? ` · ${bytes(totalSize)}` : ""}
            {filter && listing.data ? ` · filtered from ${listing.data.entries.length}` : ""}
          </span>
          {rootInfo?.free !== undefined && <span className="ml-auto shrink-0 lg:hidden">{bytes(rootInfo.free)} free</span>}
          <span className="ml-auto hidden shrink-0 lg:inline">
            {!coarse && (
              <>
                <kbd className="font-sans">Enter</kbd> open · <kbd className="font-sans">F2</kbd> rename · <kbd className="font-sans">Del</kbd> delete
              </>
            )}
          </span>
        </div>

        {dragOver && (
          <div className="pointer-events-none absolute inset-0 z-20 flex items-center justify-center border-2 border-dashed border-accent bg-accent-soft/80">
            <div className="flex flex-col items-center gap-2 text-center">
              <Upload className="size-8 text-accent" />
              <p className="font-medium">Drop to upload</p>
              <p className="num text-xs text-muted">{absPath(rootInfo, path)}</p>
            </div>
          </div>
        )}
      </Card>

      {/* Mobile selection bar */}
      {compact && selected.size > 0 && (
        <div role="toolbar" aria-label="Selection actions" className="fixed inset-x-0 bottom-0 z-40 border-t border-border-strong bg-surface pb-[env(safe-area-inset-bottom)]">
          <div className="flex items-center justify-between px-3 pt-1.5 text-xs">
            <span className="num font-medium">
              {selected.size} selected{selSize ? <span className="text-muted"> · {bytes(selSize)}</span> : null}
            </span>
            <button type="button" className="h-8 px-2 font-medium text-accent" onClick={() => setSelected(selected.size === entries.length ? new Set() : new Set(entries.map((e) => e.name)))}>
              {selected.size === entries.length ? "Select none" : "Select all"}
            </button>
          </div>
          <div className="grid grid-cols-5">
            {[
              { label: "Download", icon: <Download />, run: () => selGroups[0].find((a) => a.key === "download")?.run() },
              { label: "Move", icon: <FolderInput />, run: () => setDialog({ kind: "move", paths: selEntries.map(pathOf) }) },
              { label: "Copy", icon: <Copy />, run: () => setDialog({ kind: "copy", paths: selEntries.map(pathOf) }) },
              { label: "Delete", icon: <Trash2 />, run: () => setDialog({ kind: "delete", paths: selEntries.map(pathOf) }), danger: true },
              { label: "More", icon: <MoreHorizontal />, run: () => setSheet({ title: selected.size === 1 ? selEntries[0]?.name : `${selected.size} items`, groups: selGroups }) },
            ].map((b) => (
              <button key={b.label} type="button" onClick={b.run} className={cn("flex h-14 flex-col items-center justify-center gap-1 text-2xs font-medium active:bg-surface-2 [&_svg]:size-5", b.danger ? "text-crit" : "text-fg")}>
                {b.icon}
                {b.label}
              </button>
            ))}
          </div>
        </div>
      )}

      <TransfersPanel jobs={jobs} onDismissJob={dismiss} onClearJobs={clear} raised={compact && selected.size > 0} />

      {/* Dialogs */}
      <NameDialog
        open={dialog?.kind === "mkdir"}
        onOpenChange={closeDialog}
        title="New folder"
        label="Folder name"
        initial="New folder"
        confirmLabel="Create"
        onSubmit={async (name) => {
          await fileApi.mkdir(root, joinPath(path, name));
          refresh();
        }}
      />
      <NameDialog
        open={dialog?.kind === "newfile"}
        onOpenChange={closeDialog}
        title="New text file"
        label="File name"
        initial="untitled.txt"
        selectStem
        confirmLabel="Create"
        onSubmit={async (name) => {
          await fileApi.save(root, joinPath(path, name), "", null, undefined, true);
          refresh();
          const p = new URLSearchParams(params);
          p.set("view", name);
          window.setTimeout(() => setParams(p), 150);
        }}
      />
      <NameDialog
        open={dialog?.kind === "rename"}
        onOpenChange={closeDialog}
        title="Rename"
        label="New name"
        initial={dialog?.kind === "rename" ? dialog.entry.name : ""}
        selectStem={dialog?.kind === "rename" && !isDirEntry(dialog.entry)}
        confirmLabel="Rename"
        onSubmit={async (name) => {
          if (dialog?.kind !== "rename") return;
          await fileApi.rename(root, pathOf(dialog.entry), name);
          setSelected(new Set([name]));
          refresh();
        }}
      />
      <DeleteDialog open={dialog?.kind === "delete"} onOpenChange={closeDialog} root={root} paths={dialog?.kind === "delete" ? dialog.paths : []} trashAvailable={trashAvailable} onDone={doneAndRefresh} />
      <ChmodDialog open={dialog?.kind === "chmod"} onOpenChange={closeDialog} root={root} dir={path} entries={dialog?.kind === "chmod" ? dialog.entries : []} onDone={refresh} />
      <ChownDialog open={dialog?.kind === "chown"} onOpenChange={closeDialog} root={root} dir={path} entries={dialog?.kind === "chown" ? dialog.entries : []} onDone={refresh} />
      <CompressDialog open={dialog?.kind === "compress"} onOpenChange={closeDialog} root={root} dir={path} entries={dialog?.kind === "compress" ? dialog.entries : []} onJob={() => setSelected(new Set())} />
      <ExtractDialog open={dialog?.kind === "extract"} onOpenChange={closeDialog} root={root} dir={path} entry={dialog?.kind === "extract" ? dialog.entry : undefined} onJob={() => {}} />
      <FolderPicker
        open={dialog?.kind === "copy" || dialog?.kind === "move"}
        onOpenChange={closeDialog}
        root={root}
        rootInfo={rootInfo}
        start={path}
        title={dialog?.kind === "move" ? "Move to…" : "Copy to…"}
        confirmLabel={dialog?.kind === "move" ? "Move here" : "Copy here"}
        sources={dialog?.kind === "copy" || dialog?.kind === "move" ? dialog.paths : []}
        onPick={async (dest) => {
          if (dialog?.kind === "copy" || dialog?.kind === "move") await transfer(dialog.kind, dialog.paths, dest);
        }}
      />
      <PropertiesSheet open={dialog?.kind === "props"} onOpenChange={closeDialog} root={root} rootInfo={rootInfo} dir={path} entry={dialog?.kind === "props" ? dialog.entry : undefined} />
      <ConflictDialog state={conflict} onResolve={(c) => conflict?.resolve(c)} />
      <ActionSheet open={!!sheet} onOpenChange={(o) => !o && setSheet(null)} title={sheet?.title ?? ""} subtitle={sheet?.subtitle} groups={sheet?.groups ?? []} />
      {listing.data && <Preview root={root} dir={path} entries={previewables} name={previewName} onClose={closePreview} onNavigate={(n) => setParams(new URLSearchParams({ view: n }), { replace: true })} />}
    </div>
  );
}

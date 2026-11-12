// SPDX-License-Identifier: AGPL-3.0-only
// One independent file browser. The page shows one or more panes side by
// side; each owns its location, selection, view settings and history.
import { setPref } from "@/lib/prefs";
import { useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent, type MouseEvent } from "react";
import { ContextMenu as CM } from "radix-ui";
import {
  Archive,
  ArrowLeft,
  ArrowRight,
  ArrowUpFromLine,
  ChevronDown,
  ClipboardCopy,
  Columns2,
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
  Star,
  StarOff,
  Trash2,
  Upload,
  UserCog,
  X,
} from "lucide-react";
import { ApiError } from "@/lib/api";
import { bytes } from "@/lib/format";
import { cn } from "@/lib/cn";
import { copyWithToast } from "@/lib/clipboard";
import { Card } from "@/components/ui/card";
import { Button, IconButton } from "@/components/ui/button";
import { SearchInput } from "@/components/ui/form";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "@/components/ui/menu";
import { EmptyState, ErrorNote, Skeleton } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { absPath, baseName, downloadUrl, joinPath, parentPath, segments, startDownload, useListing, useRefreshFiles, zipUrl, type Entry, type Loc, type Root, type RootId } from "./api";
import { fileApi } from "./api";
import { isExtractable, kindOf } from "./kinds";
import { ActionMenu, ActionSheet, ContextMenuContent, type Action, type ActionGroups } from "./actions";
import { Listing, type Sort, type SortKey } from "./Listing";
import { ChmodDialog, ChownDialog, CompressDialog, DeleteDialog, ExtractDialog, FolderPicker, NameDialog, PropertiesSheet } from "./dialogs";
import { Preview, canPreview } from "./Preview";
import { Breadcrumbs } from "./Locations";
import { useLocationsApi } from "./bookmarks";
import { enqueue, fromDrop, fromInput } from "./uploads";

export const DRAG_TYPE = "application/x-wargo-files";

/** Items being dragged out of a pane (kept at page level: dataTransfer
 * contents can't be read during dragover). */
export interface DragPayload {
  pane: string;
  root: RootId;
  dir: string;
  names: string[];
}

export interface PaneMeta {
  key: string;
  view: "list" | "grid";
  sort: Sort;
  hidden: boolean;
  back: Loc[];
  fwd: Loc[];
}

export type TransferKind = "copy" | "move";

export interface OtherPane {
  index: number;
  loc: Loc;
  label: string;
}

const sortFns: Record<SortKey, (a: Entry, b: Entry) => number> = {
  name: (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }),
  size: (a, b) => a.size - b.size,
  mtime: (a, b) => a.mtime.localeCompare(b.mtime),
  type: (a, b) => kindOf(a).localeCompare(kindOf(b)) || a.name.localeCompare(b.name),
  owner: (a, b) => a.owner.localeCompare(b.owner),
  mode: (a, b) => a.octal.localeCompare(b.octal),
};

export const isDirEntry = (e: Entry) => e.type === "dir" || e.targetType === "dir";

/** "DATA / Media / Movies" */
export function locLabel(roots: Root[], l: Loc) {
  const r = roots.find((x) => x.id === l.root);
  return [r?.name ?? "…", ...segments(l.path)].join(" / ");
}

type DialogState =
  | { kind: "mkdir" | "newfile" }
  | { kind: "rename"; entry: Entry }
  | { kind: "delete"; paths: string[] }
  | { kind: "chmod" | "chown" | "compress"; entries: Entry[] }
  | { kind: "extract"; entry: Entry }
  | { kind: "copy" | "move"; paths: string[] }
  | { kind: "props"; entry: Entry }
  | null;

interface DropHint {
  mode: TransferKind | "upload";
  dest: string;
  row: number | null;
}

export interface PaneProps {
  id: string;
  index: number;
  count: number;
  loc: Loc;
  meta: PaneMeta;
  onMeta: (patch: Partial<PaneMeta>) => void;
  roots: Root[];
  active: boolean;
  /** Hidden by the pane switcher (phones, tablets with >2 panes). */
  concealed: boolean;
  /** More than one pane is on screen. */
  split: boolean;
  onActivate: () => void;
  onNavigate: (l: Loc) => void;
  onBack: () => void;
  onForward: () => void;
  onSplit?: () => void;
  onClose?: () => void;
  others: OtherPane[];
  onTransfer: (kind: TransferKind, from: Loc, paths: string[], to: Loc) => Promise<boolean>;
  drag: DragPayload | null;
  onDrag: (d: DragPayload | null) => void;
  previewName: string | null;
  onPreview: (name: string | null, replace?: boolean) => void;
  compact: boolean;
  coarse: boolean;
  trashAvailable: boolean;
  onSelectionBar: (shown: boolean) => void;
  style?: React.CSSProperties;
}

export function Pane(props: PaneProps) {
  const { loc, meta, roots, compact, coarse, active } = props;
  const { root, path } = loc;
  const { view, sort, hidden } = meta;
  const rootInfo = roots.find((r) => r.id === root);
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [focus, setFocus] = useState(-1);
  const anchor = useRef(-1);
  const [dialog, setDialog] = useState<DialogState>(null);
  const [sheet, setSheet] = useState<{ title: string; subtitle?: string; groups: ActionGroups } | null>(null);
  const [hint, setHint] = useState<DropHint | null>(null);
  const dragDepth = useRef(0);
  const listRef = useRef<HTMLDivElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const refresh = useRefreshFiles();
  const listing = useListing(root, path, hidden);
  const label = locLabel(roots, loc);
  const idPrefix = `${props.id}-item`;
  const locApi = useLocationsApi();
  const bookmark = locApi.findBookmark(loc);

  const setView = (v: "list" | "grid") => {
    props.onMeta({ view: v });
    setPref("wargo.files.view", v);
  };
  const setHidden = (h: boolean) => {
    props.onMeta({ hidden: h });
    setPref("wargo.files.hidden", h ? "1" : "0");
  };
  const setSort = (fn: (s: Sort) => Sort) => props.onMeta({ sort: fn(sort) });

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

  // Clear drop hints once a drag ends anywhere.
  useEffect(() => {
    if (!props.drag) {
      setHint((h) => (h && h.mode !== "upload" ? null : h));
      dragDepth.current = 0;
    }
  }, [props.drag]);

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

  const selectionBar = compact && active && !props.concealed && selected.size > 0;
  const { onSelectionBar } = props;
  useEffect(() => {
    onSelectionBar(selectionBar);
  }, [selectionBar, onSelectionBar]);

  const go = (p: string) => props.onNavigate({ root, path: p });
  const pickRoot = (id: RootId) => props.onNavigate({ root: id, path: "." });
  const bookmarkAction = (l: Loc, key: string): Action => {
    const b = locApi.findBookmark(l);
    const here = l === loc;
    return b
      ? { key, label: "Remove bookmark", icon: <StarOff />, shortcut: here ? "Ctrl+D" : undefined, run: () => locApi.removeBookmark(b) }
      : { key, label: here ? "Bookmark this folder" : "Add bookmark", icon: <Star />, shortcut: here ? "Ctrl+D" : undefined, run: () => locApi.toggleBookmark(l) };
  };
  // The folder the entries on screen belong to: while a placeholder listing shows,
  // that is the previous folder, not the route path.
  const pathOf = (e: Entry) => joinPath(listing.data?.path ?? path, e.name);

  const open = (e: Entry) => {
    if (e.targetType === "outside") return toast.error("Link points outside this root", `${e.name} → ${e.target}`);
    if (e.targetType === "broken") return toast.error("Broken link", `${e.name} → ${e.target}`);
    if (isDirEntry(e)) return go(pathOf(e));
    if (kindOf(e) === "archive") {
      if (isExtractable(e.name)) return setDialog({ kind: "extract", entry: e });
      return startDownload(downloadUrl(root, pathOf(e)));
    }
    if (canPreview(e)) return props.onPreview(e.name);
    startDownload(downloadUrl(root, pathOf(e)));
  };

  const closePreview = () => {
    props.onPreview(null, true);
    window.setTimeout(() => listRef.current?.focus(), 0);
  };

  const transfer = async (kind: TransferKind, paths: string[], to: Loc) => {
    if (await props.onTransfer(kind, loc, paths, to)) setSelected(new Set());
  };
  const transferSafe = (kind: TransferKind, paths: string[], to: Loc) =>
    void transfer(kind, paths, to).catch((e) => {
      if (!(e instanceof ApiError && e.code === "elevation_canceled")) toast.error(kind === "copy" ? "Copy failed" : "Move failed", e);
    });

  const copyPath = (p: string) => void copyWithToast(p, "Path copied");

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
      bookmarkAction(loc, "bookmark"),
      ...(props.onSplit ? [{ key: "split", label: "Open in split view", icon: <Columns2 />, shortcut: "Ctrl+\\", run: props.onSplit }] : []),
    ],
  ];

  const otherPaneActions = (paths: string[]): Action[] => {
    const same = (o: OtherPane) => o.loc.root === root && o.loc.path === path;
    if (props.others.length === 0) return [];
    if (props.others.length === 1) {
      const o = props.others[0];
      return [
        { key: "copyother", label: "Copy to other pane", icon: <Copy />, disabled: same(o), run: () => transferSafe("copy", paths, o.loc) },
        { key: "moveother", label: "Move to other pane", icon: <FolderInput />, disabled: same(o), run: () => transferSafe("move", paths, o.loc) },
      ];
    }
    const sub = (kind: TransferKind): Action[] =>
      props.others.map((o) => ({ key: `${kind}-${o.index}`, label: `Pane ${o.index + 1}: ${o.label}`, disabled: same(o), run: () => transferSafe(kind, paths, o.loc) }));
    return [
      { key: "copypane", label: "Copy to pane", icon: <Copy />, run: () => {}, children: sub("copy") },
      { key: "movepane", label: "Move to pane", icon: <FolderInput />, run: () => {}, children: sub("move") },
    ];
  };

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
        { key: "dup", label: "Duplicate", icon: <CopyPlus />, run: () => transferSafe("copy", paths, loc) },
      ],
      otherPaneActions(paths),
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
        ...(one && isDirEntry(one) && one.targetType !== "outside" && one.targetType !== "broken" ? [bookmarkAction({ root, path: pathOf(one) }, "bookmark")] : []),
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
    if (e.target !== e.currentTarget || listing.isPlaceholderData) return;
    const n = entries.length;
    const mod = e.ctrlKey || e.metaKey;
    if (e.altKey && !mod) {
      if (e.key === "ArrowLeft" && meta.back.length) return (e.preventDefault(), props.onBack());
      if (e.key === "ArrowRight" && meta.fwd.length) return (e.preventDefault(), props.onForward());
      if (e.key === "ArrowUp" && path !== ".") return (e.preventDefault(), go(parentPath(path)));
    }
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
      const r = anchorEl.getBoundingClientRect();
      anchorEl.dispatchEvent(new window.MouseEvent("contextmenu", { bubbles: true, clientX: r.left, clientY: r.bottom }));
    }
  };

  // ---- drag & drop: uploads from the desktop, items between panes ----
  const types = (e: DragEvent) => [...e.dataTransfer.types];
  const isInternal = (e: DragEvent) => types(e).includes(DRAG_TYPE) && !!props.drag;
  const isFiles = (e: DragEvent) => !types(e).includes(DRAG_TYPE) && types(e).includes("Files");

  /** Where a drag over this pane would land, or null if it can't. */
  const target = (e: DragEvent): { dest: string; row: number | null } | null => {
    const d = props.drag;
    if (!d) return null;
    const rowEl = (e.target as HTMLElement).closest?.("[data-row]");
    const ri = rowEl ? Number(rowEl.getAttribute("data-row")) : -1;
    const re = ri >= 0 ? entries[ri] : undefined;
    const samePlace = d.root === root && d.dir === path;
    if (re && isDirEntry(re) && re.targetType !== "outside" && re.targetType !== "broken" && !(samePlace && d.names.includes(re.name))) {
      return { dest: joinPath(path, re.name), row: ri };
    }
    if (samePlace) return null; // dropping back where it came from
    // Dropping a folder into its own subtree.
    if (d.root === root && d.names.some((n) => path === joinPath(d.dir, n) || path.startsWith(joinPath(d.dir, n) + "/"))) return null;
    return { dest: path, row: null };
  };

  const dropMode = (e: DragEvent): TransferKind => (e.ctrlKey || e.altKey || e.metaKey ? "copy" : "move");

  const dropHandlers = {
    onDragEnter: (e: DragEvent) => {
      if (!isInternal(e) && !isFiles(e)) return;
      e.preventDefault();
      dragDepth.current++;
    },
    onDragOver: (e: DragEvent) => {
      if (listing.isPlaceholderData) return;
      if (isFiles(e)) {
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
        if (hint?.mode !== "upload") setHint({ mode: "upload", dest: path, row: null });
        return;
      }
      if (!isInternal(e)) return;
      const t = target(e);
      if (!t) {
        e.dataTransfer.dropEffect = "none";
        if (hint) setHint(null);
        return;
      }
      e.preventDefault();
      const mode = dropMode(e);
      e.dataTransfer.dropEffect = mode;
      if (!hint || hint.mode !== mode || hint.dest !== t.dest || hint.row !== t.row) setHint({ mode, dest: t.dest, row: t.row });
    },
    onDragLeave: (e: DragEvent) => {
      if (!isInternal(e) && !isFiles(e)) return;
      dragDepth.current = Math.max(0, dragDepth.current - 1);
      if (!dragDepth.current) setHint(null);
    },
    onDrop: async (e: DragEvent) => {
      dragDepth.current = 0;
      setHint(null);
      if (listing.isPlaceholderData) return;
      if (isFiles(e)) {
        e.preventDefault();
        const files = await fromDrop(e.dataTransfer);
        if (files.length) enqueue(root, path, files);
        return;
      }
      if (!isInternal(e)) return;
      const t = target(e);
      const d = props.drag;
      props.onDrag(null);
      if (!t || !d) return;
      e.preventDefault();
      props.onActivate();
      void props
        .onTransfer(
          dropMode(e),
          { root: d.root, path: d.dir },
          d.names.map((n) => joinPath(d.dir, n)),
          { root, path: t.dest },
        )
        .catch((err) => {
          if (!(err instanceof ApiError && err.code === "elevation_canceled")) toast.error("Drop failed", err);
        });
    },
  };

  const onDragStart = (i: number, e: DragEvent) => {
    const it = entries[i];
    if (!it) return;
    const names = selected.has(it.name) ? selEntries.map((x) => x.name) : [it.name];
    if (!selected.has(it.name)) selectOnly(i);
    e.dataTransfer.effectAllowed = "copyMove";
    e.dataTransfer.setData(DRAG_TYPE, JSON.stringify({ root, dir: path, names }));
    e.dataTransfer.setData("text/plain", names.map((n) => absPath(rootInfo, joinPath(path, n))).join("\n"));
    props.onDrag({ pane: props.id, root, dir: path, names });
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
  const dragging = props.drag && props.drag.pane === props.id ? new Set(props.drag.names) : null;
  const firstRoot = roots[0];

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

  const hintText =
    hint &&
    (hint.mode === "upload"
      ? "Drop to upload"
      : `${hint.mode === "copy" ? "Copy" : "Move"} ${props.drag?.names.length === 1 ? `“${props.drag.names[0]}”` : `${props.drag?.names.length ?? 0} items`} to ${hint.dest === "." ? (rootInfo?.name ?? "") : baseName(hint.dest)}`);

  return (
    <section
      id={props.id}
      aria-label={props.split || props.count > 1 ? `Pane ${props.index + 1}: ${label}` : `Files: ${label}`}
      hidden={props.concealed}
      className="@container/pane relative flex min-h-0 min-w-0 flex-col"
      style={props.style}
      onPointerDownCapture={() => !active && props.onActivate()}
      onFocusCapture={() => !active && props.onActivate()}
    >
      <Card
        className={cn(
          "relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden transition-[border-color,box-shadow]",
          props.split && active && "border-accent/70 shadow-[0_0_0_1px_var(--color-accent)]",
        )}
        {...dropHandlers}
      >
        <input ref={fileInput} type="file" multiple className="hidden" aria-label="Upload files" onChange={(e) => (enqueue(root, path, fromInput(e.target.files)), (e.target.value = ""))} />
        <input ref={folderInput} type="file" multiple className="hidden" aria-label="Upload folder" onChange={(e) => (enqueue(root, path, fromInput(e.target.files)), (e.target.value = ""))} />

        {/* Path bar */}
        <div className="flex h-11 items-center gap-1 border-b border-border px-1.5 @lg/pane:px-2">
          {props.split && (
            <span className={cn("num flex size-5 shrink-0 items-center justify-center rounded text-2xs font-semibold", active ? "bg-accent text-accent-fg" : "bg-surface-3 text-muted")} aria-hidden="true">
              {props.index + 1}
            </span>
          )}
          <div className="flex items-center @max-md/pane:hidden">
            <IconButton label="Back" size="sm" disabled={!meta.back.length} onClick={props.onBack} title="Back (Alt+←)">
              <ArrowLeft />
            </IconButton>
            <IconButton label="Forward" size="sm" disabled={!meta.fwd.length} onClick={props.onForward} title="Forward (Alt+→)">
              <ArrowRight />
            </IconButton>
          </div>
          <IconButton label="Up one level" size="md" disabled={path === "."} onClick={() => go(parentPath(path))}>
            <CornerLeftUp />
          </IconButton>
          <Breadcrumbs root={rootInfo} path={path} onNav={go} onPick={props.onNavigate} alwaysSwitch={props.count > 1} />
          <IconButton
            label="Bookmark this folder"
            title={bookmark ? `Remove bookmark “${bookmark.name}” (Ctrl+D)` : "Bookmark this folder (Ctrl+D)"}
            aria-pressed={!!bookmark}
            disabled={!listing.data || !locApi.ready}
            onClick={() => locApi.toggleBookmark(loc)}
            className={cn(bookmark && "text-accent")}
          >
            <Star className={cn(bookmark && "fill-current")} />
          </IconButton>
          <IconButton label="Refresh" onClick={() => refresh()} className="@max-lg/pane:hidden">
            <RefreshCw className={cn(listing.isFetching && "animate-spin")} />
          </IconButton>
          {props.onSplit && (
            <IconButton label="Split view" title="Split view (Ctrl+\)" onClick={props.onSplit}>
              <Columns2 />
            </IconButton>
          )}
          {props.onClose && (
            <IconButton label={`Close pane ${props.index + 1}`} onClick={props.onClose}>
              <X />
            </IconButton>
          )}
        </div>

        {/* Toolbar */}
        <div className="flex min-h-11 flex-wrap items-center gap-1.5 border-b border-border px-2 py-1.5">
          {selected.size > 0 && !compact ? (
            <div className="flex min-w-0 flex-1 items-center gap-1" role="toolbar" aria-label="Selection actions">
              <IconButton label="Clear selection" size="sm" onClick={() => setSelected(new Set())}>
                <X />
              </IconButton>
              <span className="num mr-2 truncate whitespace-nowrap text-ui font-medium" aria-live="polite">
                {selected.size} selected{selSize ? <span className="text-muted @max-lg/pane:hidden"> · {bytes(selSize)}</span> : null}
              </span>
              <Button size="sm" variant="ghost" icon={<Download />} onClick={() => selGroups[0].find((a) => a.key === "download")?.run()} className="@max-xl/pane:hidden">
                Download
              </Button>
              <IconButton label="Download" size="sm" onClick={() => selGroups[0].find((a) => a.key === "download")?.run()} className="@xl/pane:hidden">
                <Download />
              </IconButton>
              <Button size="sm" variant="ghost" icon={<Copy />} onClick={() => setDialog({ kind: "copy", paths: selEntries.map(pathOf) })} className="@max-3xl/pane:hidden">
                Copy to
              </Button>
              <Button size="sm" variant="ghost" icon={<FolderInput />} onClick={() => setDialog({ kind: "move", paths: selEntries.map(pathOf) })} className="@max-3xl/pane:hidden">
                Move to
              </Button>
              <Button size="sm" variant="ghost" icon={<Trash2 />} className="text-crit hover:text-crit @max-xl/pane:hidden" onClick={() => setDialog({ kind: "delete", paths: selEntries.map(pathOf) })}>
                Delete
              </Button>
              <IconButton label="Delete" size="sm" className="text-crit hover:text-crit @xl/pane:hidden" onClick={() => setDialog({ kind: "delete", paths: selEntries.map(pathOf) })}>
                <Trash2 />
              </IconButton>
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
              <Button size="sm" variant="primary" icon={<Upload />} onClick={() => fileInput.current?.click()} className="@max-xl/pane:hidden">
                Upload
              </Button>
              <IconButton label="Upload files" size="sm" variant="primary" onClick={() => fileInput.current?.click()} className="@xl/pane:hidden">
                <ArrowUpFromLine />
              </IconButton>
              <Menu
                align="start"
                trigger={
                  <Button size="sm" icon={<FolderPlus />} aria-label="New">
                    <span className="@max-xl/pane:sr-only">New</span>
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
          <div className={cn("ml-auto flex min-w-0 items-center gap-1", selected.size > 0 && !compact && "@max-3xl/pane:hidden")}>
            <SearchInput value={filter} onChange={setFilter} placeholder="Filter" label="Filter this folder" className="w-24 @md/pane:w-32 @xl/pane:w-44" />
            <Menu
              trigger={
                <IconButton label="View options" size="sm" className="@2xl/pane:hidden">
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
            <div className="flex items-center gap-1 @max-2xl/pane:hidden">
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
                ).map(([v, icon, lbl]) => (
                  <button
                    key={v}
                    type="button"
                    role="radio"
                    aria-checked={view === v}
                    aria-label={lbl}
                    title={lbl}
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
              className={cn("scroll-area relative min-h-0 flex-1", selectionBar && "pb-28")}
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
                  ) : err.code === "unknown_root" ? (
                    <EmptyState icon={<HardDrive />} title="Location not available" action={firstRoot && <Button onClick={() => pickRoot(firstRoot.id)}>Go to {firstRoot.name}</Button>}>
                      {err.message}
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
                  {filter ? `Nothing here matches “${filter}”.` : props.count > 1 ? "Drop files or items from another pane here." : "Drop files here or use Upload."}
                </EmptyState>
              )}
              {listing.data && entries.length > 0 && (
                <div aria-busy={listing.isPlaceholderData || undefined} inert={listing.isPlaceholderData} className={cn("min-h-full transition-opacity", listing.isPlaceholderData && "opacity-60")}>
                <Listing
                  ref={listRef}
                  idPrefix={idPrefix}
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
                  label={`Files in ${baseName(path) === "." ? rootInfo?.name : baseName(path)}${props.count > 1 ? ` (pane ${props.index + 1})` : ""}`}
                  dnd={coarse ? undefined : { onDragStart, onDragEnd: () => props.onDrag(null), dropRow: hint?.row ?? null, dragging }}
                />
                </div>
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
          {rootInfo?.free !== undefined && <span className="ml-auto shrink-0 @3xl/pane:hidden">{bytes(rootInfo.free)} free</span>}
          <span className="ml-auto hidden shrink-0 @3xl/pane:inline">
            {!coarse && (
              <>
                <kbd className="font-sans">Enter</kbd> open · <kbd className="font-sans">F2</kbd> rename · <kbd className="font-sans">Del</kbd> delete
                {props.count > 1 && (
                  <>
                    {" "}
                    · drag to move, <kbd className="font-sans">Ctrl</kbd> copies
                  </>
                )}
              </>
            )}
          </span>
        </div>

        {hint && (hint.mode === "upload" || hint.row === null) && (
          <div className={cn("pointer-events-none absolute inset-0 z-20 flex items-center justify-center border-2 border-dashed border-accent", hint.mode === "upload" ? "bg-accent-soft/80" : "bg-accent-soft/40")}>
            <div className="flex flex-col items-center gap-2 rounded-lg bg-surface/90 px-4 py-3 text-center shadow-lg">
              {hint.mode === "upload" ? <Upload className="size-7 text-accent" /> : hint.mode === "copy" ? <Copy className="size-7 text-accent" /> : <FolderInput className="size-7 text-accent" />}
              <p className="font-medium">{hintText}</p>
              <p className="num max-w-[16rem] truncate text-xs text-muted">{absPath(rootInfo, hint.dest)}</p>
              {hint.mode !== "upload" && <p className="text-2xs text-subtle">{hint.mode === "move" ? "Hold Ctrl to copy instead" : "Release Ctrl to move instead"}</p>}
            </div>
          </div>
        )}
        {hint && hint.mode !== "upload" && hint.row !== null && (
          <div className="pointer-events-none absolute inset-x-0 bottom-10 z-20 flex justify-center px-2">
            <div className="num max-w-full truncate rounded-md border border-accent/50 bg-surface px-3 py-1.5 text-xs font-medium shadow-lg">
              {hintText} <span className="text-subtle">· {hint.mode === "move" ? "Ctrl to copy" : "copying"}</span>
            </div>
          </div>
        )}
      </Card>

      {/* Mobile selection bar */}
      {selectionBar && (
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
          window.setTimeout(() => props.onPreview(name), 150);
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
      <DeleteDialog open={dialog?.kind === "delete"} onOpenChange={closeDialog} root={root} paths={dialog?.kind === "delete" ? dialog.paths : []} trashAvailable={props.trashAvailable} onDone={doneAndRefresh} />
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
          if (dialog?.kind === "copy" || dialog?.kind === "move") await transfer(dialog.kind, dialog.paths, { root, path: dest });
        }}
      />
      <PropertiesSheet open={dialog?.kind === "props"} onOpenChange={closeDialog} root={root} rootInfo={rootInfo} dir={path} entry={dialog?.kind === "props" ? dialog.entry : undefined} />
      <ActionSheet open={!!sheet} onOpenChange={(o) => !o && setSheet(null)} title={sheet?.title ?? ""} subtitle={sheet?.subtitle} groups={sheet?.groups ?? []} />
      {active && listing.data && <Preview root={root} dir={path} entries={previewables} name={props.previewName} onClose={closePreview} onNavigate={(n) => props.onPreview(n, true)} />}
    </section>
  );
}

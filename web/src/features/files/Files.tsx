// SPDX-License-Identifier: AGPL-3.0-only
// File manager page: the Locations sidebar plus one to four independent
// panes side by side (split view). The URL encodes every pane:
//   /files/<root>/<path>?pane=<root>:<path>&pane=…&active=<n>&view=<file>
// The first pane lives in the path; extra panes in ?pane=; ?active is
// 1-based and ?view previews a file in the active pane.
import { readPref, setPref } from "@/lib/prefs";
import { Fragment, useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent } from "react";
import { useLocation, useNavigate } from "react-router";
import { HardDrive } from "lucide-react";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useMedia } from "@/lib/hooks";
import { useCapabilities } from "@/app/session";
import { Card } from "@/components/ui/card";
import { EmptyState, PageHeader } from "@/components/ui/misc";
import { toast } from "@/components/ui/toast";
import { decodePane, encodePane, fileApi, parseRoute, routeFor, segments, useLocations, useRefreshFiles, type JobSnapshot, type Loc, type Root } from "./api";
import { LocationsProvider, useLocationsApi } from "./bookmarks";
import { ConflictDialog } from "./dialogs";
import { RootIcon, RootsSidebar } from "./Locations";
import { Pane, locLabel, type DragPayload, type PaneMeta, type TransferKind } from "./Pane";
import { TransfersPanel, useFileJobs } from "./Transfers";
import { onUploadDone } from "./uploads";
import type { Sort } from "./Listing";

const MAX_PANES = 4;
const LAYOUT_KEY = "wargo.files.layout";
const MIN_SHARE = 0.15;

let keySeq = 0;
const newKey = () => `pane${++keySeq}`;

function defaultMeta(from?: PaneMeta): PaneMeta {
  return {
    key: newKey(),
    view: from?.view ?? ((readPref("wargo.files.view") as "list" | "grid" | null) ?? "list"),
    sort: from?.sort ?? ({ key: "name", dir: "asc" } as Sort),
    hidden: from?.hidden ?? readPref("wargo.files.hidden") === "1",
    back: [],
    fwd: [],
  };
}

interface SavedLayout {
  panes: Loc[];
  weights: number[];
}

function loadLayout(): SavedLayout | null {
  try {
    const v = JSON.parse(readPref(LAYOUT_KEY) ?? "null") as SavedLayout | null;
    if (!v || !Array.isArray(v.panes)) return null;
    // localStorage can hold anything (older versions, manual edits): keep only well-formed entries.
    const panes = v.panes.filter((l) => l && typeof l.root === "string" && typeof l.path === "string").slice(0, MAX_PANES);
    if (!panes.length) return null;
    const weights = Array.isArray(v.weights) && v.weights.length === panes.length && v.weights.every((x) => Number.isFinite(x) && x > 0) ? v.weights : [];
    return { panes, weights };
  } catch {
    return null;
  }
}

const equal = (n: number) => Array.from({ length: n }, () => 1);
const sameLoc = (a: Loc, b: Loc) => a.root === b.root && a.path === b.path;

/** Like URLSearchParams.toString() but keeps ":" and "/" readable. */
function encodeQuery(q: [string, string][]) {
  return q.map(([k, v]) => `${k}=${encodeURIComponent(v).replace(/%3A/gi, ":").replace(/%2F/gi, "/")}`).join("&");
}

function buildUrl(locs: Loc[], active: number, view?: string | null) {
  const q: [string, string][] = locs.slice(1).map((l) => ["pane", encodePane(l)]);
  if (locs.length > 1 && active > 0) q.push(["active", String(active + 1)]);
  if (view) q.push(["view", view]);
  const s = encodeQuery(q);
  return routeFor(locs[0].root, locs[0].path) + (s ? `?${s}` : "");
}

// ---- resizer -----------------------------------------------------------------

function Resizer({ index, share, onResize, onReset, label }: { index: number; share: number; onResize: (delta: number) => void; onReset: () => void; label: string }) {
  const start = useRef<{ x: number; width: number } | null>(null);
  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    const row = e.currentTarget.parentElement;
    if (!row) return;
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    start.current = { x: e.clientX, width: row.getBoundingClientRect().width };
  };
  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (!start.current) return;
    const dx = e.clientX - start.current.x;
    if (Math.abs(dx) < 1) return;
    onResize(dx / start.current.width);
    start.current.x = e.clientX;
  };
  const end = () => {
    start.current = null;
  };
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 0.1 : 0.02;
    if (e.key === "ArrowLeft") onResize(-step);
    else if (e.key === "ArrowRight") onResize(step);
    else if (e.key === "Home") onResize(-1);
    else if (e.key === "End") onResize(1);
    else if (e.key === "Enter") onReset();
    else return;
    e.preventDefault();
  };
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuenow={Math.round(share * 100)}
      aria-valuemin={Math.round(MIN_SHARE * 100)}
      aria-valuemax={Math.round((1 - MIN_SHARE) * 100)}
      aria-valuetext={`Pane ${index + 1} takes ${Math.round(share * 100)}% of the pair`}
      tabIndex={0}
      title="Drag to resize · double-click to make equal"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={end}
      onPointerCancel={end}
      onDoubleClick={onReset}
      onKeyDown={onKeyDown}
      className="group relative z-10 flex w-2.5 shrink-0 cursor-col-resize touch-none items-center justify-center rounded outline-none"
    >
      <span className="h-12 w-1 rounded-full bg-border-strong transition-colors group-hover:bg-accent group-focus-visible:bg-accent group-active:bg-accent" aria-hidden="true" />
    </div>
  );
}

// ---- page --------------------------------------------------------------------

export default function FilesPage() {
  const roots = useLocations();
  const caps = useCapabilities();
  const capRoots = caps.data?.files?.roots;
  const rootList: Root[] = useMemo(
    () => roots.data?.roots ?? (capRoots ?? []).map((r, i) => ({ id: String(r.id ?? i), kind: r.kind ?? "place", name: r.name, path: r.path, hidden: r.hidden })),
    [roots.data, capRoots],
  );
  return (
    <LocationsProvider roots={rootList} data={roots.data}>
      <FilesView rootList={rootList} loading={roots.isLoading || caps.isLoading} />
    </LocationsProvider>
  );
}

function FilesView({ rootList, loading }: { rootList: Root[]; loading: boolean }) {
  const location = useLocation();
  const nav = useNavigate();
  const caps = useCapabilities();
  const locApi = useLocationsApi();
  const trashAvailable = caps.data?.files?.trash !== false;
  const refresh = useRefreshFiles();

  const phone = useMedia("(max-width: 639px)");
  const tablet = useMedia("(max-width: 1023px)");
  const compact = useMedia("(max-width: 767px)");
  const coarse = useMedia("(pointer: coarse)");
  const maxVisible = phone ? 1 : tablet ? 2 : MAX_PANES;

  // ---- URL state ----
  const params = useMemo(() => new URLSearchParams(location.search), [location.search]);
  const first = parseRoute(location.pathname);
  const locs: Loc[] = useMemo(() => {
    if (first.root === null) return [];
    const extra = params
      .getAll("pane")
      .map(decodePane)
      .filter((x): x is Loc => !!x)
      .slice(0, MAX_PANES - 1);
    return [{ root: first.root, path: first.path }, ...extra];
  }, [first.root, first.path, params]);
  const count = locs.length;
  const active = Math.min(Math.max(0, Number(params.get("active") ?? 1) - 1 || 0), Math.max(0, count - 1));
  const previewName = params.get("view");

  // Bare /files: restore the last layout, else open the first root.
  const saved = useRef(loadLayout());
  useEffect(() => {
    if (first.root !== null || !rootList.length) return;
    const s = saved.current;
    const known = s?.panes.filter((l) => rootList.some((r) => r.id === l.root)) ?? [];
    nav(known.length ? buildUrl(known, 0) : routeFor(rootList[0].id, "."), { replace: true });
  }, [first.root, rootList, nav]);

  // ---- per-pane state that isn't in the URL ----
  const [metas, setMetas] = useState<PaneMeta[]>([]);
  const [weights, setWeights] = useState<number[]>(() => saved.current?.weights ?? []);
  const metaList = useMemo(() => {
    const out = metas.slice(0, count);
    while (out.length < count) out.push(defaultMeta(out[out.length - 1]));
    return out;
  }, [metas, count]);
  useEffect(() => {
    if (metaList !== metas && (metas.length !== metaList.length || metaList.some((m, i) => m !== metas[i]))) setMetas(metaList);
  }, [metaList, metas]);
  const w = weights.length === count ? weights : equal(count);

  // Persist the layout.
  useEffect(() => {
    if (!count) return;
    setPref(LAYOUT_KEY, JSON.stringify({ panes: locs, weights: w } satisfies SavedLayout));
  }, [locs, w, count]);

  // ---- visible window (phones show one pane, tablets two) ----
  const [winStart, setWinStart] = useState(0);
  const start = Math.max(0, Math.min(winStart, count - maxVisible));
  const visStart = active < start ? active : active >= start + maxVisible ? active - maxVisible + 1 : start;
  useEffect(() => {
    if (visStart !== winStart) setWinStart(visStart);
  }, [visStart, winStart]);
  const visible = (i: number) => i >= visStart && i < visStart + maxVisible;
  const split = Math.min(count, maxVisible) > 1;

  // ---- navigation ----
  const commit = useCallback((next: Loc[], act: number, opts?: { replace?: boolean; view?: string | null }) => nav(buildUrl(next, act, opts?.view), { replace: opts?.replace }), [nav]);

  const updateMeta = (i: number, patch: Partial<PaneMeta>) => setMetas((ms) => metaList.map((m, k) => (k === i ? { ...(ms[k] ?? m), ...patch } : (ms[k] ?? m))));

  const navigatePane = (i: number, l: Loc) => {
    const cur = locs[i];
    if (!cur) return;
    if (!sameLoc(cur, l)) updateMeta(i, { back: [...metaList[i].back, cur].slice(-50), fwd: [] });
    commit(
      locs.map((x, k) => (k === i ? l : x)),
      i,
    );
  };
  const historyStep = (i: number, dir: "back" | "fwd") => {
    const m = metaList[i];
    const stack = dir === "back" ? m.back : m.fwd;
    const target = stack[stack.length - 1];
    if (!target) return;
    const rest = stack.slice(0, -1);
    updateMeta(i, dir === "back" ? { back: rest, fwd: [...m.fwd, locs[i]] } : { fwd: rest, back: [...m.back, locs[i]] });
    commit(
      locs.map((x, k) => (k === i ? target : x)),
      i,
    );
  };
  const activate = (i: number) => {
    if (i !== active) commit(locs, i, { replace: true });
  };
  const splitPane = (from = active) => {
    if (count >= MAX_PANES || !locs[from]) return;
    const next = [...locs.slice(0, from + 1), { ...locs[from] }, ...locs.slice(from + 1)];
    setMetas([...metaList.slice(0, from + 1), defaultMeta(metaList[from]), ...metaList.slice(from + 1)]);
    setWeights(equal(next.length));
    commit(next, from + 1);
  };
  const closePane = (i: number) => {
    if (count < 2) return;
    const next = locs.filter((_, k) => k !== i);
    setMetas(metaList.filter((_, k) => k !== i));
    setWeights(w.filter((_, k) => k !== i));
    commit(next, Math.min(i > active ? active : Math.max(0, active - (i < active ? 1 : 0)), next.length - 1));
  };
  const setPreview = (name: string | null, replace?: boolean) => commit(locs, active, { view: name, replace });

  // Ctrl/Cmd+\ splits the active pane; Ctrl/Cmd+D (un)bookmarks its folder.
  const splitRef = useRef(splitPane);
  splitRef.current = splitPane;
  const bookmarkRef = useRef(() => {});
  bookmarkRef.current = () => {
    const l = locs[active];
    if (l) locApi.toggleBookmark(l);
  };
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey) return;
      const isSplit = e.key === "\\";
      const isBookmark = e.key === "d" || e.key === "D";
      if (!isSplit && !isBookmark) return;
      const t = e.target as HTMLElement | null;
      if (t?.closest("input, textarea, [contenteditable=true], [role=dialog]")) return;
      e.preventDefault();
      if (isSplit) splitRef.current();
      else bookmarkRef.current();
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, []);

  // ---- resizing ----
  const resize = (i: number, delta: number) => {
    // Move `delta` (fraction of the whole row) from pane i+1 to pane i.
    const vis = locs.map((_, k) => k).filter(visible);
    const total = vis.reduce((s, k) => s + w[k], 0);
    const pair = w[i] + w[i + 1];
    const min = MIN_SHARE * pair;
    const d = Math.max(min - w[i], Math.min(pair - min - w[i], delta * total));
    setWeights(w.map((x, k) => (k === i ? x + d : k === i + 1 ? x - d : x)));
  };
  const resetPair = (i: number) => {
    const pair = w[i] + w[i + 1];
    setWeights(w.map((x, k) => (k === i || k === i + 1 ? pair / 2 : x)));
  };

  // ---- shared transfers, jobs and uploads ----
  const [conflict, setConflict] = useState<{ count: number; names: string[]; resolve: (c: "rename" | "skip" | null) => void } | null>(null);
  const [drag, setDrag] = useState<DragPayload | null>(null);
  const [selectionBar, setSelectionBar] = useState(false);

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

  /** Copies or moves paths (relative to from.root) into to.path of to.root. */
  const transfer = async (kind: TransferKind, from: Loc, paths: string[], to: Loc): Promise<boolean> => {
    const r = await withConflict<{ jobId?: string }>((oc) => (kind === "copy" ? fileApi.copy(from.root, paths, to.path, oc, to.root) : fileApi.move(from.root, paths, to.path, oc, to.root)));
    if (!r) return false;
    if (r.jobId) toast.info(kind === "copy" ? "Copying in the background" : "Moving in the background");
    else toast.ok(kind === "copy" ? "Copied" : "Moved", `${paths.length} item${paths.length === 1 ? "" : "s"} → ${locLabel(rootList, to)}`);
    refresh();
    return true;
  };

  // ---- render ----
  if (rootList.length === 0 && !loading) {
    return (
      <Card>
        <EmptyState icon={<HardDrive />} title="No file roots configured">
          Add <code className="num">[[files.roots]]</code> entries to the Wargo config, or enable <code className="num">files.show_mounts</code>, to browse files.
        </EmptyState>
      </Card>
    );
  }

  const switcher = count > maxVisible;
  const ids = metaList.map((m) => `files-${m.key}`);

  return (
    <div className="flex h-[calc(100dvh-6.25rem)] gap-4">
      <PageHeader title="Files" />
      <RootsSidebar current={locs[active]} onPick={(l) => navigatePane(active, l)} />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        {switcher && (
          <div role="tablist" aria-label="Panes" className="flex shrink-0 items-center gap-1 overflow-x-auto rounded-lg border border-border bg-surface p-1 [scrollbar-width:none]">
            {locs.map((l, i) => {
              const r = rootList.find((x) => x.id === l.root);
              const name = segments(l.path).at(-1) ?? r?.name ?? "…";
              return (
                <button
                  key={metaList[i].key}
                  type="button"
                  role="tab"
                  aria-selected={i === active}
                  aria-controls={ids[i]}
                  title={locLabel(rootList, l)}
                  onClick={() => activate(i)}
                  className={cn(
                    "flex h-8 min-w-0 max-w-[12rem] flex-1 items-center gap-1.5 rounded-md px-2 text-ui transition-colors",
                    i === active ? "bg-accent-soft font-medium text-fg ring-1 ring-accent/40" : visible(i) ? "bg-surface-3 text-fg" : "text-muted hover:bg-surface-2 hover:text-fg",
                  )}
                >
                  <span className={cn("num flex size-5 shrink-0 items-center justify-center rounded text-2xs font-semibold", i === active ? "bg-accent text-accent-fg" : "bg-surface-3 text-muted")}>{i + 1}</span>
                  <RootIcon root={r} className="size-3.5 shrink-0 text-subtle max-sm:hidden" />
                  <span className="truncate">{name}</span>
                </button>
              );
            })}
          </div>
        )}
        <div className="flex min-h-0 min-w-0 flex-1">
          {locs.map((l, i) => {
            const m = metaList[i];
            const shown = visible(i);
            const nextShown = i + 1 < count && visible(i + 1);
            const others = locs.map((loc, k) => ({ index: k, loc, label: locLabel(rootList, loc) })).filter((o) => o.index !== i);
            return (
              <Fragment key={m.key}>
                <Pane
                  id={ids[i]}
                  index={i}
                  count={count}
                  loc={l}
                  meta={m}
                  onMeta={(p) => updateMeta(i, p)}
                  roots={rootList}
                  active={i === active}
                  concealed={!shown}
                  split={split}
                  onActivate={() => activate(i)}
                  onNavigate={(to) => navigatePane(i, to)}
                  onBack={() => historyStep(i, "back")}
                  onForward={() => historyStep(i, "fwd")}
                  onSplit={count < MAX_PANES ? () => splitPane(i) : undefined}
                  onClose={count > 1 ? () => closePane(i) : undefined}
                  others={others}
                  onTransfer={transfer}
                  drag={drag}
                  onDrag={setDrag}
                  previewName={i === active ? previewName : null}
                  onPreview={setPreview}
                  compact={compact}
                  coarse={coarse}
                  trashAvailable={trashAvailable}
                  onSelectionBar={i === active ? setSelectionBar : noop}
                  style={{ flex: `${w[i]} 1 0%` }}
                />
                {shown && nextShown && (
                  <Resizer index={i} share={w[i] / (w[i] + w[i + 1])} label={`Resize panes ${i + 1} and ${i + 2}`} onResize={(d) => resize(i, d)} onReset={() => resetPair(i)} />
                )}
              </Fragment>
            );
          })}
        </div>
      </div>

      <TransfersPanel jobs={jobs} onDismissJob={dismiss} onClearJobs={clear} raised={selectionBar} />
      <ConflictDialog state={conflict} onResolve={(c) => conflict?.resolve(c)} />
    </div>
  );
}

const noop = () => {};

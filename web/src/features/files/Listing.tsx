// SPDX-License-Identifier: AGPL-3.0-only
import { forwardRef, useEffect, useRef, useState, type KeyboardEvent, type MouseEvent, type PointerEvent } from "react";
import { ArrowDown, ArrowUp, MoreHorizontal } from "lucide-react";
import { ago, bytes, datetime } from "@/lib/format";
import { cn } from "@/lib/cn";
import { Checkbox } from "@/components/ui/form";
import { downloadUrl, joinPath, type Entry } from "./api";
import { FileIcon, kindOf } from "./kinds";

export type SortKey = "name" | "size" | "mtime" | "type" | "owner" | "mode";
export interface Sort {
  key: SortKey;
  dir: "asc" | "desc";
}

export interface ListingProps {
  entries: Entry[];
  view: "list" | "grid";
  root: number;
  dir: string;
  selected: Set<string>;
  focus: number;
  sort: Sort;
  onSort: (k: SortKey) => void;
  coarse: boolean;
  compact: boolean;
  onItemClick: (i: number, e: MouseEvent) => void;
  onItemOpen: (i: number) => void;
  onToggle: (i: number) => void;
  onLongPress: (i: number) => void;
  onMore: (i: number, anchor: HTMLElement) => void;
  onContext: (i: number | null) => void;
  onKeyDown: (e: KeyboardEvent<HTMLDivElement>, cols: number) => void;
  onSelectAll: (on: boolean) => void;
  label: string;
}

const PAGE = 400;

function useLongPress(onLong: () => void, enabled: boolean) {
  const t = useRef<number | undefined>(undefined);
  const fired = useRef(false);
  const start = useRef<{ x: number; y: number } | null>(null);
  const clear = () => window.clearTimeout(t.current);
  return {
    fired,
    handlers: enabled
      ? {
          onPointerDown: (e: PointerEvent) => {
            if (e.pointerType === "mouse") return;
            fired.current = false;
            start.current = { x: e.clientX, y: e.clientY };
            t.current = window.setTimeout(() => {
              fired.current = true;
              navigator.vibrate?.(10);
              onLong();
            }, 450);
          },
          onPointerMove: (e: PointerEvent) => {
            if (start.current && Math.hypot(e.clientX - start.current.x, e.clientY - start.current.y) > 8) clear();
          },
          onPointerUp: clear,
          onPointerCancel: clear,
        }
      : {},
  };
}

function SortHeader({ k, sort, onSort, children, className, align }: { k: SortKey; sort: Sort; onSort: (k: SortKey) => void; children: React.ReactNode; className?: string; align?: "right" }) {
  const active = sort.key === k;
  return (
    <div role="columnheader" aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"} className={cn("flex items-center", align === "right" && "justify-end", className)}>
      <button type="button" onClick={() => onSort(k)} className={cn("inline-flex h-7 items-center gap-1 uppercase tracking-wider hover:text-fg", active && "text-fg", align === "right" && "flex-row-reverse")}>
        {children}
        {active ? sort.dir === "asc" ? <ArrowUp className="size-3" /> : <ArrowDown className="size-3" /> : <span className="size-3" />}
      </button>
    </div>
  );
}

const listCols = "grid-cols-[1.75rem_minmax(0,1fr)_5.5rem_2rem] md:grid-cols-[1.75rem_minmax(0,1fr)_6rem_9.5rem_2rem] lg:grid-cols-[1.75rem_minmax(0,1fr)_6rem_10rem_7rem_2rem] xl:grid-cols-[1.75rem_minmax(0,1fr)_6rem_10rem_7rem_8rem_2rem]";

function Row({
  e,
  i,
  props,
  thumb,
}: {
  e: Entry;
  i: number;
  props: ListingProps;
  thumb: boolean;
}) {
  const sel = props.selected.has(e.name);
  const focused = props.focus === i;
  const lp = useLongPress(() => props.onLongPress(i), props.coarse);
  const selecting = props.selected.size > 0;
  const kind = kindOf(e);
  const click = (ev: MouseEvent) => {
    if (lp.fired.current) {
      lp.fired.current = false;
      return;
    }
    if (props.coarse) {
      if (selecting) props.onToggle(i);
      else props.onItemOpen(i);
      return;
    }
    props.onItemClick(i, ev);
  };
  const common = {
    id: `file-item-${i}`,
    role: "row",
    "aria-selected": sel,
    "data-row": i,
    onClick: click,
    onDoubleClick: () => !props.coarse && props.onItemOpen(i),
    onContextMenu: () => props.onContext(i),
    ...lp.handlers,
  };
  const more = (
    <span role="gridcell" className="flex items-center justify-end">
    <button
      type="button"
      tabIndex={-1}
      aria-label={`Actions for ${e.name}`}
      title="Actions"
      onClick={(ev) => {
        ev.stopPropagation();
        props.onMore(i, ev.currentTarget);
      }}
      onDoubleClick={(ev) => ev.stopPropagation()}
      className={cn(
        "flex size-8 items-center justify-center rounded-md text-subtle hover:bg-surface-3 hover:text-fg",
        !props.coarse && !sel && !focused && "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
      )}
    >
      <MoreHorizontal className="size-4" />
    </button>
    </span>
  );
  const check = (
    <span
      role="gridcell"
      onClick={(ev) => {
        ev.stopPropagation();
        props.onToggle(i);
      }}
      onDoubleClick={(ev) => ev.stopPropagation()}
      className={cn("flex items-center justify-center", !sel && !selecting && !props.coarse && "opacity-0 group-hover:opacity-100", props.coarse && !selecting && "hidden")}
    >
      <Checkbox checked={sel} onCheckedChange={() => {}} aria-label={`Select ${e.name}`} />
    </span>
  );
  const sizeText = e.type === "dir" || e.targetType === "dir" ? "—" : bytes(e.size);
  const linkNote =
    e.type === "symlink" ? (
      <span className={cn("num truncate text-2xs", e.targetType === "outside" ? "text-warn" : e.targetType === "broken" ? "text-crit" : "text-subtle")}>→ {e.target}</span>
    ) : null;

  if (props.view === "grid") {
    return (
      <div
        {...common}
        className={cn(
          "group relative flex cursor-default select-none flex-col overflow-hidden rounded-md border bg-surface transition-colors",
          sel ? "border-accent bg-accent-soft" : "border-border hover:border-border-strong",
          focused && "ring-2 ring-focus/50",
          e.name.startsWith(".") && "opacity-70",
        )}
      >
        <div role="gridcell" aria-label={e.name} className="relative flex aspect-[4/3] items-center justify-center bg-surface-2">
          {thumb ? (
            <img src={downloadUrl(props.root, joinPath(props.dir, e.name), true)} alt="" loading="lazy" decoding="async" className="size-full object-cover" draggable={false} />
          ) : (
            <FileIcon entry={e} className="size-10" strokeWidth={1.25} />
          )}
        </div>
        <div className="absolute left-1.5 top-1.5">{check}</div>
        <div className="absolute right-1 top-1 rounded-md bg-surface/80">{more}</div>
        <div role="gridcell" className="min-w-0 px-2 py-1.5">
          <div className="truncate text-[0.8125rem] font-medium" title={e.name}>
            {e.name}
          </div>
          <div className="num truncate text-2xs text-muted">{kind === "dir" ? (e.type === "symlink" ? `→ ${e.target}` : "Folder") : `${sizeText} · ${ago(e.mtime)}`}</div>
        </div>
      </div>
    );
  }

  if (props.compact) {
    return (
      <div {...common} className={cn("group flex min-h-14 cursor-default select-none items-center gap-3 border-b border-border px-3", sel ? "bg-accent-soft" : "active:bg-surface-2", focused && !props.coarse && "ring-2 ring-inset ring-focus/50")}>
        {selecting && check}
        <div role="gridcell" className="flex min-w-0 flex-1 items-center gap-3">
        <FileIcon entry={e} className="size-5" />
        <div className="min-w-0 flex-1 py-2">
          <div className={cn("truncate text-[0.9375rem]", e.name.startsWith(".") && "text-muted")}>{e.name}</div>
          <div className="num flex min-w-0 gap-1.5 truncate text-xs text-muted">
            {e.type === "symlink" ? linkNote : <span>{kind === "dir" ? datetime(e.mtime) : `${sizeText} · ${ago(e.mtime)}`}</span>}
          </div>
        </div>
        </div>
        {more}
      </div>
    );
  }

  return (
    <div
      {...common}
      className={cn(
        "group grid h-9 cursor-default select-none items-center gap-x-3 border-b border-border px-2 text-[0.8125rem]",
        listCols,
        sel ? "bg-accent-soft" : "hover:bg-surface-2",
        focused && "outline outline-2 -outline-offset-2 outline-focus/60",
      )}
    >
      {check}
      <div role="gridcell" className="flex min-w-0 items-center gap-2">
        <FileIcon entry={e} className="size-4" />
        <span className={cn("truncate", e.name.startsWith(".") && "text-muted")} title={e.name}>
          {e.name}
        </span>
        {linkNote}
      </div>
      <span role="gridcell" className="num text-right text-muted">{sizeText}</span>
      <span role="gridcell" className="num truncate text-muted max-md:hidden" title={datetime(e.mtime)}>
        {datetime(e.mtime)}
      </span>
      <span role="gridcell" className="num text-muted max-lg:hidden" title={e.octal}>
        {e.mode}
      </span>
      <span role="gridcell" className="truncate text-muted max-xl:hidden">
        {e.owner}
        <span className="text-subtle">:{e.group}</span>
      </span>
      {more}
    </div>
  );
}

export const Listing = forwardRef<HTMLDivElement, ListingProps>(function Listing(props, ref) {
  const { entries, view, selected } = props;
  const [limit, setLimit] = useState(PAGE);
  const sentinel = useRef<HTMLDivElement>(null);
  const gridRef = useRef<HTMLDivElement>(null);
  useEffect(() => setLimit(PAGE), [props.dir, props.root]);
  useEffect(() => {
    if (props.focus >= limit) setLimit(Math.ceil((props.focus + 1) / PAGE) * PAGE);
  }, [props.focus, limit]);
  useEffect(() => {
    const el = sentinel.current;
    if (!el) return;
    const io = new IntersectionObserver((es) => es.some((x) => x.isIntersecting) && setLimit((l) => l + PAGE), { rootMargin: "400px" });
    io.observe(el);
    return () => io.disconnect();
  }, [entries.length, limit]);
  useEffect(() => {
    document.getElementById(`file-item-${props.focus}`)?.scrollIntoView({ block: "nearest" });
  }, [props.focus]);

  const cols = () => {
    if (view !== "grid" || !gridRef.current) return 1;
    return getComputedStyle(gridRef.current).gridTemplateColumns.split(" ").filter(Boolean).length || 1;
  };
  const shown = entries.slice(0, limit);
  const all = entries.length > 0 && selected.size === entries.length;

  return (
    <div
      ref={ref}
      role="grid"
      aria-multiselectable="true"
      aria-rowcount={entries.length + (view === "list" && !props.compact ? 1 : 0)}
      aria-label={props.label}
      aria-activedescendant={props.focus >= 0 ? `file-item-${props.focus}` : undefined}
      tabIndex={0}
      onKeyDown={(e) => props.onKeyDown(e, cols())}
      className="min-h-full outline-none"
    >
      {view === "list" && !props.compact && (
        <div role="row" className={cn("sticky top-0 z-10 grid h-8 items-center gap-x-3 border-b border-border bg-surface-2 px-2 text-2xs font-semibold text-subtle", listCols)}>
          <span role="columnheader" className="flex items-center justify-center">
            <Checkbox checked={all ? true : selected.size > 0 ? "indeterminate" : false} onCheckedChange={(v) => props.onSelectAll(v)} aria-label="Select all" />
          </span>
          <SortHeader k="name" sort={props.sort} onSort={props.onSort}>
            Name
          </SortHeader>
          <SortHeader k="size" sort={props.sort} onSort={props.onSort} align="right">
            Size
          </SortHeader>
          <SortHeader k="mtime" sort={props.sort} onSort={props.onSort} className="max-md:hidden">
            Modified
          </SortHeader>
          <SortHeader k="mode" sort={props.sort} onSort={props.onSort} className="max-lg:hidden">
            Permissions
          </SortHeader>
          <SortHeader k="owner" sort={props.sort} onSort={props.onSort} className="max-xl:hidden">
            Owner
          </SortHeader>
          <span role="columnheader" aria-label="Actions" />
        </div>
      )}
      {view === "grid" ? (
        <div ref={gridRef} role="rowgroup" className="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-2 p-2 sm:grid-cols-[repeat(auto-fill,minmax(9.5rem,1fr))] sm:gap-3 sm:p-3">
          {shown.map((e, i) => (
            <Row key={e.name} e={e} i={i} props={props} thumb={kindOf(e) === "image" && e.readable} />
          ))}
        </div>
      ) : (
        shown.map((e, i) => <Row key={e.name} e={e} i={i} props={props} thumb={false} />)
      )}
      {limit < entries.length && (
        <div ref={sentinel} className="py-4 text-center text-xs text-muted">
          Showing {limit.toLocaleString()} of {entries.length.toLocaleString()}…
        </div>
      )}
    </div>
  );
});

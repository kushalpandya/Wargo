// SPDX-License-Identifier: AGPL-3.0-only
// Column order and widths for tables the user can rearrange (Processes and
// the Files list). Pure and import-free so `node --test` can load it.

export interface ColumnDef {
  id: string;
  /** Default width in px (ignored for elastic columns). */
  width?: number;
  /** Smallest width in px (default 48). */
  min?: number;
  /** Pinned columns keep their place: "start" ones first, "end" ones last. */
  pinned?: "start" | "end";
  /** Takes the leftover space; has no stored width and is not resizable. */
  elastic?: boolean;
  /** Width never changes (utility columns such as a checkbox). */
  fixed?: boolean;
}

export interface ColumnLayout {
  order: string[];
  widths: Record<string, number>;
}

const MIN = 48;
const MAX = 1200;
const FALLBACK = 120;

const clamp = (w: number, def: ColumnDef) => Math.min(MAX, Math.max(def.min ?? MIN, Math.round(w)));
const resizable = (def: ColumnDef | undefined) => !!def && !def.elastic && !def.fixed;

function compose(defs: ColumnDef[], movable: string[]): string[] {
  const start = defs.filter((d) => d.pinned === "start").map((d) => d.id);
  const end = defs.filter((d) => d.pinned === "end").map((d) => d.id);
  return [...start, ...movable, ...end];
}

const movableOf = (l: ColumnLayout, defs: ColumnDef[]) => {
  const pinned = new Set(defs.filter((d) => d.pinned).map((d) => d.id));
  return l.order.filter((id) => !pinned.has(id));
};

export function defaultLayout(defs: ColumnDef[]): ColumnLayout {
  const widths: Record<string, number> = {};
  for (const d of defs) if (!d.elastic) widths[d.id] = d.width ?? FALLBACK;
  return { order: defs.map((d) => d.id), widths };
}

/** Turns whatever was saved (possibly from an older build, or junk) into a valid layout. */
export function normalizeLayout(raw: unknown, defs: ColumnDef[]): ColumnLayout {
  const base = defaultLayout(defs);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return base;
  const r = raw as { order?: unknown; widths?: unknown };
  const known = new Map(defs.map((d) => [d.id, d]));

  const saved = Array.isArray(r.order) ? r.order.filter((id): id is string => typeof id === "string") : [];
  const movable: string[] = [];
  for (const id of saved) {
    const d = known.get(id);
    if (d && !d.pinned && !movable.includes(id)) movable.push(id);
  }
  for (const d of defs) if (!d.pinned && !movable.includes(d.id)) movable.push(d.id);

  const widths = { ...base.widths };
  const rw = r.widths && typeof r.widths === "object" && !Array.isArray(r.widths) ? (r.widths as Record<string, unknown>) : {};
  for (const d of defs) {
    const v = rw[d.id];
    if (resizable(d) && typeof v === "number" && Number.isFinite(v) && v > 0) widths[d.id] = clamp(v, d);
  }
  return { order: compose(defs, movable), widths };
}

/** Moves `id` to index `to` among the movable (unpinned) columns. */
export function moveColumn(l: ColumnLayout, defs: ColumnDef[], id: string, to: number): ColumnLayout {
  const d = defs.find((x) => x.id === id);
  if (!d || d.pinned) return l;
  const movable = movableOf(l, defs).filter((x) => x !== id);
  movable.splice(Math.max(0, Math.min(movable.length, to)), 0, id);
  return { ...l, order: compose(defs, movable) };
}

/** Moves `id` one place left (-1) or right (1); no-op at the edges. */
export function shiftColumn(l: ColumnLayout, defs: ColumnDef[], id: string, delta: -1 | 1): ColumnLayout {
  const movable = movableOf(l, defs);
  const i = movable.indexOf(id);
  if (i < 0 || i + delta < 0 || i + delta >= movable.length) return l;
  return moveColumn(l, defs, id, i + delta);
}

export function resizeColumn(l: ColumnLayout, defs: ColumnDef[], id: string, width: number): ColumnLayout {
  const d = defs.find((x) => x.id === id);
  if (!resizable(d) || !Number.isFinite(width)) return l;
  return { ...l, widths: { ...l.widths, [id]: clamp(width, d!) } };
}

/** The saved order, limited to the columns currently shown. */
export function visibleColumns(l: ColumnLayout, visible: Set<string>): string[] {
  return l.order.filter((id) => visible.has(id));
}

/**
 * CSS grid template for `order` (the shown columns) in a container `paneW`
 * px wide. Saved widths are used as they are when they fit; otherwise the
 * resizable ones scale down (for display only) so the row never overflows,
 * and the elastic column gives up its minimum only when nothing else is
 * left. `paneW` 0 means not measured yet: full widths.
 */
export function gridTemplate(order: string[], defs: ColumnDef[], widths: Record<string, number>, paneW: number, { pad, gap }: { pad: number; gap: number }): string {
  const def = (id: string) => defs.find((d) => d.id === id);
  const fixedW = (id: string) => widths[id] ?? def(id)?.width ?? FALLBACK;
  const elastic = order.find((id) => def(id)?.elastic);
  const elasticMin = elastic ? (def(elastic)?.min ?? MIN) : 0;
  const sized = order.filter((id) => id !== elastic && !def(id)?.fixed);
  const fixed = order.filter((id) => def(id)?.fixed).reduce((t, id) => t + fixedW(id), 0);
  const want = sized.reduce((t, id) => t + fixedW(id), 0);
  const avail = Math.max(0, paneW - pad - gap * (order.length - 1) - fixed);

  let k = 1;
  let min = elasticMin;
  if (paneW > 0 && want + elasticMin > avail) {
    // Elastic keeps its minimum if at least half the room is left for it.
    min = Math.floor(Math.min(elasticMin, avail / 2));
    k = want > 0 ? Math.max(0, avail - min) / want : 1;
  }
  return order
    .map((id) => {
      if (id === elastic) return `minmax(${min}px,1fr)`;
      if (def(id)?.fixed) return `${fixedW(id)}px`;
      return `${Math.floor(fixedW(id) * k)}px`;
    })
    .join(" ");
}

// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback, useMemo, useRef, useState, useSyncExternalStore, type CSSProperties, type KeyboardEvent, type MouseEvent, type PointerEvent } from "react";
import { moveColumn, normalizeLayout, resizeColumn, shiftColumn, visibleColumns, type ColumnDef, type ColumnLayout } from "./columns";
import { readPref, setPref, subscribePrefs } from "./prefs";

/** Pointer travel before a header press becomes a drag (below it, the click sorts). */
const DRAG_THRESHOLD = 6;
const KEY_STEP = 16;

export interface DropIndicator {
  id: string;
  side: "before" | "after";
}

function parse(raw: string | null): unknown {
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}

/**
 * Width a cell's content needs, ignoring how wide the cell (or any
 * full-width child) currently is: the span of its text runs and of small
 * leaf elements such as icons and meters, plus the cell's padding.
 */
function contentWidth(cell: HTMLElement): number {
  const box = cell.getBoundingClientRect();
  let left = Infinity;
  let right = -Infinity;
  const take = (r: DOMRect) => {
    if (r.width <= 0) return;
    left = Math.min(left, r.left);
    right = Math.max(right, r.right);
  };
  const walk = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
  const range = document.createRange();
  for (let n = walk.nextNode(); n; n = walk.nextNode()) {
    if (!n.textContent?.trim()) continue;
    range.selectNodeContents(n);
    // Text in a truncated element still lays out at its full width.
    take(range.getBoundingClientRect());
  }
  for (const el of cell.querySelectorAll<HTMLElement | SVGElement>("*")) {
    if (el.children.length || el.closest("[aria-hidden=true][class*=cursor-col-resize]")) continue;
    const r = el.getBoundingClientRect();
    if (r.width < box.width - 2) take(r);
  }
  if (left === Infinity) return 0;
  // Keep the gap on the side the content is anchored to and mirror it on the
  // other: left-aligned text gives the first reading, right-aligned numbers
  // the second; the smaller one is the right one.
  const fromLeft = right - box.left + (left - box.left);
  const fromRight = box.right - left + (box.right - right);
  return Math.ceil(Math.min(fromLeft, fromRight));
}

/**
 * Reorderable, resizable columns remembered per browser under `key`.
 * Headers spread `headerProps(id)` and contain an element with
 * `gripProps(id)` on their right edge; every header and cell carries
 * `data-col`, inside an element with `data-columns-root`. `defs` must be a
 * stable (module-level) array.
 */
export function useColumns(key: string, defs: ColumnDef[]) {
  const raw = useSyncExternalStore(subscribePrefs, () => readPref(key));
  const saved = useMemo(() => normalizeLayout(parse(raw), defs), [raw, defs]);
  // Live layout while a resize is in progress; written to storage on release.
  const [live, setLive] = useState<ColumnLayout | null>(null);
  const layout = live ?? saved;
  const layoutRef = useRef(layout);
  layoutRef.current = layout;

  const [drop, setDrop] = useState<DropIndicator | null>(null);
  const [dragging, setDragging] = useState<string | null>(null);

  const save = useCallback((l: ColumnLayout) => setPref(key, JSON.stringify(l)), [key]);
  const reset = useCallback(() => setPref(key, null), [key]);
  const defOf = useCallback((id: string) => defs.find((d) => d.id === id), [defs]);

  // Depends on the order alone, so a live resize (widths only) does not hand
  // callers a new column list and re-render every row.
  const columnOrder = layout.order;
  const order = useCallback((visible: Set<string>) => visibleColumns({ order: columnOrder, widths: {} }, visible), [columnOrder]);
  const width = useCallback((id: string) => (defOf(id)?.elastic ? undefined : layout.widths[id]), [layout, defOf]);

  /**
   * `scale` is how much the view currently shrinks saved widths (Files does
   * when a pane is too narrow); pointer and key steps are divided by it so
   * the column moves exactly with the pointer.
   */
  const headerProps = (id: string, scale = 1) => {
    const def = defOf(id);
    const movable = !!def && !def.pinned;
    return {
      "data-col": id,
      "data-col-header": "",
      style: (dragging === id ? { opacity: 0.5 } : undefined) as CSSProperties | undefined,
      onPointerDown: (e: PointerEvent<HTMLElement>) => {
        if (!movable || e.button !== 0 || e.pointerType === "touch") return;
        const header = e.currentTarget;
        const root = header.closest("[data-columns-root]");
        const startX = e.clientX;
        let active = false;
        let target: DropIndicator | null = null;
        const onMove = (ev: globalThis.PointerEvent) => {
          if (!active && Math.abs(ev.clientX - startX) <= DRAG_THRESHOLD) return;
          if (!active) {
            active = true;
            setDragging(id);
          }
          target = null;
          const heads = root ? Array.from(root.querySelectorAll<HTMLElement>("[data-col-header]")) : [];
          for (const h of heads) {
            const hid = h.dataset.col!;
            const d = defOf(hid);
            if (!d || d.pinned || hid === id) continue;
            const r = h.getBoundingClientRect();
            if (ev.clientX >= r.left && ev.clientX <= r.right) {
              target = { id: hid, side: ev.clientX < r.left + r.width / 2 ? "before" : "after" };
              break;
            }
          }
          setDrop(target);
        };
        const onUp = () => {
          removeEventListener("pointermove", onMove);
          removeEventListener("pointerup", onUp);
          removeEventListener("pointercancel", onUp);
          if (!active) return;
          // The click that ends a drag must not sort.
          // It fires in the same task as pointerup, before any timer, so drop
          // the listener right after in case no click follows.
          const swallow = (ce: Event) => (ce.stopPropagation(), ce.preventDefault());
          addEventListener("click", swallow, { capture: true, once: true });
          setTimeout(() => removeEventListener("click", swallow, { capture: true }), 0);
          setDragging(null);
          setDrop(null);
          if (target) {
            const l = layoutRef.current;
            const rest = l.order.filter((x) => x !== id && !defOf(x)?.pinned);
            const at = rest.indexOf(target.id) + (target.side === "after" ? 1 : 0);
            save(moveColumn(l, defs, id, at));
          }
        };
        addEventListener("pointermove", onMove);
        addEventListener("pointerup", onUp);
        addEventListener("pointercancel", onUp);
      },
      onKeyDown: (e: KeyboardEvent<HTMLElement>) => {
        // Column options from the keyboard: browsers on macOS never fire
        // contextmenu for Shift+F10, so open the header's menu ourselves.
        if ((e.key === "F10" && e.shiftKey) || e.key === "ContextMenu") {
          e.preventDefault();
          e.stopPropagation();
          const r = e.currentTarget.getBoundingClientRect();
          e.currentTarget.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: r.left + 8, clientY: r.bottom }));
          return;
        }
        if (!e.shiftKey || e.ctrlKey || e.metaKey || (e.key !== "ArrowLeft" && e.key !== "ArrowRight")) return;
        const dir = e.key === "ArrowLeft" ? -1 : 1;
        const l = layoutRef.current;
        const next = e.altKey ? resizeColumn(l, defs, id, (l.widths[id] ?? 0) + (dir * KEY_STEP) / scale) : shiftColumn(l, defs, id, dir);
        if (next !== l) {
          // Handled here: the Files grid must not also treat it as a selection key.
          e.preventDefault();
          e.stopPropagation();
          save(next);
          // Moving the focused header re-inserts its node; WebKit and Gecko
          // drop focus when that happens (Chrome keeps it). Put it back.
          const root = e.currentTarget.closest("[data-columns-root]");
          requestAnimationFrame(() => {
            const h = root?.querySelector<HTMLElement>(`[data-col-header][data-col="${id}"]`);
            if (h && !h.contains(document.activeElement)) (h.querySelector<HTMLElement>("button") ?? h).focus();
          });
        }
      },
    };
  };

  /** Keyboard help for the header's focusable control (its aria-description). */
  const describe = (id: string) => {
    const def = defOf(id);
    if (!def) return undefined;
    const move = !def.pinned;
    const size = !def.elastic && !def.fixed;
    const menu = "Shift and F10 for column options.";
    if (move && size) return `Shift and arrow keys move this column; Alt, Shift and arrow keys resize it. ${menu}`;
    if (move) return `Shift and arrow keys move this column. ${menu}`;
    if (size) return `Alt, Shift and arrow keys resize this column. ${menu}`;
    return menu;
  };

  const gripProps = (id: string, scale = 1) => {
    const def = defOf(id);
    const can = !!def && !def.elastic && !def.fixed;
    return {
      "aria-hidden": true as const,
      hidden: !can,
      className:
        "absolute -right-1 top-0 z-10 h-full w-2 cursor-col-resize touch-none select-none after:absolute after:inset-y-1.5 after:left-1/2 after:w-px after:-translate-x-1/2 after:bg-transparent hover:after:bg-accent active:after:bg-accent",
      onClick: (e: MouseEvent) => e.stopPropagation(),
      onPointerDown: (e: PointerEvent<HTMLElement>) => {
        if (!can || e.button !== 0) return;
        e.stopPropagation();
        e.preventDefault();
        const grip = e.currentTarget;
        grip.setPointerCapture(e.pointerId);
        const header = grip.closest<HTMLElement>("[data-col-header]");
        const startX = e.clientX;
        const startW = layoutRef.current.widths[id] ?? header?.getBoundingClientRect().width ?? 0;
        const start = layoutRef.current;
        let last = start;
        const onMove = (ev: globalThis.PointerEvent) => {
          last = resizeColumn(layoutRef.current, defs, id, startW + (ev.clientX - startX) / scale);
          setLive(last);
        };
        const onUp = () => {
          for (const t of ["pointermove", "pointerup", "pointercancel", "lostpointercapture"] as const) grip.removeEventListener(t, t === "pointermove" ? onMove : onUp);
          // A plain click changes nothing; saving would freeze today's
          // defaults into storage.
          if (last !== start) save(last);
          setLive(null);
        };
        grip.addEventListener("pointermove", onMove);
        grip.addEventListener("pointerup", onUp);
        grip.addEventListener("pointercancel", onUp);
        // Also ends the resize if the grip goes away mid-drag (the table
        // re-rendered into another state), so the live layout never sticks.
        grip.addEventListener("lostpointercapture", onUp);
      },
      onDoubleClick: (e: MouseEvent<HTMLElement>) => {
        e.stopPropagation();
        if (!can) return;
        const root = e.currentTarget.closest("[data-columns-root]");
        if (!root) return;
        // Widest content among the rendered cells of this column (and its label).
        let w = 0;
        for (const cell of root.querySelectorAll<HTMLElement>(`[data-col="${id}"]`)) w = Math.max(w, contentWidth(cell));
        if (w > 0) save(resizeColumn(layoutRef.current, defs, id, w + 4));
      },
    };
  };

  return { layout, order, width, reset, headerProps, gripProps, describe, drop, resizing: live !== null };
}

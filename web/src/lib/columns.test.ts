// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/lib/columns.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { defaultLayout, fitScale, gridTemplate, moveColumn, normalizeLayout, resizeColumn, shiftColumn, visibleColumns, type ColumnDef } from "./columns.ts";

const defs: ColumnDef[] = [
  { id: "select", width: 28, pinned: "start", fixed: true },
  { id: "name", elastic: true, pinned: "start", min: 160 },
  { id: "size", width: 96 },
  { id: "mtime", width: 160 },
  { id: "mode", width: 112, min: 64 },
  { id: "actions", width: 32, pinned: "end", fixed: true },
];

test("defaultLayout keeps def order and default widths, none for elastic", () => {
  assert.deepEqual(defaultLayout(defs), {
    order: ["select", "name", "size", "mtime", "mode", "actions"],
    widths: { select: 28, size: 96, mtime: 160, mode: 112, actions: 32 },
  });
});

test("normalizeLayout falls back to the default for junk", () => {
  for (const raw of ["junk", null, undefined, 5, { order: 5 }, { order: ["size"], widths: "x" }, []]) {
    const got = normalizeLayout(raw, defs);
    assert.deepEqual(got.order, defaultLayout(defs).order, `order for ${JSON.stringify(raw)}`);
  }
  assert.deepEqual(normalizeLayout("junk", defs), defaultLayout(defs));
});

test("normalizeLayout drops unknown ids, appends missing ones, keeps pinned in place", () => {
  const got = normalizeLayout({ order: ["actions", "mode", "ghost", "size", "name"], widths: {} }, defs);
  assert.deepEqual(got.order, ["select", "name", "mode", "size", "mtime", "actions"]);
});

test("normalizeLayout sanitises widths", () => {
  const got = normalizeLayout({ order: [], widths: { size: -5, mtime: Number.NaN, mode: 10, name: 400, select: 999, ghost: 50 } }, defs);
  assert.equal(got.widths.size, 96, "negative falls back to default");
  assert.equal(got.widths.mtime, 160, "NaN falls back to default");
  assert.equal(got.widths.mode, 64, "below min clamps to min");
  assert.equal(got.widths.select, 28, "fixed widths stay at their default");
  assert.equal("name" in got.widths, false, "elastic never has a width");
  assert.equal("ghost" in got.widths, false, "unknown dropped");
});

test("moveColumn moves among movable columns only", () => {
  const l = defaultLayout(defs);
  assert.deepEqual(moveColumn(l, defs, "mode", 0).order, ["select", "name", "mode", "size", "mtime", "actions"]);
  assert.deepEqual(moveColumn(l, defs, "size", 99).order, ["select", "name", "mtime", "mode", "size", "actions"]);
  assert.deepEqual(moveColumn(l, defs, "name", 2), l, "pinned start cannot move");
  assert.deepEqual(moveColumn(l, defs, "actions", 0), l, "pinned end cannot move");
  assert.deepEqual(moveColumn(l, defs, "ghost", 0), l, "unknown id is a no-op");
});

test("shiftColumn steps one place and stops at the edges", () => {
  const l = defaultLayout(defs);
  assert.deepEqual(shiftColumn(l, defs, "mtime", -1).order, ["select", "name", "mtime", "size", "mode", "actions"]);
  assert.deepEqual(shiftColumn(l, defs, "size", -1), l);
  assert.deepEqual(shiftColumn(l, defs, "mode", 1), l);
});

test("resizeColumn clamps and ignores elastic and fixed columns", () => {
  const l = defaultLayout(defs);
  assert.equal(resizeColumn(l, defs, "size", 300).widths.size, 300);
  assert.equal(resizeColumn(l, defs, "size", 5).widths.size, 48, "default min is 48");
  assert.equal(resizeColumn(l, defs, "size", 99999).widths.size, 1200, "max is 1200");
  assert.deepEqual(resizeColumn(l, defs, "name", 500), l, "elastic ignores resize");
  assert.deepEqual(resizeColumn(l, defs, "select", 100), l, "fixed ignores resize");
  const pinnedName: ColumnDef[] = [{ id: "name", width: 200, pinned: "start" }, { id: "cmd", elastic: true }];
  assert.equal(resizeColumn(defaultLayout(pinnedName), pinnedName, "name", 260).widths.name, 260, "pinned but not fixed resizes");
});

test("visibleColumns keeps saved relative order", () => {
  const l = moveColumn(defaultLayout(defs), defs, "mode", 0);
  assert.deepEqual(visibleColumns(l, new Set(["select", "name", "size", "mode", "actions"])), ["select", "name", "mode", "size", "actions"]);
});

// Files list: grid template for the shown columns in a pane of a given width.
const filesDefs: ColumnDef[] = [
  { id: "select", width: 28, pinned: "start", fixed: true },
  { id: "name", elastic: true, pinned: "start", min: 160 },
  { id: "size", width: 96 },
  { id: "mtime", width: 160 },
  { id: "actions", width: 32, pinned: "end", fixed: true },
];
const total = (tpl: string) => tpl.split(" ").reduce((t, part) => t + (part.startsWith("minmax(") ? Number(part.slice(7).split("px")[0]) : Number(part.replace("px", ""))), 0);
const order = ["select", "name", "size", "mtime", "actions"];
const opts = { pad: 16, gap: 12 };

test("gridTemplate keeps saved widths when they fit", () => {
  const widths = { size: 96, mtime: 160, select: 28, actions: 32 };
  assert.equal(gridTemplate(order, filesDefs, widths, 900, opts), "28px minmax(160px,1fr) 96px 160px 32px");
});

test("gridTemplate never overflows a narrow pane, even with huge saved widths", () => {
  const widths = { size: 400, mtime: 600, select: 28, actions: 32 };
  for (const pane of [200, 260, 310, 520, 900]) {
    const tpl = gridTemplate(order, filesDefs, widths, pane, opts);
    assert.ok(total(tpl) + opts.pad + opts.gap * (order.length - 1) <= pane, `pane ${pane}: ${tpl}`);
  }
});

test("gridTemplate keeps the elastic minimum when the pane allows it", () => {
  const tpl = gridTemplate(order, filesDefs, { size: 400, mtime: 600 }, 900, opts);
  assert.match(tpl, /minmax\(160px,1fr\)/);
});

test("gridTemplate uses full widths before the pane is measured", () => {
  assert.equal(gridTemplate(order, filesDefs, { size: 400, mtime: 600, select: 28, actions: 32 }, 0, opts), "28px minmax(160px,1fr) 400px 600px 32px");
});

test("fitScale is 1 when saved widths fit and below 1 when they don't", () => {
  assert.equal(fitScale(order, filesDefs, { size: 96, mtime: 160 }, 900, opts).k, 1);
  const tight = fitScale(order, filesDefs, { size: 400, mtime: 600 }, 520, opts);
  assert.ok(tight.k > 0 && tight.k < 1, `k ${tight.k}`);
});

test("gridTemplate honours a given scale (held while a resize is in progress)", () => {
  assert.equal(gridTemplate(order, filesDefs, { size: 200, mtime: 100, select: 28, actions: 32 }, 520, opts, { k: 0.5, min: 160 }), "28px minmax(160px,1fr) 100px 50px 32px");
});

// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/notifications/format.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { badgeText, sortActive } from "./format.ts";
import type { Active } from "./api.ts";

test("badgeText hides zero and caps at 99+", () => {
  assert.equal(badgeText(0), "");
  assert.equal(badgeText(5), "5");
  assert.equal(badgeText(99), "99");
  assert.equal(badgeText(150), "99+");
});

const active = (id: string, startedAt: string): Active => ({
  id,
  category: "download.started",
  title: id,
  detail: "",
  progress: 0,
  startedAt,
  link: "",
});

test("sortActive puts the newest first without changing its input", () => {
  const list = [active("old", "2026-11-13T08:00:00Z"), active("new", "2026-11-13T10:00:00Z"), active("mid", "2026-11-13T09:00:00Z")];
  assert.deepEqual(
    sortActive(list).map((a) => a.id),
    ["new", "mid", "old"],
  );
  assert.deepEqual(
    list.map((a) => a.id),
    ["old", "new", "mid"],
  );
});

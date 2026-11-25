// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/torrents/finished.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { confirmPlan, countFinished, isFinished } from "./finished.ts";
import type { Torrent, TorrentStatus } from "./api.ts";

// The same table as Go TestFinished in internal/integrations/autoremove_test.go.
const cases: { status: TorrentStatus; percentDone: number; error: number; want: boolean }[] = [
  { status: "seeding", percentDone: 1, error: 0, want: true },
  { status: "seed-wait", percentDone: 1, error: 0, want: true },
  { status: "stopped", percentDone: 1, error: 0, want: true },
  { status: "stopped", percentDone: 0.99, error: 0, want: false },
  { status: "checking", percentDone: 1, error: 0, want: false },
  { status: "check-wait", percentDone: 1, error: 0, want: false },
  { status: "seeding", percentDone: 1, error: 3, want: false },
  { status: "seeding", percentDone: 1, error: 2, want: true },
  { status: "downloading", percentDone: 0.5, error: 0, want: false },
  { status: "downloading", percentDone: 1, error: 0, want: false },
  { status: "download-wait", percentDone: 1, error: 0, want: false },
  { status: "unknown", percentDone: 1, error: 0, want: false },
];

test("isFinished matches the server rule", () => {
  for (const c of cases) {
    assert.equal(isFinished(c), c.want, `${c.status} ${c.percentDone} error ${c.error}`);
  }
});

test("countFinished counts the finished ones", () => {
  assert.equal(countFinished(cases as unknown as Torrent[]), 4);
  assert.equal(countFinished([]), 0);
});

test("confirmPlan saves at once only when nothing is finished", () => {
  assert.deepEqual(confirmPlan(0), { save: true });
});

test("confirmPlan asks with the generic text when the count is unknown", () => {
  assert.deepEqual(confirmPlan(null), { confirm: "Any finished torrents will be removed now. Their files are kept." });
});

test("confirmPlan asks with the count otherwise", () => {
  assert.deepEqual(confirmPlan(1), { confirm: "1 finished torrent will be removed now. Their files are kept." });
  assert.deepEqual(confirmPlan(3), { confirm: "3 finished torrents will be removed now. Their files are kept." });
});

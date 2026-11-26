// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/system/health.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { healthFacts } from "./health.ts";

test("healthFacts lists wear, hours and spare, then warning notes", () => {
  assert.deepEqual(healthFacts({ status: "warn", wearPct: 4, powerOnHours: 6802, sparePct: 100, notes: ["2 media errors"] }), [
    "wear 4%",
    `${(6802).toLocaleString()} h on`,
    "spare 100%",
    "2 media errors",
  ]);
});

test("healthFacts is empty when the drive reports nothing", () => {
  assert.deepEqual(healthFacts({ status: "unknown", reason: "needs root" }), []);
});

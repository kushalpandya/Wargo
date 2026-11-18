// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/system/sections.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { SECTIONS, sectionOf } from "./sections.ts";

test("sectionOf names only the System Monitor sections", () => {
  assert.equal(sectionOf("#cpu-memory"), SECTIONS.cpuMemory);
  assert.equal(sectionOf("#network"), SECTIONS.network);
  assert.equal(sectionOf("#storage"), SECTIONS.storage);
  assert.equal(sectionOf("storage"), SECTIONS.storage);
  assert.equal(sectionOf(""), null);
  assert.equal(sectionOf("#main"), null); // an element on the page, but not a section
});

// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/app/returnTo.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { returnTo } from "./returnTo.ts";

test("returnTo is the page the sign-in interrupted", () => {
  assert.equal(returnTo({ from: "/approve/abc" }), "/approve/abc");
  assert.equal(returnTo({ from: "/settings/general?x=1#y" }), "/settings/general?x=1#y");
});

test("returnTo falls back to home for anything else", () => {
  const others = [null, undefined, "x", {}, { from: 5 }, { from: "" }, { from: "https://evil.example/" }, { from: "//evil.example" }, { from: "/\\evil.example" }, { from: "/login" }];
  for (const s of others) assert.equal(returnTo(s), "/", JSON.stringify(s));
});

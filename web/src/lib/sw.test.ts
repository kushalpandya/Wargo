// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/lib/sw.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { shouldApply, shouldRegister, shouldReloadForChunk, shouldReloadOnControllerChange } from "./sw.ts";

const ok = { prod: true, secure: true, supported: true, base: "/" };

test("shouldRegister only for production, secure, supported, root base", () => {
  assert.equal(shouldRegister(ok), true);
  assert.equal(shouldRegister({ ...ok, prod: false }), false);
  assert.equal(shouldRegister({ ...ok, secure: false }), false); // plain http on the LAN
  assert.equal(shouldRegister({ ...ok, supported: false }), false);
  assert.equal(shouldRegister({ ...ok, base: "/Wargo/" }), false); // gh-pages demo
});

test("shouldReloadForChunk reloads once per minute at most", () => {
  assert.equal(shouldReloadForChunk(null, 100_000), true);
  assert.equal(shouldReloadForChunk(90_000, 100_000), false);
  assert.equal(shouldReloadForChunk(30_000, 100_000), true);
});

test("shouldApply only takes over with a worker already waiting on a controlled page", () => {
  assert.equal(shouldApply({ waiting: true, controlled: true }), true);
  assert.equal(shouldApply({ waiting: false, controlled: true }), false); // still installing: next launch or resume
  assert.equal(shouldApply({ waiting: true, controlled: false }), false); // first install
});

test("shouldReloadOnControllerChange reloads the tab that asked for the update right away", () => {
  assert.equal(shouldReloadOnControllerChange({ requested: true, hidden: false, controlledBefore: true }), "reload");
});

test("shouldReloadOnControllerChange reloads a hidden tab right away", () => {
  assert.equal(shouldReloadOnControllerChange({ requested: false, hidden: true, controlledBefore: true }), "reload");
});

test("shouldReloadOnControllerChange waits for another visible tab to be hidden", () => {
  // Another tab took the update: this one is in use, so it reloads once it goes to the background.
  assert.equal(shouldReloadOnControllerChange({ requested: false, hidden: false, controlledBefore: true }), "when-hidden");
});

test("shouldReloadOnControllerChange never reloads on a first install", () => {
  assert.equal(shouldReloadOnControllerChange({ requested: false, hidden: false, controlledBefore: false }), "none");
  assert.equal(shouldReloadOnControllerChange({ requested: false, hidden: true, controlledBefore: false }), "none");
  assert.equal(shouldReloadOnControllerChange({ requested: true, hidden: false, controlledBefore: false }), "none");
});

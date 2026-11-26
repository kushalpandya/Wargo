// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/settings/saveOptions.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { MutationObserver, QueryClient } from "@tanstack/react-query";
import { nextSettings, settingsSaveOptions, type SettingsSave } from "./saveOptions.ts";

type S = { enabled: boolean; checkHours: number };
const key = ["s"];

/** A put whose replies the test sends by hand, in any order. */
function fakePut() {
  const calls: { body: S; resolve: (v: S) => void; reject: (e: Error) => void }[] = [];
  const put = (body: S) => new Promise<S>((resolve, reject) => calls.push({ body, resolve, reject }));
  return { calls, put };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

function setup(start: S) {
  const qc = new QueryClient();
  qc.setQueryData(key, start);
  const { calls, put } = fakePut();
  const failed: Error[] = [];
  const o: SettingsSave<S, S> = { queryKey: key, scope: "test", put };
  const obs = new MutationObserver(qc, settingsSaveOptions(qc, o, (e) => failed.push(e)));
  const update = (patch: Partial<S>) => {
    const next = nextSettings(qc, o, patch);
    if (next) obs.mutate(next).catch(() => {});
  };
  return { qc, calls, failed, update, shown: () => qc.getQueryData<S>(key) };
}

test("a quick second change is neither lost nor briefly undone, and saves go in order", async () => {
  const { calls, update, shown } = setup({ enabled: false, checkHours: 24 });
  update({ enabled: true });
  await tick();
  assert.deepEqual(shown(), { enabled: true, checkHours: 24 });
  update({ checkHours: 12 });
  await tick();
  assert.deepEqual(shown(), { enabled: true, checkHours: 12 });
  // The second save waits for the first, and carries both changes.
  assert.equal(calls.length, 1);
  calls[0].resolve({ enabled: true, checkHours: 24 });
  await tick();
  assert.deepEqual(shown(), { enabled: true, checkHours: 12 }, "the first reply must not undo the second change");
  assert.equal(calls.length, 2);
  assert.deepEqual(calls[1].body, { enabled: true, checkHours: 12 });
  calls[1].resolve({ enabled: true, checkHours: 12 });
  await tick();
  assert.deepEqual(shown(), { enabled: true, checkHours: 12 });
});

test("a failed save puts back the value from before it", async () => {
  const { calls, failed, update, shown } = setup({ enabled: true, checkHours: 24 });
  update({ enabled: false });
  await tick();
  assert.deepEqual(shown(), { enabled: false, checkHours: 24 });
  calls[0].reject(new Error("nope"));
  await tick();
  assert.equal(failed.length, 1);
  assert.deepEqual(shown(), { enabled: true, checkHours: 24 });
});

test("a part of the cached data can be saved", async () => {
  const qc = new QueryClient();
  qc.setQueryData(key, { other: 1, settings: { enabled: false, checkHours: 6 } });
  const { calls, put } = fakePut();
  type D = { other: number; settings: S };
  const o: SettingsSave<S, D> = { queryKey: key, scope: "part", put, get: (d) => d.settings, set: (d, settings) => ({ ...d, settings }) };
  const obs = new MutationObserver(qc, settingsSaveOptions(qc, o, () => {}));
  obs.mutate(nextSettings(qc, o, { enabled: true })!);
  await tick();
  assert.deepEqual(qc.getQueryData(key), { other: 1, settings: { enabled: true, checkHours: 6 } });
  calls[0].resolve({ enabled: true, checkHours: 6 });
  await tick();
  assert.deepEqual(qc.getQueryData(key), { other: 1, settings: { enabled: true, checkHours: 6 } });
});

test("nothing is saved before the settings load", () => {
  const qc = new QueryClient();
  assert.equal(nextSettings(qc, { queryKey: key, scope: "none", put: async (v: S) => v }, { enabled: true }), undefined);
});

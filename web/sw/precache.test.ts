// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test sw/precache.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { cacheVersion, precacheList } from "./precache.ts";

test("precacheList keeps app files, root-relative, sorted, without sw.js or maps", () => {
  assert.deepEqual(
    precacheList([
      "sw.js",
      "index.html",
      "assets/index-abc.js",
      "assets/index-abc.js.map",
      "icon-192.png",
      "manifest.webmanifest",
      ".DS_Store",
    ]),
    ["/", "/assets/index-abc.js", "/icon-192.png", "/index.html", "/manifest.webmanifest"],
  );
});

test("cacheVersion changes when any file changes and ignores order", () => {
  const a = cacheVersion([
    { path: "a.js", hash: "1" },
    { path: "b.js", hash: "2" },
  ]);
  assert.equal(
    a,
    cacheVersion([
      { path: "b.js", hash: "2" },
      { path: "a.js", hash: "1" },
    ]),
  );
  assert.notEqual(
    a,
    cacheVersion([
      { path: "a.js", hash: "1" },
      { path: "b.js", hash: "3" },
    ]),
  );
  assert.match(a, /^[0-9a-f]{12}$/);
});

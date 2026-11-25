// SPDX-License-Identifier: AGPL-3.0-only
// Run: cd web && node --test src/features/torrents/add.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { isMagnet, splitTorrentFiles, torrentFileError, toBase64 } from "./add.ts";

test("isMagnet accepts magnet links, trimmed", () => {
  assert.equal(isMagnet("magnet:?xt=urn:btih:abc"), true);
  assert.equal(isMagnet("  magnet:?xt=urn:btih:abc  "), true);
});

test("isMagnet rejects other text and oversized links", () => {
  assert.equal(isMagnet("http://x/a.torrent"), false);
  assert.equal(isMagnet(""), false);
  assert.equal(isMagnet("magnet:?" + "a".repeat(16385)), false);
});

test("torrentFileError checks the name and size", () => {
  assert.equal(torrentFileError({ name: "a.torrent", size: 10 }), null);
  assert.equal(torrentFileError({ name: "A.TORRENT", size: 10 }), null);
  assert.equal(torrentFileError({ name: "a.txt", size: 10 }), "Not a .torrent file");
  assert.equal(torrentFileError({ name: "a.torrent", size: 10 * 1024 * 1024 + 1 }), "Larger than 10 MB");
});

test("toBase64 encodes bytes", () => {
  assert.equal(toBase64(new TextEncoder().encode("d4:infoe").buffer as ArrayBuffer), "ZDQ6aW5mb2U=");
});

test("splitTorrentFiles passes .torrent files on and rejects the rest", () => {
  const a = { name: "a.torrent", size: 10 };
  const big = { name: "B.TORRENT", size: 11 * 1024 * 1024 };
  const txt = { name: "notes.txt", size: 10 };
  assert.deepEqual(splitTorrentFiles([a, txt, big]), { accepted: [a, big], rejected: [{ file: txt, reason: "Not a .torrent file" }] });
  assert.deepEqual(splitTorrentFiles([]), { accepted: [], rejected: [] });
});

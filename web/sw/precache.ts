// SPDX-License-Identifier: AGPL-3.0-only
// Build-time helpers for the service worker (see sw/plugin.ts).
import { createHash } from "node:crypto";

/**
 * The app files the worker stores, as root-relative URLs, from the paths in
 * dist/. "/" is listed too: navigations are answered with index.html under
 * that key.
 */
export function precacheList(paths: string[]): string[] {
  const keep = paths.filter((p) => p !== "sw.js" && !p.endsWith(".map") && !p.split("/").some((s) => s.startsWith(".")));
  return ["/", ...keep.map((p) => "/" + p)].sort();
}

/** A short version that changes whenever any stored file does. */
export function cacheVersion(entries: { path: string; hash: string }[]): string {
  const lines = entries.map((e) => `${e.path}:${e.hash}`).sort();
  return createHash("sha256").update(lines.join("\n")).digest("hex").slice(0, 12);
}

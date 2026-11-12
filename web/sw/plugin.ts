// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";
import { cacheVersion, precacheList } from "./precache.ts";

const template = fileURLToPath(new URL("./sw.js", import.meta.url));

function walk(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)]));
}

/**
 * Writes dist/sw.js: sw/sw.js with the build version and the list of app
 * files to store. Only for production builds served at "/" (the gh-pages
 * demo builds under another base and runs its own MSW worker).
 */
export function serviceWorker(): Plugin {
  let base = "/";
  let outDir = "dist";
  return {
    name: "wargo-service-worker",
    apply: "build",
    configResolved(c) {
      base = c.base;
      outDir = c.build.outDir.startsWith("/") ? c.build.outDir : join(c.root, c.build.outDir);
    },
    // After Vite has written the bundle and copied public/.
    writeBundle() {
      if (base !== "/") return;
      const paths = walk(outDir)
        .map((f) => relative(outDir, f).split(sep).join("/"))
        .filter((p) => p !== "sw.js");
      const entries = paths.map((p) => ({
        path: p,
        hash: createHash("sha256")
          .update(readFileSync(join(outDir, p)))
          .digest("hex"),
      }));
      const src = readFileSync(template, "utf8")
        .replace("__WARGO_SW_VERSION__", JSON.stringify(cacheVersion(entries)))
        .replace("__WARGO_SW_FILES__", JSON.stringify(precacheList(paths)));
      writeFileSync(join(outDir, "sw.js"), src);
    },
  };
}

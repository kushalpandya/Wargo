// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "node:url";

const api = process.env.WARGO_API ?? "http://127.0.0.1:2207";

/** Preloads the main UI font (latin Plus Jakarta Sans) so text doesn't reflow on first paint. */
function preloadFont(): Plugin {
  let base = "/";
  return {
    name: "wargo-preload-font",
    apply: "build",
    configResolved(c) {
      base = c.base;
    },
    transformIndexHtml(_html, ctx) {
      const file = Object.keys(ctx.bundle ?? {}).find((f) => /plus-jakarta-sans-latin-wght-normal-[\w-]+\.woff2$/.test(f));
      if (!file) return;
      return [{ tag: "link", attrs: { rel: "preload", href: `${base}${file}`, as: "font", type: "font/woff2", crossorigin: "" }, injectTo: "head" }];
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), preloadFont()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    proxy: {
      "/api": { target: api, changeOrigin: false },
      "/ws": { target: api.replace(/^http/, "ws"), ws: true },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    assetsInlineLimit: 0,
    chunkSizeWarningLimit: 700,
  },
});

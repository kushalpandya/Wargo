// SPDX-License-Identifier: AGPL-3.0-only
// Vite config for the demo site: Wargo's own config, served under /Wargo/.
// Copied next to Wargo's vite.config.ts by build.sh (and dev.sh).
import { defineConfig, mergeConfig } from "vite";
import app from "./vite.config";

export default mergeConfig(
  app,
  defineConfig({
    base: process.env.WARGO_DEMO_BASE ?? "/Wargo/",
    // dev.sh links src/demo to this branch's sources; resolve imports from
    // the link, not its target, so they find Wargo's node_modules.
    resolve: { preserveSymlinks: true },
    define: {
      __WARGO_DEMO_REF__: JSON.stringify(process.env.WARGO_REF ?? "dev"),
    },
  }),
);

// SPDX-License-Identifier: AGPL-3.0-only
// Demo entry point (index.html points here instead of src/main.tsx): starts
// the in-browser fake backend, then loads the real Wargo app unchanged.
import { setupWorker } from "msw/browser";
import { handlers } from "./worker";
import { mountBanner } from "./banner";
import { installDownloads } from "./downloads";
import { installLoginHint } from "./login-hint";

async function start() {
  const base = import.meta.env.BASE_URL;
  const worker = setupWorker(...handlers);
  await worker.start({
    serviceWorker: { url: `${base}mockServiceWorker.js`, options: { scope: base } },
    quiet: true,
  });
  mountBanner();
  installDownloads();
  installLoginHint();
  await import("../main");
}

start().catch((e) => {
  document.body.innerHTML = `<p style="font:14px system-ui;padding:2rem">The demo couldn't start: ${String(e?.message ?? e)}. It needs a browser with service workers (not a private window in some browsers).</p>`;
});

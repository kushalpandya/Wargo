// SPDX-License-Identifier: AGPL-3.0-only
// A small fixed note so nobody mistakes the demo for a real server.
import { resetDemo } from "./core/store";

export function mountBanner() {
  const el = document.createElement("div");
  el.setAttribute("role", "note");
  el.setAttribute("aria-label", "Demo");
  el.className =
    "fixed bottom-3 left-3 z-[65] flex max-w-[calc(100vw-1.5rem)] items-center gap-2 rounded-full border border-accent/40 bg-surface/95 py-1 pl-3 pr-1 text-xs text-muted shadow-lg backdrop-blur max-md:bottom-[calc(env(safe-area-inset-bottom)+0.75rem)]";
  el.innerHTML = `
    <span class="size-1.5 shrink-0 rounded-full bg-accent" aria-hidden="true"></span>
    <span><strong class="font-semibold text-fg">Demo</strong> with made-up data<span class="max-sm:hidden"> · sign in with <span class="num text-fg">demo</span> / <span class="num text-fg">demo</span></span></span>
    <button type="button" class="inline-flex h-6 items-center rounded-full px-2 font-medium text-accent hover:bg-accent-soft focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25" title="Forget every change made in this demo">Reset</button>
    <a href="https://github.com/kushalpandya/wargo" class="inline-flex h-6 items-center rounded-full px-2 font-medium text-accent hover:bg-accent-soft focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25" target="_blank" rel="noopener noreferrer">GitHub</a>`;
  el.querySelector("button")!.addEventListener("click", () => {
    if (confirm("Reset the demo? Every change you made here is forgotten.")) resetDemo();
  });
  document.body.appendChild(el);
}

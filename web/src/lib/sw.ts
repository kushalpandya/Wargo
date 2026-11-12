// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Service worker registration (see web/sw/sw.js). The worker keeps the app's
 * own files on the device so the installed Home Screen app opens without
 * waiting on the network. New releases are applied when the app is opened or
 * comes back to the foreground, never while it is in use.
 */

/** Only real Wargo over HTTPS (or localhost): not dev, not plain http, not the gh-pages demo. */
export function shouldRegister(env: { prod: boolean; secure: boolean; supported: boolean; base: string }): boolean {
  return env.prod && env.secure && env.supported && env.base === "/";
}

/** At most one automatic reload a minute for a missing route chunk, so it can never loop. */
export function shouldReloadForChunk(lastReloadAt: number | null, now: number): boolean {
  return lastReloadAt === null || now - lastReloadAt >= 60_000;
}

/**
 * A new version takes over only if it has finished installing by the time
 * the app is opened or resumed; one still downloading waits for the next
 * launch or resume, so the app never reloads while it is being used. A first
 * install (no controller yet) never reloads.
 */
export function shouldApply(s: { waiting: boolean; controlled: boolean }): boolean {
  return s.waiting && s.controlled;
}

/** Tells a waiting worker to take over; the page reloads on controllerchange. */
function apply(reg: ServiceWorkerRegistration) {
  if (reg.waiting && shouldApply({ waiting: true, controlled: !!navigator.serviceWorker.controller })) {
    reg.waiting.postMessage({ type: "SKIP_WAITING" });
  }
}

export function registerServiceWorker(): void {
  if (
    !shouldRegister({
      prod: import.meta.env.PROD,
      secure: window.isSecureContext,
      supported: "serviceWorker" in navigator,
      base: import.meta.env.BASE_URL,
    })
  ) {
    return;
  }
  const sw = navigator.serviceWorker;
  // A first install takes control without a reload; a new version taking
  // over from an old one reloads once onto the new files.
  let controlled = !!sw.controller;
  let reloading = false;
  sw.addEventListener("controllerchange", () => {
    if (controlled && !reloading) {
      reloading = true;
      window.location.reload();
    }
    controlled = true;
  });
  sw.register("/sw.js", { scope: "/", updateViaCache: "none" })
    .then((reg) => {
      apply(reg); // launch
      // iOS resumes a Home Screen app instead of reloading it, so check for
      // a new release whenever it comes back to the foreground.
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState !== "visible") return;
        reg
          .update()
          .then(() => apply(reg))
          .catch(() => {});
      });
    })
    .catch(() => {});
}

const chunkReloadKey = "wargo.chunkReloadAt";

/** Reloads once when a lazily loaded route chunk is gone (a page still running an older build). */
export function reloadOnStaleChunk(): void {
  window.addEventListener("vite:preloadError", (e) => {
    let last: number | null = null;
    try {
      const v = window.sessionStorage.getItem(chunkReloadKey);
      last = v ? Number(v) : null;
    } catch {
      return; // without storage the guard can't hold across reloads, so never reload
    }
    const now = Date.now();
    if (!shouldReloadForChunk(last, now)) return;
    try {
      window.sessionStorage.setItem(chunkReloadKey, String(now));
    } catch {
      return;
    }
    e.preventDefault();
    window.location.reload();
  });
}

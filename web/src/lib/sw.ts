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

/**
 * What a tab does when a new version takes over. The worker claims every open
 * tab at once, so only the tab that asked for the update (it is being opened
 * or resumed) or a tab in the background reloads now; a tab still in use waits
 * until it is hidden, so it is fresh when its owner comes back. A first
 * install (no controller before) never reloads.
 */
export function shouldReloadOnControllerChange(s: {
  requested: boolean;
  hidden: boolean;
  controlledBefore: boolean;
}): "reload" | "when-hidden" | "none" {
  if (!s.controlledBefore) return "none";
  return s.requested || s.hidden ? "reload" : "when-hidden";
}

/** Tells a waiting worker to take over; the page reloads on controllerchange. Returns whether it asked. */
function apply(reg: ServiceWorkerRegistration): boolean {
  if (reg.waiting && shouldApply({ waiting: true, controlled: !!navigator.serviceWorker.controller })) {
    reg.waiting.postMessage({ type: "SKIP_WAITING" });
    return true;
  }
  return false;
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
  // over from an old one reloads once onto the new files, right away in the
  // tab that asked for it and in background tabs, later in other tabs in use.
  let controlled = !!sw.controller;
  let requested = false;
  let pending = false;
  let reloading = false;
  const reload = () => {
    if (reloading) return;
    reloading = true;
    window.location.reload();
  };
  sw.addEventListener("controllerchange", () => {
    const next = shouldReloadOnControllerChange({ requested, hidden: document.visibilityState === "hidden", controlledBefore: controlled });
    controlled = true;
    if (next === "reload") reload();
    else if (next === "when-hidden") pending = true;
  });
  document.addEventListener("visibilitychange", () => {
    if (pending && document.visibilityState === "hidden") reload();
  });
  sw.register("/sw.js", { scope: "/", updateViaCache: "none" })
    .then((reg) => {
      if (apply(reg)) requested = true; // launch
      // iOS resumes a Home Screen app instead of reloading it, so check for
      // a new release whenever it comes back to the foreground.
      document.addEventListener("visibilitychange", () => {
        if (document.visibilityState !== "visible") return;
        reg
          .update()
          .then(() => {
            if (apply(reg)) requested = true;
          })
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

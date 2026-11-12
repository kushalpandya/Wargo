// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef } from "react";
import { WifiOff } from "lucide-react";
import { Spinner } from "@/components/ui/misc";
import { Logo } from "./Logo";

/**
 * While active, calls retry after 3 s, then backs off (doubling) to every
 * 30 s, and also as soon as the device is back online or the app returns to
 * the foreground.
 */
export function useAutoRetry(active: boolean, retry: () => void) {
  const fn = useRef(retry);
  fn.current = retry;
  useEffect(() => {
    if (!active) return;
    let delay = 3_000;
    let timer = 0;
    const schedule = () => {
      timer = window.setTimeout(() => {
        fn.current();
        delay = Math.min(delay * 2, 30_000);
        schedule();
      }, delay);
    };
    const now = () => {
      if (document.visibilityState === "visible") fn.current();
    };
    schedule();
    window.addEventListener("online", now);
    document.addEventListener("visibilitychange", now);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("online", now);
      document.removeEventListener("visibilitychange", now);
    };
  }, [active]);
}

/**
 * Full page shown when the server can't be reached at launch: one alert and
 * nothing else, until a retry gets through.
 */
export function Unreachable({ onRetry, retrying }: { onRetry: () => void; retrying: boolean }) {
  return (
    <main className="flex min-h-dvh items-start justify-center bg-bg px-4 pb-10 pt-[max(2.5rem,env(safe-area-inset-top))]">
      <div className="w-full max-w-lg">
        <div className="mb-6 flex items-center gap-2.5">
          <Logo className="size-8" />
          <div className="text-base font-semibold tracking-tight">Wargo</div>
        </div>
        <div role="alert" className="rounded-lg border border-border bg-surface p-5 shadow-card">
          <h1 className="flex items-center gap-2 text-md font-semibold">
            <WifiOff className="size-4 text-crit" aria-hidden="true" /> Wargo can't be reached
          </h1>
          <p className="mt-2 text-sm text-muted">
            Check that the server is running and that this device is online. Wargo will keep trying.
          </p>
          <button
            type="button"
            onClick={onRetry}
            disabled={retrying}
            className="mt-4 inline-flex h-8 items-center gap-2 rounded-md bg-accent px-3 text-sm font-medium text-accent-fg hover:bg-accent-strong disabled:opacity-60"
          >
            {retrying && <Spinner className="size-3.5" />}
            {retrying ? "Trying…" : "Try again"}
          </button>
        </div>
      </div>
    </main>
  );
}

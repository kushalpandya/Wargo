// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useReducer, useState } from "react";
import type { TermSession } from "./session";

/** Re-renders when the session's state, title, modifiers or search results change. */
export function useSessionState(s: TermSession | undefined) {
  const [, bump] = useReducer((n: number) => n + 1, 0);
  useEffect(() => {
    if (!s) return;
    const off = s.subscribe(bump);
    return () => {
      off();
    };
  }, [s]);
  return s;
}

/** Height of the visual viewport (shrinks when an on-screen keyboard opens). */
export function useVisualHeight(enabled: boolean) {
  const [h, setH] = useState<number | null>(null);
  useEffect(() => {
    const vv = window.visualViewport;
    if (!enabled || !vv) {
      setH(null);
      return;
    }
    const on = () => setH(vv.height);
    on();
    vv.addEventListener("resize", on);
    return () => vv.removeEventListener("resize", on);
  }, [enabled]);
  return h;
}

export function useFullscreen(el: HTMLElement | null) {
  const [fs, setFs] = useState(false);
  useEffect(() => {
    const on = () => setFs(!!el && document.fullscreenElement === el);
    document.addEventListener("fullscreenchange", on);
    return () => document.removeEventListener("fullscreenchange", on);
  }, [el]);
  const toggle = async () => {
    if (!el) return;
    if (document.fullscreenElement) await document.exitFullscreen().catch(() => {});
    else await el.requestFullscreen?.().catch(() => {});
  };
  return [fs, toggle, !!document.fullscreenEnabled] as const;
}

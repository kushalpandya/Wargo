// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

function mediaStore(query: string) {
  return {
    subscribe: (l: () => void) => {
      const m = window.matchMedia(query);
      m.addEventListener("change", l);
      return () => m.removeEventListener("change", l);
    },
    get: () => window.matchMedia(query).matches,
  };
}

/** Live result of a CSS media query. */
export function useMedia(query: string) {
  const store = useMemo(() => mediaStore(query), [query]);
  return useSyncExternalStore(store.subscribe, store.get);
}

/** useState persisted as JSON in localStorage (per browser, never secrets). */
export function useLocalState<T>(key: string, initial: T) {
  const [v, setV] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key);
      return raw == null ? initial : (JSON.parse(raw) as T);
    } catch {
      return initial;
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem(key, JSON.stringify(v));
    } catch {
      /* quota or disabled storage */
    }
  }, [key, v]);
  return [v, setV] as const;
}

/**
 * Height for a scrollable region so it reaches the bottom of the viewport
 * (when the page is scrolled to the top), leaving `reserve` pixels for
 * what follows it (footer rows, page padding), and never below `min`.
 * Recomputed on resize and when content above it changes size.
 */
export function useFillViewport(el: HTMLElement | null, { min = 320, reserve = 0 }: { min?: number; reserve?: number } = {}) {
  const [height, setHeight] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (!el) return;
    let frame = 0;
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        if (!el.offsetParent) return; // hidden (e.g. the other breakpoint's list)
        const top = el.getBoundingClientRect().top + window.scrollY;
        const pad = parseFloat(getComputedStyle(el.closest("main") ?? document.body).paddingBottom) || 0;
        setHeight(Math.max(min, Math.floor(window.innerHeight - top - reserve - pad)));
      });
    };
    measure();
    window.addEventListener("resize", measure);
    const ro = new ResizeObserver(measure);
    ro.observe(document.body);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener("resize", measure);
      ro.disconnect();
    };
  }, [el, min, reserve]);
  return height;
}

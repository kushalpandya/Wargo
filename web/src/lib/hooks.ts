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

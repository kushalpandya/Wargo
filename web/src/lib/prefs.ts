// SPDX-License-Identifier: AGPL-3.0-only
import { useCallback, useSyncExternalStore } from "react";

/*
 * Small per-browser preferences (Home feed shown or collapsed, accent
 * colour…), kept in localStorage under "wargo.*". Components re-render when
 * a preference changes, in this tab or another.
 */

const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());
addEventListener("storage", (e) => {
  // key is null when another tab clears storage.
  if (e.key === null || e.key.startsWith("wargo.")) notify();
});

/** Calls l after any preference changes (this tab or another); returns the unsubscribe. */
export function subscribePrefs(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function readPref(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

/** Stores a preference (null removes it) and notifies subscribers. */
export function setPref(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    /* storage blocked: keep the default */
  }
  notify();
}

/** The stored value if allowed, else the fallback. */
export function prefValue<T extends string>(raw: string | null, fallback: T, allowed?: readonly T[]): T {
  return raw !== null && (!allowed || (allowed as readonly string[]).includes(raw)) ? (raw as T) : fallback;
}

/** A string preference with a default; returns [value, set]. */
export function usePref<T extends string>(key: string, fallback: T, allowed?: readonly T[]): [T, (v: T) => void] {
  const raw = useSyncExternalStore(subscribePrefs, () => readPref(key));
  const value = prefValue(raw, fallback, allowed);
  const set = useCallback((v: T) => setPref(key, v === fallback ? null : v), [key, fallback]);
  return [value, set];
}

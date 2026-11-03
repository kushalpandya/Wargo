// SPDX-License-Identifier: AGPL-3.0-only
// Demo state that survives reloads: kept in this browser's localStorage
// under "wargo-demo:*". Nothing ever leaves the browser. "Reset demo"
// clears it all.

const PREFIX = "wargo-demo:";
const all = new Map<string, Persisted<unknown>>();
/** Saves waiting for their short delay; written at once when the page goes away. */
const pending = new Map<string, () => void>();
addEventListener("pagehide", () => {
  for (const write of pending.values()) write();
  pending.clear();
});

export interface Persisted<T> {
  /** Current value (loaded on first use). */
  get(): T;
  /** Replaces the value, or updates it from the current one. */
  set(v: T | ((cur: T) => T)): T;
  /** Changes the value in place and saves it. */
  update(fn: (cur: T) => void): T;
}

/**
 * A value saved per browser. `init` builds the starting data; bump
 * `version` when its shape changes so old saved data is replaced.
 */
export function persisted<T>(key: string, init: () => T, version = 1): Persisted<T> {
  let cur: T | undefined;
  let timer = 0;
  const storageKey = PREFIX + key;
  const load = (): T => {
    if (cur !== undefined) return cur;
    try {
      const raw = localStorage.getItem(storageKey);
      if (raw) {
        const saved = JSON.parse(raw) as { v: number; data: T };
        if (saved.v === version) return (cur = saved.data);
      }
    } catch {
      /* corrupt or unavailable: start fresh */
    }
    return (cur = init());
  };
  const write = () => {
    clearTimeout(timer);
    pending.delete(storageKey);
    try {
      localStorage.setItem(storageKey, JSON.stringify({ v: version, data: cur }));
    } catch {
      /* storage full or blocked: keep it in memory only */
    }
  };
  const save = () => {
    clearTimeout(timer);
    pending.set(storageKey, write);
    timer = window.setTimeout(write, 50);
  };
  const p: Persisted<T> = {
    get: load,
    set(v) {
      cur = typeof v === "function" ? (v as (c: T) => T)(load()) : v;
      save();
      return cur;
    },
    update(fn) {
      fn(load());
      save();
      return cur!;
    },
  };
  all.set(key, p as Persisted<unknown>);
  return p;
}

/** Forgets every change made in the demo and reloads the page. */
export function resetDemo() {
  for (const k of Object.keys(localStorage)) if (k.startsWith(PREFIX)) localStorage.removeItem(k);
  sessionStorage.clear();
  location.reload();
}

let seq = Date.now() % 100000;
/** Short unique id for new objects. */
export const newId = (prefix = "") => prefix + (++seq).toString(36) + Math.random().toString(36).slice(2, 6);

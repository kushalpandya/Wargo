// SPDX-License-Identifier: AGPL-3.0-only
// The mutation behind useSettingsSave (save.ts), kept free of React so it can be tested on its own.
import type { MutationOptions, QueryClient, QueryKey } from "@tanstack/react-query";

export interface SettingsSave<T, D> {
  queryKey: QueryKey;
  /** Saves sharing a scope run one at a time, in order. */
  scope: string;
  /** Saves the whole object and returns the server's copy. */
  put: (v: T) => Promise<T>;
  /** Picks the settings out of the cached query data (default: it is the settings). */
  get?: (d: D) => T;
  /** Puts the settings back into the cached query data. */
  set?: (d: D, v: T) => D;
  onSaved?: (saved: T) => void;
}

/** The settings as cached now, with patch applied; undefined before they load. */
export function nextSettings<T extends object, D>(qc: QueryClient, o: SettingsSave<T, D>, patch: Partial<T>): T | undefined {
  const d = qc.getQueryData<D>(o.queryKey);
  if (d === undefined) return undefined;
  return { ...(o.get ? o.get(d) : (d as unknown as T)), ...patch };
}

/**
 * Mutation options for saving settings: each save writes its value to the
 * cache at once, a reply that a later save is about to replace isn't
 * written back, and a failed save calls onFail and, when no later save is
 * on its way, puts back the value from before it and refetches.
 */
export function settingsSaveOptions<T extends object, D = T>(
  qc: QueryClient,
  o: SettingsSave<T, D>,
  onFail: (e: Error) => void,
): MutationOptions<T, Error, T, { prev?: T }> {
  const get = o.get ?? ((d: D) => d as unknown as T);
  const set = o.set ?? ((_d: D, v: T) => v as unknown as D);
  const mutationKey = ["settings-save", o.scope];
  const write = (v: T) => qc.setQueryData<D>(o.queryKey, (d) => (d === undefined ? d : set(d, v)));
  // A save counts itself while its callbacks run.
  const last = () => qc.isMutating({ mutationKey }) <= 1;
  return {
    mutationKey,
    scope: { id: o.scope },
    mutationFn: o.put,
    onMutate: async (next) => {
      // An older GET landing now would overwrite the change shown.
      await qc.cancelQueries({ queryKey: o.queryKey, exact: true });
      const d = qc.getQueryData<D>(o.queryKey);
      const prev = d === undefined ? undefined : get(d);
      write(next);
      return { prev };
    },
    onSuccess: (saved) => {
      if (last()) write(saved);
      o.onSaved?.(saved);
    },
    onError: (e, _next, ctx) => {
      onFail(e);
      if (!last()) return;
      if (ctx?.prev) write(ctx.prev);
      qc.invalidateQueries({ queryKey: o.queryKey, exact: true });
    },
  };
}

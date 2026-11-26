// SPDX-License-Identifier: AGPL-3.0-only
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { HeaderToggle } from "@/components/ui/card";
import { toast } from "@/components/ui/toast";
import { nextSettings, settingsSaveOptions, type SettingsSave } from "./saveOptions";

/**
 * Saves a settings object kept in the query cache under `queryKey` (or a
 * part of it, picked by `get` and put back by `set`). The updater it
 * returns shows a change at once by writing it to the cache, then saves the
 * whole new object; saves sharing `scope` run one at a time, in order. Each
 * builds on the cache, so it carries every change made before it, and a
 * reply that a later save is about to replace isn't written back, so a
 * quick second change is never lost or briefly undone. A failed save toasts
 * and puts back the value from before it. Read what to show from the query.
 */
export function useSettingsSave<T extends object, D = T>(o: SettingsSave<T, D>) {
  const qc = useQueryClient();
  const m = useMutation(settingsSaveOptions(qc, o, (e) => toast.error("Could not save", e)));
  return (patch: Partial<T>) => {
    const next = nextSettings(qc, o, patch);
    if (next) m.mutate(next);
  };
}

/**
 * The header switch of a card whose settings have `enabled`: a placeholder
 * while they load, none when they failed to load (the card shows the error).
 */
export function enabledToggle<T extends { enabled: boolean }>(
  label: string,
  v: T | undefined,
  update: (patch: Partial<T>) => void,
  error: unknown,
): HeaderToggle | undefined {
  if (error && !v) return undefined;
  if (!v) return { label, loading: true };
  return { label, checked: v.enabled, onChange: (enabled) => update({ enabled } as Partial<T>) };
}

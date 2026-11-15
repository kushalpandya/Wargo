// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect } from "react";
import { useIsMutating, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { ACCENTS, setAccent, type Accent } from "@/lib/theme";

/*
 * The accent colour is saved on the server, so it follows you to every
 * browser and device (the colour mode stays per device). This browser keeps
 * a copy only to paint the first frame before the server answers.
 */

interface AppearanceSettings {
  accent: Accent;
}

const key = ["appearance"] as const;
const known = (a: string): a is Accent => ACCENTS.some((x) => x.id === a);

/** Applies the server's accent whenever it loads or changes (mounted once, in the app shell). */
export function useAccentSync() {
  const { data } = useQuery({ queryKey: key, queryFn: () => api.get<AppearanceSettings>("/api/appearance"), staleTime: 60_000 });
  const accent = data?.accent;
  // While a pick is saving, a refetch may still hold the old value.
  const saving = useIsMutating({ mutationKey: key }) > 0;
  useEffect(() => {
    if (!saving && accent && known(accent)) setAccent(accent);
  }, [accent, saving]);
}

/**
 * Picks an accent: shown at once, saved on the server, undone if the save
 * fails. With quick clicks only the newest pick may touch the cache, so an
 * earlier save finishing late can't bring its colour back.
 */
export function useSaveAccent() {
  const qc = useQueryClient();
  // This mutation counts itself while its callbacks run.
  const latest = () => qc.isMutating({ mutationKey: key }) === 1;
  return useMutation({
    mutationKey: key,
    mutationFn: (accent: Accent) => api.put<AppearanceSettings>("/api/appearance", { accent }),
    onMutate: async (accent) => {
      await qc.cancelQueries({ queryKey: key });
      const prev = qc.getQueryData<AppearanceSettings>(key);
      qc.setQueryData<AppearanceSettings>(key, { accent });
      setAccent(accent);
      return { prev };
    },
    onError: (_e, _accent, ctx) => {
      if (ctx?.prev && latest()) {
        qc.setQueryData(key, ctx.prev);
        setAccent(ctx.prev.accent);
      }
    },
    onSuccess: (v) => {
      if (latest()) qc.setQueryData(key, v);
    },
  });
}

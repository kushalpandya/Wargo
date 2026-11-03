// SPDX-License-Identifier: AGPL-3.0-only
// How often updates are looked for in the background (Settings → General).
// Registered before the container handlers so /api/containers/settings is
// not taken for a container id.
import type { RequestHandler } from "msw";
import { api, badRequest, body, json } from "../core/http";
import { persisted } from "../core/store";
import { recordAudit } from "./settings";

function schedule(path: string, key: string, action: string, fallback: number, allowed: number[]): RequestHandler[] {
  const saved = persisted<{ checkHours: number }>(key, () => ({ checkHours: fallback }));
  return [
    api.get(path, () => json(saved.get())),
    api.put(path, async ({ request }) => {
      const { checkHours } = await body<{ checkHours: number }>(request);
      if (!allowed.includes(checkHours)) return badRequest(`checkHours must be ${allowed.join(", ")}`);
      recordAudit({ action, target: `check every ${checkHours}h` });
      return json(saved.set({ checkHours }));
    }),
  ];
}

export const handlers: RequestHandler[] = [
  ...schedule("/api/pkg/settings", "pkg.settings", "pkg.settings", 24, [0, 6, 12, 24, 72, 168]),
  ...schedule("/api/containers/settings", "containers.settings", "containers.settings", 6, [0, 1, 3, 6, 12, 24, 72, 168]),
];

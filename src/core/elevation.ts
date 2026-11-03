// SPDX-License-Identifier: AGPL-3.0-only
// Admin approval, as on a real server: an action that needs root first
// answers 403 "elevation_required"; the app asks for the sudo password and
// retries with X-Wargo-Elevation. In the demo any password works.
import type { ElevationRequest } from "@/lib/api";
import { api, fail, json } from "./http";

const methods: ElevationRequest["methods"] = { sudo: true, passkey: false };

/**
 * Returns a 403 asking for approval when the request carries none, or null
 * when approved (go ahead). Use for actions that would need sudo.
 */
export function requireApproval(request: Request, action: string, target: string) {
  if (request.headers.get("X-Wargo-Elevation")) return null;
  return fail(403, "elevation_required", "Administrator approval required", { action, target, methods } satisfies ElevationRequest);
}

export const elevationHandlers = [
  api.get("/api/elevate/methods", () => json(methods)),
  api.get("/api/elevate/privd", () => json({ available: false, trusted: [], reason: "wargo-privd isn't running in the demo" })),
  api.post("/api/elevate/challenge", () => fail(501, "not_in_demo", "Passkey approval isn't available in the demo. Use the password prompt (any password works).")),
  api.post("/api/elevate/privd/trust", () => fail(501, "not_in_demo", "wargo-privd isn't available in the demo.")),
];

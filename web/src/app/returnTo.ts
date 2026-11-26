// SPDX-License-Identifier: AGPL-3.0-only

/**
 * Where to go after signing in: the in-app page the sign-in interrupted
 * (router state { from }), or home. Only a path within the app counts, so
 * the state can never send the browser elsewhere.
 */
export function returnTo(state: unknown): string {
  const from = typeof state === "object" && state !== null ? (state as { from?: unknown }).from : undefined;
  if (typeof from !== "string" || !from.startsWith("/") || from.startsWith("//") || from.startsWith("/\\") || from.startsWith("/login")) return "/";
  return from;
}

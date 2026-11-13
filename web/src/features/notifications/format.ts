// SPDX-License-Identifier: AGPL-3.0-only
import type { Active } from "./api";

/** The bell's count: nothing for 0, "99+" above 99. */
export function badgeText(n: number): string {
  if (n <= 0) return "";
  return n > 99 ? "99+" : String(n);
}

/** Active rows, the most recently started first (a new array). */
export function sortActive(a: Active[]): Active[] {
  return [...a].sort((x, y) => Date.parse(y.startedAt) - Date.parse(x.startedAt));
}

// SPDX-License-Identifier: AGPL-3.0-only
import type { Torrent, TorrentStatus } from "./api.ts";

/** The statuses a finished torrent can have. Anything else (a check, a download, a status Wargo does not know) is not finished. */
const finishedStatuses: readonly TorrentStatus[] = ["stopped", "seed-wait", "seeding"];

/**
 * Whether the server would remove this torrent when "Remove torrents when they
 * finish" is on. Keep in step with finished() in internal/integrations/autoremove.go.
 */
export function isFinished(t: Pick<Torrent, "percentDone" | "status" | "error">): boolean {
  return finishedStatuses.includes(t.status) && t.percentDone >= 1 && t.error !== 3;
}

export function countFinished(ts: Torrent[]): number {
  return ts.filter(isFinished).length;
}

/**
 * What turning removal on should do, given how many finished torrents a fresh
 * fetch found (null when it could not tell): save straight away when there are
 * none, or ask first with this dialog body.
 */
export function confirmPlan(count: number | null): { save: true } | { confirm: string } {
  if (count === 0) return { save: true };
  if (count === null) return { confirm: "Any finished torrents will be removed now. Their files are kept." };
  return { confirm: `${count} finished ${count === 1 ? "torrent" : "torrents"} will be removed now. Their files are kept.` };
}

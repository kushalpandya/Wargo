// SPDX-License-Identifier: AGPL-3.0-only

/** Same cap the server applies to .torrent uploads. */
export const MAX_TORRENT_BYTES = 10 * 1024 * 1024;

const MAX_MAGNET_CHARS = 16384;

export function isMagnet(s: string): boolean {
  const t = s.trim();
  return t.startsWith("magnet:?") && t.length <= MAX_MAGNET_CHARS;
}

/** Why a file cannot be added, or null when it can. */
export function torrentFileError(f: { name: string; size: number }): string | null {
  if (!f.name.toLowerCase().endsWith(".torrent")) return "Not a .torrent file";
  if (f.size > MAX_TORRENT_BYTES) return "Larger than 10 MB";
  return null;
}

/**
 * Splits files dropped on the Downloads card: .torrent files go on to the Add
 * dialog (which shows any that are too large), anything else is rejected
 * with the reason to report.
 */
export function splitTorrentFiles<F extends { name: string }>(files: F[]): { accepted: F[]; rejected: { file: F; reason: string }[] } {
  const accepted: F[] = [];
  const rejected: { file: F; reason: string }[] = [];
  for (const f of files) {
    if (f.name.toLowerCase().endsWith(".torrent")) accepted.push(f);
    else rejected.push({ file: f, reason: "Not a .torrent file" });
  }
  return { accepted, rejected };
}

export function toBase64(bytes: ArrayBuffer): string {
  const a = new Uint8Array(bytes);
  let bin = "";
  for (let i = 0; i < a.length; i += 0x8000) bin += String.fromCharCode(...a.subarray(i, i + 0x8000));
  return btoa(bin);
}

// SPDX-License-Identifier: AGPL-3.0-only

const units = ["B", "KB", "MB", "GB", "TB", "PB"];

/** 1536 -> "1.5 KB" (binary multiples, short labels). */
export function bytes(n: number | undefined | null, digits = 1): string {
  if (n === undefined || n === null || !isFinite(n)) return "—";
  if (n < 1024) return `${Math.round(n)} B`;
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 ? 0 : digits)} ${units[i]}`;
}

/** Splits bytes into value + unit for typographic display. */
export function bytesParts(n: number, digits = 1): [string, string] {
  const s = bytes(n, digits);
  const i = s.lastIndexOf(" ");
  return [s.slice(0, i), s.slice(i + 1)];
}

export function rate(n: number | undefined): string {
  if (n === undefined) return "—";
  return `${bytes(n)}/s`;
}

export function pct(n: number | undefined | null, digits = 0): string {
  if (n === undefined || n === null || !isFinite(n)) return "—";
  return `${n.toFixed(digits)}%`;
}

export function temp(c: number | undefined): string {
  if (c === undefined || c === null) return "—";
  return `${Math.round(c)}°C`;
}

/** Seconds -> "3d 4h", "5h 12m", "42s". */
export function duration(sec: number | undefined): string {
  if (sec === undefined || !isFinite(sec)) return "—";
  sec = Math.max(0, Math.floor(sec));
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${sec % 60}s`;
  return `${sec}s`;
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "short" });

/** Date -> "5 min. ago". */
export function ago(t: string | number | Date | undefined | null): string {
  if (!t) return "—";
  const ms = new Date(t).getTime();
  if (!ms || ms < 0) return "—";
  const diff = (ms - Date.now()) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return "just now";
  if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), "hour");
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), "day");
  if (abs < 86400 * 365) return rtf.format(Math.round(diff / (86400 * 30)), "month");
  return rtf.format(Math.round(diff / (86400 * 365)), "year");
}

const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
export function datetime(t: string | number | Date | undefined | null): string {
  if (!t) return "—";
  const d = new Date(t);
  return isNaN(d.getTime()) ? "—" : dtf.format(d);
}

export function num(n: number | undefined, digits = 0): string {
  if (n === undefined || !isFinite(n)) return "—";
  return n.toLocaleString(undefined, { maximumFractionDigits: digits, minimumFractionDigits: digits });
}

/** Compact number: 12300 -> "12.3k". */
export function compact(n: number | undefined): string {
  if (n === undefined) return "—";
  return Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(n);
}

/** Severity from a percentage. */
export function level(p: number, warn = 75, crit = 90): "ok" | "warn" | "crit" {
  return p >= crit ? "crit" : p >= warn ? "warn" : "ok";
}

/** "update" or "updates" for n. */
export const plural = (n: number, one: string, many = `${one}s`) => (n === 1 ? one : many);

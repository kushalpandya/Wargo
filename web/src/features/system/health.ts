// SPDX-License-Identifier: AGPL-3.0-only
// No imports: node --test runs this file directly, and tsc rejects the
// ".ts" suffix node needs to import lib/format.ts.

export interface DriveHealth {
  status: "ok" | "warn" | "fail" | "unknown";
  /** Why health is unknown or failing. */
  reason?: string;
  wearPct?: number;
  powerOnHours?: number;
  sparePct?: number;
  /** What raised a warning, e.g. "8 reallocated sectors". */
  notes?: string[];
}

const pct = (n: number) => `${n.toFixed(0)}%`;

/** Key SMART numbers, then what raised a warning. */
export function healthFacts(h: DriveHealth): string[] {
  const out: string[] = [];
  if (h.wearPct != null) out.push(`wear ${pct(h.wearPct)}`);
  if (h.powerOnHours) out.push(`${h.powerOnHours.toLocaleString()} h on`);
  if (h.sparePct != null) out.push(`spare ${pct(h.sparePct)}`);
  for (const n of h.notes ?? []) if (!out.includes(n)) out.push(n);
  return out;
}

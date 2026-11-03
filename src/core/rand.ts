// SPDX-License-Identifier: AGPL-3.0-only
// Small helpers for believable made-up numbers.

/** Deterministic random numbers (same demo data on every visit). */
export function seeded(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
export const pick = <T>(list: readonly T[], r = Math.random) => list[Math.floor(r() * list.length)];
export const between = (lo: number, hi: number, r = Math.random) => lo + r() * (hi - lo);

/**
 * A value that drifts smoothly around a target, with occasional spikes:
 * looks like real CPU or network activity on a chart.
 */
export class Walk {
  constructor(
    public value: number,
    private target: number,
    private spread: number,
    private min = 0,
    private max = 100,
    private spikiness = 0.03,
  ) {}
  next(): number {
    const pull = (this.target - this.value) * 0.08;
    let v = this.value + pull + (Math.random() - 0.5) * this.spread;
    if (Math.random() < this.spikiness) v += Math.random() * (this.max - this.min) * 0.35;
    this.value = clamp(v, this.min, this.max);
    return this.value;
  }
  retarget(t: number) {
    this.target = t;
  }
}

export const MB = 1024 * 1024;
export const GB = 1024 * MB;
export const TB = 1024 * GB;

/** Unix seconds and milliseconds relative to now. */
export const now = () => Date.now();
export const ago = (ms: number) => Date.now() - ms;
export const MIN = 60_000;
export const HOUR = 60 * MIN;
export const DAY = 24 * HOUR;

// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef } from "react";
import { useLocation } from "react-router";

/**
 * The cards of System Monitor's Activity tab that the summary tiles link to,
 * as /system#<id>.
 */
export const SECTIONS = { cpuMemory: "cpu-memory", network: "network", storage: "storage" } as const;
export type Section = (typeof SECTIONS)[keyof typeof SECTIONS];

const ids = new Set<string>(Object.values(SECTIONS));

/** The section a location hash names, if it is one of SECTIONS. */
export function sectionOf(hash: string): Section | null {
  // The ids are plain ASCII: no decoding, so a malformed escape cannot throw.
  const id = hash.replace(/^#/, "");
  return ids.has(id) ? (id as Section) : null;
}

/**
 * Below the sticky page header (4rem plus the notch on phones) with the
 * page's 0.75rem gap. Put it on each section's element.
 */
export const sectionScrollMargin = "scroll-mt-[calc(4.75rem+env(safe-area-inset-top))]";

/**
 * The cards above the section may keep growing until settleQuietMs pass
 * with no change in the page's height, and for settleMaxMs at most (a cold
 * load on a phone waits on the page's code, the first metrics sample and
 * Storage).
 */
const settleQuietMs = 1500;
const settleMaxMs = 10_000;

/**
 * Scrolls to the section the URL hash names, on every visit to such a link.
 * Arriving from another page, the cards above it are still filling in (the
 * charts' first sample, the interface and disk lists, Storage), so until the
 * owner scrolls, taps or types, or the page settles, each change in the
 * page's height scrolls it back into place. On the page already, it scrolls
 * there smoothly unless reduced motion is asked for.
 */
export function useSectionScroll() {
  const { hash, key } = useLocation();
  // The location the page mounted with: a later key is a link followed on
  // the page. (A ref set inside the effect would not do: React runs effects
  // twice on mount in development.)
  const mountKey = useRef(key);
  useEffect(() => {
    const first = key === mountKey.current;
    const id = sectionOf(hash);
    const el = id && document.getElementById(id);
    if (!el) return;
    const reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    el.scrollIntoView({ block: "start", behavior: first || reduce ? "instant" : "smooth" });
    // Screen readers and the keyboard continue from the card.
    el.focus({ preventScroll: true });
    if (!first) return;

    const main = document.getElementById("main");
    const inputs = ["wheel", "touchstart", "pointerdown", "keydown"] as const;
    let done = false;
    let quiet: ReturnType<typeof setTimeout> | undefined;
    let max: ReturnType<typeof setTimeout> | undefined;
    let ro: ResizeObserver | undefined;
    const stop = () => {
      if (done) return;
      done = true;
      ro?.disconnect();
      clearTimeout(quiet);
      clearTimeout(max);
      for (const ev of inputs) window.removeEventListener(ev, stop);
    };
    max = setTimeout(stop, settleMaxMs);
    const rearm = () => {
      clearTimeout(quiet);
      quiet = setTimeout(stop, settleQuietMs);
    };
    rearm();
    if (main) {
      // ResizeObserver also reports the size it starts with: only a change
      // in height moves the section.
      let height = main.offsetHeight;
      ro = new ResizeObserver(() => {
        if (done || main.offsetHeight === height) return;
        height = main.offsetHeight;
        el.scrollIntoView({ block: "start", behavior: "instant" });
        rearm();
      });
      ro.observe(main);
    }
    for (const ev of inputs) window.addEventListener(ev, stop, { passive: true });
    return stop;
  }, [hash, key]);
}

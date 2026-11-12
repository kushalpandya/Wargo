// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useSyncExternalStore } from "react";
import { prefValue, readPref, setPref, subscribePrefs, usePref } from "./prefs";

export type ThemeMode = "auto" | "light" | "dark";

const KEY = "wargo.theme";
const listeners = new Set<() => void>();
const media = window.matchMedia("(prefers-color-scheme: dark)");

function read(): ThemeMode {
  const v = readPref(KEY);
  return v === "light" || v === "dark" ? v : "auto";
}

function apply(mode: ThemeMode) {
  const dark = mode === "dark" || (mode === "auto" && media.matches);
  const root = document.documentElement;
  root.classList.toggle("dark", dark);
  root.dataset.theme = mode;
  listeners.forEach((l) => l());
}

media.addEventListener("change", () => apply(read()));

export function setTheme(mode: ThemeMode) {
  setPref(KEY, mode);
  apply(mode);
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

export function useThemeMode(): ThemeMode {
  return useSyncExternalStore(subscribe, read);
}

const isDark = () => document.documentElement.classList.contains("dark");

/** True when the dark palette is active (resolved from auto). */
export function useIsDark(): boolean {
  return useSyncExternalStore(subscribe, isDark);
}

/** Accent colours (Settings → Appearance); "periwinkle" is the default. */
export const ACCENTS = [
  { id: "periwinkle", label: "Periwinkle", swatch: "oklch(0.62 0.15 278)" },
  { id: "lilac", label: "Lilac", swatch: "oklch(0.62 0.15 305)" },
  { id: "sky", label: "Sky", swatch: "oklch(0.64 0.12 235)" },
  { id: "mint", label: "Mint", swatch: "oklch(0.66 0.12 165)" },
  { id: "rose", label: "Rose", swatch: "oklch(0.63 0.16 10)" },
  { id: "peach", label: "Peach", swatch: "oklch(0.7 0.13 55)" },
] as const;
export type Accent = (typeof ACCENTS)[number]["id"];

const ACCENT_KEY = "wargo.accent";
const ACCENT_IDS = ACCENTS.map((a) => a.id);

/** Mirrors the stored accent onto <html data-accent> (theme-init.js does this before first paint). */
function applyAccent() {
  const a = prefValue<Accent>(readPref(ACCENT_KEY), "periwinkle", ACCENT_IDS);
  const root = document.documentElement;
  if (a === "periwinkle") delete root.dataset.accent;
  else root.dataset.accent = a;
}
// One listener covers this tab's setAccent and changes made in other tabs.
subscribePrefs(applyAccent);

/** Applies and remembers the accent; the default is stored as no value. */
export function setAccent(a: Accent) {
  setPref(ACCENT_KEY, a === "periwinkle" ? null : a);
}

export function useAccent(): Accent {
  return usePref<Accent>(ACCENT_KEY, "periwinkle", ACCENT_IDS)[0];
}

/** Reads a CSS custom property (for canvas-based charts). */
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

export function useApplyTheme() {
  useEffect(() => apply(read()), []);
}

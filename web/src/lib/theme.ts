// SPDX-License-Identifier: AGPL-3.0-only
import { useSyncExternalStore } from "react";
import { prefValue, readPref, setPref, subscribePrefs, usePref } from "./prefs";

export type ThemeMode = "auto" | "light" | "dark";

const KEY = "wargo.theme";
const listeners = new Set<() => void>();
const media = window.matchMedia("(prefers-color-scheme: dark)");

function read(): ThemeMode {
  const v = readPref(KEY);
  return v === "light" || v === "dark" ? v : "auto";
}

/** The page background (--bg) per accent and theme, also used for the
 *  browser and iOS status bar colour. The neutrals take the accent's hue, so
 *  each accent has its own pair. public/theme-init.js keeps a copy for first
 *  paint; regenerate both when --bg changes in styles/index.css. */
export const THEME_BG: Record<string, { light: string; dark: string }> = {
  periwinkle: { light: "#f3f4fd", dark: "#060610" },
  lilac: { light: "#f7f3fc", dark: "#09050e" },
  sky: { light: "#eef7fc", dark: "#01080f" },
  mint: { light: "#eef8f3", dark: "#010a06" },
  rose: { light: "#fdf2f3", dark: "#0f0406" },
  peach: { light: "#fcf3ee", dark: "#0e0502" },
  mono: { light: "#f5f5f5", dark: "#070707" },
};

/** Sets the status bar colour from the current theme and accent. */
function syncThemeColor() {
  const root = document.documentElement;
  const bg = THEME_BG[root.dataset.accent ?? "periwinkle"] ?? THEME_BG.periwinkle;
  document.getElementById("theme-color")?.setAttribute("content", root.classList.contains("dark") ? bg.dark : bg.light);
}

function apply(mode: ThemeMode) {
  const dark = mode === "dark" || (mode === "auto" && media.matches);
  const root = document.documentElement;
  root.classList.toggle("dark", dark);
  root.dataset.theme = mode;
  syncThemeColor();
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
  // Greys only: half ink, half paper.
  { id: "mono", label: "Mono", swatch: "linear-gradient(135deg, oklch(0.25 0 0) 50%, oklch(0.96 0 0) 50%)" },
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
  syncThemeColor();
}
// One listener covers this tab's setAccent and changes made in other tabs.
subscribePrefs(applyAccent);

/**
 * Applies the accent in this browser and keeps it as the copy theme-init.js
 * paints the first frame with; the default is stored as no value. The saved
 * choice lives on the server (features/settings/accent.ts).
 */
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

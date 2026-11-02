// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useSyncExternalStore } from "react";

export type ThemeMode = "auto" | "light" | "dark";

const KEY = "wargo.theme";
const listeners = new Set<() => void>();
const media = window.matchMedia("(prefers-color-scheme: dark)");

function read(): ThemeMode {
  const v = localStorage.getItem(KEY);
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
  localStorage.setItem(KEY, mode);
  apply(mode);
}

export function useThemeMode(): ThemeMode {
  return useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    read,
  );
}

/** True when the dark palette is active (resolved from auto). */
export function useIsDark(): boolean {
  return useSyncExternalStore(
    (l) => (listeners.add(l), () => listeners.delete(l)),
    () => document.documentElement.classList.contains("dark"),
  );
}

/** Reads a CSS custom property (for canvas-based charts). */
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

export function useApplyTheme() {
  useEffect(() => apply(read()), []);
}

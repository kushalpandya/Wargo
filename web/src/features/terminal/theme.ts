// SPDX-License-Identifier: AGPL-3.0-only
import type { ITheme } from "@xterm/xterm";
import { cssVar } from "@/lib/theme";

let ctx: CanvasRenderingContext2D | null = null;

/** Normalises any CSS colour to #rrggbb so xterm (and alpha maths) can use it. */
function hex(color: string, fallback: string): string {
  if (!color) return fallback;
  ctx ??= document.createElement("canvas").getContext("2d");
  if (!ctx) return color;
  ctx.fillStyle = fallback;
  ctx.fillStyle = color;
  const v = ctx.fillStyle;
  return v.startsWith("#") ? v : fallback;
}

function rgba(color: string, alpha: number): string {
  const h = color.replace("#", "");
  const n = parseInt(h.length === 3 ? h.replace(/./g, "$&$&") : h, 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

/** Mixes two #rrggbb colours (t = 0 → a, 1 → b). */
function mix(a: string, b: string, t: number): string {
  const pa = parseInt(a.slice(1), 16);
  const pb = parseInt(b.slice(1), 16);
  const ch = (s: number) => Math.round(((pa >> s) & 255) * (1 - t) + ((pb >> s) & 255) * t);
  return "#" + ((ch(16) << 16) | (ch(8) << 8) | ch(0)).toString(16).padStart(6, "0");
}

/**
 * Terminal palette derived from the Northern design tokens. The ANSI
 * colours reuse the semantic tokens (crit/ok/warn/frost/accent) so shell
 * output matches the rest of the dashboard in both themes.
 */
export function terminalTheme(dark: boolean): ITheme {
  const v = (name: string, fb: string) => hex(cssVar(name), fb);
  const bg = dark ? v("--bg", "#0a0d11") : v("--surface", "#ffffff");
  const fg = v("--fg", dark ? "#e2e7ed" : "#121820");
  const muted = v("--fg-muted", dark ? "#a0abb7" : "#4a5561");
  const subtle = v("--fg-subtle", dark ? "#808c99" : "#646f7b");
  const accent = v("--accent", dark ? "#f0a43a" : "#9a5508");
  const frost = v("--frost", dark ? "#74bde0" : "#246d92");
  const ok = v("--ok", dark ? "#52c492" : "#1a7a4e");
  const warn = v("--warn", dark ? "#e7b84a" : "#8a5900");
  const crit = v("--crit", dark ? "#f06a60" : "#b8322a");
  const border = v("--border-strong", dark ? "#2f3944" : "#c5cdd6");
  // Two hues the token set doesn't have, tuned to sit beside frost/crit.
  const magenta = dark ? "#c49be8" : "#7b4fa8";
  const cyan = dark ? "#6fd0c8" : "#16756f";
  const white = dark ? mix(fg, bg, 0.12) : mix(fg, bg, 0.45);
  const black = dark ? mix(bg, fg, 0.22) : fg;
  const bright = (c: string) => (dark ? mix(c, "#ffffff", 0.22) : mix(c, "#000000", 0.12));

  return {
    background: bg,
    foreground: fg,
    cursor: accent,
    cursorAccent: bg,
    selectionBackground: rgba(accent, dark ? 0.32 : 0.24),
    selectionInactiveBackground: rgba(muted, 0.22),
    scrollbarSliderBackground: rgba(subtle, 0.3),
    scrollbarSliderHoverBackground: rgba(subtle, 0.5),
    scrollbarSliderActiveBackground: rgba(subtle, 0.65),
    overviewRulerBorder: border,
    black,
    red: crit,
    green: ok,
    yellow: warn,
    blue: frost,
    magenta,
    cyan,
    white,
    brightBlack: subtle,
    brightRed: bright(crit),
    brightGreen: bright(ok),
    brightYellow: bright(warn),
    brightBlue: bright(frost),
    brightMagenta: bright(magenta),
    brightCyan: bright(cyan),
    brightWhite: dark ? "#ffffff" : mix(fg, "#000000", 0.4),
  };
}

/** Search highlight colours (must be #rrggbb). */
export function searchDecorations(dark: boolean) {
  const v = (name: string, fb: string) => hex(cssVar(name), fb);
  const accent = v("--accent", dark ? "#f0a43a" : "#9a5508");
  const frost = v("--frost", dark ? "#74bde0" : "#246d92");
  const bg = dark ? v("--bg", "#0a0d11") : v("--surface", "#ffffff");
  return {
    matchBackground: mix(bg, frost, dark ? 0.35 : 0.25),
    matchOverviewRuler: frost,
    activeMatchBackground: mix(bg, accent, dark ? 0.55 : 0.4),
    activeMatchColorOverviewRuler: accent,
  };
}

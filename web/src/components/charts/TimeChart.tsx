// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { cssVar, useIsDark } from "@/lib/theme";

export interface SeriesDef {
  label: string;
  /** CSS variable name, e.g. "--accent", or a literal CSS color. */
  color: string;
  fill?: boolean;
  dash?: boolean;
  /** Line width in CSS pixels (default 1.75). */
  width?: number;
}

const resolve = (c: string) => (c.startsWith("--") ? cssVar(c) : c);
const clock = (t: number) => new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const FONT = "500 11px Plus Jakarta Sans Variable, system-ui, sans-serif";

/**
 * Hover tooltip: the time and each series' value, in a small card styled
 * with the app's tokens. With many series (per-core CPU) only the busiest
 * few are listed, highest first.
 */
function tooltipPlugin(
  seriesRef: { current: SeriesDef[] },
  colors: string[],
  fmt: { current: (v: number) => string },
): uPlot.Plugin {
  let tip: HTMLDivElement;
  return {
    hooks: {
      init: (u) => {
        tip = document.createElement("div");
        tip.className =
          "pointer-events-none absolute z-10 hidden min-w-32 rounded-lg border border-border-strong bg-surface px-2.5 py-2 text-xs shadow-pop";
        u.over.appendChild(tip);
        u.over.addEventListener("mouseleave", () => tip.classList.add("hidden"));
      },
      setCursor: (u) => {
        const i = u.cursor.idx;
        if (i == null || u.cursor.left == null || u.cursor.left < 0) {
          tip.classList.add("hidden");
          return;
        }
        // Read through the ref: labels can change without re-creating the chart.
        const series = seriesRef.current;
        const many = series.length > 4;
        const rows = series
          .map((s, k) => ({ s, k, v: u.data[k + 1][i] as number | null | undefined }))
          .filter((r) => r.v != null);
        if (many) rows.sort((a, b) => (b.v as number) - (a.v as number));
        const shown = many ? rows.slice(0, 8) : rows;
        tip.replaceChildren();
        const head = document.createElement("div");
        head.className = "num mb-1 font-semibold text-fg";
        head.textContent = new Date((u.data[0][i] as number) * 1000).toLocaleTimeString();
        tip.appendChild(head);
        for (const r of shown) {
          const row = document.createElement("div");
          row.className = "flex items-center gap-2 text-muted";
          const dot = document.createElement("span");
          dot.className = "size-2 shrink-0 rounded-full";
          dot.style.background = colors[r.k];
          const name = document.createElement("span");
          name.className = "flex-1";
          name.textContent = r.s.label;
          const val = document.createElement("span");
          val.className = "num font-semibold text-fg";
          val.textContent = fmt.current(r.v as number);
          row.append(dot, name, val);
          tip.appendChild(row);
        }
        tip.classList.remove("hidden");
        // Beside the cursor, flipped to the left near the right edge.
        const w = tip.offsetWidth;
        const left = u.cursor.left + 14 + w > u.over.clientWidth ? u.cursor.left - w - 14 : u.cursor.left + 14;
        tip.style.left = `${Math.max(0, left)}px`;
        tip.style.top = `${Math.max(0, (u.cursor.top ?? 0) - tip.offsetHeight / 2)}px`;
      },
    },
  };
}

/**
 * Lightweight time-series chart on uPlot (canvas). `data[0]` is unix
 * seconds; the rest are series aligned to it.
 */
export function TimeChart({
  data,
  series,
  height = 140,
  yFormat = (v) => String(v),
  yMax,
  yMin = 0,
  className,
  label,
}: {
  data: uPlot.AlignedData;
  series: SeriesDef[];
  height?: number;
  yFormat?: (v: number) => string;
  yMax?: number;
  yMin?: number;
  className?: string;
  label: string;
}) {
  const el = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const dark = useIsDark();
  const fmt = useRef(yFormat);
  fmt.current = yFormat;
  const seriesRef = useRef(series);
  seriesRef.current = series;

  useEffect(() => {
    const node = el.current;
    if (!node) return;
    const grid = withAlpha(cssVar("--border"), dark ? 0.9 : 1);
    const text = cssVar("--fg-subtle");
    const surface = cssVar("--surface");
    const colors = series.map((s) => resolve(s.color));
    const opts: uPlot.Options = {
      width: node.clientWidth || 300,
      height,
      padding: [8, 6, 0, 0],
      cursor: {
        points: {
          size: (u, i) => (series.length > 4 ? 0 : (u.series[i].width ?? 2) * 3.5),
          width: 2,
          stroke: (_u, i) => colors[i - 1] ?? text,
          fill: () => surface,
        },
        drag: { x: false, y: false },
        y: false,
      },
      legend: { show: false },
      scales: {
        x: { time: true },
        y: { range: (_u, min, max) => [Math.min(yMin, min), yMax ?? Math.max(max * 1.15, yMin + 1e-9)] },
      },
      axes: [
        {
          stroke: text,
          grid: { show: false },
          ticks: { show: false },
          size: 24,
          font: FONT,
          values: (_u, ticks) => ticks.map(clock),
          space: 80,
        },
        {
          stroke: text,
          grid: { stroke: grid, width: 1, dash: [3, 4] },
          ticks: { show: false },
          size: 48,
          font: FONT,
          values: (_u, ticks) => ticks.map((t) => fmt.current(t)),
          space: 32,
        },
      ],
      series: [
        {},
        ...series.map((s, i) => {
          const c = colors[i];
          return {
            label: s.label,
            stroke: c,
            width: s.width ?? 1.75,
            dash: s.dash ? [5, 4] : undefined,
            fill: s.fill
              ? (u: uPlot) => {
                  const g = u.ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height);
                  g.addColorStop(0, withAlpha(c, dark ? 0.3 : 0.24));
                  g.addColorStop(1, withAlpha(c, 0));
                  return g;
                }
              : undefined,
            points: { show: false },
          } satisfies uPlot.Series;
        }),
      ],
      plugins: [tooltipPlugin(seriesRef, colors, fmt)],
    };
    const p = new uPlot(opts, data, node);
    plot.current = p;
    const ro = new ResizeObserver(() => p.setSize({ width: node.clientWidth, height }));
    ro.observe(node);
    return () => {
      ro.disconnect();
      p.destroy();
      plot.current = null;
    };
    // Re-create on theme, shape or scale change. Labels and the formatter are
    // read through refs, so new series objects each render don't rebuild it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dark, height, series.length, yMax, yMin, series.map((s) => s.color).join()]);

  useEffect(() => {
    plot.current?.setData(data);
  }, [data]);

  return <div ref={el} role="img" aria-label={label} className={className} />;
}

const rgbCache = new Map<string, [number, number, number]>();

/**
 * Any CSS colour (hex, rgb(), oklch()…) with alpha a, as rgba(). The browser
 * resolves the colour by painting one pixel, so theme tokens in any colour
 * space work for canvas gradients.
 */
function withAlpha(color: string, a: number): string {
  const c = color.trim();
  let rgb = rgbCache.get(c);
  if (!rgb) {
    const cv = document.createElement("canvas");
    cv.width = cv.height = 1;
    const ctx = cv.getContext("2d", { willReadFrequently: true });
    if (!ctx) return c;
    ctx.fillStyle = "#000";
    ctx.fillStyle = c;
    ctx.fillRect(0, 0, 1, 1);
    const d = ctx.getImageData(0, 0, 1, 1).data;
    rgb = [d[0], d[1], d[2]];
    rgbCache.set(c, rgb);
  }
  return `rgba(${rgb[0]},${rgb[1]},${rgb[2]},${a})`;
}

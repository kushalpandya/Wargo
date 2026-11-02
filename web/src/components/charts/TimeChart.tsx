// SPDX-License-Identifier: AGPL-3.0-only
import { useEffect, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { cssVar, useIsDark } from "@/lib/theme";

export interface SeriesDef {
  label: string;
  /** CSS variable name, e.g. "--accent". */
  color: string;
  fill?: boolean;
  dash?: boolean;
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

  useEffect(() => {
    const node = el.current;
    if (!node) return;
    const grid = cssVar("--border");
    const text = cssVar("--fg-subtle");
    const opts: uPlot.Options = {
      width: node.clientWidth || 300,
      height,
      padding: [6, 4, 0, 0],
      cursor: { points: { size: 6 }, drag: { x: false, y: false } },
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
          size: 22,
          font: "10px JetBrains Mono Variable, monospace",
          values: (_u, ticks) => ticks.map((t) => new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })),
          space: 70,
        },
        {
          stroke: text,
          grid: { stroke: grid, width: 1 },
          ticks: { show: false },
          size: 46,
          font: "10px JetBrains Mono Variable, monospace",
          values: (_u, ticks) => ticks.map((t) => fmt.current(t)),
          space: 28,
        },
      ],
      series: [
        {},
        ...series.map((s) => {
          const c = cssVar(s.color);
          return {
            label: s.label,
            stroke: c,
            width: 1.5,
            dash: s.dash ? [4, 3] : undefined,
            fill: s.fill ? (u: uPlot) => {
              const g = u.ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height);
              g.addColorStop(0, hexA(c, 0.28));
              g.addColorStop(1, hexA(c, 0.01));
              return g;
            } : undefined,
            points: { show: false },
          } satisfies uPlot.Series;
        }),
      ],
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
    // Re-create on theme or shape change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dark, height, series.length, yMax]);

  useEffect(() => {
    plot.current?.setData(data);
  }, [data]);

  return <div ref={el} role="img" aria-label={label} className={className} />;
}

function hexA(color: string, a: number): string {
  const c = color.trim();
  if (c.startsWith("#") && c.length === 7) {
    const r = parseInt(c.slice(1, 3), 16);
    const g = parseInt(c.slice(3, 5), 16);
    const b = parseInt(c.slice(5, 7), 16);
    return `rgba(${r},${g},${b},${a})`;
  }
  return c;
}

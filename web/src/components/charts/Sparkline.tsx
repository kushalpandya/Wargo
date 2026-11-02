// SPDX-License-Identifier: AGPL-3.0-only
import { useId } from "react";
import { cn } from "@/lib/cn";

/** Tiny SVG area sparkline. Values are plotted against [0, max]. */
export function Sparkline({
  values,
  max,
  className,
  stroke = "var(--accent)",
  height = 28,
}: {
  values: number[];
  max?: number;
  className?: string;
  stroke?: string;
  height?: number;
}) {
  const id = useId();
  const w = 100;
  const h = height;
  if (values.length < 2) return <div className={cn("w-full", className)} style={{ height }} aria-hidden="true" />;
  const m = max ?? Math.max(...values, 1e-9) * 1.1;
  const step = w / (values.length - 1);
  const pts = values.map((v, i) => [i * step, h - (Math.min(v, m) / m) * (h - 2) - 1] as const);
  const line = pts.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(2)},${y.toFixed(2)}`).join("");
  const area = `${line}L${w},${h}L0,${h}Z`;
  return (
    <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className={cn("w-full", className)} style={{ height }} aria-hidden="true">
      <defs>
        <linearGradient id={id} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor={stroke} stopOpacity="0.25" />
          <stop offset="1" stopColor={stroke} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#${CSS.escape(id)})`} />
      <path d={line} fill="none" stroke={stroke} strokeWidth="1.25" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

/** Ring gauge 0..100. */
export function Ring({
  value,
  size = 56,
  stroke = 5,
  tone = "accent",
  children,
  label,
}: {
  value: number;
  size?: number;
  stroke?: number;
  tone?: "accent" | "ok" | "warn" | "crit" | "frost";
  children?: React.ReactNode;
  label: string;
}) {
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  const v = Math.max(0, Math.min(100, value || 0));
  return (
    <div className="relative shrink-0" style={{ width: size, height: size }} role="meter" aria-label={label} aria-valuenow={Math.round(v)} aria-valuemin={0} aria-valuemax={100}>
      <svg width={size} height={size} className="-rotate-90">
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--surface-3)" strokeWidth={stroke} />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          stroke={`var(--${tone})`}
          strokeWidth={stroke}
          strokeDasharray={c}
          strokeDashoffset={c * (1 - v / 100)}
          strokeLinecap="round"
          className="transition-[stroke-dashoffset] duration-700"
        />
      </svg>
      <div className="absolute inset-0 flex items-center justify-center">{children}</div>
    </div>
  );
}

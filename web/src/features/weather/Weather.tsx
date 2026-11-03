// SPDX-License-Identifier: AGPL-3.0-only
import type { ReactNode } from "react";
import { Link } from "react-router";
import {
  Cloud,
  CloudDrizzle,
  CloudFog,
  CloudLightning,
  CloudMoon,
  CloudRain,
  CloudSnow,
  CloudSun,
  Moon,
  Sun,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { useWeather } from "./api";

/** WMO weather codes (as used by Open-Meteo) to an icon and a short description. */
function describe(code: number, day: boolean): { Icon: LucideIcon; text: string; tint: string } {
  if (code === 0) return day ? { Icon: Sun, text: "Clear", tint: "text-butter" } : { Icon: Moon, text: "Clear", tint: "text-iris" };
  if (code === 1 || code === 2) return { ...(day ? { Icon: CloudSun, tint: "text-butter" } : { Icon: CloudMoon, tint: "text-iris" }), text: code === 1 ? "Mostly clear" : "Partly cloudy" };
  if (code === 3) return { Icon: Cloud, text: "Cloudy", tint: "text-sky" };
  if (code === 45 || code === 48) return { Icon: CloudFog, text: "Fog", tint: "text-subtle" };
  if (code >= 51 && code <= 57) return { Icon: CloudDrizzle, text: "Drizzle", tint: "text-sky" };
  if ((code >= 61 && code <= 67) || (code >= 80 && code <= 82)) return { Icon: CloudRain, text: code >= 80 ? "Showers" : "Rain", tint: "text-sky" };
  if ((code >= 71 && code <= 77) || code === 85 || code === 86) return { Icon: CloudSnow, text: "Snow", tint: "text-sky" };
  if (code >= 95) return { Icon: CloudLightning, text: "Thunderstorm", tint: "text-peach" };
  return { Icon: Cloud, text: "Cloudy", tint: "text-sky" };
}

/**
 * Current weather in one line ("☀ 21° Clear · Toronto · H 21° L 12°"), for
 * the Home greeting. Renders nothing when weather is off; a short hint when
 * no location is known.
 */
export function WeatherInline({ className }: { className?: string }): ReactNode {
  const { data } = useWeather();
  if (!data?.enabled) return null;
  if (!data.current || !data.location) {
    return data.error ? (
      <Link to="/settings/appearance" className={cn("text-subtle underline-offset-2 hover:underline", className)}>
        {data.error}
      </Link>
    ) : null;
  }
  const { Icon, text, tint } = describe(data.current.code, data.current.isDay);
  const deg = (v: number) => `${Math.round(v)}°`;
  const unit = data.units === "imperial" ? "°F" : "°C";
  return (
    <span
      className={cn("inline-flex min-w-0 items-center gap-1.5", className)}
      title={`${text}, feels like ${deg(data.current.feels)}${unit.slice(1)} · humidity ${Math.round(data.current.humidity)}% · wind ${Math.round(data.current.wind)} ${data.units === "imperial" ? "mph" : "km/h"}${data.auto ? " · location from the server's timezone" : ""} · Open-Meteo`}
    >
      <Icon className={cn("size-4 shrink-0", tint)} aria-hidden="true" />
      <span className="num font-semibold text-fg">{deg(data.current.temp)}</span>
      <span className="truncate">
        {text} · {data.location.name}
        {data.today && <span className="num max-md:hidden"> · H {deg(data.today.max)} L {deg(data.today.min)}</span>}
      </span>
    </span>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
import { Cable, Network, ShieldCheck, Wifi, WifiHigh, WifiLow, WifiOff, WifiZero } from "lucide-react";
import { cn } from "@/lib/cn";
import type { Link } from "./useMetrics";

/** Icon for a link, reflecting Wi-Fi signal strength. */
export function LinkIcon({ link, className }: { link?: Link; className?: string }) {
  const c = cn("size-3.5 shrink-0", className);
  if (!link) return <Network className={c} aria-hidden="true" />;
  if (link.kind === "wifi") {
    if (!link.up) return <WifiOff className={c} aria-hidden="true" />;
    const s = link.signalDbm ?? 0;
    const Icon = s === 0 || s >= -55 ? Wifi : s >= -67 ? WifiHigh : s >= -75 ? WifiLow : WifiZero;
    return <Icon className={c} aria-hidden="true" />;
  }
  if (link.kind === "ethernet") return <Cable className={c} aria-hidden="true" />;
  if (link.kind === "vpn") return <ShieldCheck className={c} aria-hidden="true" />;
  return <Network className={c} aria-hidden="true" />;
}

export function rate(mbps?: number): string | undefined {
  if (!mbps) return undefined;
  return mbps >= 1000 ? `${(mbps / 1000).toFixed(mbps % 1000 ? 1 : 0)} Gb/s` : `${Math.round(mbps)} Mb/s`;
}

/** Signal quality in words; dBm thresholds as used by most OSes. */
export function signalWord(dbm?: number): string | undefined {
  if (!dbm) return undefined;
  return dbm >= -55 ? "excellent" : dbm >= -67 ? "good" : dbm >= -75 ? "fair" : "weak";
}

/** Short name of what the interface is connected to ("Sohrot", "Ethernet"). */
export function linkName(link?: Link): string | undefined {
  if (!link) return undefined;
  if (link.kind === "wifi") return link.up ? link.ssid || "Wi-Fi" : "Wi-Fi (disconnected)";
  if (link.kind === "ethernet") return link.up ? link.connection && !/^Wired connection/i.test(link.connection) ? link.connection : "Ethernet" : "Ethernet (unplugged)";
  if (link.kind === "vpn") return link.connection || "VPN";
  return link.connection;
}

/** Details line: "5 GHz · −44 dBm (excellent) · 1.2 Gb/s". */
export function linkDetails(link?: Link): string | undefined {
  if (!link || !link.up) return undefined;
  const parts: string[] = [];
  if (link.kind === "wifi") {
    if (link.band) parts.push(link.band);
    if (link.signalDbm) parts.push(`${link.signalDbm} dBm (${signalWord(link.signalDbm)})`);
  }
  const r = rate(link.rateMbps);
  if (r) parts.push(r);
  return parts.length ? parts.join(" · ") : undefined;
}

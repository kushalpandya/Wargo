// SPDX-License-Identifier: AGPL-3.0-only
import { ExternalLink } from "lucide-react";
import { cn } from "@/lib/cn";
import type { Container, Port } from "./types";

/** "lscr.io/linuxserver/plex:latest" -> ["lscr.io/linuxserver/plex", "latest"]. */
export function splitImage(image: string): [string, string] {
  const at = image.indexOf("@");
  if (at >= 0) return [image.slice(0, at), image.slice(at + 1, at + 20)];
  const slash = image.lastIndexOf("/");
  const colon = image.lastIndexOf(":");
  if (colon > slash) return [image.slice(0, colon), image.slice(colon + 1)];
  return [image, "latest"];
}

/** Short display name for an image (drops registry and "library/"). */
export function shortImage(image: string): string {
  const [repo] = splitImage(image);
  const parts = repo.split("/");
  if (parts.length > 1 && (parts[0].includes(".") || parts[0].includes(":"))) parts.shift();
  return parts.join("/").replace(/^library\//, "");
}

const httpsPorts = new Set([443, 8443, 9443, 10443]);

export function portUrl(p: Port): string | null {
  if (!p.public || p.type !== "tcp") return null;
  const proto = httpsPorts.has(p.private) || httpsPorts.has(p.public) ? "https" : "http";
  const host = p.ip && p.ip !== "127.0.0.1" ? p.ip : location.hostname;
  if (p.ip === "127.0.0.1") return null;
  return `${proto}://${host.includes(":") ? `[${host}]` : host}:${p.public}`;
}

/** Ports to show: own published ports, else those of the network container. */
export function displayPorts(c: Container): { ports: Port[]; via?: string } {
  if (c.ports.some((p) => p.public)) return { ports: c.ports.filter((p) => p.public) };
  if (c.networkContainer && c.networkPorts?.some((p) => p.public)) {
    return { ports: c.networkPorts.filter((p) => p.public), via: c.networkContainer };
  }
  return { ports: [] };
}

export function PortLinks({ c, max = 3, className }: { c: Container; max?: number; className?: string }) {
  const { ports, via } = displayPorts(c);
  if (c.networkMode === "host") return <span className={cn("text-xs text-muted", className)}>host network</span>;
  if (ports.length === 0) return <span className={cn("text-xs text-subtle", className)}>—</span>;
  const shown = ports.slice(0, max);
  return (
    <div className={cn("flex min-w-0 flex-wrap items-center gap-1", className)}>
      {shown.map((p) => {
        const url = portUrl(p);
        const label = `${p.public}${p.public !== p.private ? `→${p.private}` : ""}${p.type === "udp" ? "/udp" : ""}`;
        return url ? (
          <a
            key={`${p.public}-${p.type}`}
            href={url}
            target="_blank"
            rel="noreferrer noopener"
            onClick={(e) => e.stopPropagation()}
            className={cn(
              "num inline-flex h-6 items-center gap-1 rounded border border-border bg-surface-2 px-1.5 text-xs text-frost hover:border-frost/40 hover:bg-frost-soft",
              via && "border-dashed",
            )}
            title={via ? `${url} (published by ${via})` : url}
          >
            {label}
            <ExternalLink className="size-3" aria-hidden="true" />
          </a>
        ) : (
          <span key={`${p.public}-${p.type}`} className="num inline-flex h-6 items-center rounded border border-border px-1.5 text-xs text-muted">
            {label}
          </span>
        );
      })}
      {ports.length > max && <span className="text-xs text-subtle">+{ports.length - max}</span>}
    </div>
  );
}

/** "Up 18 hours (healthy)" -> "Up 18 hours". */
export function statusText(c: Container): string {
  return c.status.replace(/\s*\((healthy|unhealthy|health: starting)\)/, "");
}

/** Parses RFC 3339 with nanoseconds (trimmed to ms for Date). */
export function parseTime(s?: string): number {
  if (!s) return 0;
  const t = new Date(s.replace(/(\.\d{3})\d+/, "$1")).getTime();
  return isNaN(t) ? 0 : t;
}

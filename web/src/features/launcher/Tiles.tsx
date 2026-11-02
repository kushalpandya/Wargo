// SPDX-License-Identifier: AGPL-3.0-only
import { ExternalLink } from "lucide-react";
import { cn } from "@/lib/cn";
import { StatusDot } from "@/components/ui/misc";

export interface Tile {
  id: string;
  name: string;
  url: string;
  icon?: string;
  group?: string;
  sort: number;
  newTab: boolean;
  healthUrl?: string;
  enabled: boolean;
  /** Result of the last health check, when healthUrl is set. */
  status?: "up" | "down" | null;
}

export function TileIcon({ tile, className }: { tile: Pick<Tile, "name" | "icon">; className?: string }) {
  if (tile.icon) {
    return <img src={`/api/launcher/icons/${encodeURIComponent(tile.icon)}`} alt="" className={cn("size-8 rounded-md object-contain", className)} loading="lazy" />;
  }
  return (
    <span className={cn("flex size-8 items-center justify-center rounded-md bg-accent-soft text-sm font-semibold text-accent", className)} aria-hidden="true">
      {tile.name.slice(0, 1).toUpperCase()}
    </span>
  );
}

export function TileGrid({ tiles, compact }: { tiles: Tile[]; compact?: boolean }) {
  const groups = new Map<string, Tile[]>();
  for (const t of [...tiles].sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name))) {
    const g = compact ? "" : t.group || "";
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g)!.push(t);
  }
  return (
    <div className="flex flex-col gap-4">
      {[...groups.entries()].map(([g, list]) => (
        <section key={g || "_"} aria-label={g || "Apps"}>
          {g && <h2 className="mb-2 text-2xs font-semibold uppercase tracking-wider text-subtle">{g}</h2>}
          <div className={cn("grid gap-2", compact ? "grid-cols-2 sm:grid-cols-4 lg:grid-cols-6 2xl:grid-cols-8" : "grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5")}>
            {list.map((t) => (
              <a
                key={t.id}
                href={t.url}
                target={t.newTab ? "_blank" : undefined}
                rel="noopener noreferrer"
                className="group flex min-w-0 items-center gap-2.5 rounded-lg border border-border bg-surface px-3 py-2.5 transition-colors hover:border-border-strong hover:bg-surface-2"
              >
                <TileIcon tile={t} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate text-[0.8125rem] font-medium">{t.name}</span>
                    {t.status && <StatusDot tone={t.status === "up" ? "ok" : "crit"} label={t.status === "up" ? "Online" : "Offline"} />}
                  </div>
                  <div className="num truncate text-2xs text-muted">{t.url.replace(/^https?:\/\//, "")}</div>
                </div>
                {t.newTab && <ExternalLink className="size-3.5 shrink-0 text-subtle opacity-0 transition-opacity group-hover:opacity-100" />}
              </a>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

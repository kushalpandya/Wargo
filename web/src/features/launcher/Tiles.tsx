// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import { ArrowLeft, ArrowRight, EllipsisVertical, Pencil, Trash2 } from "lucide-react";
import { cn } from "@/lib/cn";
import { safeHttpUrl } from "@/lib/url";
import { StatusDot } from "@/components/ui/misc";
import { Menu, MenuItem, MenuSeparator } from "@/components/ui/menu";

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

export function TileIcon({ tile, className, large }: { tile: Pick<Tile, "name" | "icon">; className?: string; large?: boolean }) {
  if (tile.icon) {
    return (
      <img
        src={`/api/launcher/icons/${encodeURIComponent(tile.icon)}`}
        alt=""
        draggable={false}
        className={cn(large ? "size-12 rounded-lg" : "size-8 rounded-md", "object-contain", className)}
        loading="lazy"
      />
    );
  }
  return (
    <span
      className={cn("flex items-center justify-center bg-accent-soft font-semibold text-accent", large ? "size-12 rounded-lg text-xl" : "size-8 rounded-md text-sm", className)}
      aria-hidden="true"
    >
      {tile.name.slice(0, 1).toUpperCase()}
    </span>
  );
}

export interface TileGridEdit {
  onEdit: (t: Tile) => void;
  onDelete: (t: Tile) => void;
  /** Called with the new order of the shown tiles after a drag or move. */
  onReorder: (tiles: Tile[]) => void;
}

const sortTiles = (tiles: Tile[]) => [...tiles].sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name));

/** Square app tiles. `compact` (Home) leaves out group headings. */
export function TileGrid({ tiles, compact, edit }: { tiles: Tile[]; compact?: boolean; edit?: TileGridEdit }) {
  const [preview, setPreview] = useState<Tile[] | null>(null);
  const [dragId, setDragId] = useState<string | null>(null);
  const [announce, setAnnounce] = useState("");
  const ordered = preview ?? sortTiles(tiles);
  const groupOf = (t: Tile) => (compact ? "" : t.group || "");
  const groups = new Map<string, Tile[]>();
  for (const t of ordered) {
    const g = groupOf(t);
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g)!.push(t);
  }

  const moveTo = (list: Tile[], id: string, to: number) => {
    const from = list.findIndex((x) => x.id === id);
    if (from < 0 || from === to) return list;
    const next = [...list];
    const [m] = next.splice(from, 1);
    next.splice(to, 0, m);
    return next;
  };
  // Keyboard/menu alternative to dragging: swap with the neighbour in the same group.
  const step = (t: Tile, d: -1 | 1) => {
    const peers = groups.get(groupOf(t)) ?? [];
    const i = peers.findIndex((x) => x.id === t.id);
    const other = peers[i + d];
    if (!other) return;
    const next = moveTo(ordered, t.id, ordered.findIndex((x) => x.id === other.id));
    setAnnounce(`${t.name} moved to position ${i + d + 1} of ${peers.length}`);
    edit?.onReorder(next);
  };

  return (
    <div className="flex flex-col gap-4">
      {edit && (
        <p className="sr-only" aria-live="polite">
          {announce}
        </p>
      )}
      {[...groups.entries()].map(([g, list]) => (
        <section key={g || "_"} aria-label={g || "Apps"}>
          {g && <h2 className="mb-2 text-2xs font-semibold uppercase tracking-wider text-subtle">{g}</h2>}
          <div
            // Square tiles that keep their size as the screen widens.
            className="grid grid-cols-[repeat(auto-fill,minmax(7rem,1fr))] gap-2 sm:grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] sm:gap-3"
          >
            {list.map((t, i) => (
              <div
                key={t.id}
                className={cn("group relative min-w-0 rounded-lg", dragId === t.id && "opacity-50")}
                draggable={!!edit}
                onDragStart={(e) => {
                  setDragId(t.id);
                  setPreview(ordered);
                  e.dataTransfer.effectAllowed = "move";
                  e.dataTransfer.setData("text/plain", t.id);
                }}
                onDragOver={(e) => {
                  if (!dragId) return;
                  const dragged = ordered.find((x) => x.id === dragId);
                  // Tiles only move within their own group.
                  if (!dragged || groupOf(dragged) !== g) return;
                  e.preventDefault();
                  if (dragId !== t.id) setPreview(moveTo(ordered, dragId, ordered.findIndex((x) => x.id === t.id)));
                }}
                onDrop={(e) => e.preventDefault()}
                onDragEnd={() => {
                  const before = sortTiles(tiles).map((x) => x.id).join();
                  if (preview && preview.map((x) => x.id).join() !== before) edit?.onReorder(preview);
                  setDragId(null);
                  setPreview(null);
                }}
              >
                <a
                  // The server validates tile URLs; never link to anything but http(s) regardless.
                  href={safeHttpUrl(t.url)}
                  target={t.newTab ? "_blank" : undefined}
                  rel="noopener noreferrer"
                  draggable={false}
                  className={cn(
                    "relative flex aspect-square min-w-0 flex-col items-center justify-center gap-2 rounded-xl border border-border bg-surface px-2 pb-2 pt-3 text-center transition-colors hover:border-border-strong hover:bg-surface-2 sm:gap-2.5 sm:px-2.5 sm:pb-2.5 sm:pt-4",
                    edit && "cursor-grab active:cursor-grabbing",
                  )}
                >
                  {t.status && (
                    <span className="absolute left-2.5 top-2.5">
                      <StatusDot tone={t.status === "up" ? "ok" : "crit"} label={t.status === "up" ? "Online" : "Offline"} />
                    </span>
                  )}
                  <TileIcon tile={t} large />
                  <div className="w-full min-w-0">
                    <div className="truncate text-ui font-medium">{t.name}</div>
                    <div className="num truncate text-2xs text-muted">{t.url.replace(/^https?:\/\//, "")}</div>
                  </div>
                </a>
                {edit && (
                  <div className="absolute right-1.5 top-1.5">
                    <Menu
                      trigger={
                        <button
                          type="button"
                          aria-label={`Actions for ${t.name}`}
                          className="flex size-7 items-center justify-center rounded-md text-subtle opacity-0 transition-opacity hover:bg-surface-3 hover:text-fg focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25 group-hover:opacity-100 data-[state=open]:bg-surface-3 data-[state=open]:opacity-100 [@media(hover:none)]:opacity-100"
                        >
                          <EllipsisVertical className="size-4" />
                        </button>
                      }
                    >
                      <MenuItem icon={<Pencil />} onSelect={() => edit.onEdit(t)}>
                        Edit
                      </MenuItem>
                      <MenuItem icon={<ArrowLeft />} disabled={i === 0} onSelect={() => step(t, -1)}>
                        Move left
                      </MenuItem>
                      <MenuItem icon={<ArrowRight />} disabled={i === list.length - 1} onSelect={() => step(t, 1)}>
                        Move right
                      </MenuItem>
                      <MenuSeparator />
                      <MenuItem icon={<Trash2 />} danger onSelect={() => edit.onDelete(t)}>
                        Delete
                      </MenuItem>
                    </Menu>
                  </div>
                )}
              </div>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

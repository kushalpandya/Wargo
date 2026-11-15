// SPDX-License-Identifier: AGPL-3.0-only
import { useId, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { ArrowLeft, ArrowRight, EllipsisVertical, Pencil, Trash2 } from "lucide-react";
import { cn } from "@/lib/cn";
import { safeHttpUrl } from "@/lib/url";
import { StatusDot } from "@/components/ui/misc";
import { Menu, MenuItem, MenuSeparator } from "@/components/ui/menu";
import { tileBoxClass, tileGridClass, type TileSize } from "./layout";

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
  /** A tile Wargo provides: it links inside Wargo and can be reordered, not edited or removed. */
  builtin?: "files" | "terminal" | "torrents";
}

/** The second line under a tile's name: where it goes. */
export function tileDetail(t: Pick<Tile, "url" | "builtin">) {
  if (t.builtin === "files") return "Browse files";
  if (t.builtin === "terminal") return "Command line";
  if (t.builtin === "torrents") return "Search and downloads";
  return t.url.replace(/^https?:\/\//, "");
}

/** Files' tile icon: a blue folder with a sheet of paper peeking out of it. */
function FilesGlyph({ className }: { className?: string }) {
  const id = useId();
  return (
    <svg viewBox="0 0 48 48" className={className} aria-hidden="true">
      <defs>
        <linearGradient id={`${id}-back`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#3d8be0" />
          <stop offset="1" stopColor="#1f5fb3" />
        </linearGradient>
        <linearGradient id={`${id}-front`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#7cc8ff" />
          <stop offset="1" stopColor="#3f97ee" />
        </linearGradient>
      </defs>
      <path d="M3 12a4 4 0 0 1 4-4h11.2a4 4 0 0 1 2.9 1.2L24 12h17a4 4 0 0 1 4 4v22a4 4 0 0 1-4 4H7a4 4 0 0 1-4-4z" fill={`url(#${id}-back)`} />
      <rect x="9" y="14.5" width="30" height="16" rx="2" fill="#ffffff" fillOpacity="0.92" />
      <path d="M3 22a4 4 0 0 1 4-4h34a4 4 0 0 1 4 4v16a4 4 0 0 1-4 4H7a4 4 0 0 1-4-4z" fill={`url(#${id}-front)`} />
      <path d="M7 19.5h34" stroke="#ffffff" strokeOpacity="0.45" strokeWidth="1.2" strokeLinecap="round" />
    </svg>
  );
}

/** Torrents' tile icon: a white horseshoe magnet with red tips on a blue square. */
function TorrentsGlyph({ className }: { className?: string }) {
  const id = useId();
  return (
    <svg viewBox="0 0 48 48" className={className} aria-hidden="true">
      <defs>
        <linearGradient id={`${id}-bg`} x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#4cc3ff" />
          <stop offset="1" stopColor="#2459d8" />
        </linearGradient>
      </defs>
      <rect x="1" y="1" width="46" height="46" rx="10" fill={`url(#${id}-bg)`} />
      <rect x="1.5" y="1.5" width="45" height="45" rx="9.5" fill="none" stroke="#ffffff" strokeOpacity="0.18" />
      <path d="M13 12h7.5v12.5a3.5 3.5 0 0 0 7 0V12H35v12.5a11 11 0 0 1-22 0z" fill="#ffffff" />
      <path d="M13 12h7.5v5.5H13zM27.5 12H35v5.5h-7.5z" fill="#ff5d73" />
    </svg>
  );
}

/** Terminal's tile icon: a dark console window with a green prompt and an accent cursor. */
function TerminalGlyph({ className }: { className?: string }) {
  const id = useId();
  return (
    <svg viewBox="0 0 48 48" className={className} aria-hidden="true">
      <defs>
        <linearGradient id={`${id}-bg`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#3a3f5c" />
          <stop offset="1" stopColor="#161825" />
        </linearGradient>
      </defs>
      <rect x="1" y="1" width="46" height="46" rx="10" fill={`url(#${id}-bg)`} />
      <rect x="1.5" y="1.5" width="45" height="45" rx="9.5" fill="none" stroke="#ffffff" strokeOpacity="0.14" />
      <path d="M1 11V11a10 10 0 0 1 10-10h26a10 10 0 0 1 10 10v0z" fill="#ffffff" fillOpacity="0.08" />
      <path d="M12 21l7 5.5-7 5.5" fill="none" stroke="#4ade80" strokeWidth="3.2" strokeLinecap="round" strokeLinejoin="round" />
      <rect x="22.5" y="30" width="12" height="3.2" rx="1.6" fill="#8b9cff" />
    </svg>
  );
}

export function TileIcon({ tile, className, large }: { tile: Pick<Tile, "name" | "icon" | "builtin">; className?: string; large?: boolean }) {
  const size = large ? "size-12" : "size-8";
  if (tile.builtin === "files") return <FilesGlyph className={cn(size, "shrink-0", className)} />;
  if (tile.builtin === "terminal") return <TerminalGlyph className={cn(size, "shrink-0", className)} />;
  if (tile.builtin === "torrents") return <TorrentsGlyph className={cn(size, "shrink-0", className)} />;
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

/** A built-in tile opens its page in Wargo; any other opens the app's URL. */
function TileLink({ tile, className, children }: { tile: Tile; className: string; children: ReactNode }) {
  if (tile.builtin) {
    return (
      <Link to={tile.url} draggable={false} className={className}>
        {children}
      </Link>
    );
  }
  return (
    <a
      // The server validates tile URLs; never link to anything but http(s) regardless.
      href={safeHttpUrl(tile.url)}
      target={tile.newTab ? "_blank" : undefined}
      rel="noopener noreferrer"
      draggable={false}
      className={className}
    >
      {children}
    </a>
  );
}

export interface TileGridEdit {
  onEdit: (t: Tile) => void;
  onDelete: (t: Tile) => void;
  /** Called with the new order of the shown tiles after a drag or move. */
  onReorder: (tiles: Tile[]) => void;
}

/** Tiles in their saved order. */
export const sortTiles = (tiles: Tile[]) => [...tiles].sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name));

/** The group names in use, for the editor's group field. */
export const tileGroups = (tiles: Tile[]) => [...new Set(tiles.map((t) => t.group).filter(Boolean) as string[])].sort();

/** The health check's result, when the tile has one. */
export function TileStatus({ tile }: { tile: Pick<Tile, "status"> }) {
  if (!tile.status) return null;
  return <StatusDot tone={tile.status === "up" ? "ok" : "crit"} label={tile.status === "up" ? "Online" : "Offline"} />;
}

/** The line under a tile's name. */
export function TileDetail({ tile }: { tile: Pick<Tile, "url" | "builtin"> }) {
  return <div className={cn("truncate text-2xs text-muted", !tile.builtin && "num")}>{tileDetail(tile)}</div>;
}

/**
 * Home's app tiles: large squares, or small rows with the icon left of the
 * name. `trailing` goes after the last tile, in the same grid (the add
 * button).
 */
export function TileGrid({ tiles, edit, trailing, size = "large" }: { tiles: Tile[]; edit?: TileGridEdit; trailing?: ReactNode; size?: TileSize }) {
  const small = size === "small";
  const [preview, setPreview] = useState<Tile[] | null>(null);
  const [dragId, setDragId] = useState<string | null>(null);
  const [announce, setAnnounce] = useState("");
  const ordered = preview ?? sortTiles(tiles);

  const moveTo = (list: Tile[], id: string, to: number) => {
    const from = list.findIndex((x) => x.id === id);
    if (from < 0 || from === to) return list;
    const next = [...list];
    const [m] = next.splice(from, 1);
    next.splice(to, 0, m);
    return next;
  };
  // Keyboard/menu alternative to dragging: swap with the neighbour.
  const step = (i: number, d: -1 | 1) => {
    const t = ordered[i];
    if (!ordered[i + d]) return;
    setAnnounce(`${t.name} moved to position ${i + d + 1} of ${ordered.length}`);
    edit?.onReorder(moveTo(ordered, t.id, i + d));
  };

  return (
    <section aria-label="Apps">
      {edit && (
        <p className="sr-only" aria-live="polite">
          {announce}
        </p>
      )}
      {/* Tiles keep their size as the screen widens; more fit on a line. */}
      <div className={tileGridClass[size]}>
        {ordered.map((t, i) => (
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
              e.preventDefault();
              if (dragId !== t.id) setPreview(moveTo(ordered, dragId, i));
            }}
            onDrop={(e) => e.preventDefault()}
            onDragEnd={() => {
              const before = sortTiles(tiles).map((x) => x.id).join();
              if (preview && preview.map((x) => x.id).join() !== before) edit?.onReorder(preview);
              setDragId(null);
              setPreview(null);
            }}
          >
            {small ? (
              <TileLink
                tile={t}
                className={cn(
                  "flex min-w-0 items-center gap-3 border border-border bg-surface px-3 transition-colors hover:border-border-strong hover:bg-surface-2",
                  tileBoxClass.small,
                  // Touch screens always show the actions button: keep the text clear of it.
                  edit && "cursor-grab active:cursor-grabbing [@media(hover:none)]:pr-10",
                )}
              >
                <TileIcon tile={t} />
                <div className="min-w-0 flex-1">
                  <div className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate text-ui font-medium">{t.name}</span>
                    <TileStatus tile={t} />
                  </div>
                  <TileDetail tile={t} />
                </div>
              </TileLink>
            ) : (
              <TileLink
                tile={t}
                className={cn(
                  "relative flex min-w-0 flex-col items-center justify-center gap-2 border border-border bg-surface px-2 pb-2 pt-3 text-center transition-colors hover:border-border-strong hover:bg-surface-2 sm:gap-2.5 sm:px-2.5 sm:pb-2.5 sm:pt-4",
                  tileBoxClass.large,
                  edit && "cursor-grab active:cursor-grabbing",
                )}
              >
                {t.status && (
                  <span className="absolute left-2.5 top-2.5">
                    <TileStatus tile={t} />
                  </span>
                )}
                <TileIcon tile={t} large />
                <div className="w-full min-w-0">
                  <div className="truncate text-ui font-medium">{t.name}</div>
                  <TileDetail tile={t} />
                </div>
              </TileLink>
            )}
            {edit && (
              // Small rows: centred at the right end, on the tile's hover colour so it covers the text it overlaps.
              <div className={cn("absolute right-1.5", small ? "top-1/2 -translate-y-1/2" : "top-1.5")}>
                <Menu
                  trigger={
                    <button
                      type="button"
                      aria-label={`Actions for ${t.name}`}
                      className={cn(
                        "flex size-7 items-center justify-center rounded-md text-subtle opacity-0 transition-opacity hover:bg-surface-3 hover:text-fg focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus/25 group-hover:opacity-100 data-[state=open]:bg-surface-3 data-[state=open]:opacity-100 [@media(hover:none)]:opacity-100",
                        small && "bg-surface-2",
                      )}
                    >
                      <EllipsisVertical className="size-4" />
                    </button>
                  }
                >
                  {!t.builtin && (
                    <MenuItem icon={<Pencil />} onSelect={() => edit.onEdit(t)}>
                      Edit
                    </MenuItem>
                  )}
                  <MenuItem icon={<ArrowLeft />} disabled={i === 0} onSelect={() => step(i, -1)}>
                    Move earlier
                  </MenuItem>
                  <MenuItem icon={<ArrowRight />} disabled={i === ordered.length - 1} onSelect={() => step(i, 1)}>
                    Move later
                  </MenuItem>
                  {!t.builtin && (
                    <>
                      <MenuSeparator />
                      <MenuItem icon={<Trash2 />} danger onSelect={() => edit.onDelete(t)}>
                        Delete
                      </MenuItem>
                    </>
                  )}
                </Menu>
              </div>
            )}
          </div>
        ))}
        {trailing}
      </div>
    </section>
  );
}

// SPDX-License-Identifier: AGPL-3.0-only
import { cn } from "@/lib/cn";
import { readPref } from "@/lib/prefs";
import { Skeleton } from "@/components/ui/misc";

/*
 * How Home's app tiles are laid out, kept apart from the tiles themselves so
 * the launch placeholder (app/AppSkeleton.tsx) doesn't pull them into the
 * main bundle.
 */

export type TileSize = "large" | "small";

/** The grid the tiles sit in: square tiles, or rows of icon and name. */
export const tileGridClass: Record<TileSize, string> = {
  large: "grid grid-cols-[repeat(auto-fill,minmax(7rem,1fr))] gap-2 sm:grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] sm:gap-3",
  small: "grid grid-cols-[repeat(auto-fill,minmax(10.5rem,1fr))] gap-2 sm:grid-cols-[repeat(auto-fill,minmax(13rem,1fr))]",
};

/** One tile's box at each size; the add button uses the same box. */
export const tileBoxClass: Record<TileSize, string> = {
  large: "aspect-square rounded-xl",
  small: "h-14 rounded-xl",
};

/** The size this browser last saw (the server holds the setting), for placeholders. */
export const tileSizePref = "wargo.home.tileSize";
export const lastTileSize = (): TileSize => (readPref(tileSizePref) === "small" ? "small" : "large");

/** Grey tiles standing in for Files and Terminal while the real ones load. */
export function TilePlaceholders({ size }: { size: TileSize }) {
  return (
    <div className={tileGridClass[size]} aria-hidden="true">
      {[0, 1].map((i) => (
        <Skeleton key={i} className={cn("block", tileBoxClass[size])} />
      ))}
    </div>
  );
}

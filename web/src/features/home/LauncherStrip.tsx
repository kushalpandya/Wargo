// SPDX-License-Identifier: AGPL-3.0-only
import { TileGrid } from "@/features/launcher/Tiles";
import { useTiles } from "@/features/launcher/api";

export function LauncherStrip() {
  const { data } = useTiles();
  const tiles = (data ?? []).filter((t) => t.enabled);
  if (!tiles.length) return null;
  return (
    <div className="mb-4">
      <TileGrid tiles={tiles} compact />
    </div>
  );
}

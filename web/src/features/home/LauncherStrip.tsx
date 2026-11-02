// SPDX-License-Identifier: AGPL-3.0-only
import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { TileGrid, type Tile } from "@/features/launcher/Tiles";

export function LauncherStrip() {
  const { data } = useQuery({ queryKey: ["launcher", "tiles"], queryFn: () => api.get<Tile[]>("/api/launcher/tiles"), refetchInterval: 60_000 });
  const tiles = (data ?? []).filter((t) => t.enabled);
  if (!tiles.length) return null;
  return (
    <div className="mb-4">
      <TileGrid tiles={tiles} compact />
    </div>
  );
}

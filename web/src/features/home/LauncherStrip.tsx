// SPDX-License-Identifier: AGPL-3.0-only
import { ErrorNote } from "@/components/ui/misc";
import { LauncherGrid } from "@/features/launcher/TileEditor";
import { useLauncherSettings, useTileAvailable, useTiles } from "@/features/launcher/api";
import { TilePlaceholders, lastTileSize } from "@/features/launcher/layout";

export function LauncherStrip() {
  const { data, isLoading, error } = useTiles();
  const { data: settings } = useLauncherSettings();
  const available = useTileAvailable();
  // Until the setting loads, the size this browser last saw (no jump from large to small).
  const size = settings?.tileSize ?? lastTileSize();
  if (isLoading) return <TilePlaceholders size={size} />;
  const tiles = (data ?? []).filter((t) => t.enabled && available(t));
  return (
    <>
      {error && <ErrorNote error={error} className="mb-2" />}
      {/* Even without tiles the add button stays, as the way into the apps editor. */}
      <LauncherGrid tiles={tiles} size={size} />
    </>
  );
}

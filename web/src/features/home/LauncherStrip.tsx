// SPDX-License-Identifier: AGPL-3.0-only
import { LauncherGrid } from "@/features/launcher/TileEditor";
import { useTiles } from "@/features/launcher/api";

export function LauncherStrip() {
  const { data } = useTiles();
  const tiles = (data ?? []).filter((t) => t.enabled);
  if (!tiles.length) return null;
  return <LauncherGrid tiles={tiles} compact />;
}

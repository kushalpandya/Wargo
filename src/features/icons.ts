// SPDX-License-Identifier: AGPL-3.0-only
// App icons from dashboard-icons (Apache-2.0, see src/icons/LICENSE),
// bundled into the demo so no image is fetched from elsewhere.

const files = import.meta.glob("../icons/*.svg", { query: "?raw", import: "default", eager: true }) as Record<string, string>;

/** slug -> SVG source */
export const ICONS: Record<string, string> = Object.fromEntries(
  Object.entries(files).map(([path, svg]) => [path.replace(/^.*\/(.+)\.svg$/, "$1"), svg]),
);

/** An SVG response for an icon slug, or a 404. */
export function iconResponse(slug: string) {
  const svg = ICONS[slug];
  return svg
    ? new Response(svg, { headers: { "Content-Type": "image/svg+xml", "Cache-Control": "max-age=3600" } })
    : new Response(null, { status: 404 });
}

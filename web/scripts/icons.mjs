// SPDX-License-Identifier: AGPL-3.0-only
// Renders the app icons from public/favicon.svg (a full-bleed square) with
// headless Chrome:
//   node scripts/icons.mjs
// - apple-touch-icon.png (180, square: iOS rounds it itself)
// - icon-192.png, icon-512.png (rounded corners, transparent outside)
// - icon-maskable-512.png (square, art shrunk into the round safe zone)
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

const pub = fileURLToPath(new URL("../public/", import.meta.url));
// Drop the fixed size so the art fills whatever box it is drawn in.
const svg = readFileSync(pub + "favicon.svg", "utf8").replace(/^<svg([^>]*?) width="\d+" height="\d+"/, "<svg$1");
// The background gradient, drawn behind the shrunk maskable art.
const bg = "linear-gradient(135deg, #4a4766, #2c2a42)";
const noBg = svg.replace(/<rect width="512" height="512" fill="url\(#bg\)"\/>/, "");
if (noBg === svg) throw new Error("background rect not found in favicon.svg");

const full = (radius) => `<div style="width:100%;height:100%;border-radius:${radius};overflow:hidden">${svg}</div>`;
const targets = [
  ["apple-touch-icon.png", 180, full("0")],
  ["icon-192.png", 192, full("22%")],
  ["icon-512.png", 512, full("22%")],
  ["icon-maskable-512.png", 512, `<div style="width:100%;height:100%;background:${bg};display:grid;place-items:center"><div style="width:88%;height:88%">${noBg}</div></div>`],
];

const browser = await chromium.launch({ executablePath: process.env.CHROME ?? "/usr/bin/google-chrome-stable", headless: true });
const page = await browser.newPage({ deviceScaleFactor: 1 });
for (const [name, size, html] of targets) {
  await page.setViewportSize({ width: size, height: size });
  await page.setContent(`<!doctype html><style>html,body{margin:0;width:${size}px;height:${size}px;background:transparent}svg{display:block;width:100%;height:100%}</style>${html}`);
  await page.screenshot({ path: pub + name, omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } });
  console.log("wrote", name);
}
await browser.close();

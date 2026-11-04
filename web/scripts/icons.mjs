// SPDX-License-Identifier: AGPL-3.0-only
// Renders the app icons from public/favicon.svg (a full-bleed square) with
// headless Chrome:
//   node scripts/icons.mjs
// - apple-touch-icon.png (180, square, the size Apple documents; iOS and
//   macOS round it themselves) and apple-touch-icon-512.png for larger uses
// - icon-192.png, icon-512.png (rounded corners, transparent outside)
// - icon-maskable-512.png (square, art shrunk into the round safe zone)
// - favicon-32.png and favicon.ico (16, 32, 48): Safari ignores SVG
//   favicons and falls back to these for tabs and bookmarks
// - apple-touch-icon-precomposed.png: the older name some iOS versions ask for
// Then it stamps every icon link in index.html and the manifest with a hash
// of the file (?v=…), so browsers that cached an older icon fetch the new one.
import { createHash } from "node:crypto";
import { copyFileSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
  ["apple-touch-icon-512.png", 512, full("0")],
  ["icon-192.png", 192, full("22%")],
  ["icon-512.png", 512, full("22%")],
  ["favicon-32.png", 32, full("22%")],
  ["ico-16.png", 16, full("22%")],
  ["ico-48.png", 48, full("22%")],
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

// favicon.ico: an icon directory holding the PNGs as they are.
const pngs = [["ico-16.png", 16], ["favicon-32.png", 32], ["ico-48.png", 48]].map(([f, s]) => [readFileSync(pub + f), s]);
const head = Buffer.alloc(6 + 16 * pngs.length);
head.writeUInt16LE(1, 2); // type: icon
head.writeUInt16LE(pngs.length, 4);
let offset = head.length;
pngs.forEach(([png, size], i) => {
  const e = 6 + 16 * i;
  head.writeUInt8(size, e);
  head.writeUInt8(size, e + 1);
  head.writeUInt16LE(1, e + 4); // planes
  head.writeUInt16LE(32, e + 6); // bits per pixel
  head.writeUInt32LE(png.length, e + 8);
  head.writeUInt32LE(offset, e + 12);
  offset += png.length;
});
writeFileSync(pub + "favicon.ico", Buffer.concat([head, ...pngs.map(([png]) => png)]));
console.log("wrote favicon.ico");
copyFileSync(pub + "apple-touch-icon.png", pub + "apple-touch-icon-precomposed.png");
console.log("wrote apple-touch-icon-precomposed.png");
for (const f of ["ico-16.png", "ico-48.png"]) rmSync(pub + f);

// Version stamps: index.html links and manifest icons point at name?v=<hash>.
const stamp = (name) => `${name}?v=${createHash("sha256").update(readFileSync(pub + name)).digest("hex").slice(0, 10)}`;
const manifestPath = pub + "manifest.webmanifest";
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
// PNG only: Apple's platforms can't use an SVG as an app icon.
manifest.icons = [
  { src: stamp("icon-192.png"), sizes: "192x192", type: "image/png", purpose: "any" },
  { src: stamp("icon-512.png"), sizes: "512x512", type: "image/png", purpose: "any" },
  { src: stamp("icon-maskable-512.png"), sizes: "512x512", type: "image/png", purpose: "maskable" },
];
writeFileSync(manifestPath, JSON.stringify(manifest, null, 2) + "\n");
const html = fileURLToPath(new URL("../index.html", import.meta.url));
let doc = readFileSync(html, "utf8");
for (const name of ["favicon.ico", "favicon-32.png", "favicon.svg", "apple-touch-icon.png", "apple-touch-icon-512.png", "manifest.webmanifest"]) {
  doc = doc.replace(new RegExp(`href="/${name.replace(".", "\\.")}(\\?v=[0-9a-f]+)?"`), `href="/${stamp(name)}"`);
}
writeFileSync(html, doc);
console.log("stamped index.html and manifest.webmanifest");

// SPDX-License-Identifier: AGPL-3.0-only
// Screenshot helper for UI review using the system Chrome.
//
//   node scripts/shot.mjs <url> <out-prefix> [--sizes=desktop,tablet,phone] [--schemes=dark,light]
//                         [--wait=ms] [--full] [--click=selector] [--script=file.mjs]
//
// Writes <out-prefix>-<size>-<scheme>.png and prints console errors.
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const pos = args.filter((a) => !a.startsWith("--"));
const opt = Object.fromEntries(args.filter((a) => a.startsWith("--")).map((a) => {
  const [k, v] = a.slice(2).split("=");
  return [k, v ?? true];
}));
const [url, out] = pos;
if (!url || !out) {
  console.error("usage: shot.mjs <url> <out-prefix> [options]");
  process.exit(2);
}

const SIZES = {
  desktop: { width: 1440, height: 900 },
  wide: { width: 1920, height: 1080 },
  ultrawide: { width: 2560, height: 1440 },
  laptop: { width: 1280, height: 800 },
  tablet: { width: 820, height: 1180 },
  phone: { width: 390, height: 844, isMobile: true, hasTouch: true, deviceScaleFactor: 2 },
};
const sizes = String(opt.sizes ?? "desktop").split(",");
const schemes = String(opt.schemes ?? "dark").split(",");
const wait = Number(opt.wait ?? 1500);

const browser = await chromium.launch({
  executablePath: process.env.CHROME ?? "/usr/bin/google-chrome-stable",
  headless: true,
  args: ["--no-first-run", "--disable-gpu"],
});

let failed = false;
for (const size of sizes) {
  for (const scheme of schemes) {
    const vp = SIZES[size];
    const ctx = await browser.newContext({
      viewport: { width: vp.width, height: vp.height },
      deviceScaleFactor: vp.deviceScaleFactor ?? 1,
      isMobile: vp.isMobile ?? false,
      hasTouch: vp.hasTouch ?? false,
      colorScheme: scheme,
    });
    const page = await ctx.newPage();
    const errors = [];
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
    page.on("pageerror", (e) => errors.push(String(e)));
    await page.goto(url, { waitUntil: "networkidle" }).catch((e) => errors.push(String(e)));
    if (opt.click) {
      for (const sel of String(opt.click).split("|")) {
        await page.click(sel, { timeout: 5000 }).catch((e) => errors.push(`click ${sel}: ${e.message}`));
        await page.waitForTimeout(400);
      }
    }
    if (opt.script) {
      const mod = await import(new URL(String(opt.script), `file://${process.cwd()}/`).href);
      await mod.default(page).catch((e) => errors.push(`script: ${e.message}`));
    }
    await page.waitForTimeout(wait);
    const file = `${out}-${size}-${scheme}.png`;
    await page.screenshot({ path: file, fullPage: !!opt.full });
    console.log(`saved ${file}`);
    if (errors.length) {
      failed = true;
      console.log(`  console errors (${size}/${scheme}):\n   - ` + [...new Set(errors)].slice(0, 10).join("\n   - "));
    }
    await ctx.close();
  }
}
await browser.close();
process.exit(failed ? 1 : 0);

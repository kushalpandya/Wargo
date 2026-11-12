// SPDX-License-Identifier: AGPL-3.0-only
// Layout + accessibility audit with the system Chrome and axe-core.
//
//   node scripts/audit.mjs <url> [--sizes=desktop,phone] [--schemes=dark,light] [--wait=ms]
//
// Reports horizontal overflow, elements escaping the viewport, small touch
// targets on phones, and axe-core violations (WCAG 2.1 A/AA).
import { chromium } from "playwright-core";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const axeSource = readFileSync(require.resolve("axe-core/axe.min.js"), "utf8");

const args = process.argv.slice(2);
const url = args.find((a) => !a.startsWith("--"));
const opt = Object.fromEntries(args.filter((a) => a.startsWith("--")).map((a) => a.slice(2).split("=")));
const SIZES = {
  desktop: { width: 1440, height: 900 },
  wide: { width: 1920, height: 1080 },
  ultrawide: { width: 2560, height: 1440 },
  tablet: { width: 820, height: 1180 },
  phone: { width: 390, height: 844, isMobile: true, hasTouch: true },
};
const sizes = (opt.sizes ?? "desktop,phone").split(",");
const schemes = (opt.schemes ?? "dark,light").split(",");

// Installed Chrome: CHROME overrides, else the usual path on macOS or Linux.
const chrome = process.env.CHROME ?? (process.platform === "darwin" ? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" : "/usr/bin/google-chrome-stable");
const browser = await chromium.launch({ executablePath: chrome, headless: true });
let problems = 0;
for (const size of sizes) {
  for (const scheme of schemes) {
    const vp = SIZES[size];
    const ctx = await browser.newContext({ bypassCSP: true, viewport: { width: vp.width, height: vp.height }, isMobile: !!vp.isMobile, hasTouch: !!vp.hasTouch, colorScheme: scheme });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(String(e)));
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
    await page.goto(url, { waitUntil: "networkidle" });
    await page.waitForTimeout(Number(opt.wait ?? 2500));
    const layout = await page.evaluate((isPhone) => {
      const out = { overflow: null, escaping: [], smallTargets: [] };
      const de = document.documentElement;
      if (de.scrollWidth > de.clientWidth + 1) out.overflow = `${de.scrollWidth} > ${de.clientWidth}`;
      const W = de.clientWidth;
      const describe = (el) => {
        const id = el.id ? `#${el.id}` : "";
        const cls = typeof el.className === "string" ? "." + el.className.split(/\s+/).slice(0, 3).join(".") : "";
        const txt = (el.getAttribute("aria-label") || el.textContent || "").trim().slice(0, 40);
        return `${el.tagName.toLowerCase()}${id}${cls} "${txt}"`;
      };
      for (const el of document.querySelectorAll("body *")) {
        const r = el.getBoundingClientRect();
        if (r.width === 0 || r.height === 0) continue;
        const cs = getComputedStyle(el);
        if (cs.position === "fixed") continue;
        let p = el.parentElement, clipped = false;
        while (p && p !== document.body) {
          const pcs = getComputedStyle(p);
          if (/(auto|scroll|hidden|clip)/.test(pcs.overflowX)) { clipped = true; break; }
          p = p.parentElement;
        }
        if (!clipped && (r.right > W + 1 || r.left < -1)) out.escaping.push(`${describe(el)} [${Math.round(r.left)}..${Math.round(r.right)}]`);
        if (isPhone && el.matches("button, a[href], input, select, [role=button], [role=tab], [role=radio]")) {
          // Invisible helpers (e.g. a form control's hidden mirror input) can't be tapped.
          const untouchable = cs.pointerEvents === "none" || cs.opacity === "0" || el.closest("[aria-hidden=true]");
          if ((r.height < 24 || r.width < 24) && cs.visibility !== "hidden" && !untouchable) out.smallTargets.push(`${describe(el)} ${Math.round(r.width)}x${Math.round(r.height)}`);
        }
      }
      out.escaping = out.escaping.slice(0, 15);
      out.smallTargets = out.smallTargets.slice(0, 15);
      return out;
    }, !!vp.isMobile);
    await page.addScriptTag({ content: axeSource });
    const axe = await page.evaluate(async () => {
      // eslint-disable-next-line no-undef
      const r = await axe.run(document, { runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"] } });
      return r.violations.map((v) => ({ id: v.id, impact: v.impact, help: v.help, nodes: v.nodes.slice(0, 4).map((n) => n.target.join(" ") + (n.failureSummary ? " :: " + n.failureSummary.split("\n").slice(1, 2).join(" ") : "")) }));
    });
    const issues = [];
    if (layout.overflow) issues.push(`horizontal overflow ${layout.overflow}`);
    if (layout.escaping.length) issues.push(`escaping viewport:\n      ${layout.escaping.join("\n      ")}`);
    if (layout.smallTargets.length) issues.push(`small touch targets:\n      ${layout.smallTargets.join("\n      ")}`);
    for (const v of axe) issues.push(`axe ${v.impact} ${v.id}: ${v.help}\n      ${v.nodes.join("\n      ")}`);
    for (const e of [...new Set(errors)].slice(0, 5)) issues.push(`console: ${e}`);
    console.log(`\n== ${size}/${scheme}: ${issues.length ? issues.length + " issue(s)" : "clean"}`);
    for (const i of issues) console.log("  - " + i);
    problems += issues.length;
    await ctx.close();
  }
}
await browser.close();
process.exit(problems ? 1 : 0);

// SPDX-License-Identifier: AGPL-3.0-only
// Content the demo makes up on the fly: landscape photos and app
// screenshots drawn on a canvas, and store-only zip files.
import { seeded } from "../../core/rand";

type Ctx = OffscreenCanvasRenderingContext2D;

interface Look {
  sky: [string, string, string];
  sun?: { color: string; x: number; y: number; r: number };
  far: string;
  near: string;
  haze: string;
  water?: string;
  stars?: boolean;
  aurora?: boolean;
  caps?: boolean;
  trees?: string;
  dunes?: boolean;
  flat?: boolean;
  clouds?: number;
}

const LOOKS: Record<string, Look> = {
  sunset: { sky: ["#2b1b4a", "#c2456b", "#ffb36b"], sun: { color: "#ffe2a8", x: 0.62, y: 0.6, r: 0.07 }, far: "#7a3d6b", near: "#1d1030", haze: "#f08a6c", clouds: 4 },
  dawn: { sky: ["#3c5a8c", "#c9a6c8", "#ffd9b0"], sun: { color: "#fff1d6", x: 0.3, y: 0.66, r: 0.05 }, far: "#8a8fb8", near: "#2c3150", haze: "#e9c3c0", clouds: 5 },
  alpine: { sky: ["#2f6fb8", "#7fb4e6", "#d7ecfb"], sun: { color: "#ffffff", x: 0.8, y: 0.18, r: 0.04 }, far: "#8aa3c2", near: "#263c2c", haze: "#cfe3f5", caps: true, trees: "#1d3324", clouds: 3 },
  snow: { sky: ["#8fa9c4", "#c9d8e6", "#eef3f8"], far: "#b8c7d8", near: "#e9eef4", haze: "#f2f5f9", caps: true, clouds: 2 },
  night: { sky: ["#050816", "#14204a", "#2d3d73"], sun: { color: "#f4f1de", x: 0.75, y: 0.2, r: 0.03 }, far: "#1e2a52", near: "#05070f", haze: "#33467e", stars: true, water: "#0a1230" },
  aurora: { sky: ["#030712", "#0b1d33", "#14324a"], far: "#0f2638", near: "#03070c", haze: "#1a4a5a", stars: true, aurora: true, caps: true },
  forest: { sky: ["#5d7f8f", "#a9c2bf", "#dfe8dc"], far: "#6f8f84", near: "#13261b", haze: "#c3d5cc", trees: "#0e1f15", clouds: 6 },
  autumn: { sky: ["#5c86b5", "#b9d0e2", "#f3e2c4"], sun: { color: "#fff0c9", x: 0.2, y: 0.3, r: 0.045 }, far: "#b06a3b", near: "#3b1d0f", haze: "#e9c49a", trees: "#7a2e12", clouds: 3 },
  lake: { sky: ["#2a6db0", "#88bde6", "#e2f1fb"], sun: { color: "#ffffff", x: 0.25, y: 0.2, r: 0.035 }, far: "#7f9bb8", near: "#22402e", haze: "#cfe4f2", caps: true, water: "#2d6a8f", trees: "#183222", clouds: 4 },
  fjord: { sky: ["#4b6c8a", "#9fb7c9", "#dbe5ea"], far: "#6c8295", near: "#1b2a2c", haze: "#b9c9d2", water: "#2b4a5a", trees: "#13201d", clouds: 7 },
  ocean: { sky: ["#1f6fb2", "#6fb6e8", "#cfeafa"], sun: { color: "#fffbe6", x: 0.7, y: 0.25, r: 0.04 }, far: "#5d86a6", near: "#33506a", haze: "#bfe0f2", water: "#1b5f8c", flat: true, clouds: 5 },
  desert: { sky: ["#3f7cc0", "#9cc7e8", "#f6dcb2"], sun: { color: "#fff6d8", x: 0.7, y: 0.22, r: 0.05 }, far: "#d39a5c", near: "#8a4b22", haze: "#f2cfa0", dunes: true },
};

function hex(c: string): [number, number, number] {
  const n = parseInt(c.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
function mix(a: string, b: string, t: number) {
  const x = hex(a);
  const y = hex(b);
  return `rgb(${x.map((v, i) => Math.round(v + (y[i] - v) * t)).join(",")})`;
}

/** Midpoint displacement ridge: y values across the width. */
function ridge(r: () => number, n: number, base: number, amp: number, rough: number, smooth = false): number[] {
  const a = new Array<number>(n).fill(base);
  a[0] = base - r() * amp;
  a[n - 1] = base - r() * amp;
  let step = n - 1;
  let d = amp;
  while (step > 1) {
    const half = step / 2;
    for (let i = half; i < n; i += step) a[i] = (a[i - half] + a[i + half]) / 2 + (r() - 0.5) * d;
    step = half;
    d *= rough;
  }
  if (smooth) for (let k = 0; k < 6; k++) for (let i = 1; i < n - 1; i++) a[i] = (a[i - 1] + a[i] * 2 + a[i + 1]) / 4;
  return a;
}

function landscape(g: Ctx, w: number, h: number, look: Look, r: () => number) {
  const horizon = h * (look.water ? 0.62 : look.flat ? 0.58 : 0.72);
  const sky = g.createLinearGradient(0, 0, 0, horizon);
  sky.addColorStop(0, look.sky[0]);
  sky.addColorStop(0.6, look.sky[1]);
  sky.addColorStop(1, look.sky[2]);
  g.fillStyle = sky;
  g.fillRect(0, 0, w, h);

  if (look.stars) {
    for (let i = 0; i < (w * h) / 2500; i++) {
      g.fillStyle = `rgba(255,255,255,${0.2 + r() * 0.7})`;
      const s = r() < 0.08 ? 1.8 : 1;
      g.fillRect(r() * w, r() * horizon * 0.9, s, s);
    }
  }
  if (look.aurora) {
    g.save();
    g.filter = `blur(${Math.round(w / 60)}px)`;
    g.globalCompositeOperation = "lighter";
    for (let k = 0; k < 4; k++) {
      const y0 = h * (0.15 + r() * 0.25);
      const grad = g.createLinearGradient(0, y0 - h * 0.15, 0, y0 + h * 0.1);
      grad.addColorStop(0, "rgba(120,60,200,0)");
      grad.addColorStop(0.5, k % 2 ? "rgba(80,255,170,0.35)" : "rgba(60,230,150,0.45)");
      grad.addColorStop(1, "rgba(20,120,90,0)");
      g.fillStyle = grad;
      g.beginPath();
      g.moveTo(0, y0);
      g.bezierCurveTo(w * 0.3, y0 - h * 0.2 * r(), w * 0.6, y0 + h * 0.2 * r(), w, y0 - h * 0.1);
      g.lineTo(w, y0 + h * 0.08);
      g.bezierCurveTo(w * 0.6, y0 + h * 0.25, w * 0.3, y0, 0, y0 + h * 0.1);
      g.fill();
    }
    g.restore();
  }
  if (look.sun) {
    const { color, x, y, r: rad } = look.sun;
    const cx = x * w;
    const cy = y * h;
    const R = rad * Math.max(w, h);
    const glow = g.createRadialGradient(cx, cy, R * 0.6, cx, cy, R * 6);
    glow.addColorStop(0, color + "99");
    glow.addColorStop(1, color + "00");
    g.fillStyle = glow;
    g.fillRect(0, 0, w, h);
    g.fillStyle = color;
    g.beginPath();
    g.arc(cx, cy, R, 0, Math.PI * 2);
    g.fill();
  }
  if (look.clouds) {
    g.save();
    g.filter = `blur(${Math.round(w / 90)}px)`;
    for (let i = 0; i < look.clouds; i++) {
      const cx = r() * w;
      const cy = h * (0.08 + r() * 0.35);
      g.fillStyle = `rgba(255,255,255,${0.25 + r() * 0.3})`;
      for (let k = 0; k < 5; k++) {
        g.beginPath();
        g.ellipse(cx + (k - 2) * w * 0.035, cy + (r() - 0.5) * h * 0.02, w * (0.04 + r() * 0.05), h * (0.015 + r() * 0.02), 0, 0, Math.PI * 2);
        g.fill();
      }
    }
    g.restore();
  }

  // Mountain layers, far to near.
  const layers = look.flat ? 1 : 5;
  const n = 257;
  const bottom = look.water ? horizon : h;
  for (let i = 0; i < layers; i++) {
    const t = layers === 1 ? 0 : i / (layers - 1);
    const base = horizon - (layers - i) * h * (look.dunes ? 0.035 : 0.05) + (look.water ? h * 0.04 : 0);
    const amp = h * (look.flat ? 0.04 : look.dunes ? 0.12 : 0.32 - t * 0.2);
    const ys = ridge(r, n, base, amp, look.dunes ? 0.45 : 0.55, look.dunes);
    const path = new Path2D();
    path.moveTo(0, bottom + 2);
    ys.forEach((y, k) => path.lineTo((k / (n - 1)) * w, Math.min(y, bottom)));
    path.lineTo(w, bottom + 2);
    path.closePath();
    const top = Math.min(...ys);
    const fill = g.createLinearGradient(0, top, 0, bottom);
    fill.addColorStop(0, mix(look.haze, look.far, 0.35 + t * 0.4));
    fill.addColorStop(1, mix(look.far, look.near, Math.min(1, t * 1.1)));
    g.fillStyle = fill;
    g.fill(path);
    if (look.caps && i < 2) {
      g.save();
      g.clip(path);
      const capLine = top + amp * 0.22;
      const cap = g.createLinearGradient(0, top, 0, capLine + amp * 0.08);
      cap.addColorStop(0, "rgba(255,255,255,0.95)");
      cap.addColorStop(0.75, "rgba(245,248,255,0.7)");
      cap.addColorStop(1, "rgba(245,248,255,0)");
      g.fillStyle = cap;
      g.fillRect(0, top, w, capLine + amp * 0.08 - top);
      g.restore();
    }
    if (look.trees && i >= layers - 2) {
      g.fillStyle = i === layers - 1 ? look.trees : mix(look.trees, look.haze, 0.35);
      const count = Math.round(w / (i === layers - 1 ? 9 : 14));
      for (let k = 0; k < count; k++) {
        const x = r() * w;
        const y = ys[Math.round((x / w) * (n - 1))];
        const th = h * (i === layers - 1 ? 0.05 + r() * 0.04 : 0.025 + r() * 0.02);
        g.beginPath();
        g.moveTo(x, y - th);
        g.lineTo(x - th * 0.28, y + 2);
        g.lineTo(x + th * 0.28, y + 2);
        g.fill();
      }
    }
  }

  if (look.water) {
    const water = g.createLinearGradient(0, horizon, 0, h);
    water.addColorStop(0, mix(look.sky[2], look.water, 0.4));
    water.addColorStop(1, mix(look.water, "#000000", 0.35));
    g.fillStyle = water;
    g.fillRect(0, horizon, w, h - horizon);
    // Reflection of everything above the horizon.
    const copy = new OffscreenCanvas(w, Math.ceil(horizon));
    copy.getContext("2d")!.drawImage(g.canvas, 0, 0);
    g.save();
    g.globalAlpha = 0.38;
    g.translate(0, horizon * 2);
    g.scale(1, -1);
    g.drawImage(copy, 0, 0);
    g.restore();
    for (let i = 0; i < h / 6; i++) {
      const y = horizon + r() * (h - horizon);
      g.fillStyle = `rgba(255,255,255,${0.04 + r() * 0.08})`;
      g.fillRect(r() * w, y, w * (0.05 + r() * 0.2), 1);
    }
  }

  // Soft vignette like a real lens.
  const v = g.createRadialGradient(w / 2, h / 2, Math.min(w, h) * 0.35, w / 2, h / 2, Math.max(w, h) * 0.75);
  v.addColorStop(0, "rgba(0,0,0,0)");
  v.addColorStop(1, "rgba(0,0,0,0.35)");
  g.fillStyle = v;
  g.fillRect(0, 0, w, h);
}

/** A dark dashboard screenshot. */
function screenshot(g: Ctx, w: number, h: number, r: () => number) {
  g.fillStyle = "#0b1020";
  g.fillRect(0, 0, w, h);
  g.fillStyle = "#111827";
  g.fillRect(0, 0, 220, h);
  g.fillStyle = "#1f2937";
  g.fillRect(220, 0, w - 220, 56);
  g.fillStyle = "#7dd3fc";
  g.fillRect(24, 22, 28, 28);
  g.fillStyle = "#e5e7eb";
  g.font = "600 18px sans-serif";
  g.fillText("Wargo", 64, 43);
  g.font = "14px sans-serif";
  ["Overview", "Containers", "Files", "Terminal", "System", "Updates", "Settings"].forEach((s, i) => {
    if (i === 0) {
      g.fillStyle = "#1e293b";
      g.fillRect(12, 84, 196, 34);
    }
    g.fillStyle = i === 0 ? "#f9fafb" : "#9ca3af";
    g.fillText(s, 32, 106 + i * 42);
  });
  const colors = ["#7dd3fc", "#a78bfa", "#34d399", "#fbbf24", "#f87171", "#60a5fa"];
  const cw = (w - 220 - 24 * 4) / 3;
  for (let row = 0; row < 2; row++) {
    for (let col = 0; col < 3; col++) {
      const x = 220 + 24 + col * (cw + 24);
      const y = 80 + row * 260;
      g.fillStyle = "#111827";
      g.fillRect(x, y, cw, 236);
      g.strokeStyle = "#1f2937";
      g.strokeRect(x + 0.5, y + 0.5, cw - 1, 235);
      g.fillStyle = "#9ca3af";
      g.font = "13px sans-serif";
      g.fillText(["CPU", "Memory", "Network", "Disk I/O", "Containers", "Temperature"][row * 3 + col], x + 16, y + 28);
      g.fillStyle = "#f3f4f6";
      g.font = "600 26px sans-serif";
      g.fillText(`${Math.round(r() * 80 + 5)}%`, x + 16, y + 64);
      const c = colors[(row * 3 + col) % colors.length];
      g.strokeStyle = c;
      g.lineWidth = 2;
      g.beginPath();
      let v = 0.5;
      for (let k = 0; k <= 60; k++) {
        v = Math.min(0.95, Math.max(0.05, v + (r() - 0.5) * 0.18));
        const px = x + 16 + (k / 60) * (cw - 32);
        const py = y + 220 - v * 130;
        if (k) g.lineTo(px, py);
        else g.moveTo(px, py);
      }
      g.stroke();
      g.lineWidth = 1;
    }
  }
  g.fillStyle = "#111827";
  g.fillRect(244, 600, w - 220 - 48, h - 624);
  for (let i = 0; i < 6; i++) {
    g.fillStyle = "#374151";
    g.fillRect(268, 630 + i * 36, 160 + r() * 200, 10);
    g.fillStyle = colors[i % colors.length];
    g.fillRect(w - 320, 628 + i * 36, 60 + r() * 180, 14);
  }
}

const cache = new Map<string, Promise<Blob>>();

/** Renders a generated image ("img:<seed>:<style>:<w>x<h>"). */
export function renderImage(gen: string, type: "image/jpeg" | "image/png" | "image/webp"): Promise<Blob> {
  const key = `${gen}|${type}`;
  let p = cache.get(key);
  if (!p) {
    p = (async () => {
      const [, seedS, style, size] = gen.split(":");
      const [w, h] = (size ?? "1600x1067").split("x").map(Number);
      const c = new OffscreenCanvas(w, h);
      const g = c.getContext("2d")!;
      const r = seeded(Number(seedS) * 7919 + 13);
      if (style === "ui") screenshot(g, w, h, r);
      else landscape(g, w, h, LOOKS[style] ?? Object.values(LOOKS)[Number(seedS) % 12], r);
      return c.convertToBlob({ type, quality: 0.86 });
    })();
    cache.set(key, p);
  }
  return p;
}

// ---- zip ---------------------------------------------------------------------------

const CRC = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c >>> 0;
  }
  return t;
})();

function crc32(b: Uint8Array) {
  let c = 0xffffffff;
  for (let i = 0; i < b.length; i++) c = CRC[(c ^ b[i]) & 255] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

export interface ZipItem {
  name: string;
  data?: Uint8Array;
  mtime: number;
  mode: number;
  dir?: boolean;
}

/** A store-only (uncompressed) zip. */
export function makeZip(items: ZipItem[]): Uint8Array<ArrayBuffer> {
  const enc = new TextEncoder();
  const parts: Uint8Array[] = [];
  const central: Uint8Array[] = [];
  let offset = 0;
  for (const it of items) {
    const name = enc.encode(it.dir ? `${it.name}/` : it.name);
    const data = it.dir ? new Uint8Array(0) : (it.data ?? new Uint8Array(0));
    const d = new Date(it.mtime);
    const time = (d.getHours() << 11) | (d.getMinutes() << 5) | (d.getSeconds() >> 1);
    const date = ((Math.max(1980, d.getFullYear()) - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate();
    const crc = crc32(data);
    const local = new DataView(new ArrayBuffer(30));
    local.setUint32(0, 0x04034b50, true);
    local.setUint16(4, 20, true);
    local.setUint16(6, 0x0800, true);
    local.setUint16(8, 0, true);
    local.setUint16(10, time, true);
    local.setUint16(12, date, true);
    local.setUint32(14, crc, true);
    local.setUint32(18, data.length, true);
    local.setUint32(22, data.length, true);
    local.setUint16(26, name.length, true);
    local.setUint16(28, 0, true);
    parts.push(new Uint8Array(local.buffer), name, data);
    const cd = new DataView(new ArrayBuffer(46));
    cd.setUint32(0, 0x02014b50, true);
    cd.setUint16(4, (3 << 8) | 20, true);
    cd.setUint16(6, 20, true);
    cd.setUint16(8, 0x0800, true);
    cd.setUint16(10, 0, true);
    cd.setUint16(12, time, true);
    cd.setUint16(14, date, true);
    cd.setUint32(16, crc, true);
    cd.setUint32(20, data.length, true);
    cd.setUint32(24, data.length, true);
    cd.setUint16(28, name.length, true);
    cd.setUint32(38, ((it.dir ? 0o40000 : 0o100000) | (it.mode & 0o7777)) * 65536, true);
    cd.setUint32(42, offset, true);
    central.push(new Uint8Array(cd.buffer), name);
    offset += 30 + name.length + data.length;
  }
  const cdSize = central.reduce((s, p) => s + p.length, 0);
  const end = new DataView(new ArrayBuffer(22));
  end.setUint32(0, 0x06054b50, true);
  end.setUint16(8, items.length, true);
  end.setUint16(10, items.length, true);
  end.setUint32(12, cdSize, true);
  end.setUint32(16, offset, true);
  const all = [...parts, ...central, new Uint8Array(end.buffer)];
  const out = new Uint8Array(all.reduce((s, p) => s + p.length, 0));
  let o = 0;
  for (const p of all) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

// ---- base64 ----------------------------------------------------------------------

export function toB64(b: Uint8Array): string {
  let s = "";
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
  return btoa(s);
}

export function fromB64(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

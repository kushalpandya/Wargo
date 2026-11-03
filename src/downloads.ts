// SPDX-License-Identifier: AGPL-3.0-only
// Downloads in the demo. The app starts downloads with <a href="/api/..."
// download> links, which the browser loads as page navigations, and the
// mock service worker leaves those alone. So such clicks are caught here,
// fetched through the fake backend and saved from a blob: URL instead.

function filenameOf(res: Response, url: URL) {
  const cd = res.headers.get("Content-Disposition") ?? "";
  const star = /filename\*=UTF-8''([^;]+)/i.exec(cd);
  if (star) return decodeURIComponent(star[1]);
  const plain = /filename="?([^";]+)"?/i.exec(cd);
  if (plain) return plain[1];
  return decodeURIComponent(url.pathname.split("/").pop() || "download");
}

async function save(url: URL) {
  const res = await fetch(url, { credentials: "same-origin" });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = ((await res.json()) as { message?: string }).message ?? msg;
    } catch {
      /* not JSON */
    }
    alert(`Download failed: ${msg}`);
    return;
  }
  const blob = await res.blob();
  const href = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = href;
  a.download = filenameOf(res, url);
  a.rel = "noopener";
  document.body.appendChild(a);
  nativeClick.call(a);
  a.remove();
  setTimeout(() => URL.revokeObjectURL(href), 10_000);
}

/** A same-origin /api/ link the app wants to download or open. */
function apiLink(a: HTMLAnchorElement): URL | null {
  if (!a.href) return null;
  const u = new URL(a.href, location.href);
  return u.origin === location.origin && u.pathname.startsWith("/api/") ? u : null;
}

const nativeClick = HTMLAnchorElement.prototype.click;

export function installDownloads() {
  // Links created and clicked by code (startDownload, downloadBlob…).
  HTMLAnchorElement.prototype.click = function (this: HTMLAnchorElement) {
    const u = apiLink(this);
    if (u) {
      void save(u);
      return;
    }
    nativeClick.call(this);
  };
  // Links the visitor clicks.
  document.addEventListener(
    "click",
    (ev) => {
      if (ev.defaultPrevented || ev.button !== 0) return;
      const a = (ev.target as Element | null)?.closest?.("a");
      if (!(a instanceof HTMLAnchorElement)) return;
      const u = apiLink(a);
      if (!u) return;
      ev.preventDefault();
      void save(u);
    },
    true,
  );
}

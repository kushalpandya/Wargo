// SPDX-License-Identifier: AGPL-3.0-only
/// <reference lib="webworker" />
// Wargo's service worker. The build (sw/plugin.ts) replaces the two
// placeholders below and writes the result to dist/sw.js.
//
// It stores the app's own files (the same public files the login page
// loads) so the installed app opens without waiting on the network. It never
// answers or stores anything under /api/ or /ws/: data, downloads and
// sockets always go to the server.

const VERSION = __WARGO_SW_VERSION__;
const FILES = __WARGO_SW_FILES__;
const CACHE = "wargo-" + VERSION;
const STORED = new Set(FILES);

self.addEventListener("install", (event) => {
  // Fetch past the HTTP cache so a new version never stores old files. The
  // new worker then waits; the page tells it when to take over.
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll(FILES.map((u) => new Request(u, { cache: "reload" })))));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith("wargo-") && k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  // Data, downloads and sockets: always the server, never stored.
  if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/ws/")) return;
  if (req.mode === "navigate") {
    event.respondWith(caches.match("/", { cacheName: CACHE }).then((r) => r || fetch(req)));
    return;
  }
  if (STORED.has(url.pathname)) {
    // index.html asks for icons with ?v=<hash>; the stored copy is the same file.
    event.respondWith(caches.match(url.pathname, { cacheName: CACHE, ignoreSearch: true }).then((r) => r || fetch(req)));
  }
});

// Push: the server sends {title, body, link, tag, badge} (internal/notify/push.go).
self.addEventListener("push", (event) => {
  let p = {};
  try {
    p = event.data ? event.data.json() : {};
  } catch {
    p = {};
  }
  const shown = self.registration.showNotification(p.title || "Wargo", {
    body: p.body || "",
    tag: p.tag || undefined,
    icon: "/icon-192.png",
    data: { link: p.link || "", tag: p.tag || "" },
  });
  const badge = typeof p.badge === "number" && self.navigator.setAppBadge ? self.navigator.setAppBadge(p.badge).catch(() => {}) : null;
  event.waitUntil(Promise.all([shown, badge]));
});

// A tap opens Wargo with the panel showing, whatever the category; the
// item's page is a tap away from its row. An open window is focused and
// told to open the panel (keeping its state); a new window opens only when
// none is open.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      const win = wins.find((w) => new URL(w.url).origin === self.location.origin);
      if (!win) return self.clients.openWindow(new URL("/?notifications=1", self.location.origin).href);
      return win
        .focus()
        .catch(() => {})
        .then(() => win.postMessage({ type: "notification-open", link: null }));
    }),
  );
});

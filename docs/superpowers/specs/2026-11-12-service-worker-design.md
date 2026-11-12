# Service worker for the Home Screen app

Date: 2026-11-12. Status: design, awaiting approval. Reviewed by a builder check; its questions are answered below.

## Why

The owner opens Wargo from an iPhone Home Screen icon, through
`https://deimos.kushalpandya.in` (cloudflared tunnel to the Linux box; Wargo's
own sign-in is the only login, there is no Cloudflare Access). The manifest
already asks for `display: standalone`, so it opens without browser chrome, but
every launch still downloads `index.html` and the JS/CSS bundles over the
tunnel before the first API request goes out. It feels like loading a website,
not opening an app.

A service worker that keeps the app's own files on the device removes that
cold start: the UI appears from local files and only data crosses the network.
It is also the prerequisite for web push on iOS (installed Home Screen app +
service worker), which is the next planned feature; push itself is not part of
this work.

## Goals

- Launching the installed app (and reloading any page) renders the UI from
  files stored on the device, with no network wait for HTML, JS, CSS, fonts or
  the app's icons.
- Data (everything under `/api/` and `/ws/`) is never cached by the service
  worker: it always comes live from the server, as today.
- New Wargo releases reach the device on their own: the worker fetches the new
  files in the background and the app switches to them the next time it is
  opened or brought back to the foreground. The device never stays stuck on an
  old version.
- When the server can't be reached at launch, the app shows a clear
  "can't reach Wargo" alert at the top of an otherwise empty page and loads
  nothing else, then recovers by itself when the connection is back.

## Non-goals

- Offline mode. Nothing pretends to work without the server.
- Caching or showing last-known data (metrics, containers, update counts).
- Push notifications (next project; this only has to leave room for them, see
  below).
- Background sync, periodic sync, install prompts or custom "Add to Home
  Screen" UI.

## Anti-goals (would count as failure even if it technically works)

- Stale data shown as if it were current.
- Any authenticated response (API JSON, file downloads, logos from
  `/api/system/logo`, container icons, terminal or event sockets) stored in a
  service worker cache, where it would outlive sign-out.
- A device stuck on an old build after the server was updated, or a mix of old
  HTML with new bundles (blank screen, chunk load errors).
- The app showing the login page, or a pile of per-card errors, just because
  the server was unreachable.
- Breaking the gh-pages demo, which runs its own MSW service worker.
- Breaking plain-HTTP use (LAN `scripts/dev.sh --lan`, `http://IP:port`
  installs): there the worker simply isn't available and the app works as it
  does today.

## Constraints

- Service workers only run in secure contexts: HTTPS or `localhost` /
  `127.0.0.1`. Registration must be skipped silently elsewhere.
- The frontend is built by Vite into `web/dist` and embedded in the Go binary.
  `internal/server/spa.go` serves `assets/*` (hashed names) as
  `immutable`, everything else (`index.html`, `manifest.webmanifest`, icons,
  `theme-init.js`) as `no-cache`, and falls back to `index.html` for client
  routes. The worker file must be served `no-cache` from the site root so its
  scope covers the whole app.
- Wargo itself is always served at `/` (`spa.go` has no base path). Only the
  gh-pages demo builds with a different base (`/Wargo/`).
- The demo (gh-pages branch, `src/entry.ts`) starts MSW's own service worker
  with scope `/Wargo/` and then runs `await import("../main")`, so
  `src/main.tsx` does run there, in a production build over HTTPS. Its
  `vite.demo.config.ts` merges Wargo's `vite.config.ts`, so build steps land in
  the demo output too. Wargo's worker must neither register nor take over
  MSW's scope there.
- `index.html` references icons and the manifest with `?v=<hash>` query
  strings; `theme-init.js` runs before the app to avoid a theme flash.
- No new heavy dependencies without reason; the project has avoided them so
  far.

## How it behaves (decided)

### Launch and navigation

- A navigation request (opening the app, reload, typing a URL) is answered
  from the stored copy of `index.html` immediately. In the background the
  worker checks the server for a newer build.
- The JS/CSS bundles, the lazily loaded route chunks, fonts, icons, the
  manifest and `theme-init.js` all come from the device, so the first render
  needs no network.
- Everything under `/api/` and `/ws/` goes straight to the network, untouched.
  So does any other request the worker doesn't recognise as an app file.

### "Can't reach Wargo"

Today the first thing the app does is `GET /api/auth/state`; while it loads a
full-page spinner shows, and if it fails the router treats the user as signed
out and shows the login page. That must change:

- If the first auth check fails because the server can't be reached, the app
  shows a connection page in the style of `app/HostBlocked.tsx`: an alert at
  the top saying Wargo can't be reached, with a "Try again" button, and nothing
  else. It retries on its own (on the browser's `online` event, when the app
  comes back to the foreground, and on a gentle interval) and goes straight on
  to the normal app or login page once the server answers.
- "Can't be reached" means: the request fails at the network level, it times
  out, or the answer is clearly not Wargo's (for example a Cloudflare error
  page, HTML instead of JSON, or a 502/503/504 from the tunnel).
- A real 401 still means "signed out" and goes to the login page as today.
- After the app is running, the existing top-bar pill ("Reconnecting…" /
  "Offline") keeps covering short drops of the live connection. This page is
  only for the launch case where the first data never arrives.

### New releases

- Each build has its own version. The worker for a new build installs in the
  background; the old one keeps serving until the switch.
- iOS rarely closes a Home Screen app; it suspends and resumes it, and a
  resume is not a page load. So whenever the app comes back to the
  foreground, it asks the server for a newer worker. If one is ready, it takes
  over and the app reloads itself right away (owner's decision: a forced
  reload once the Wargo container is updated is fine). Everything is local by
  then, so the reload is a brief blink.
- A real launch or reload also picks up a ready worker. The app never reloads
  on its own while it stays in the foreground.
- When the new version takes over, files from older builds are deleted.
- If a lazily loaded route chunk can't be found (an old page asking for a chunk
  that no longer exists), the app reloads once to pick up the current build
  instead of showing an error.

### Security and sign-out

- The worker's cache holds only the public app files that anyone can already
  fetch without signing in (the same files `spa.go` serves to the login page).
- Signing out needs no extra step, since no private data is in the cache.
- `docs/SECURITY.md` gets a short note on what the worker stores and what it
  never touches.

### Room for push notifications

The worker file is plain and owned by the project, so the push project can add
`push` and `notificationclick` handlers to it later. No push code now.

## Decided by Claude, open to change

- **Instant frame:** the auth check still has to finish before the app knows
  whether to show the app or the login page. Claude's call: keep the
  full-page spinner for that moment (it is one small request once the files
  are local), rather than guessing the signed-in state from a stored flag. If
  it still feels slow on the phone, a follow-up can show the empty app frame
  during that check.
- **Hand-written worker, no plugin:** a small worker file plus a few lines in
  the Vite build that write the list of files to store and the build version
  into it, instead of adding `vite-plugin-pwa`/Workbox. Keeps the behaviour
  easy to read and the push handlers easy to add.
- **Where registration lives, and keeping the demo safe without touching
  gh-pages:** registration is in `src/main.tsx`, and runs only in production
  builds, in secure contexts, and when `import.meta.env.BASE_URL` is `/`. The
  build step that writes the worker also skips non-`/` bases. Real Wargo is
  always at `/`, the demo never is, so the demo keeps MSW and needs no change.
  The Pages workflow's demo build should still pass.
- **No kill switch setting:** the worker file is served `no-cache` and
  registered so the browser always checks it against the server
  (`updateViaCache: "none"`), so a fixed release replaces a broken worker on
  the next launch. No config flag for turning it off.
- **Retry interval** for the connection page: a few seconds at first, backing
  off to about 30 seconds.

## Left to the builder

- Exact cache names, the worker's file name and how the build injects the file
  list and version.
- Whether the worker is generated into `dist/` by a Vite plugin or copied from
  `public/` and patched.
- How the connection page detects "not Wargo's answer" in detail, as long as
  it matches the cases above.
- Wording of the connection alert, following the existing tone ("Wargo can't
  be reached" style, plain language, no dashes).
- Whether to store every route chunk on install (about 4 MB in roughly 200
  files in `dist/`, cheap enough) or only the core and the rest as used.
- Tests: Go test that `spa.go` serves the worker with `no-cache` and the right
  content type; a way to check the worker's file list matches `dist/`. Manual
  check on an iPhone through the HTTPS URL, and in a desktop browser with
  DevTools (offline toggle, update flow).

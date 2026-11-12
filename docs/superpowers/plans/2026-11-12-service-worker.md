# Service Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wargo's installed Home Screen app opens from files stored on the device, shows a single "can't reach Wargo" page when the server is unreachable at launch, and picks up new releases when it is opened or brought back to the foreground.

**Architecture:** A hand-written service worker (`web/sw/sw.js`) is turned into `dist/sw.js` by a small Vite build plugin that injects the build version and the list of app files to store. The page registers it from `src/main.tsx` behind a guard, applies waiting updates on launch and on return to the foreground, and reloads once when a lazily loaded chunk is missing. `lib/api.ts` reports unreachable servers as `ApiError` code `"unreachable"`, and the auth gates in `app/routes.tsx` render a new `app/Unreachable.tsx` page for it.

**Tech Stack:** Vite 8 plugin API, plain JS service worker, React 19 + TanStack Query, `node --test` for pure TS helpers, Go `net/http` tests.

**Spec:** `docs/superpowers/specs/2026-11-12-service-worker-design.md`

## Global Constraints

- The worker caches only public app files from `dist/`. Nothing under `/api/` or `/ws/` is ever cached or answered by the worker, including navigations to `/api/...` (file downloads).
- Register only when: production build, `window.isSecureContext`, `"serviceWorker" in navigator`, and `import.meta.env.BASE_URL === "/"`. The build plugin emits `sw.js` only for base `/` and only on `vite build`.
- Worker URL `/sw.js`, scope `/`, registered with `updateViaCache: "none"`. Served by `spa.go` with `Cache-Control: no-cache`.
- The app never reloads by itself while it stays in the foreground. It may reload at launch and right after returning to the foreground.
- Connection page retry: first after 3 s, doubling to a 30 s cap; also on `online` and on `visibilitychange` to visible; plus a "Try again" button.
- "Unreachable" means: fetch rejects (network error), the request times out (auth state check: 10 s), or the answer is not Wargo's JSON: a non-JSON response with status 0 or >= 500, or any HTML response to an `/api/` request. A JSON 401 keeps today's sign-out behaviour.
- No new npm dependencies.
- Copy and commit messages: no em dashes or en dashes as punctuation; no AI attribution lines in commits.
- New files start with `// SPDX-License-Identifier: AGPL-3.0-only`. Web code is formatted with Prettier `--print-width 140` (there is no project Prettier config; never run Prettier without that flag).

## Review Focus

- **Navigation to an `/api/` URL** (file downloads open `/api/files/download?...` as a navigation): must reach the server, never get the cached `index.html`. Test: Task 2 Playwright check.
- **A Cloudflare or proxy HTML page answering an `/api/` request with 200 or 5xx**: must become the connection page, not a JSON parse error or the login page. Test: Task 4 `classifyResponse` cases.
- **An old page asking for a chunk the new build deleted**: reloads once, never loops. Test: Task 3 `shouldReloadForChunk` cases.
- **The gh-pages demo** (base `/Wargo/`, MSW worker on the same scope): no `sw.js` emitted and no registration. Test: Task 1 base build check and Task 3 `shouldRegister` cases.
- **Signed-out device**: login page loads from the stored files, a JSON 401 still routes to `/login`, and nothing private is in the cache. Test: Task 2 Playwright check (cache contents list) and Task 4 `classifyResponse` 401 case.

---

### Task 1: Build the worker file with its version and file list

**Files:**
- Create: `web/sw/precache.ts` (pure helpers, imported by the Vite config and by tests)
- Create: `web/sw/precache.test.ts`
- Create: `web/sw/sw.js` (worker template; in this task only the two placeholders and a minimal install/activate so the build has something to emit; Task 2 fills in behaviour)
- Create: `web/sw/plugin.ts` (Vite plugin)
- Modify: `web/vite.config.ts` (add `serviceWorker()` to `plugins`)
- Modify: `web/package.json` `test` script to `node --test "src/**/*.test.ts" "sw/**/*.test.ts"`
- Modify: `internal/server/spa_test.go`

**Interfaces:**
- Produces: `precacheList(paths: string[]): string[]`, `cacheVersion(entries: { path: string; hash: string }[]): string` in `web/sw/precache.ts`; `serviceWorker(): Plugin` in `web/sw/plugin.ts`; placeholders `__WARGO_SW_VERSION__` (string literal) and `__WARGO_SW_FILES__` (JSON array literal) in `web/sw/sw.js`, replaced in the emitted `dist/sw.js`.

- [ ] **Step 1: Write the failing tests** in `web/sw/precache.test.ts` (style of `src/lib/columns.test.ts`):

```ts
test("precacheList keeps app files, root-relative, sorted, without sw.js or maps", () => {
  assert.deepEqual(
    precacheList(["sw.js", "index.html", "assets/index-abc.js", "assets/index-abc.js.map", "icon-192.png", "manifest.webmanifest", ".DS_Store"]),
    ["/", "/assets/index-abc.js", "/icon-192.png", "/index.html", "/manifest.webmanifest"],
  );
});
test("cacheVersion changes when any file changes and ignores order", () => {
  const a = cacheVersion([{ path: "a.js", hash: "1" }, { path: "b.js", hash: "2" }]);
  assert.equal(a, cacheVersion([{ path: "b.js", hash: "2" }, { path: "a.js", hash: "1" }]));
  assert.notEqual(a, cacheVersion([{ path: "a.js", hash: "1" }, { path: "b.js", hash: "3" }]));
  assert.match(a, /^[0-9a-f]{12}$/);
});
```

`"/"` is listed because navigations are answered with the stored `index.html` under that key (Task 2).

- [ ] **Step 2: Run** `cd web && node --test sw/precache.test.ts`. Expected: FAIL (module not found).

- [ ] **Step 3: Implement** `precacheList` (drop `sw.js`, `*.map`, dotfiles; prefix `/`; add `/`; sort) and `cacheVersion` (sha256 over sorted `path:hash` lines, first 12 hex chars, `node:crypto`).

- [ ] **Step 4: Run** the test again. Expected: PASS.

- [ ] **Step 5: Implement `serviceWorker(): Plugin`** in `web/sw/plugin.ts`: `apply: "build"`; record `config.base` in `configResolved`; in `writeBundle` (after Vite has copied `public/`), if base is `/`, walk `outDir`, hash each file (sha256 of contents), read `sw/sw.js`, replace `__WARGO_SW_VERSION__` with `JSON.stringify(cacheVersion(...))` and `__WARGO_SW_FILES__` with `JSON.stringify(precacheList(...))`, write `outDir/sw.js`. For other bases, write nothing. Register it in `vite.config.ts` after `preloadFont()`.

- [ ] **Step 6: Verify the build.** Run `cd web && npm run build && grep -c '"/assets/' dist/sw.js && grep -q __WARGO_ dist/sw.js; echo $?`. Expected: a count above 0, then `1` (no placeholders left). Then `npx vite build --base /Wargo/ --outDir "$TMPDIR/wargo-demo-check" --emptyOutDir && test ! -e "$TMPDIR/wargo-demo-check/sw.js" && echo NO_SW`. Expected: `NO_SW`.

- [ ] **Step 7: Write the Go test** `TestSPAServiceWorker` in `internal/server/spa_test.go`: with `sw.js` in the `fstest.MapFS`, `GET /sw.js` returns 200, `Content-Type` containing `javascript`, `Cache-Control: no-cache`; with no `sw.js`, `GET /sw.js` returns 404 (not `index.html`). Run `go test ./internal/server -run TestSPAServiceWorker`. Expected: PASS (`spa.go` already behaves this way; the test pins it). If the content type assertion fails, add `mime.AddExtensionType(".js", "text/javascript; charset=utf-8")` to `spa.go`'s `init`.

- [ ] **Step 8: Commit** `web/sw web/vite.config.ts web/package.json internal/server/spa_test.go` with message "Build a service worker with the app's file list".

### Task 2: Worker behaviour: serve app files, never touch data

**Files:**
- Modify: `web/sw/sw.js`

**Interfaces:**
- Consumes: placeholders from Task 1.
- Produces: the worker accepts a `postMessage({ type: "SKIP_WAITING" })` and then calls `self.skipWaiting()`. Cache names are `wargo-<version>`. Task 3 relies on both.

- [ ] **Step 1: Implement the worker** (plain JS, `/// <reference lib="webworker" />` comment only):
  - `install`: open `wargo-<version>`, `addAll` every listed file using `new Request(url, { cache: "reload" })`, with `"/"` stored as the response for `/index.html`. Do not call `skipWaiting` here.
  - `activate`: delete every cache whose name starts with `wargo-` and is not the current one; `clients.claim()`.
  - `message`: `SKIP_WAITING` calls `self.skipWaiting()`.
  - `fetch`: ignore (no `respondWith`) unless the request is a same-origin `GET`. Ignore every path starting with `/api/` or `/ws/` before any other rule. For `request.mode === "navigate"`, answer `caches.match("/")`, falling back to `fetch(request)`. For a path in the file list, answer from the cache with `ignoreSearch: true` (index.html asks for `/favicon.ico?v=...`), falling back to the network. Everything else goes to the network untouched.

- [ ] **Step 2: Build and run the real server** on a scratch config (port 2219, its own `data_dir`, `[dev] auth_bypass = true`): `make web && go run ./cmd/wargo -config <scratch>.toml`. `127.0.0.1` counts as a secure context.

- [ ] **Step 3: Verify in Playwright** (Task 3 adds registration; for this task, register by hand in the console with `navigator.serviceWorker.register("/sw.js", { scope: "/", updateViaCache: "none" })`):
  - after it activates, `caches.keys()` is exactly `["wargo-<version>"]`, and its `keys()` contain no URL with `/api/` or `/ws/`;
  - with the backend stopped, a reload still renders the app's HTML and scripts (the page then fails its API calls; Task 4 handles that);
  - with the backend running, navigating to `/api/capabilities` returns the server's JSON, not the app.
  Expected: all three hold.

- [ ] **Step 4: Commit** `web/sw/sw.js`, message "Serve the app's files from the service worker".

### Task 3: Register the worker and apply updates on launch and resume

**Files:**
- Create: `web/src/lib/sw.ts`
- Create: `web/src/lib/sw.test.ts`
- Modify: `web/src/main.tsx` (call `registerServiceWorker()` and `reloadOnStaleChunk()` once, after `createRoot(...).render`)

**Interfaces:**
- Consumes: `SKIP_WAITING` message and `/sw.js` from Tasks 1 and 2.
- Produces: `shouldRegister(env: { prod: boolean; secure: boolean; supported: boolean; base: string }): boolean`, `shouldReloadForChunk(lastReloadAt: number | null, now: number): boolean`, `registerServiceWorker(): void`, `reloadOnStaleChunk(): void`.

- [ ] **Step 1: Write the failing tests** in `web/src/lib/sw.test.ts`:

```ts
const ok = { prod: true, secure: true, supported: true, base: "/" };
test("shouldRegister only for production, secure, supported, root base", () => {
  assert.equal(shouldRegister(ok), true);
  assert.equal(shouldRegister({ ...ok, prod: false }), false);
  assert.equal(shouldRegister({ ...ok, secure: false }), false); // plain http on the LAN
  assert.equal(shouldRegister({ ...ok, supported: false }), false);
  assert.equal(shouldRegister({ ...ok, base: "/Wargo/" }), false); // gh-pages demo
});
test("shouldReloadForChunk reloads once per minute at most", () => {
  assert.equal(shouldReloadForChunk(null, 100_000), true);
  assert.equal(shouldReloadForChunk(90_000, 100_000), false);
  assert.equal(shouldReloadForChunk(30_000, 100_000), true);
});
```

`sw.ts` must not touch `window` or `import.meta.env` at module load, so the test can import it under Node.

- [ ] **Step 2: Run** `cd web && node --test src/lib/sw.test.ts`. Expected: FAIL.

- [ ] **Step 3: Implement the helpers**, then:
  - `registerServiceWorker()`: return unless `shouldRegister({ prod: import.meta.env.PROD, secure: window.isSecureContext, supported: "serviceWorker" in navigator, base: import.meta.env.BASE_URL })`. Register `/sw.js` (scope `/`, `updateViaCache: "none"`). Define `apply(reg)`: if `reg.waiting` and the page already has a controller, post `SKIP_WAITING` to it. Call `apply` once after registration (launch). On `visibilitychange` to visible: `await reg.update()`, then `apply`; if a worker is still installing, wait for its `statechange` to `installed` for up to 10 s and `apply` then, otherwise leave it for the next resume. On `navigator.serviceWorker` `controllerchange`, reload once (module-level flag) if the page had a controller before (a first install must not reload).
  - `reloadOnStaleChunk()`: listen for Vite's `vite:preloadError` window event; if `shouldReloadForChunk` with the time stored in `sessionStorage` key `wargo.chunkReloadAt` (read and write in try/catch), store now, `preventDefault()` and `location.reload()`.

- [ ] **Step 4: Run** the tests. Expected: PASS. Run `npx tsc -b`. Expected: no errors.

- [ ] **Step 5: Verify the update flow in Playwright** against the Go server from Task 2: load the app (worker installs, no reload); change any file in `web/src` and rebuild + restart the server; switch the tab away and back (`page.evaluate` dispatching `visibilitychange` is not enough: use a second tab via `browser_tabs` and switch back). Expected: one reload, `caches.keys()` holds only the new version. Leave the tab in the foreground across a rebuild: expected no reload.

- [ ] **Step 6: Commit** `web/src/lib/sw.ts web/src/lib/sw.test.ts web/src/main.tsx`, message "Register the service worker and pick up new releases on resume".

### Task 4: "Wargo can't be reached" page

**Files:**
- Modify: `web/src/lib/api.ts`
- Create: `web/src/lib/reach.ts` (pure classifier, kept out of `api.ts` so Node can test it without the DOM)
- Create: `web/src/lib/reach.test.ts`
- Create: `web/src/app/Unreachable.tsx`
- Modify: `web/src/app/session.ts` (`useAuthState` passes a 10 s timeout)
- Modify: `web/src/app/routes.tsx` (`RequireAuth`, `PublicOnly`)

**Interfaces:**
- Produces: `classifyResponse(r: { path: string; status: number; contentType: string }): "ok" | "unreachable" | "error"` in `reach.ts`; `isUnreachable(e: unknown): boolean` exported from `api.ts`; `request` rejects with `new ApiError(0, "unreachable", "Wargo can't be reached")` for network failures and timeouts, and with `ApiError(status, "unreachable", ...)` when `classifyResponse` says so; `<Unreachable onRetry={() => void} retrying={boolean} />`; `useAutoRetry(active: boolean, retry: () => void): void` in `Unreachable.tsx`.

- [ ] **Step 1: Write the failing tests** in `web/src/lib/reach.test.ts`:

```ts
const api = (status: number, contentType: string) => classifyResponse({ path: "/api/auth/state", status, contentType });
test("Wargo's own JSON is never unreachable", () => {
  assert.equal(api(200, "application/json"), "ok");
  assert.equal(api(401, "application/json; charset=utf-8"), "error"); // still signs out
  assert.equal(api(503, "application/json"), "error");
});
test("proxy and tunnel answers count as unreachable", () => {
  assert.equal(api(200, "text/html; charset=UTF-8"), "unreachable"); // a login or error page
  assert.equal(api(502, "text/html"), "unreachable");
  assert.equal(api(530, ""), "unreachable"); // Cloudflare tunnel down
});
test("non-JSON 4xx is an ordinary error", () => {
  assert.equal(api(404, "text/plain"), "error");
});
```

- [ ] **Step 2: Run** `cd web && node --test src/lib/reach.test.ts`. Expected: FAIL.

- [ ] **Step 3: Implement** `classifyResponse`, then wire it into `api.ts`: wrap the `fetch` in `send` so a rejected fetch or a `TimeoutError` becomes the unreachable `ApiError` (an `AbortError` from TanStack Query cancelling still rethrows unchanged); in `request`, check `classifyResponse` before the 401/403 handling and before parsing JSON. Add `isUnreachable`. In `session.ts`, `useAuthState`'s `queryFn` passes `AbortSignal.any([signal, AbortSignal.timeout(10_000)])` to `api.get`.

- [ ] **Step 4: Run** the tests and `npx tsc -b`. Expected: PASS, no type errors.

- [ ] **Step 5: Build the page.** `Unreachable.tsx` follows `HostBlocked.tsx`'s layout: logo and name, then a `role="alert"` card titled "Wargo can't be reached", body "Check that the server is running and that this device is online. Wargo will keep trying.", and a "Try again" button that shows a spinner while `retrying`. Nothing else on the page. `useAutoRetry(active, retry)` schedules `retry` at 3 s doubling to 30 s while `active`, and also calls it on `online` and on `visibilitychange` to visible; it clears everything when `active` turns false.

- [ ] **Step 6: Use it in the gates.** In `RequireAuth` and `PublicOnly`, when `isUnreachable(state.error)` (and there is no successful `state.data` yet), render `<Unreachable onRetry={() => state.refetch()} retrying={state.isFetching} />` and call `useAutoRetry` with the same condition. Hooks stay above the early returns. Once a retry succeeds, the existing flow continues (app or login).

- [ ] **Step 7: Verify in Playwright** against the Go server (dev bypass off, so the login page is reachable): stop the backend and reload. Expected: only the connection page, no login form, no toasts. Start the backend. Expected: within 30 s the app (or login page) appears without touching anything. Then check that a signed-out session still lands on `/login`. Check phone width (390 px) and dark mode (`dark` class on `<html>`).

- [ ] **Step 8: Commit** the five files, message "Show a connection page when Wargo can't be reached at launch".

### Task 5: Docs and the full check

**Files:**
- Modify: `docs/SECURITY.md` (one bullet in the section about the web app: the service worker stores only the public app files that the login page already serves, never API responses, downloads or sockets, so signing out leaves nothing private on the device)
- Modify: `docs/STATUS.md` (a row for the installed-app service worker)
- Modify: `README.md` only if it describes installing to the Home Screen; otherwise leave it.

- [ ] **Step 1: Write the doc changes.**
- [ ] **Step 2: Run everything:** `go vet ./... && go test ./... && cd web && npm test && npx tsc -b && npm run build`. Expected: all pass.
- [ ] **Step 3: Check the demo build still works:** build with `--base /Wargo/` as in Task 1 Step 6. Expected: builds, no `sw.js`.
- [ ] **Step 4: Commit** the docs, message "Document the service worker".
- [ ] **Step 5: Hand-off note for the owner:** on the iPhone, after deploying, open the Home Screen app once (it installs the worker), close and reopen it from the switcher: the UI should appear without waiting on the network; update the container and switch back to the app: one quick reload onto the new version.

# Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A bell panel (Active and Recent) fed by server-side watchers, Web Push for opted-in devices with per-category Off / In app / In app and push settings, and a Feed reduced to history.

**Architecture:** A new Go package `internal/notify` owns notification state (SQLite), the occurrence rules, the live `notifications` hub topic, the HTTP API and Web Push (RFC 8291 encryption, RFC 8292 VAPID). Existing features report into it through `Deps.Notify`: docker events, the automatic update checks, a disk check in the metrics collector, and new background watchers for Transmission and Plex. The web app adds a bell and anchored panel in the top bar, a Notifications card in Settings → General, push handlers in the service worker, and removes the live sections from the Feed.

**Tech Stack:** Go (stdlib `crypto/ecdh`, `crypto/ecdsa`, `crypto/hkdf`, `crypto/aes`, `crypto/cipher`; `github.com/golang-jwt/jwt/v5` promoted from indirect), SQLite via `internal/store`, React + TanStack Query + Radix (`radix-ui` Popover), service worker in `web/sw/sw.js`.

**Spec:** `docs/superpowers/specs/2026-11-12-notifications-design.md`

## Global Constraints

- Categories (exact ids): `download.started`, `download.finished`, `stream.started`, `container.unhealthy`, `container.failed`, `updates.found`, `download.error`, `disk.full`.
- Modes (exact ids and labels): `off` "Off", `app` "In app", `push` "In app and push". Default for every category: `push`.
- Push counts: download 2 (start, finish), stream 1 (start), everything else 1 per occurrence. Nothing is pushed for what already exists when Wargo starts (baseline).
- Same stream: same Plex session key; or same user and same title, or same user and same show (grandparent rating key), within 30 minutes of the last one ending.
- Disk: occurrence at `>= 90%` used, resolved below `85%`.
- Recent retention: 7 days or 100 items, whichever is smaller. "Clear all" clears Recent only. Active rows can't be cleared.
- Badge = Active items + Recent items not cleared and not resolved.
- A manual update check updates the panel but never pushes.
- Push registrations belong to a session: a device whose session is gone (signed out, expired, revoked, replaced) is never sent anything; push service 404/410 deletes the registration.
- Push payloads are encrypted (RFC 8291, aes128gcm) and signed (RFC 8292 VAPID, ES256). VAPID private key stored with `store.Seal`.
- The service worker still never caches or answers `/api/` or `/ws/`.
- No new Go module or npm dependency (golang-jwt is already in the module graph; move it to a direct require).
- Copy: plain language, no em or en dashes as punctuation. Commits: no AI attribution lines.
- Web code formatting: Prettier `--print-width 140` only on files you create; edit existing files by hand (Prettier reflows hand-formatted code here).

## Review Focus

- **Wargo restarts while a stream plays, a download runs and a container is unhealthy:** all show in the panel, nothing is pushed. Test: Task 2 `TestBaselineDoesNotPush`, Task 5 `TestDockerBaselineNoPush`.
- **A Plex or Transmission poll fails (network blip) or one poll misses a stream:** the stream or download must not end and then "start" again with a second push. Test: Task 2 `TestStreamBlipSamePush`; Task 6 watchers skip `Set*` on fetch errors.
- **The same torrent removed and re-added, or re-checked:** no second "started" push. Test: Task 2 `TestDownloadRestartNoSecondPush`.
- **A device signed out or expired:** never receives a push. Test: Task 4 `TestPushSkipsDeadSessions`.
- **A category switched to Off while items exist:** they disappear from Active and Recent and from the badge, and come back when switched on. Test: Task 1 `TestOffHidesCategory`.

---

### Task 1: Notification store, rules for Recent, API

**Files:**
- Create: `internal/notify/notify.go` (Center, types, settings), `internal/notify/recent.go` (Raise, Resolve, Clear, retention), `internal/notify/http.go`, `internal/notify/notify_test.go`
- Modify: `internal/store/store.go` (append migration 3), `internal/server/server.go` (`Deps.Notify *notify.Center`), `cmd/wargo/main.go` (construct the Center with the store and hub; register its routes and `Run` as a feature: create `cmd/wargo/feature_notify.go` like `feature_brew.go`)

**Interfaces:**
- Produces:
  - `type Category string` with the eight constants above; `type Mode string` (`ModeOff`, `ModeApp`, `ModePush`).
  - `type Item struct { ID string; Category Category; Key string; Title, Body, Link string; CreatedAt time.Time; ResolvedAt, ClearedAt *time.Time }` (JSON camelCase).
  - `type Active struct { ID string; Category Category; Title, Detail string; Progress float64; StartedAt time.Time; Link string }`.
  - `type Snapshot struct { Active []Active; Recent []Item; Badge int; Settings map[Category]Mode }`, published on hub topic `"notifications"` (sticky) on every change.
  - `func New(st *store.Store, hub *events.Hub) (*Center, error)`; `func (c *Center) Mode(cat Category) Mode`.
  - `func (c *Center) Raise(ctx context.Context, o Occurrence) (pushed bool)` where `type Occurrence struct { Category Category; Key, Title, Body, Link string; Baseline bool }`. A Raise for a (category, key) that has an unresolved item is a no-op. `Baseline` stores without pushing.
  - `func (c *Center) Resolve(ctx context.Context, cat Category, key, title string)` sets `resolved_at` and replaces the title (for example "gluetun is healthy again").
  - Push hook used by Task 4: `c.push func(ctx context.Context, cat Category, title, body, link string)` called from Raise when mode is `push` and not baseline (nil until Task 4).
  - Routes: `GET /api/notifications` (Snapshot), `POST /api/notifications/clear` body `{"id": "..."}` or `{"all": true}`, `GET /api/notifications/settings`, `PUT /api/notifications/settings` body `map[Category]Mode` (unknown ids rejected 400).
- Migration 3:

```sql
CREATE TABLE notify_items (
  id TEXT PRIMARY KEY, category TEXT NOT NULL, key TEXT NOT NULL,
  title TEXT NOT NULL, body TEXT, link TEXT, created_at INTEGER NOT NULL,
  resolved_at INTEGER, cleared_at INTEGER);
CREATE INDEX notify_items_cat_key ON notify_items(category, key);
CREATE TABLE notify_subs (
  id TEXT PRIMARY KEY, session TEXT NOT NULL, endpoint TEXT NOT NULL UNIQUE,
  p256dh TEXT NOT NULL, auth TEXT NOT NULL, created_at INTEGER NOT NULL);
```

Settings live in the `settings` table under key `notify.settings` (JSON).

- [ ] **Step 1: Write failing tests** in `internal/notify/notify_test.go` (helper `newTestCenter(t)` opens a store in `t.TempDir()`):
  - `TestRaiseOncePerOccurrence`: two `Raise` with same category and key give one Recent item; after `Resolve` a third `Raise` gives a second item.
  - `TestResolvedLeavesBadge`: Badge 1 after Raise, 0 after Resolve; the item stays in Recent with `ResolvedAt` set and the new title.
  - `TestClearAndClearAll`: clearing one hides it; "all" hides every Recent item; Active is untouched; a cleared, unresolved problem raised again with the same key stays hidden (no-op).
  - `TestRetention`: 120 items keep the newest 100; an item 8 days old is dropped.
  - `TestOffHidesCategory`: an item in `container.unhealthy`, set mode `off`: Snapshot has no such item and Badge 0; set back to `push`: it is back.
  - `TestSettingsDefaultsAndValidation`: every category defaults to `push`; PUT with `{"bogus":"app"}` returns 400; PUT `{"stream.started":"app"}` persists across `New`.
- [ ] **Step 2: Run** `go test ./internal/notify/`. Expected: FAIL (package or symbols missing).
- [ ] **Step 3: Implement** the types, migration, Center (mutex-guarded, reads from SQLite, publishes Snapshot after each change), retention applied on every write, and the HTTP handlers with `httpx.Handler` like other features. Wire into `Deps` and register as a feature.
- [ ] **Step 4: Run** `go test ./internal/notify/ ./internal/store/ ./internal/server/`. Expected: PASS.
- [ ] **Step 5: Commit** "Add the notification store and API".

### Task 2: Live downloads and streams

**Files:**
- Create: `internal/notify/active.go`, `internal/notify/active_test.go`

**Interfaces:**
- Consumes: Task 1 `Center`, `Raise`, `Resolve`, Snapshot publish.
- Produces:
  - `type Download struct { Hash, Name, Status string; Percent, RateDown float64; ETA time.Duration; Error string }` (Status: `downloading`, `checking`, `queued`, `seeding`, `stopped`, `done`).
  - `type Stream struct { SessionKey, User, RatingKey, ShowKey, Title, Player, State string; ViewOffset, Duration time.Duration }` (`ShowKey` = Plex grandparent rating key, empty for movies).
  - `func (c *Center) SetDownloads(ctx context.Context, list []Download)` and `func (c *Center) SetStreams(ctx context.Context, list []Stream)`. The first call of each after `New` is the baseline (no pushes). Callers must not call them when their fetch failed.
  - Active ids: `dl:<hash>`, `st:<sessionKey>`. Active rows only for categories whose mode is not `off`: downloads appear if `download.started` or `download.finished` is on; streams if `stream.started` is on.
- Rules:
  - Download started: hash first seen in `downloading`, never pushed before for this hash in this process; occurrence `download.started` key = hash (pushed, then immediately resolved so it doesn't sit in Recent: started lives in Active only).
  - Download finished: hash seen with `Percent >= 1` or status `seeding`/`done` after being seen downloading in this process; Raise `download.finished` key = hash, title "Download finished", body = name; removes it from Active.
  - Download error: `Error != ""` raises `download.error` key = hash; error cleared resolves it.
  - Stream: Raise `stream.started` (push only, then resolved immediately like download.started) unless the stream matches a remembered one: same SessionKey; or same User and (same RatingKey or same non-empty ShowKey) and last seen within 30 minutes. Remember ended streams with their end time for 30 minutes.

- [ ] **Step 1: Write failing tests** (fake clock: `Center.now func() time.Time`; capture pushes by setting `c.push`):
  - `TestBaselineDoesNotPush`: first `SetDownloads` with one downloading torrent and first `SetStreams` with one stream: Active has 2 rows, 0 pushes.
  - `TestDownloadStartFinish`: new hash downloading → 1 push "started"; then 100% → 1 push "finished", Recent has "Download finished", Active empty. Total 2.
  - `TestDownloadRestartNoSecondPush`: hash pushed as started, disappears (removed), comes back downloading → no second push.
  - `TestStreamPauseResumeOnePush`: same SessionKey with state playing, paused, playing → 1 push.
  - `TestStreamBlipSamePush`: stream present, absent for one SetStreams call, present again with a new SessionKey but same user and RatingKey within 30 min → 1 push total.
  - `TestBingeOnePush`: same user, next episode (different RatingKey, same ShowKey) 2 minutes after the previous ended → 1 push; same after 31 minutes → 2 pushes; different user same show → 2 pushes.
  - `TestDownloadError`: error appears → Recent item, Badge counts it; error clears → resolved.
- [ ] **Step 2: Run** `go test ./internal/notify/ -run 'Baseline|Download|Stream|Binge'`. Expected: FAIL.
- [ ] **Step 3: Implement** in `active.go`.
- [ ] **Step 4: Run** `go test ./internal/notify/`. Expected: PASS.
- [ ] **Step 5: Commit** "Track live downloads and streams for notifications".

### Task 3: Updates and disk

**Files:**
- Create: `internal/notify/updates.go`, `internal/notify/updates_test.go`

**Interfaces:**
- Consumes: Task 1 Center.
- Produces:
  - `func (c *Center) ReportUpdates(ctx context.Context, source string, names []string, automatic bool)`; `source` is `containers`, `system` or `homebrew`. Maintains one Recent item `updates.found` key `pending` whose title is "N updates available" and body "Containers 3 · System 4 · Homebrew 2" (sources with 0 omitted), link `/updates`. Push only when `automatic` and at least one name is new since the last push for that source. When all sources report 0 the item is deleted (not resolved). Its first report after `New` is baseline.
  - `type Disk struct { Mount string; UsedPct float64 }`; `func (c *Center) ReportDisks(ctx context.Context, list []Disk)`: Raise `disk.full` key = mount at `>= 90`, Resolve below `85` with title "<mount> is back to N%"; first call baseline.
- [ ] **Step 1: Write failing tests:** `TestUpdatesAggregate` (counts per source in body, title total), `TestUpdatesSameSetNoPush` (same names again, automatic → no push), `TestUpdatesManualNoPush` (new names, automatic=false → item updated, no push), `TestUpdatesGoneWhenZero`, `TestDiskHysteresis` (89 → nothing, 91 → raise and push, 87 → still open, 84 → resolved, 91 → second raise).
- [ ] **Step 2: Run** `go test ./internal/notify/ -run 'Updates|Disk'`. Expected: FAIL.
- [ ] **Step 3: Implement** in `updates.go`.
- [ ] **Step 4: Run** `go test ./internal/notify/`. Expected: PASS.
- [ ] **Step 5: Commit** "Report pending updates and full disks as notifications".

### Task 4: Web Push

**Files:**
- Create: `internal/notify/webpush.go` (encryption, VAPID), `internal/notify/push.go` (subscriptions, sending), `internal/notify/push_test.go`
- Modify: `internal/notify/http.go` (push routes), `go.mod` (golang-jwt/jwt/v5 direct)

**Interfaces:**
- Consumes: Task 1 `c.push` hook, `notify_subs` table, `auth.FromContext(ctx) *auth.Session` (`IDHash`).
- Produces:
  - `func encrypt(uaPublic, authSecret, plaintext []byte, salt []byte, asPriv *ecdh.PrivateKey) ([]byte, error)` (RFC 8291 aes128gcm with one record; exported wrapper picks random salt and a fresh key).
  - `func vapidHeader(endpoint, subject string, key *ecdsa.PrivateKey, now time.Time) (string, error)` returning `vapid t=<jwt>, k=<base64url public key>`; JWT claims `aud` = endpoint origin, `exp` = now + 12h, `sub` = `mailto:wargo@localhost` (no personal data).
  - Routes: `GET /api/notifications/push` → `{ "publicKey": "<base64url>", "subscribed": bool }` for the current session; `POST /api/notifications/push` body `PushSubscription` JSON (`endpoint`, `keys.p256dh`, `keys.auth`) stores it under the current session (replacing an existing row for the same endpoint); `DELETE /api/notifications/push` removes the current session's registrations.
  - Sending: payload JSON `{ "title", "body", "link", "tag": "<category>:<key>", "badge": <int> }`; POST to the endpoint with headers `TTL: 86400`, `Urgency: normal`, `Content-Encoding: aes128gcm`, `Authorization: <vapid>`. Only to rows whose `session` exists in `sessions` with `expires_at` > now (join); rows without a live session are deleted. 404 or 410 deletes the row. Sending runs in a goroutine with a 10 s timeout per endpoint.
- [ ] **Step 1: Write failing tests:**
  - `TestEncryptRFC8291Vector`: the example in RFC 8291 Section 5 (take the keys, salt, auth secret and plaintext from the RFC text) produces exactly the RFC's ciphertext.
  - `TestVAPIDHeader`: parse the JWT with the public key: ES256, `aud` is `https://push.example.net`, `exp` 12 h after `now`.
  - `TestPushSkipsDeadSessions`: subs for a live session and for an expired one; a Raise in `push` mode sends exactly one request to an `httptest.Server` and the dead row is gone.
  - `TestPushGoneDeletes`: server answers 410 → row deleted.
  - `TestPushOnlyInPushMode`: mode `app` → no request.
- [ ] **Step 2: Run** `go test ./internal/notify/ -run 'Encrypt|VAPID|Push'`. Expected: FAIL.
- [ ] **Step 3: Implement.** VAPID key: generated once (P-256), private part stored sealed in `settings` key `notify.vapid`.
- [ ] **Step 4: Run** `go test ./internal/notify/ && go vet ./...`. Expected: PASS.
- [ ] **Step 5: Commit** "Send Web Push notifications to opted-in devices".

### Task 5: Docker, update checks and disk report in

**Files:**
- Modify: `internal/docker/service.go` (event loop), `internal/docker/checks.go` (`autoCheck` result), `internal/pkg/autocheck.go`, `internal/pkg/service.go` (`handleCheck`), `internal/brew/brew.go` (`Run` loop and `handleCheck`), `internal/metrics/metrics.go` (`Collector.Run`), `cmd/wargo/feature_*.go` where constructors need `d.Notify`
- Create: `internal/docker/notify.go`, `internal/docker/notify_test.go`

**Interfaces:**
- Consumes: Tasks 1 and 3 (`Raise`, `Resolve`, `ReportUpdates`, `ReportDisks`).
- Produces: `func containerEvent(action, exit string) (cat notify.Category, resolve bool, ok bool)` in `internal/docker/notify.go`: `health_status: unhealthy` → unhealthy raise; `health_status: healthy` → unhealthy resolve; `die` with exit not in {0, 137, 143} → failed raise; `start` → failed resolve, but only once the container has stayed up for 2 minutes with no `die` in between (a restart loop is one occurrence, not one per restart; add `TestRestartLoopOneOccurrence` with a fake clock to Step 1). Keys are container names; titles "<name> is unhealthy", "<name> failed to start", resolved "<name> is healthy again", "<name> is running again". Wargo's own container is skipped.
- Baseline: when the docker service starts, containers already unhealthy or exited badly are raised with `Baseline: true`.
- Update checks: after each background check, call `ReportUpdates(source, names, true)`; after a manual check handler, `ReportUpdates(source, names, false)`. Containers: names of containers with `updateAvailable`; system: package names from the check; Homebrew: formula and cask names.
- Disk: `Collector.Run` calls `ReportDisks` with mounted filesystems every 5 minutes.
- [ ] **Step 1: Write failing tests** `TestContainerEventMapping` (the four actions, exits 0/137/143 ignored, `oom` ignored) and `TestDockerBaselineNoPush` (helper feeding a fake container list to the baseline function: items raised, no push).
- [ ] **Step 2: Run** `go test ./internal/docker/ -run 'ContainerEvent|DockerBaseline'`. Expected: FAIL.
- [ ] **Step 3: Implement** and wire the calls listed above.
- [ ] **Step 4: Run** `go test ./... && GOOS=linux go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./...`. Expected: PASS.
- [ ] **Step 5: Commit** "Report container problems, updates and disk usage to notifications".

### Task 6: Transmission and Plex watchers

**Files:**
- Modify: `internal/integrations/plex/plex.go` (`Metadata`: add `SessionKey string json:"sessionKey"`, `GrandparentRatingKey string json:"grandparentRatingKey"`), `internal/integrations/plexsvc.go` (`PlexSession`: add `SessionKey`, `RatingKey`, `ShowKey`), `internal/integrations/handlers.go` (watcher)
- Create: `internal/integrations/watch.go`, `internal/integrations/watch_test.go`

**Interfaces:**
- Consumes: Task 2 `SetDownloads`, `SetStreams`, `Download`, `Stream`; existing `payload(ctx)` and `plexActivity(ctx)`.
- Produces: `func toDownloads(ts []TorrentView) []notify.Download`, `func toStreams(ss []PlexSession) []notify.Stream`; a `watch(ctx)` loop started from the integrations feature's `Run`: Transmission every 10 s while its URL is set and `download.started`, `download.finished` or `download.error` is not `off`; Plex every 15 s while configured and `stream.started` is not `off`. On a fetch error, skip the `Set*` call.
- [ ] **Step 1: Write failing tests** `TestToDownloads` (status mapping from Transmission views, percent, error string) and `TestToStreams` (episode title "Show S2 E4", movie title with year, ShowKey from grandparent key) plus `TestWatchSkipsOnError` (fake fetch returning an error → SetDownloads not called; use a small interface for the fetchers).
- [ ] **Step 2: Run** `go test ./internal/integrations/ -run 'ToDownloads|ToStreams|WatchSkips'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/integrations/ ./internal/notify/`. Expected: PASS.
- [ ] **Step 5: Commit** "Watch Transmission and Plex for notifications".

### Task 7: Bell and panel

**Files:**
- Create: `web/src/features/notifications/api.ts`, `web/src/features/notifications/Bell.tsx`, `web/src/features/notifications/Panel.tsx`, `web/src/features/notifications/format.ts`, `web/src/features/notifications/format.test.ts`
- Modify: `web/src/app/Layout.tsx` (`TopBar`: render `<Bell />` after `<ConnectionPill />`, visible on all widths)

**Interfaces:**
- Consumes: Task 1 routes and `notifications` topic.
- Produces: `useNotifications()` (topic + GET, query key `["notifications"]`), `useClearNotification()`, `useClearAll()`; `badgeText(n: number): string` (`""` for 0, `"99+"` above 99) and `sortActive(a: Active[]): Active[]` (newest first) in `format.ts`; `<Bell />`.
- Panel: Radix `Popover` anchored to the bell with `Popover.Arrow`; header "Notifications", ⚙︎ link to `/settings/general#notifications`, ✕. Sections "Active" (no dismiss) and "Recent" ("Clear all" button, ✕ per row; resolved rows muted). Height `max-h-[calc(100dvh-4.5rem-env(safe-area-inset-top)-env(safe-area-inset-bottom))]` with the list scrolling inside; width `min(24rem, calc(100vw-1.5rem))`. Opens when the URL has `?notifications=1` (then removes the parameter). Sets the Home Screen icon badge with `navigator.setAppBadge(badge)` / `clearAppBadge()` when available (try/catch).
- [ ] **Step 1: Write failing tests** in `format.test.ts`: `badgeText(0) === ""`, `badgeText(5) === "5"`, `badgeText(150) === "99+"`; `sortActive` orders by `startedAt` descending.
- [ ] **Step 2: Run** `cd web && node --test src/features/notifications/format.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** the files.
- [ ] **Step 4: Run** `cd web && npm test && npx tsc -b`. Expected: PASS. Then check in Playwright against a built server (`WARGO_PKG_FAKE` or seeded rows): bell badge, panel anchored with arrow, Active and Recent, clear one and all, at 390x844, 390x600 and 1280x900, light and dark; the list scrolls inside the panel at 390x600.
- [ ] **Step 5: Commit** "Add the notifications bell and panel".

### Task 8: Settings and push on the device

**Files:**
- Create: `web/src/features/settings/NotificationsCard.tsx`, `web/src/features/notifications/push.ts`, `web/src/features/notifications/push.test.ts`
- Modify: `web/src/features/settings/General.tsx` (add `<NotificationsCard />` with `id="notifications"`, scroll into view when the hash is `#notifications`), `web/sw/sw.js` (`push` and `notificationclick`)

**Interfaces:**
- Consumes: Task 1 settings routes, Task 4 push routes.
- Produces: `pushSupport(env: { secure: boolean; sw: boolean; push: boolean; standalone: boolean; ios: boolean; permission: string }): "ok" | "not-https" | "unsupported" | "install-first" | "denied"` in `push.ts`; `enablePush()` / `disablePush()` (register with `PushManager.subscribe({ userVisibleOnly: true, applicationServerKey })`, POST or DELETE `/api/notifications/push`).
- Card: one row per category (labels: "Download started", "Download finished", "Plex stream started", "Container unhealthy", "Container failed to start", "Updates found", "Download error", "Disk nearly full"), each with a three-way segmented control "Off" / "In app" / "In app and push". Below, "Push on this device": state text plus "Enable push on this device" or "Turn off push on this device"; for `install-first`: "On iPhone, add Wargo to your Home Screen and open it from there to get push notifications."; `denied`: tell them to allow notifications for Wargo in the system settings; `not-https`: push needs Wargo over HTTPS.
- Service worker: `push` shows `registration.showNotification(title, { body, tag, data: { link } })` and calls `self.navigator.setAppBadge?.(badge)`; `notificationclick` focuses an open window or opens `/?notifications=1` (or the item's link for updates and containers).
- [ ] **Step 1: Write failing tests** in `push.test.ts`: iOS not standalone → `install-first`; not secure → `not-https`; permission `denied` → `denied`; all good → `ok`; no `PushManager` (not iOS) → `unsupported`.
- [ ] **Step 2: Run** `cd web && node --test src/features/notifications/push.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npm test && npx tsc -b && npm run build`. Expected: PASS. In Chromium on `127.0.0.1` (secure context): enable push, see the POST, trigger a Raise (for example stop a test container with exit 1) and see a notification.
- [ ] **Step 5: Commit** "Add notification settings and push on the device".

### Task 9: Feed keeps history only

**Files:**
- Modify: `web/src/features/home/Feed.tsx`

- Remove: the "Needs attention" section (containers and broken torrents), Updates, Plex "Now playing", active downloads, and the System events section with its query. Keep: Plex "Recently watched" and "Recently added", finished downloads, Recent files. Drop queries the Feed no longer needs. The "All quiet" state stays.
- [ ] **Step 1: Make the change** and run `cd web && npx tsc -b` (no unused imports left). Expected: PASS.
- [ ] **Step 2: Check** in Playwright that Home shows the reduced Feed and the skeleton still lines up (`AppSkeleton` FeedSkeleton height for "All quiet" is unchanged).
- [ ] **Step 3: Commit** "Keep the Home feed to history".

### Task 10: Docs and full check

**Files:**
- Modify: `docs/SECURITY.md` (push: what is sent, encrypted end to end to the browser's push service, VAPID key sealed, registrations tied to sessions), `docs/STATUS.md` (row for notifications and push), `README.md` if it lists Home features.

- [ ] **Step 1: Write the docs.**
- [ ] **Step 2: Run** `go vet ./... && go test ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./... && cd web && npm test && npx tsc -b && npm run build`. Expected: all pass.
- [ ] **Step 3: Commit** "Document notifications and push".
- [ ] **Step 4: Phone check** through the HTTPS sandbox on the Linux box (see the service worker work): enable push from the Home Screen app, start a download and a Plex stream, and confirm one push each; pause and resume the stream (no push); finish the download (one push).

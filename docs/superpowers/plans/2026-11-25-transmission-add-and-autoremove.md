# Adding torrents, removing finished ones, one Transmission poller: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** With Transmission alone, the Torrents page can add magnet links and `.torrent` files; one setting (off by default) removes finished torrents from Transmission keeping their files; one shared poller replaces Wargo's separate Transmission polling and makes fewer calls.

**Architecture:** A poller in `internal/integrations` takes "snapshots" of Transmission (full when the page is watched, light in the background) on a pace set by demand, and feeds the live topic, download notifications and auto-remove, in that order. Page loads reuse a fresh full snapshot. The web app gains an Add dialog, a setting switch with confirmation, and drops the live topic for tabs hidden 30 s.

**Tech Stack:** Go 1.26 (stdlib only, no new modules), React 19 + TanStack Query + Radix (existing `web/src/components/ui`), `node --test` for web logic.

**Spec:** `docs/superpowers/specs/2026-11-25-transmission-add-and-autoremove-design.md`

## Global Constraints

- Never write an em dash or an en dash used as punctuation, in code, comments, copy, commits or docs. No AI attribution in commits.
- Auto-remove never deletes data: `torrent-remove` with `delete-local-data: false`, always.
- Nothing is removed while the setting is off; the setting is off when absent from the store.
- Pace: 2 s while the `transmission` topic has subscribers; 10 s when not, but a download notification category is on or the setting is on; no RPCs otherwise.
- Full snapshot = `torrent-get` with `transmission.TorrentFields` + `session-stats`. Light snapshot = one `torrent-get` with `downloadFields` (today's list). Download dir (`session-get`) and `free-space` at most once a minute, and right after the Transmission connection settings change.
- Backoff after a failed poll: 2 s, doubling, at most 60 s; back to the pace after a success.
- `GET /api/transmission/torrents` reuses a full snapshot at most 2 s old from the current client; concurrent misses share one poll.
- Order after each good snapshot: publish (if subscribers and full), notifications, auto-remove.
- Finished = `percentDone >= 1`, status not `checking`/`check-wait`, `error != 3`.
- Web: a hidden tab drops the `transmission` topic after 30 s hidden and resubscribes when visible.
- `.torrent` files at most 10 MB (`torrent.MaxSize`); magnets start with `magnet:?`.

## Review Focus

- Transmission restarting returns a short or empty list for a moment: a poll that succeeds with fewer torrents must not remove anything that is not itself finished, and must not trip the notifications "new Transmission" reset. (Test in Task 3.)
- Turning the setting on with the page closed: finished torrents go within about a second, not after a 10 s wait. (Test in Task 3.)
- The Transmission connection settings change (another server): the next snapshot is from the new client, slow data is refetched at once, and a page load never returns the old server's snapshot. (Test in Task 1.)
- Several `.torrent` files dropped where one is invalid: the valid ones are still added and the invalid one gets its own error toast. (Test in Task 4.)
- A torrent paused at 100% (status stopped) counts as finished; one paused at 99% does not. (Test in Task 3.)

---

### Task 1: Snapshots, slow data and page loads

**Files:**
- Create: `internal/integrations/snapshot.go`
- Modify: `internal/integrations/handlers.go` (replace `payload` body, `handleTorrents`, `publish`)
- Modify: `internal/integrations/service.go` (Service fields)
- Modify: `internal/integrations/transmission/transmissiontest/fake.go` (per-torrent error, addedDate; `Update`)
- Test: `internal/integrations/snapshot_test.go`

**Interfaces:**
- Produces:
  - `type snapshot struct { full bool; at time.Time; c *transmission.Client; torrents []TorrentView; stats StatsView }` (`stats` only set when `full`; `stats.FreeSpace` from the slow cache).
  - `func (s *Service) poll(ctx context.Context, full bool) (*snapshot, error)`: one snapshot from the current client; stores it as the latest on success; refreshes slow data when `full` and the cache is older than 1 min or from another client. A failed `session-get` or `free-space` does not fail the poll.
  - `func (s *Service) latest() *snapshot` (nil when none).
  - `func (s *Service) pagePayload(ctx context.Context) (*Payload, error)`: latest full snapshot when at most 2 s old and from `s.transmissionClient()`, else `poll(ctx, true)`; concurrent callers that miss share one in-flight poll (a small mutex + channel, no new module).
  - `func (snap *snapshot) payload() *Payload`.
  - Fake: `Torrent` gains `Error int`, `ErrorString string`, `AddedDate int64` (default 1700000000 when 0), served by `torrent-get`; `func (f *Fake) Update(id int64, fn func(*Torrent))` and `func (f *Fake) ClearFail(method string)`, both under the lock.
- `pageTorrents` stays until Task 2 deletes it.

- [ ] **Step 1: Write the failing tests** in `snapshot_test.go`, using `newWatchHarness`:
  - `TestPollFullCalls`: first `poll(ctx, true)` calls exactly `torrent-get, session-stats, session-get, free-space`; a second at +30 s (`h.s.now`) calls only `torrent-get, session-stats`; at +61 s all four again.
  - `TestPollLightCalls`: `poll(ctx, false)` calls only `torrent-get`, with `ft.LastFields()` equal to `downloadFields`.
  - `TestPollSlowDataFailure`: `ft.Fail("free-space", "x")`: poll succeeds, `payload().Stats.FreeSpace == nil`.
  - `TestPagePayloadReuse`: after `poll(ctx, true)` at t, `pagePayload` at t+2 s makes no call; at t+2.1 s makes one full poll; after a light poll only, `pagePayload` makes a full poll.
  - `TestPagePayloadShared`: 5 goroutines call `pagePayload` with no snapshot; `torrent-get` appears once in `ft.Methods()`.
  - `TestPagePayloadClientChange`: after a full poll, point `h.s.tr.URL` at a second fake: `pagePayload` returns the second fake's torrents and calls `session-get` and `free-space` on it.
  - `TestTransmissionEndpoints` (existing) keeps passing unchanged.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/integrations/ -run 'TestPoll|TestPagePayload' -v`
Expected: FAIL (undefined: poll, pagePayload)

- [ ] **Step 3: Implement** `snapshot.go` as above; `handleTorrents` calls `pagePayload`; `payload(ctx)` is removed (callers use `pagePayload` or `poll(ctx, true)` then `payload()`); fake changes.

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/integrations/... -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/integrations
git commit -m "Take Transmission snapshots and reuse them for page loads"
```

### Task 2: The shared poller

**Files:**
- Create: `internal/integrations/poller.go`
- Modify: `internal/integrations/watch.go` (keep Plex streams; remove `pageTorrents`, `fetchDownloads`, the download half of `watchSource`, `watchDownloadsEvery`)
- Modify: `internal/integrations/handlers.go` (`TransmissionFeature.Run`, `afterMutation`)
- Modify: `internal/integrations/service.go` (fields: poller state, `wake chan struct{}` made in `New`)
- Test: `internal/integrations/poller_test.go`; move the download cases of `watch_test.go` there

**Interfaces:**
- Consumes: Task 1 `poll`, `latest`, `snapshot.payload`.
- Produces:
  - `type pace struct { every time.Duration; full bool }`; `func (s *Service) demand() pace`: `{2 s, true}` when `s.hub.Subscribers(Topic) > 0`; `{10 s, false}` when a download category is not Off (`anyOn` on `s.notify`) or `s.removeFinished()` (Task 3; until then a stub returning false); else `{0, false}`. Not configured: `{0, false}`.
  - `func (s *Service) pollerStep(ctx context.Context, now time.Time, woken bool)`: polls when `woken`, or when `now` reaches last poll + `demand().every` and is past the backoff; `woken` always polls (when Transmission is configured), whatever the demand, and resets the backoff. After a good snapshot calls, in order, `s.publishSnapshot`, `s.reportDownloads`, `s.autoRemove` (stub until Task 3). Keeps `failures` and `retryAt` (2 s doubling to 60 s).
  - `func (s *Service) reportDownloads(ctx context.Context, snap *snapshot)`: today's `pollDownloads` rules against `s.notify`: categories all Off or not configured: `ResetDownloads`; a snapshot from another client than the last reported one: `ResetDownloads` then `SetDownloads`.
  - `func (s *Service) wakePoller()`: non-blocking send on `s.wake`.
  - `func (s *Service) runPoller(ctx context.Context)`: 1 s ticker plus `s.wake`, calling `pollerStep(ctx, s.now(), woken)`.
  - `TransmissionFeature.Run` starts `go s.watch(ctx)` (Plex only now) and runs `s.runPoller(ctx)`. `afterMutation` calls `wakePoller`.

- [ ] **Step 1: Write the failing tests** in `poller_test.go` (drive `pollerStep` with explicit `now`; real `notify.Center` not needed: give `Service` a `watchSink`-typed field for notifications, set to a `countingSink` in tests):
  - `TestPaceWatched`: one hub subscriber to `Topic`: steps at t, t+1 s, t+2 s make full polls at t and t+2 s only.
  - `TestPaceBackground`: no subscriber, notifications on: light polls at t and t+10 s, none between.
  - `TestPaceIdle`: no subscriber, every download category Off: no RPC over 60 steps; `ResetDownloads` called.
  - `TestBackoff`: `ft.Fail("torrent-get", ...)` with a subscriber: attempts at t, t+2, t+6, t+14, t+30, t+62, t+122 s (capped at 60 s gaps); after `ft.ClearFail("torrent-get")` the next good poll returns to 2 s.
  - `TestStepOrder`: record call order through the sink and a hub subscriber: publish, then `SetDownloads`.
  - `TestWokenPollsNow`: `wakePoller()` then `pollerStep(.., woken=true)` polls immediately even if the last poll was 1 s ago.
  - Moved from `watch_test.go` and adapted: fetch fields in background (`TestWatchFetchesOnlyTorrents`), stats failure irrelevant in background (`TestWatchIgnoresStatsFailure`), reset on client change (`TestWatchResetsOnClientChange`), skip resets baseline (`TestWatchSkipResetsBaseline` downloads half). Delete `TestWatchReusesPageData` (one poller now; covered by `TestPaceWatched` reporting downloads from full snapshots).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/integrations/ -run 'TestPace|TestBackoff|TestStep|TestWoken' -v`
Expected: FAIL (undefined: pollerStep)

- [ ] **Step 3: Implement** `poller.go` and the wiring above; remove the old download polling from `watch.go`.

- [ ] **Step 4: Run the package and the whole repo**

Run: `go test ./internal/integrations/... -race && go vet ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/integrations
git commit -m "Poll Transmission from one loop paced by who needs it"
```

### Task 3: Remove torrents when they finish

**Files:**
- Create: `internal/integrations/autoremove.go`
- Modify: `internal/integrations/transmission/transmission.go` (`RemoveMany`)
- Modify: `internal/integrations/service.go` (load the setting in `New`; key constant; routes; capabilities)
- Test: `internal/integrations/autoremove_test.go`

**Interfaces:**
- Consumes: Task 2 `pollerStep`, `wakePoller`, `snapshot`.
- Produces:
  - Store key `keyTransmissionPrefs = "integrations.transmission.prefs"`, value `transmissionPrefs{ RemoveFinished bool `json:"removeFinished"` }`.
  - `func (s *Service) removeFinished() bool`.
  - `func finished(t TorrentView) bool`: `PercentDone >= 1 && Status != "checking" && Status != "check-wait" && Error != 3`.
  - `func (s *Service) autoRemove(ctx context.Context, snap *snapshot)`: when on, one `RemoveMany` for every finished torrent by hash, `deleteData` false; on success one audit entry per torrent via `s.audit.Write(nil, audit.Event{Action: "transmission.remove", Target: hash, Details: map[string]any{"name": name, "automatic": true, "deleteData": false}})`; on failure `s.audit.Outcome("transmission.autoremove", "background", err, nil)` and nothing else (the next snapshot retries); on success after a failure, `Outcome` with nil records recovery. After a removal, `wakePoller()` so the page updates.
  - `func (c *Client) RemoveMany(ctx context.Context, ids []ID, deleteData bool) error`; `Remove` calls it with one id.
  - `GET /api/transmission/settings` returns `{"removeFinished": bool}`; `PUT` takes the same, saves, audits `transmission.settings` (details `removeFinished`), calls `wakePoller()` when turned on, returns the saved value. 400 on a body without `removeFinished`.
  - `TransmissionFeature.Capabilities` adds `"removeFinished": bool`.

- [ ] **Step 1: Write the failing tests** in `autoremove_test.go` (fake with `Update` for status, percent, error):
  - `TestFinished`: table over status/percent/error: seeding 1.0 yes; seed-wait 1.0 yes; stopped 1.0 yes; stopped 0.99 no; checking 1.0 no; check-wait 1.0 no; seeding 1.0 error 3 no; seeding 1.0 error 2 yes; downloading 0.5 no.
  - `TestAutoRemoveOff`: setting off, a seeding torrent at 1.0: after a step no `torrent-remove` in `ft.Methods()`.
  - `TestAutoRemoveOn`: on, torrents A downloading 0.5, B seeding 1.0, C stopped 1.0: one `torrent-remove`; B and C `Removed && !DataDeleted`; A untouched; two audit rows with action `transmission.remove` and `"automatic":true`.
  - `TestAutoRemoveAfterNotify`: sink records order: for a torrent going 0.5 to 1.0 between two steps, `SetDownloads` sees it at 1.0 before `torrent-remove` is called.
  - `TestAutoRemoveRetries`: `ft.Fail("torrent-remove", "x")`: torrent still there, one audit failure row; after clearing, the next step removes it; a second failure step writes no second failure row.
  - `TestAutoRemoveShortList`: on, a snapshot that lists only one unfinished torrent (others missing): nothing removed, no notifications reset.
  - `TestSettingsEndpoint`: GET default `false`; PUT `true` then GET `true`; capabilities `removeFinished` true; PUT `{}` is 400; audit row `transmission.settings`.
  - `TestTurnOnRemovesSoon`: page closed, notifications all Off (pace idle), a seeding torrent: PUT `true`, then `pollerStep(ctx, now+1 s, woken=true)` removes it.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/integrations/ -run 'TestFinished|TestAutoRemove|TestSettingsEndpoint|TestTurnOn' -v`
Expected: FAIL

- [ ] **Step 3: Implement** `autoremove.go`, `RemoveMany`, routes, capabilities; replace Task 2's stubs.

- [ ] **Step 4: Run all Go tests**

Run: `go test ./... -race && go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/integrations
git commit -m "Remove torrents from Transmission when they finish, if asked"
```

### Task 4: Add magnet links and .torrent files

**Files:**
- Create: `web/src/features/torrents/add.ts`, `web/src/features/torrents/add.test.ts`, `web/src/features/torrents/AddDialog.tsx`
- Modify: `web/src/features/torrents/api.ts` (`Added` type, `addTorrent`), `web/src/features/torrents/Downloads.tsx` (toolbar button, card drop, empty-state copy), `web/src/features/torrents/Torrents.tsx` (description)

**Interfaces:**
- Consumes: existing `POST /api/transmission/torrents` (`{magnet}` or `{torrentBase64}`; returns `{id, name, hashString, duplicate}`).
- Produces:
  - `add.ts`: `isMagnet(s: string): boolean` (trimmed, starts with `magnet:?`, at most 16384 chars); `MAX_TORRENT_BYTES = 10 * 1024 * 1024`; `torrentFileError(f: { name: string; size: number }): string | null` ("Not a .torrent file" unless the name ends in `.torrent` case-insensitively; "Larger than 10 MB"); `toBase64(bytes: ArrayBuffer): string`.
  - `api.ts`: `interface Added { id: number; name: string; hashString: string; duplicate: boolean }`; `addTorrent(body: { magnet: string } | { torrentBase64: string }): Promise<Added>`.
  - `AddDialog({ open, onOpenChange, initialFiles }: { open: boolean; onOpenChange: (o: boolean) => void; initialFiles?: File[] })`.

- [ ] **Step 1: Write the failing tests** in `add.test.ts`:
  - `isMagnet`: `"magnet:?xt=urn:btih:abc"` true; `"  magnet:?xt=urn:btih:abc  "` true; `"http://x/a.torrent"` false; `""` false; `"magnet:?" + "a".repeat(16385)` false.
  - `torrentFileError`: `{name: "a.torrent", size: 10}` null; `{name: "A.TORRENT", size: 10}` null; `{name: "a.txt", size: 10}` "Not a .torrent file"; `{name: "a.torrent", size: 10 * 1024 * 1024 + 1}` "Larger than 10 MB".
  - `toBase64(new TextEncoder().encode("d4:infoe").buffer)` equals `"ZDQ6aW5mb2U="`.

- [ ] **Step 2: Run them to verify they fail**

Run: `cd web && npm test`
Expected: FAIL (cannot find module ./add)

- [ ] **Step 3: Implement** `add.ts`, `addTorrent`, and `AddDialog`: magnet input (`Input`, error under it while not a magnet), a drop area that is also a button opening a hidden `<input type="file" accept=".torrent,application/x-bittorrent" multiple>`, the chosen files listed with remove buttons and their `torrentFileError`; Add sends the magnet (if any) and each valid file as its own request, in parallel; per result `toast.ok("Added", name)`, `toast.ok("Already in Transmission", name)` for duplicates, `toast.error("Could not add", message)` per failure; the dialog closes when at least one succeeded and none failed, otherwise stays open with the failed ones listed. In `Downloads.tsx`: an **Add** button (`Plus` icon) in `toolbarClass` before `FilterMenu`, disabled unless `caps.transmission.available`; `onDragOver`/`onDrop` on the `Card` that open `AddDialog` with the dropped `.torrent` files; empty state text without Prowlarr: "Add a magnet link or .torrent file and it will appear here." In `Torrents.tsx`, the Transmission-only description: "Add and manage Transmission downloads."

- [ ] **Step 4: Run web checks**

Run: `cd web && npm test && npm run typecheck`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src/features/torrents
git commit -m "Add magnet links and .torrent files from the Downloads view"
```

### Task 5: The setting, its note, and hidden tabs

**Files:**
- Create: `web/src/features/torrents/finished.ts`, `web/src/features/torrents/finished.test.ts`
- Modify: `web/src/features/torrents/api.ts`, `web/src/features/torrents/IntegrationSettings.tsx` (`TransmissionCard`), `web/src/features/torrents/Downloads.tsx` (status bar note, topic gating), `web/src/features/torrents/Torrents.tsx` (topic gating), `web/src/lib/hooks.ts`, `web/src/app/session.ts*` (`Capabilities.transmission.removeFinished?: boolean`)

**Interfaces:**
- Consumes: Task 3 endpoints and capability.
- Produces:
  - `finished.ts`: `isFinished(t: Pick<Torrent, "percentDone" | "status" | "error">): boolean`, the same rule as Go `finished`; `countFinished(ts: Torrent[]): number`.
  - `api.ts`: `transmissionSettingsKey = ["transmission", "settings"]`; `useTransmissionSettings()`; `useSaveTransmissionSettings()` (PUT, then invalidates `transmissionSettingsKey` and `["capabilities"]`).
  - `hooks.ts`: `useHiddenFor(ms: number): boolean`, true once `document.visibilityState` has been `"hidden"` for `ms`, false as soon as it is visible.

- [ ] **Step 1: Write the failing test** `finished.test.ts`: the same table as Go `TestFinished`, plus `countFinished` over it equals 4.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd web && npm test`
Expected: FAIL

- [ ] **Step 3: Implement**:
  - In `TransmissionCard`, when configured: `Switch` labelled "Remove torrents when they finish", description "Finished torrents are taken off Transmission so they stop seeding. Their files are kept. Torrents that are already finished are removed when you turn this on." Turning it on: when `countFinished` of `useTorrents()` data is above 0, a `Dialog` asks first: title "Remove finished torrents?", body "N finished torrent(s) will be removed now. Their files are kept.", buttons Cancel / Turn on; otherwise saves at once. Turning off saves at once. Toast on failure.
  - In the Downloads status bar, when `caps.transmission.removeFinished`: a `StatusItem` "Removing finished torrents" linking to `/settings/integrations`, hidden below `sm`.
  - `Downloads.tsx` and `Torrents.tsx` pass the topic as `null` when `useHiddenFor(30_000)` is true; when it turns false again, `Downloads` invalidates `torrentsKey`.

- [ ] **Step 4: Run web checks**

Run: `cd web && npm test && npm run typecheck && npm run build`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "Offer removing finished torrents and stop live updates in hidden tabs"
```

### Task 6: Verify and give the owner a test container on Deimos

**Files:** none in the repo.

- [ ] **Step 1: Full checks**

Run: `go test ./... -race && go vet ./... && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./internal/... && (cd web && npm test && npm run build)`
Expected: PASS

- [ ] **Step 2: Build a native (not container-mode) test Wargo on Deimos**, as in the 2026-11-12 sandbox: send the local commits to `kushal@10.0.0.100` as a git bundle over scp (never push), build in `golang:1.26-bookworm` with the web build, run in a Debian slim image as user `tester` in groups `sudo` and `shadow`, `--cap-drop ALL --security-opt no-new-privileges`, its own volume `wargo-test-data`, port `10.0.0.100:2230` (LAN only), `require_totp = false` (owner asked: no second factor), `origins` including `http://10.0.0.100:2230`. Never touch the `wargo` container, its volume or port 2207. Keep files in `~/wargo-test` on Deimos, with the tester password in `~/wargo-test/tester-password.txt` (mode 600).
  Expected: `curl -s -o /dev/null -w '%{http_code}' http://10.0.0.100:2230/` prints 200, and signing in needs only the password.

- [ ] **Step 3: Tell the owner** the URL, the account, that the Transmission integration must be entered once (`http://10.0.0.100:9091/transmission/rpc`), and that turning on "Remove torrents when they finish" there acts on the real Transmission (it removes the real finished torrents, keeping files), so the OpenClaw cron and this setting would both be removing.

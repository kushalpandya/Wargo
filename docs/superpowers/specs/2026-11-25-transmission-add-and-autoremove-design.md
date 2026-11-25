# Adding torrents and removing them when done, with one Transmission poller

Date: 2026-11-25. Status: design, awaiting approval. Reviewed by a builder check; its questions are answered below (marked *builder-check call* where Claude decided). Revised after the owner chose one setting over per-torrent toggles.

## Why

The Torrents page is useful today only when both Prowlarr and Transmission are
set up: with Transmission alone it lists downloads, but there is no way to add
one. The owner wants Transmission alone to be enough to add downloads (paste a
magnet link, or drop or browse for a `.torrent` file).

The owner also wants a finished download taken off Transmission so it stops
seeding. On Deimos this is done today by an external cron (OpenClaw) that
looks for completed torrents and removes them; with this feature in Wargo that
cron can go. The owner keeps only a few torrents at a time.

Auto-remove means Wargo watches Transmission even when no page is open, which
adds to the load Wargo already puts on Transmission's RPC endpoint (see
"Polling today"). The owner wants that load reduced, not increased, so this
work also replaces Wargo's Transmission polling with one shared poller.

## Goals

- With only Transmission set up (and reachable), the Torrents page can add a
  download from a magnet link or from one or more `.torrent` files, dropped or
  picked from disk.
- One setting, "Remove torrents when they finish", shipped off. While it is
  on, Wargo removes every torrent from Transmission once it has finished
  downloading, keeping the downloaded files, however it was added (Wargo's
  Add, Search, Transmission's own UI, another tool). Torrents that are already
  finished (seeding or paused at 100%) when it is turned on are removed right
  away. This works with no browser open and across Wargo restarts, and does
  what the owner's cron does today.
- For the same work, Wargo makes no more Transmission RPC calls than today,
  and far fewer while the Torrents page is open; none when nothing needs
  Transmission's state. (Auto-remove is new work: it costs one call every
  10 s while it is in effect, replacing the owner's external cron.)

## Non-goals

- Deleting downloaded data. Auto-remove never deletes files.
- A per-torrent choice. The owner considered a toggle on each row and chose
  the single setting: it matches the cron, and there is nothing to keep track
  of per torrent. To keep one torrent seeding, the owner turns the setting
  off.
- Removing by seed ratio or seeding time (Transmission has its own limits for
  stopping seeding; this feature removes).
- A Torrents page for Prowlarr alone.
- Fetching only changed torrents (Transmission's `recently-active`): with a
  few torrents it saves almost nothing and adds merge logic.
- Other pollers in Wargo (metrics, containers): unchanged.
- The GitHub Pages demo (MSW fake backend on the gh-pages branch): updating
  its handlers for the new endpoints is a follow-up, not part of this work.

## Anti-goals (would be a failure even if it works)

- Removing anything while the setting is off (it ships off, so upgrading
  removes nothing), or removing a torrent that has not finished downloading.
- Removing a torrent before Wargo's notifications have seen it complete, so
  that a "Download finished" notification that is due is lost. (When none is
  due, for example the category is off, the torrent was already complete when
  first seen, or it finished while Wargo was down, the torrent is still
  removed: auto-remove does not depend on notification state. *Builder-check
  call.*)
- Wargo calling Transmission more often than today for the same work.

## Polling today (what changes)

`internal/integrations`:

- `TransmissionFeature.Run` publishes the `transmission` topic every 2 s while
  it has subscribers. Each publish (`payload`) makes 4 RPCs: `torrent-get`
  with every field for every torrent, `session-stats`, `session-get` (only for
  the download directory) and `free-space`.
- `GET /api/transmission/torrents` (each page load) makes the same 4 RPCs.
- The notifications watcher (`watch.go`) polls every 10 s while a download
  notification category is on. It reuses the page's list when it is at most
  10 s old (`pageTorrents`), otherwise it makes its own `torrent-get`.
- When Transmission is unreachable, the 2 s loop keeps trying, each attempt
  waiting up to its 10 s timeout.
- The web app keeps its event socket (and so its topic subscriptions) open
  while the tab is hidden, so a background tab on Torrents keeps the 2 s loop
  running.

## Approaches considered

- **A. One shared poller whose pace follows demand (chosen).** One loop
  fetches torrents and speeds; the page, the notifications watcher and
  auto-remove all read its result. Fast while the page is watched, slow when
  only background features need it, stopped otherwise. Slow-changing data is
  fetched rarely, page loads reuse a fresh result, and failures back off.
- **B. A, plus fetching only changed torrents.** Rejected: negligible saving
  for a few torrents, more code (merging, periodic full refresh).
- **C. Keep the separate loops and cache in each.** Rejected: auto-remove
  would be a third loop, and loops would still duplicate each other's calls.

The owner picked A.

## Design

### The shared poller

A single goroutine in `internal/integrations` owns all routine Transmission
polling, replacing the 2 s publish loop and the watcher's own Transmission
fetch (Plex stream polling in the watcher is unchanged).

Each poll is a **snapshot**, in one of two forms (*builder-check call*, so
background polling costs no more than today's watcher):

- **full** (when the page is watched, and for page loads): `torrent-get`
  with `transmission.TorrentFields` plus `session-stats`;
- **light** (background pace): one `torrent-get` with the fields
  notifications and auto-remove read (today's `downloadFields`, plus what the
  removal rule needs), no stats.

The latest snapshot replaces `pageTorrents` as the shared "last good list",
and records its form, its time and which client (settings) it came from. Only
a full snapshot can serve the topic or a page load.

Pace, re-evaluated before every wait:

| Who needs Transmission | Interval |
|---|---|
| The `transmission` topic has subscribers (Torrents page visible somewhere) | 2 s |
| No subscribers, but a download notification category is on, or "Remove torrents when they finish" is on | 10 s |
| Nothing | no polling |

Changes of demand must take effect promptly: a new subscriber, turning the
remove setting on, or a notification setting change wakes the poller instead of
waiting out a 10 s interval. (How it is woken is left to the builder; the
events hub already reports subscriber counts.)

After each good snapshot, in this order:

1. Publish it on the topic if there are subscribers.
2. Hand the torrents to notifications (`SetDownloads`, as `pollDownloads` does
   today, including its baseline and "new Transmission" reset rules) if a
   download category is on.
3. Run auto-remove on it if the setting is on (below).

Doing auto-remove last guarantees that the snapshot that first shows a
torrent complete has already been seen by notifications, so "Download
finished" is raised before the torrent disappears.

**Slow-changing data.** The download directory (`session-get`) and its free
space (`free-space`) are fetched at most once a minute and cached with the
snapshot; also refreshed right after the Transmission settings change. The
health check (`transmissionHealth`, `session-get` with its own cache) may
reuse a recent good snapshot as proof of health; the builder decides whether
that is worth it.

**Failures.** After a failed poll the next attempt waits 2 s, doubling up to
60 s, and returns to the demand interval after a success. While failing, the
topic publishes the error as today (`Payload.Error`). A failed poll reports
nothing to notifications and runs no auto-remove (an empty list would end
every download).

**Page loads.** `GET /api/transmission/torrents` returns the latest snapshot
when it is full, at most 2 s old and from the current settings; otherwise it
makes one full poll, and concurrent requests share that one poll.

**Mutations.** Add, start, stop, remove and turning the setting on trigger an
immediate poll (today's `afterMutation` behaviour), which counts as a poll for
pacing.

**Hidden tabs.** The Torrents page stops subscribing to the `transmission`
topic once the tab has been hidden for 30 s, and subscribes again when it
becomes visible. Only this topic: the app-wide event socket and its other
topics (notifications badge etc.) are unaffected. `useTopic` /
`useTopicValue` already accept a null topic to unsubscribe.

**Expected load.** Watching: 2 RPCs every 2 s (was 4), plus 2 a minute.
Background only: 1 RPC every 10 s (as today's watcher). Nothing needed: none.
Down: at most one attempt a minute after the backoff ramps up.

### Remove torrents when they finish

**State.** One stored value, `{ "removeFinished": bool }`, off when absent
(store key is the builder's choice). It is kept apart from the Transmission
connection settings, which `PUT /api/integrations/transmission` replaces
wholesale.

**Removing.** While it is on, every torrent in each good snapshot that has
finished downloading is removed: `percentDone >= 1`, its status is not
`checking` or `check-wait` (a torrent being verified is not done), and it has
no local error (`error` 3: the data may be unusable, so it is held for the
owner; tracker errors and warnings don't hold it). *Builder-check call.*
Seeding, queued-to-seed and paused finished torrents all count. Removal is
`torrent-remove` with `delete-local-data: false`; several finished torrents in
one snapshot can go in one call. Each removed torrent is written to the audit
log like a manual remove (`transmission.remove`, target the hash, with the
torrent name and an "automatic" marker in the details; source background). A
removal that fails is retried on later snapshots; failures are logged without
flooding (the builder can use `audit.Outcome`-style deduplication).

Turning the setting on wakes the poller, so torrents that are already
finished go within a poll.

**API.**

- `GET` / `PUT /api/transmission/settings` read and write
  `{ "removeFinished": bool }`. Audited.
- The capabilities entry for Transmission includes `removeFinished`, so the
  Downloads page can show it without another request.

### Web UI

**Add.** An **Add** button on the Downloads toolbar (next to the filter)
opens a dialog:

- a field for a magnet link (validated as `magnet:?…` before sending),
- a drop area that also opens a file picker for `.torrent` files (several at
  once is fine; each is sent as its own add),
- Add / Cancel.

Dropping `.torrent` files anywhere on the Downloads card opens the same
dialog with them filled in. Results are reported with the existing toasts:
added, already in Transmission (duplicate), or the error. The Add button
needs Transmission available (`caps.transmission.available`).

**The setting.** A switch in the Transmission card of Settings →
Integrations: "Remove torrents when they finish". Its help text says that
finished torrents are taken off Transmission so they stop seeding, that the
files are kept, and that torrents already finished are removed when it is
turned on. When turning it on would remove finished torrents right away, it
asks first, saying how many ("2 finished torrents will be removed now. Their
files are kept."). *Claude's call.*

**On the Downloads page.** While the setting is on, the status bar shows a
short note ("Removing finished torrents") linking to the setting, so the
owner knows why finished torrents disappear. *Claude's call.*

**Copy.** With Prowlarr not set up, the Downloads empty state says to add a
magnet link or `.torrent` file instead of pointing to Torrent search, and the
page description mentions adding.

### Testing

- Go: the shared poller's pace and snapshot form for each demand case, and
  its backoff; RPC counts per situation against the "Expected load" figures;
  page loads reusing a fresh full snapshot and sharing one poll; slow data
  fetched once a minute; the order publish, notify, remove (a torrent that
  finishes between polls still raises "Download finished" before it is
  removed); nothing removed while the setting is off; removal conditions
  (complete, seeding, paused at 100%, checking, local error, failure retry);
  already-finished torrents removed promptly after turning it on; the new
  endpoints. The fake Transmission in
  `internal/integrations/transmission/transmissiontest` already handles
  `torrent-add` and `torrent-remove` and records the methods called, which
  is enough to count RPCs.
- Web: the Add dialog (magnet validation, files, drop), the setting and its
  confirmation, and the Downloads note, with the existing test setup.

## Left to the builder

- Store keys, type and function names, file layout inside
  `internal/integrations`.
- How the poller is woken on demand changes.
- Whether the health check reuses snapshots.
- Exact icons, wording within the intent above, and layout.

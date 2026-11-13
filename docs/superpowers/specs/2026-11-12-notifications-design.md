# Notifications and push

Date: 2026-11-12. Status: design, awaiting approval. Reviewed by a builder check; its questions are answered below.

## Why

The Home feed mixes two different things: what is happening right now or
needs the owner (a stream playing, a download running, an unhealthy
container, updates waiting) and what already happened (finished downloads,
recently added media, file changes). The owner wants the first kind in its
own place, a notifications panel behind a bell, and wants the installed
Home Screen app (service worker shipped in cf3fef8) to buzz the phone for
the things that matter, with control over each kind.

## Goals

- A bell with a badge at the top right of the app header. Tapping it opens
  a floating panel that hangs from the bell (joined to it by a small
  pointer), with two sections: **Active** and **Recent**.
- The Feed keeps history only; everything live or needing action moves to
  the panel.
- Web Push to devices that opted in, per category, with the push counts
  below.
- Settings → General → Notifications: one row per category with three
  choices. A shortcut in the panel opens that section directly.

## Non-goals

- Email, SMS, or any channel other than Web Push.
- Quiet hours, digests, snoozing, per-device category settings.
- Telling the owner's own Plex streams apart from other people's.
- A full history of notifications beyond the retention below (the Feed and
  System → Events stay the history).
- Notifying about every error in the system log.

## Anti-goals (failure even if it technically works)

- Repeated pushes for the same thing: a stream that pauses and resumes, a
  container that flaps, the same pending updates found again.
- A burst of pushes when Wargo starts or restarts, for downloads, streams or
  problems that already existed.
- Notification text (torrent names, titles, host names) readable by the push
  service in between.
- Push working only while a Wargo page is open.
- A panel taller than the screen, or one that can't scroll on a phone.

## Panel (decided by the owner)

- **Bell** at the top right of the header, with a count badge. Tapping it
  opens the panel; tapping the bell again, outside, Escape, or ✕ closes it.
- **Shape:** a floating panel anchored to the bell, with a pointer joining
  them. Its height follows the viewport: it grows with content up to nearly
  the full height below the header (leaving a small margin and the iPhone
  home indicator area) and its list scrolls inside. On phones it spans
  nearly the full width, still hanging from the bell.
- **Header:** title "Notifications", a ⚙︎ button that navigates to
  Settings → General with the Notifications section in view, and ✕.
- **Active** (no title decoration, no red dot): live downloads and live
  Plex streams, most recent first. They cannot be dismissed; each leaves
  when its stream stops, or its download is stopped, finishes, or is
  removed. Rows show progress (stream position, download percentage, speed,
  time left).
- **Recent**: finished downloads and every non-live event below, most
  recent first. Each row has ✕ to clear it, and the section has "Clear all"
  (clears Recent only).
- A download that finishes leaves Active and appears in Recent as "Download
  finished".

## Categories and push counts

| Category | In panel | Pushes |
|---|---|---|
| Download started | Active while downloading | 1, when it starts |
| Download finished | Recent | 1, when it finishes |
| Plex stream started (any user, including the owner) | Active while playing | 1, when it starts |
| Container unhealthy | Recent | 1 per occurrence |
| Container failed to start or restart | Recent | 1 per occurrence |
| Updates found by an automatic check (containers, system packages, Homebrew) | Recent | 1 per occurrence |
| Download error (Claude's call) | Recent | 1 per occurrence |
| Disk nearly full (Claude's call) | Recent | 1 per occurrence |

Each category has three settings (labels are Claude's call, the owner asked
for common names):

- **Off:** nothing anywhere; its items don't appear in Active or Recent.
- **In app:** shown in the panel only.
- **In app and push:** in the panel, and pushed to every device that has
  enabled push.

"Download started" and "Download finished" are separate rows, so the owner
can keep one push and drop the other. If both are Off, active downloads are
not shown in Active.

### What counts as one occurrence

- **Stream:** identified by the Plex session. Pausing, resuming, seeking or
  buffering is the same stream. The same user starting the same title, or
  another episode of the same show (Plex grandparent), within 30 minutes of
  the last one ending is still the same stream: a binge through autoplay is
  one push, and its Active row follows the episode playing now (Claude's
  call, after the builder check).
- **Download:** identified by the torrent hash. Started = first seen
  downloading; finished = first seen complete. Re-checking or re-starting a
  torrent that already pushed "started" does not push again.
- **Container unhealthy / failed:** one occurrence lasts until the container
  is healthy (or running normally) again. A container stopped on purpose
  (exit 137 or 143, or stopped through Wargo or Docker) is not a failure,
  matching the Feed's existing rule. A container that keeps restarting is
  one occurrence, not one per restart.
- **Updates found:** one occurrence per automatic check result that
  contains updates not already notified. The same pending set found again
  is not new. A manual "Check for updates" on the Updates page updates the
  panel item but never pushes (the owner is already looking).
- **Download error:** one per torrent until its error clears.
- **Disk nearly full:** a filesystem at 90% or more; one occurrence until it
  drops below 85%.
- **At startup:** whatever already exists when Wargo starts (running
  streams, active downloads, unhealthy containers, pending updates) becomes
  the baseline and is shown in the panel but not pushed.

Clearing a Recent item hides that occurrence only; a new occurrence of the
same kind appears again.

### How Recent items change after they appear (decided by the owner)

- **Problems** (container unhealthy or failed, download error, disk nearly
  full) stay in Recent until cleared, as a record that it happened. When the
  problem goes away they mark themselves resolved ("gluetun is healthy
  again", "disk back to 82%") and stop counting toward the badge.
- **Updates found** is one item that tracks what is pending across
  containers, system packages and Homebrew: its counts change as updates
  are installed or new ones are found, and it disappears once nothing is
  pending. It never shows as "resolved".
- **Download finished** is a plain record until cleared.
- Clearing a problem while it is still happening hides it; it does not come
  back until a new occurrence (after it resolved).

## Feed after the split

- **Stays:** Plex recently watched and recently added, finished downloads
  (as history), recent file changes.
- **Moves to the panel:** Now playing, downloads in progress, the "Needs
  attention" section (unhealthy or crashed containers, broken torrents), the
  Updates section.
- **Removed:** the System events section (every error-level journal line).
  System → Events remains the place for those.

## Push (decided)

- Only in the installed Home Screen app on iOS (Web Push requirement); any
  browser that supports Web Push elsewhere. The settings section says when
  the current device can't receive push and why (not installed, not HTTPS,
  permission denied).
- Per device: an "Enable push on this device" button that asks the browser
  for permission and registers the device. Category settings are shared;
  devices only opt in or out.
- Tapping a push opens the app with the panel open (the item's own page for
  updates and containers is reachable from its row).
- The service worker (web/sw/sw.js) gains `push` and `notificationclick`
  handlers. It still never caches API data.

## State lives on the server

- Recent items, clears, the badge count, category settings and device
  subscriptions are stored in Wargo's SQLite store, so clearing on one
  device clears everywhere. The panel updates live through the existing
  event hub.
- Recent keeps the last 7 days or 100 items, whichever is smaller (Claude's
  call).

## Server-side detection (needed for push)

Push must work with no browser open. Today Transmission is polled only while
a page subscribes and Plex activity is fetched only on request; Docker
events are already followed continuously. Wargo needs background watchers:

- Plex sessions about every 15 s, Transmission about every 10 s, only while
  their integration is configured and at least one related category is not
  Off.
- Container health and failures from the existing Docker events stream.
- Updates from the existing automatic check loops (pkg, brew, containers).
- Disk usage from the existing metrics collection.

## Security

- Push payloads are encrypted for the device (RFC 8291) and signed with a
  VAPID key (RFC 8292); the push service in between can't read titles or
  names. The VAPID private key is stored sealed (store.Seal).
- Only a signed-in, fully enrolled session can register a device. A
  device's push registration belongs to that session: it is deleted when the
  session signs out, expires, is revoked (Settings → Security) or is
  replaced by a new sign-in from the same browser, so a device nobody is
  signed in on stops receiving names on its lock screen (Claude's call,
  after the builder check). The settings section on such a device shows
  push as off with the enable button. Registrations the push service
  reports as gone (404/410) are deleted.
- `docs/SECURITY.md` gets a short section: what is sent, to whom, and how
  it is protected.

## Decided by Claude, open to change

- Option labels: "Off", "In app", "In app and push".
- Two extra categories: download error and disk nearly full (90% in, 85%
  out).
- Default for every category: "In app and push".
- Badge counts Active items plus uncleared Recent items that are not
  resolved. The installed app's
  Home Screen icon shows the same number (Badging API) where supported.
- Same-stream window: 30 minutes.
- Recent retention: 7 days or 100 items.
- Web Push implemented with the Go standard library (crypto/ecdh,
  crypto/hkdf, AES-GCM) and golang-jwt (already in the module graph)
  rather than adding a web-push dependency.
- Pushes are sent even when the app is open on that device (no
  foreground detection).

## Left to the builder

- Table layout, package boundaries, event hub topic names.
- Exact wording of rows and pushes, following the app's plain tone (no
  dashes as punctuation).
- How Settings → General scrolls to and highlights the Notifications
  section from the ⚙︎ shortcut (anchor, query parameter).
- Testing: unit tests for occurrence rules (streams pausing, restarts at
  startup, flapping containers, repeated update sets), push encryption
  against RFC 8291 test vectors, and a real iPhone check through the HTTPS
  sandbox on the Linux box (see the service worker work).

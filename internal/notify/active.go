// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// sameStreamWindow is how long after a stream ends a new one from the same
// user, of the same title or show, still counts as the same stream.
const sameStreamWindow = 30 * time.Minute

// addedSlack allows for Transmission's clock running a little behind
// Wargo's when telling a newly added torrent from one that was there.
const addedSlack = time.Minute

const (
	downloadsLink = "/torrents?tab=downloads"
	streamsLink   = "/"
)

// Download is one torrent as the downloads poller sees it.
type Download struct {
	Hash, Name string
	// Status is downloading, checking, queued, seeding, stopped or done.
	Status string
	// Percent is 0..1; RateDown is bytes per second.
	Percent, RateDown float64
	ETA               time.Duration
	Error             string
	// Added is when the torrent was added to Transmission (zero: unknown).
	Added time.Time
}

// Stream is one Plex playback session.
type Stream struct {
	SessionKey, User, RatingKey string
	// ShowKey is the Plex grandparent rating key, empty for movies.
	ShowKey string
	// Title is the row and push text ("Alex is watching Severance S2 E4").
	Title, Player, State string
	ViewOffset, Duration time.Duration
}

// liveState is what SetDownloads and SetStreams remember between calls.
// Guarded by Center.mu.
type liveState struct {
	dlSeen    bool      // SetDownloads ran before; later calls are not baseline
	dlAt      time.Time // when SetDownloads last ran
	dls       map[string]*dlState
	downloads []Active

	stSeen  bool // SetStreams ran before
	streams map[string]*streamState
	ended   []endedStream
	playing []Active
}

type dlState struct {
	firstSeen   time.Time
	startPushed bool // "started" handled once per hash while it is listed
	// added: added to Transmission since the report before it was first
	// seen, so finishing is news even if it was never seen downloading
	// (added and done between two polls).
	added           bool
	seenDownloading bool
	// showing: it has an Active row. Checking or queued keeps the row of
	// a download that has one.
	showing  bool
	finished bool
	errored  bool
}

type streamState struct {
	s         Stream
	startedAt time.Time
}

type endedStream struct {
	streamState
	endedAt time.Time
}

// SetDownloads replaces the list of torrents. The first call after New is
// the baseline: it is shown but nothing is pushed. Don't call it when the
// fetch failed (an empty list means every torrent is gone). Calls must be
// serialized (one poller goroutine): the raises run after the lock is
// released. A torrent missing from list is forgotten, so one added again
// later (with a later Added) is a new download.
func (c *Center) SetDownloads(ctx context.Context, list []Download) {
	c.mu.Lock()
	l := &c.live
	baseline := !l.dlSeen
	l.dlSeen = true
	if l.dls == nil {
		l.dls = map[string]*dlState{}
	}
	now := c.now()
	prev := l.dlAt
	l.dlAt = now
	var raises []Occurrence
	var resolves []string
	seen := make(map[string]bool, len(list))
	rows := []Active{}
	for _, d := range list {
		seen[d.Hash] = true
		st := l.dls[d.Hash]
		if st == nil {
			// A hash first seen after the baseline is a new download only
			// when Transmission added it since the last report. Otherwise it
			// was there before (another Transmission, or back from a partial
			// list) and counts like a baseline entry.
			added := !baseline && !d.Added.IsZero() && d.Added.After(prev.Add(-addedSlack))
			st = &dlState{firstSeen: now, added: added, startPushed: !added}
			l.dls[d.Hash] = st
		}
		if baseline {
			// Listed at the baseline: already there, so starting (or
			// resuming) later is not news.
			st.startPushed = true
		}
		complete := d.Percent >= 1 || d.Status == "seeding" || d.Status == "done"
		st.showing = !complete && (d.Status == "downloading" ||
			(st.showing && (d.Status == "checking" || d.Status == "queued")))
		switch {
		case complete:
			if (st.seenDownloading || st.added) && !st.finished {
				st.finished = true
				raises = append(raises, Occurrence{Category: DownloadFinished, Key: d.Hash,
					Title: "Download finished", Body: d.Name, Link: downloadsLink, Baseline: baseline})
			}
		case d.Status == "downloading":
			st.seenDownloading = true
			if !st.startPushed {
				st.startPushed = true
				raises = append(raises, Occurrence{Category: DownloadStarted, Key: d.Hash,
					Title: "Download started", Body: d.Name, Link: downloadsLink, Live: true})
			}
		}
		if st.showing {
			rows = append(rows, Active{
				ID: "dl:" + d.Hash, Category: DownloadStarted, Title: d.Name,
				Detail: downloadDetail(d), Progress: clamp01(d.Percent),
				StartedAt: st.firstSeen, Link: downloadsLink,
			})
		}
		switch {
		case d.Error != "" && !st.errored:
			st.errored = true
			raises = append(raises, Occurrence{Category: DownloadError, Key: d.Hash,
				Title: "Download error", Body: d.Name + ": " + d.Error, Link: downloadsLink, Baseline: baseline})
		case d.Error == "" && st.errored:
			st.errored = false
			resolves = append(resolves, d.Hash)
		}
	}
	// A removed torrent's error is over too, and the torrent is forgotten.
	for hash, st := range l.dls {
		if seen[hash] {
			continue
		}
		if st.errored {
			resolves = append(resolves, hash)
		}
		delete(l.dls, hash)
	}
	l.downloads = rows
	c.publishLocked(ctx)
	c.mu.Unlock()

	if baseline {
		// Errors left open from before a restart that cleared (or whose
		// torrent went away) while Wargo was down.
		var errored []string
		for _, d := range list {
			if d.Error != "" {
				errored = append(errored, d.Hash)
			}
		}
		c.Reconcile(ctx, DownloadError, errored, nil)
	}
	for _, o := range raises {
		c.Raise(ctx, o)
	}
	for _, hash := range resolves {
		c.Resolve(ctx, DownloadError, hash, "")
	}
}

// ResetDownloads makes the next SetDownloads a baseline. The watcher calls
// it while it is not polling Transmission (every download category Off, or
// Transmission not set up), so what started meanwhile is not pushed late.
func (c *Center) ResetDownloads() {
	c.mu.Lock()
	c.live.dlSeen = false
	c.mu.Unlock()
}

// ResetStreams is ResetDownloads for SetStreams.
func (c *Center) ResetStreams() {
	c.mu.Lock()
	c.live.stSeen = false
	c.mu.Unlock()
}

// SetStreams replaces the list of Plex sessions. The first call after New
// is the baseline: it is shown but nothing is pushed. Don't call it when
// the fetch failed. Calls must be serialized (one poller goroutine).
func (c *Center) SetStreams(ctx context.Context, list []Stream) {
	list = withSessionKeys(list)
	c.mu.Lock()
	l := &c.live
	baseline := !l.stSeen
	l.stSeen = true
	if l.streams == nil {
		l.streams = map[string]*streamState{}
	}
	now := c.now()
	kept := l.ended[:0]
	for _, e := range l.ended {
		if now.Sub(e.endedAt) <= sameStreamWindow {
			kept = append(kept, e)
		}
	}
	l.ended = kept

	// Sessions gone from list end first, so a new one can carry on a
	// session that ended since the last poll (autoplay to the next episode).
	seen := make(map[string]bool, len(list))
	for _, s := range list {
		seen[s.SessionKey] = true
	}
	for key, cur := range l.streams {
		if !seen[key] {
			l.ended = append(l.ended, endedStream{streamState: *cur, endedAt: now})
			delete(l.streams, key)
		}
	}

	var raises []Occurrence
	rows := []Active{}
	for _, s := range list {
		cur := l.streams[s.SessionKey]
		if cur == nil {
			cur = &streamState{startedAt: now}
			if i := l.matchEnded(s); i >= 0 {
				// The same stream carrying on: keep when it started.
				cur.startedAt = l.ended[i].startedAt
				l.ended = append(l.ended[:i], l.ended[i+1:]...)
			} else if !baseline {
				raises = append(raises, Occurrence{Category: StreamStarted, Key: s.SessionKey,
					Title: s.Title, Body: s.Player, Link: streamsLink, Live: true})
			}
			l.streams[s.SessionKey] = cur
		}
		cur.s = s
		progress := 0.0
		if s.Duration > 0 {
			progress = clamp01(float64(s.ViewOffset) / float64(s.Duration))
		}
		rows = append(rows, Active{
			ID: "st:" + s.SessionKey, Category: StreamStarted, Title: s.Title,
			Detail: streamDetail(s), Progress: progress, StartedAt: cur.startedAt, Link: streamsLink,
		})
	}
	l.playing = rows
	c.publishLocked(ctx)
	c.mu.Unlock()

	for _, o := range raises {
		c.Raise(ctx, o)
	}
}

// withSessionKeys gives each stream without a Plex session key a key of
// its own, from its user and what it plays (or its player), so sessions
// without one do not collapse into a single row. It copies list rather
// than change the caller's.
func withSessionKeys(list []Stream) []Stream {
	out := make([]Stream, len(list))
	for i, s := range list {
		if s.SessionKey == "" {
			what := s.RatingKey
			if what == "" {
				what = s.Player
			}
			s.SessionKey = "anon:" + s.User + ":" + what
		}
		out[i] = s
	}
	return out
}

// matchEnded finds a recently ended stream that s carries on: the same
// session, or the same user playing the same title or the same show. A
// stream without a user only matches its own session.
func (l *liveState) matchEnded(s Stream) int {
	for i, e := range l.ended {
		p := e.s
		if p.SessionKey == s.SessionKey {
			return i
		}
		if s.User == "" || p.User != s.User {
			continue
		}
		if (s.RatingKey != "" && p.RatingKey == s.RatingKey) || (s.ShowKey != "" && p.ShowKey == s.ShowKey) {
			return i
		}
	}
	return -1
}

// activeLocked lists the Active rows for categories that are not Off,
// newest first. Downloads show while either download category is on.
func (c *Center) activeLocked() []Active {
	var out []Active
	if c.modeLocked(DownloadStarted) != ModeOff || c.modeLocked(DownloadFinished) != ModeOff {
		out = append(out, c.live.downloads...)
	}
	if c.modeLocked(StreamStarted) != ModeOff {
		out = append(out, c.live.playing...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// downloadDetail reads like "62% · 8.4 MB/s · 3 min left".
func downloadDetail(d Download) string {
	parts := []string{fmt.Sprintf("%d%%", int(clamp01(d.Percent)*100))}
	if d.RateDown > 0 {
		parts = append(parts, formatRate(d.RateDown))
	}
	if d.ETA > 0 {
		parts = append(parts, formatETA(d.ETA))
	}
	return strings.Join(parts, " · ")
}

// streamDetail reads like "Apple TV · Paused".
func streamDetail(s Stream) string {
	var parts []string
	if s.Player != "" {
		parts = append(parts, s.Player)
	}
	if s.State != "" {
		parts = append(parts, strings.ToUpper(s.State[:1])+s.State[1:])
	}
	return strings.Join(parts, " · ")
}

func formatRate(bps float64) string {
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	i := 0
	for bps >= 1000 && i < len(units)-1 {
		bps /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", bps, units[i])
	}
	return fmt.Sprintf("%.1f %s", bps, units[i])
}

func formatETA(d time.Duration) string {
	if d < time.Minute {
		return "under a minute left"
	}
	mins := int(math.Ceil(d.Minutes()))
	if mins < 60 {
		return fmt.Sprintf("%d min left", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%d h left", h)
	}
	return fmt.Sprintf("%d h %d min left", h, m)
}

func clamp01(f float64) float64 {
	return math.Max(0, math.Min(1, f))
}

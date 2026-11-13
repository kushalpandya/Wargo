// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func (e *testEnv) downloads(list ...Download) {
	e.clock.advance(time.Second)
	e.c.SetDownloads(context.Background(), list)
}

func (e *testEnv) streams(list ...Stream) {
	e.clock.advance(time.Second)
	e.c.SetStreams(context.Background(), list)
}

func downloading(hash string, pct float64) Download {
	return Download{Hash: hash, Name: "Torrent " + hash, Status: "downloading", Percent: pct, RateDown: 8.4e6, ETA: 3 * time.Minute}
}

func stream(session, user, rating, show string) Stream {
	return Stream{
		SessionKey: session, User: user, RatingKey: rating, ShowKey: show,
		Title: user + " is watching " + rating, Player: "Apple TV", State: "playing",
		ViewOffset: 10 * time.Minute, Duration: 40 * time.Minute,
	}
}

func (e *testEnv) countPushes(cat Category) int {
	n := 0
	for _, p := range e.pushes {
		if p.cat == cat {
			n++
		}
	}
	return n
}

func TestBaselineDoesNotPush(t *testing.T) {
	e := newTestCenter(t)
	e.downloads(downloading("h1", 0.5))
	e.streams(stream("s1", "alex", "ep4", "show"))
	s := e.snap(t)
	if len(s.Active) != 2 {
		t.Fatalf("active = %+v, want 2 rows", s.Active)
	}
	if len(e.pushes) != 0 {
		t.Fatalf("pushes = %+v, want none", e.pushes)
	}
	if s.Badge != 2 {
		t.Errorf("badge = %d, want 2", s.Badge)
	}
	// Newest first: the stream was seen after the download.
	if s.Active[0].ID != "st:s1" || s.Active[1].ID != "dl:h1" {
		t.Errorf("active order = %s, %s", s.Active[0].ID, s.Active[1].ID)
	}
	dl := s.Active[1]
	if dl.Title != "Torrent h1" || dl.Detail != "50% · 8.4 MB/s · 3 min left" || dl.Progress != 0.5 || dl.Category != DownloadStarted {
		t.Errorf("download row = %+v", dl)
	}
	st := s.Active[0]
	if st.Title != "alex is watching ep4" || st.Detail != "Apple TV · Playing" || st.Progress != 0.25 || st.Category != StreamStarted {
		t.Errorf("stream row = %+v", st)
	}
}

func TestDownloadStartFinish(t *testing.T) {
	e := newTestCenter(t)
	e.downloads()
	e.downloads(downloading("h1", 0.1))
	if len(e.pushes) != 1 || e.pushes[0].cat != DownloadStarted || e.pushes[0].key != "h1" {
		t.Fatalf("pushes = %+v, want one started", e.pushes)
	}
	if s := e.snap(t); len(s.Recent) != 0 || len(s.Active) != 1 {
		t.Fatalf("after start: active %d recent %d, want 1 and 0", len(s.Active), len(s.Recent))
	}
	e.downloads(downloading("h1", 0.6))
	done := downloading("h1", 1)
	done.Status = "seeding"
	e.downloads(done)
	e.downloads(done)
	if len(e.pushes) != 2 || e.pushes[1].cat != DownloadFinished {
		t.Fatalf("pushes = %+v, want started then finished", e.pushes)
	}
	if p := e.pushes[1]; p.title != "Download finished" || p.body != "Torrent h1" {
		t.Errorf("finished push = %+v", p)
	}
	s := e.snap(t)
	if len(s.Active) != 0 {
		t.Errorf("active = %+v, want empty", s.Active)
	}
	if len(s.Recent) != 1 || s.Recent[0].Title != "Download finished" || s.Recent[0].Category != DownloadFinished {
		t.Errorf("recent = %+v", s.Recent)
	}
}

func TestDownloadRestartNoSecondPush(t *testing.T) {
	e := newTestCenter(t)
	e.downloads()
	e.downloads(downloading("h1", 0.1))
	// Re-checked from scratch (a torrent removed in between is a new
	// download: TestReaddedTorrentNotifiesAgain).
	checking := downloading("h1", 0)
	checking.Status = "checking"
	e.downloads(checking)
	e.downloads(downloading("h1", 0))
	stopped := downloading("h1", 0.2)
	stopped.Status = "stopped"
	e.downloads(stopped)
	if s := e.snap(t); len(s.Active) != 0 {
		t.Errorf("stopped download still active: %+v", s.Active)
	}
	e.downloads(downloading("h1", 0.2))
	if n := e.countPushes(DownloadStarted); n != 1 {
		t.Fatalf("started pushes = %d, want 1", n)
	}
	if s := e.snap(t); len(s.Active) != 1 {
		t.Errorf("active = %+v, want the restarted download", s.Active)
	}
}

func TestDownloadBaselineFinishPushes(t *testing.T) {
	e := newTestCenter(t)
	e.downloads(downloading("h1", 0.9))
	e.downloads(Download{Hash: "h1", Name: "Torrent h1", Status: "seeding", Percent: 1})
	if len(e.pushes) != 1 || e.pushes[0].cat != DownloadFinished {
		t.Fatalf("pushes = %+v, want one finished", e.pushes)
	}
	// Complete at baseline: nothing to announce.
	e2 := newTestCenter(t)
	e2.downloads(Download{Hash: "h2", Name: "Torrent h2", Status: "seeding", Percent: 1})
	e2.downloads(Download{Hash: "h2", Name: "Torrent h2", Status: "seeding", Percent: 1})
	if len(e2.pushes) != 0 || len(e2.snap(t).Recent) != 0 {
		t.Errorf("baseline seeding produced pushes %+v", e2.pushes)
	}
}

func TestStreamPauseResumeOnePush(t *testing.T) {
	e := newTestCenter(t)
	e.streams()
	s := stream("s1", "alex", "ep4", "show")
	e.streams(s)
	s.State = "paused"
	e.streams(s)
	if a := e.snap(t).Active; len(a) != 1 || a[0].Detail != "Apple TV · Paused" {
		t.Errorf("paused row = %+v", a)
	}
	s.State = "playing"
	e.streams(s)
	if len(e.pushes) != 1 || e.pushes[0].cat != StreamStarted || e.pushes[0].title != "alex is watching ep4" {
		t.Fatalf("pushes = %+v, want one stream start", e.pushes)
	}
	if len(e.snap(t).Recent) != 0 {
		t.Errorf("stream start added a Recent item")
	}
}

func TestStreamBlipSamePush(t *testing.T) {
	e := newTestCenter(t)
	e.streams()
	e.streams(stream("s1", "alex", "ep4", "show"))
	e.streams()
	if a := e.snap(t).Active; len(a) != 0 {
		t.Errorf("ended stream still active: %+v", a)
	}
	e.clock.advance(time.Minute)
	e.streams(stream("s2", "alex", "ep4", "show"))
	if len(e.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(e.pushes))
	}
}

func TestBingeOnePush(t *testing.T) {
	e := newTestCenter(t)
	e.streams()
	e.streams(stream("s1", "alex", "ep1", "show"))
	e.streams()
	e.clock.advance(2 * time.Minute)
	e.streams(stream("s2", "alex", "ep2", "show"))
	if len(e.pushes) != 1 {
		t.Fatalf("next episode after 2 min: pushes = %d, want 1", len(e.pushes))
	}
	if a := e.snap(t).Active; len(a) != 1 || a[0].ID != "st:s2" || a[0].Title != "alex is watching ep2" {
		t.Errorf("binge row = %+v, want the episode playing now", a)
	}
	e.streams()
	e.clock.advance(31 * time.Minute)
	e.streams(stream("s3", "alex", "ep3", "show"))
	if len(e.pushes) != 2 {
		t.Fatalf("next episode after 31 min: pushes = %d, want 2", len(e.pushes))
	}

	e2 := newTestCenter(t)
	e2.streams()
	e2.streams(stream("s1", "alex", "ep1", "show"))
	e2.streams()
	e2.streams(stream("s2", "sam", "ep2", "show"))
	if len(e2.pushes) != 2 {
		t.Fatalf("different user same show: pushes = %d, want 2", len(e2.pushes))
	}
}

func TestDownloadError(t *testing.T) {
	e := newTestCenter(t)
	e.downloads()
	bad := downloading("h1", 0.3)
	bad.Error = "tracker unreachable"
	e.downloads(bad)
	e.downloads(bad)
	s := e.snap(t)
	if len(s.Recent) != 1 || s.Recent[0].Category != DownloadError || s.Recent[0].ResolvedAt != nil {
		t.Fatalf("recent = %+v, want one open download error", s.Recent)
	}
	// One Active row plus the open error.
	if s.Badge != 2 {
		t.Errorf("badge = %d, want 2", s.Badge)
	}
	if n := e.countPushes(DownloadError); n != 1 {
		t.Errorf("error pushes = %d, want 1", n)
	}
	e.downloads(downloading("h1", 0.4))
	s = e.snap(t)
	if len(s.Recent) != 1 || s.Recent[0].ResolvedAt == nil {
		t.Fatalf("recent = %+v, want the error resolved", s.Recent)
	}
	if s.Badge != 1 {
		t.Errorf("badge = %d, want 1", s.Badge)
	}
}

func TestActiveHiddenWhenOff(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.downloads(downloading("h1", 0.5))
	e.streams(stream("s1", "alex", "ep4", "show"))

	if err := e.c.SetModes(ctx, map[Category]Mode{DownloadStarted: ModeOff}); err != nil {
		t.Fatal(err)
	}
	if a := e.snap(t).Active; len(a) != 2 {
		t.Errorf("downloads hidden while finished is still on: %+v", a)
	}
	if err := e.c.SetModes(ctx, map[Category]Mode{DownloadFinished: ModeOff, StreamStarted: ModeOff}); err != nil {
		t.Fatal(err)
	}
	if s := e.snap(t); len(s.Active) != 0 || s.Badge != 0 {
		t.Errorf("active = %+v badge %d, want nothing while Off", s.Active, s.Badge)
	}
	if err := e.c.SetModes(ctx, map[Category]Mode{DownloadFinished: ModeApp, StreamStarted: ModePush}); err != nil {
		t.Fatal(err)
	}
	if a := e.snap(t).Active; len(a) != 2 {
		t.Errorf("active = %+v, want both rows back", a)
	}
}

func TestReconcileResolvesStale(t *testing.T) {
	for _, tc := range []struct {
		name string
		list []Download
	}{
		{"error cleared", []Download{downloading("h1", 0.5)}},
		{"torrent removed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestCenter(t)
			e.downloads()
			bad := downloading("h1", 0.3)
			bad.Error = "tracker unreachable"
			e.downloads(bad)
			if s := e.snap(t); len(s.Recent) != 1 || s.Recent[0].ResolvedAt != nil {
				t.Fatalf("recent = %+v, want one open error", s.Recent)
			}

			// Restart: a new Center on the same store.
			e2 := attach(t, e.st)
			e2.clock.t = e.clock.t
			e2.downloads(tc.list...)
			s := e2.snap(t)
			if len(s.Recent) != 1 || s.Recent[0].ResolvedAt == nil {
				t.Fatalf("recent = %+v, want the stale error resolved", s.Recent)
			}
			if s.Badge != len(s.Active) {
				t.Errorf("badge = %d, want only the %d active rows", s.Badge, len(s.Active))
			}

			e2.downloads(bad)
			if n := e2.countPushes(DownloadError); n != 1 {
				t.Errorf("error pushes after restart = %d, want 1", n)
			}
			open := 0
			for _, it := range e2.snap(t).Recent {
				if it.Category == DownloadError && it.ResolvedAt == nil {
					open++
				}
			}
			if open != 1 {
				t.Errorf("open errors = %d, want a new one", open)
			}
		})
	}
}

func TestReconcile(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	for _, k := range []string{"a", "b", "c"} {
		e.raise(unhealthy(k))
	}
	e.raise(Occurrence{Category: DiskFull, Key: "/", Title: "Disk nearly full"})
	e.c.Reconcile(ctx, ContainerUnhealthy, []string{"b"}, func(k string) string { return k + " is healthy again" })
	titles := map[string]string{}
	for _, it := range e.snap(t).Recent {
		if it.Category == DiskFull {
			if it.ResolvedAt != nil {
				t.Errorf("other category resolved: %+v", it)
			}
			continue
		}
		titles[it.Key] = it.Title
		if (it.ResolvedAt == nil) != (it.Key == "b") {
			t.Errorf("%s resolved = %v", it.Key, it.ResolvedAt != nil)
		}
	}
	if titles["a"] != "a is healthy again" || titles["c"] != "c is healthy again" || titles["b"] != "b is unhealthy" {
		t.Errorf("titles = %v", titles)
	}
	// nil resolvedTitle keeps the old title.
	e.c.Reconcile(ctx, ContainerUnhealthy, nil, nil)
	for _, it := range e.snap(t).Recent {
		if it.Key == "b" && (it.ResolvedAt == nil || it.Title != "b is unhealthy") {
			t.Errorf("b = %+v", it)
		}
	}
}

// Autoplay: the next episode replaces the last one between two polls.
func TestAutoplayWithinOnePollOnePush(t *testing.T) {
	e := newTestCenter(t)
	e.streams()
	e.streams(stream("s1", "alex", "ep1", "show"))
	e.streams(stream("s2", "alex", "ep2", "show"))
	if len(e.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(e.pushes))
	}
	if a := e.snap(t).Active; len(a) != 1 || a[0].ID != "st:s2" {
		t.Errorf("active = %+v, want the second episode", a)
	}
}

// A torrent paused when Wargo started was already there: resuming it later
// is not a new download.
func TestPausedAtBaselineResumeNoPush(t *testing.T) {
	e := newTestCenter(t)
	paused := downloading("h1", 0.4)
	paused.Status = "stopped"
	e.downloads(paused)
	e.downloads(downloading("h1", 0.4))
	if n := e.countPushes(DownloadStarted); n != 0 {
		t.Fatalf("started pushes = %d, want 0", n)
	}
}

// After a pause in polling (categories Off), the next report is a baseline:
// what is already running is shown, not pushed.
func TestResetMakesNextReportBaseline(t *testing.T) {
	e := newTestCenter(t)
	e.downloads()
	e.downloads(downloading("h1", 0.1), downloading("h2", 0.1))
	e.streams()
	e.streams(stream("s1", "alex", "ep1", "show"))
	if len(e.pushes) != 3 {
		t.Fatalf("pushes = %d, want 3", len(e.pushes))
	}
	e.c.ResetDownloads()
	e.c.ResetStreams()
	e.clock.advance(time.Hour)
	done := Download{Hash: "h2", Name: "Torrent h2", Status: "seeding", Percent: 1}
	e.downloads(downloading("h1", 0.5), done, downloading("h3", 0.1))
	e.streams(stream("s1", "alex", "ep1", "show"), stream("s2", "sam", "film", ""))
	if len(e.pushes) != 3 {
		t.Fatalf("pushes after reset = %+v, want none new", e.pushes[3:])
	}
	if s := e.snap(t); len(s.Active) != 4 {
		t.Errorf("active = %+v, want 4 rows", s.Active)
	}
	// Later reports push again.
	e.downloads(downloading("h1", 0.5), done, downloading("h3", 0.1), downloading("h4", 0))
	e.streams(stream("s1", "alex", "ep1", "show"), stream("s2", "sam", "film", ""), stream("s3", "kim", "x", ""))
	if len(e.pushes) != 5 {
		t.Fatalf("pushes = %d, want 5", len(e.pushes))
	}
}

// A torrent added and finished between two polls is first seen complete:
// that is still a finish worth announcing. One complete at the baseline is
// not.
func TestFinishBetweenPolls(t *testing.T) {
	e := newTestCenter(t)
	e.downloads()
	done := Download{Hash: "h1", Name: "Torrent h1", Status: "seeding", Percent: 1}
	e.downloads(done)
	e.downloads(done)
	if len(e.pushes) != 1 || e.pushes[0].cat != DownloadFinished || e.pushes[0].key != "h1" {
		t.Fatalf("pushes = %+v, want one finished", e.pushes)
	}
	if s := e.snap(t); len(s.Recent) != 1 || s.Recent[0].Category != DownloadFinished || len(s.Active) != 0 {
		t.Fatalf("recent = %+v active = %+v", s.Recent, s.Active)
	}

	e2 := newTestCenter(t)
	e2.downloads(Download{Hash: "h2", Name: "Torrent h2", Status: "done", Percent: 1})
	e2.downloads(Download{Hash: "h2", Name: "Torrent h2", Status: "done", Percent: 1})
	if len(e2.pushes) != 0 || len(e2.snap(t).Recent) != 0 {
		t.Errorf("complete at baseline: pushes %+v recent %+v", e2.pushes, e2.snap(t).Recent)
	}
}

// A torrent removed and added again is a new download: it announces its
// start and finish again.
func TestReaddedTorrentNotifiesAgain(t *testing.T) {
	e := newTestCenter(t)
	e.downloads()
	done := Download{Hash: "h1", Name: "Torrent h1", Status: "seeding", Percent: 1}
	e.downloads(downloading("h1", 0.2))
	e.downloads(done)
	e.downloads()
	e.downloads(downloading("h1", 0.1))
	e.downloads(done)
	var got []Category
	for _, p := range e.pushes {
		got = append(got, p.cat)
	}
	want := []Category{DownloadStarted, DownloadFinished, DownloadStarted, DownloadFinished}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pushes = %v, want %v", got, want)
	}
	if s := e.snap(t); len(s.Recent) != 2 {
		t.Errorf("recent = %+v, want two finished records", s.Recent)
	}
}

// Checking or queued after downloading is a pause in the same download:
// its Active row stays. Stopping it takes the row out.
func TestCheckingKeepsActiveRow(t *testing.T) {
	e := newTestCenter(t)
	e.downloads()
	queued := downloading("h2", 0)
	queued.Status = "queued"
	e.downloads(downloading("h1", 0.3), queued)
	for _, status := range []string{"checking", "queued", "downloading", "checking"} {
		d := downloading("h1", 0.4)
		d.Status = status
		e.downloads(d, queued)
		if a := e.snap(t).Active; len(a) != 1 || a[0].ID != "dl:h1" {
			t.Fatalf("%s: active = %+v, want the h1 row only", status, a)
		}
	}
	stopped := downloading("h1", 0.4)
	stopped.Status = "stopped"
	e.downloads(stopped, queued)
	if a := e.snap(t).Active; len(a) != 0 {
		t.Fatalf("stopped: active = %+v, want none", a)
	}
	checking := downloading("h1", 0.4)
	checking.Status = "checking"
	e.downloads(checking, queued)
	if a := e.snap(t).Active; len(a) != 0 {
		t.Fatalf("checking after stop: active = %+v, want none", a)
	}
}

// Plex can report sessions without a session key; each still gets its own
// row and push.
func TestEmptySessionKeysStaySeparate(t *testing.T) {
	e := newTestCenter(t)
	e.streams()
	a := stream("", "alex", "ep4", "show")
	b := stream("", "sam", "film", "")
	c := stream("", "kim", "", "")
	c.Player = "Roku"
	e.streams(a, b, c)
	e.streams(a, b, c)
	if n := e.countPushes(StreamStarted); n != 3 {
		t.Fatalf("stream pushes = %d, want 3", n)
	}
	s := e.snap(t)
	if len(s.Active) != 3 {
		t.Fatalf("active = %+v, want 3 rows", s.Active)
	}
	ids := map[string]bool{}
	for _, r := range s.Active {
		ids[r.ID] = true
	}
	if len(ids) != 3 || ids["st:"] {
		t.Errorf("row ids = %v, want 3 distinct synthetic ids", ids)
	}
	e.streams(a, c)
	if s := e.snap(t); len(s.Active) != 2 {
		t.Errorf("after sam stopped: active = %+v, want 2 rows", s.Active)
	}
}

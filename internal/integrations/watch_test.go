// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/notify"
)

func TestToDownloads(t *testing.T) {
	ts := []TorrentView{
		{Hash: "a", Name: "A", Status: "downloading", PercentDone: 0.25, RateDownload: 2048, ETA: 90},
		{Hash: "b", Name: "B", Status: "check-wait", ETA: -1},
		{Hash: "c", Name: "C", Status: "checking", ETA: -2},
		{Hash: "d", Name: "D", Status: "download-wait", ETA: 31 * 24 * 3600},
		{Hash: "e", Name: "E", Status: "seed-wait", PercentDone: 1},
		{Hash: "f", Name: "F", Status: "seeding", PercentDone: 1},
		{Hash: "g", Name: "G", Status: "stopped", PercentDone: 0.5, Error: 3, ErrorString: "No data found"},
		{Hash: "h", Name: "H", Status: "stopped", PercentDone: 1},
		{Hash: "i", Name: "I", Status: "downloading", Error: 1, ErrorString: "Could not connect to tracker"},
		{Hash: "j", Name: "J", Status: "downloading", Error: 2, ErrorString: "Torrent not registered"},
	}
	got := toDownloads(ts)
	want := []notify.Download{
		{Hash: "a", Name: "A", Status: "downloading", Percent: 0.25, RateDown: 2048, ETA: 90 * time.Second},
		{Hash: "b", Name: "B", Status: "checking"},
		{Hash: "c", Name: "C", Status: "checking"},
		{Hash: "d", Name: "D", Status: "queued"},
		{Hash: "e", Name: "E", Status: "seeding", Percent: 1},
		{Hash: "f", Name: "F", Status: "seeding", Percent: 1},
		{Hash: "g", Name: "G", Status: "stopped", Percent: 0.5, Error: "No data found"},
		{Hash: "h", Name: "H", Status: "done", Percent: 1},
		{Hash: "i", Name: "I", Status: "downloading"},
		{Hash: "j", Name: "J", Status: "downloading", Error: "Torrent not registered"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d downloads, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestToStreams(t *testing.T) {
	ss := []PlexSession{
		{SessionKey: "1", RatingKey: "40", ShowKey: "10", Type: "episode", Title: "Hello, Ms. Cobel",
			GrandparentTitle: "Severance", ParentIndex: 2, Index: 4, User: "Alex", Player: "Apple TV",
			Product: "Plex for Apple TV", State: "paused", ViewOffset: 61000, Duration: 3000000},
		{SessionKey: "2", RatingKey: "50", Type: "movie", Title: "Arrival", Year: 2016, Product: "Plex Web", State: "playing"},
		{SessionKey: "3", RatingKey: "60", Type: "movie", Title: "Home Video", User: "Sam"},
		{SessionKey: "4", RatingKey: "70", Type: "track", Title: "Song", User: "Sam", Player: "Phone"},
	}
	got := toStreams(ss)
	want := []notify.Stream{
		{SessionKey: "1", User: "Alex", RatingKey: "40", ShowKey: "10", Title: "Alex is watching Severance S2 E4",
			Player: "Apple TV", State: "paused", ViewOffset: 61 * time.Second, Duration: 50 * time.Minute},
		{SessionKey: "2", RatingKey: "50", Title: "Arrival (2016)", Player: "Plex Web", State: "playing"},
		{SessionKey: "3", User: "Sam", RatingKey: "60", Title: "Sam is watching Home Video"},
		{SessionKey: "4", User: "Sam", RatingKey: "70", Title: "Sam is listening to Song", Player: "Phone"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d streams, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

type fakeWatchSource struct {
	trOK, pxOK         bool
	dlErr, stErr       error
	dlFetches, stFetch int
}

func (f *fakeWatchSource) transmissionConfigured() bool { return f.trOK }
func (f *fakeWatchSource) plexConfigured() bool         { return f.pxOK }
func (f *fakeWatchSource) fetchDownloads(context.Context) ([]notify.Download, error) {
	f.dlFetches++
	return nil, f.dlErr
}
func (f *fakeWatchSource) fetchStreams(context.Context) ([]notify.Stream, error) {
	f.stFetch++
	return nil, f.stErr
}

type fakeWatchSink struct {
	modes                 map[notify.Category]notify.Mode
	setDls, setStream     int
	resetDls, resetStream int
}

func (f *fakeWatchSink) Mode(c notify.Category) notify.Mode {
	if m, ok := f.modes[c]; ok {
		return m
	}
	return notify.ModePush
}
func (f *fakeWatchSink) SetDownloads(context.Context, []notify.Download) { f.setDls++ }
func (f *fakeWatchSink) SetStreams(context.Context, []notify.Stream)     { f.setStream++ }
func (f *fakeWatchSink) ResetDownloads()                                 { f.resetDls++ }
func (f *fakeWatchSink) ResetStreams()                                   { f.resetStream++ }

func TestWatchSkipsOnError(t *testing.T) {
	ctx := context.Background()
	src := &fakeWatchSource{trOK: true, pxOK: true, dlErr: errors.New("boom"), stErr: errors.New("boom")}
	dst := &fakeWatchSink{}
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	if src.dlFetches != 1 || src.stFetch != 1 {
		t.Fatalf("fetches = %d, %d; want 1, 1", src.dlFetches, src.stFetch)
	}
	if dst.setDls != 0 || dst.setStream != 0 {
		t.Fatalf("Set calls after a failed fetch = %d, %d; want 0, 0", dst.setDls, dst.setStream)
	}

	src.dlErr, src.stErr = nil, nil
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	if dst.setDls != 1 || dst.setStream != 1 {
		t.Fatalf("Set calls after a good fetch = %d, %d; want 1, 1", dst.setDls, dst.setStream)
	}

	// Every related category Off, or the integration not configured: no fetch.
	dst.modes = map[notify.Category]notify.Mode{
		notify.DownloadStarted: notify.ModeOff, notify.DownloadFinished: notify.ModeOff,
		notify.DownloadError: notify.ModeOff, notify.StreamStarted: notify.ModeOff,
	}
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	dst.modes[notify.DownloadError] = notify.ModeApp
	dst.modes[notify.StreamStarted] = notify.ModeApp
	src.trOK, src.pxOK = false, false
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	if src.dlFetches != 2 || src.stFetch != 2 {
		t.Fatalf("fetches while off or unconfigured = %d, %d; want 2, 2 in total", src.dlFetches, src.stFetch)
	}
	src.trOK = true
	pollDownloads(ctx, src, dst)
	if src.dlFetches != 3 || dst.setDls != 2 {
		t.Fatalf("download.error alone on: fetches %d, Set %d; want 3, 2", src.dlFetches, dst.setDls)
	}
}

// A poll skipped because every related category is Off or the integration
// is not set up makes the next report a baseline; a failed fetch does not.
func TestWatchSkipResetsBaseline(t *testing.T) {
	ctx := context.Background()
	src := &fakeWatchSource{trOK: true, pxOK: true, dlErr: errors.New("boom"), stErr: errors.New("boom")}
	dst := &fakeWatchSink{}
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	src.dlErr, src.stErr = nil, nil
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	if dst.resetDls != 0 || dst.resetStream != 0 {
		t.Fatalf("resets while polling = %d, %d; want 0, 0", dst.resetDls, dst.resetStream)
	}
	dst.modes = map[notify.Category]notify.Mode{
		notify.DownloadStarted: notify.ModeOff, notify.DownloadFinished: notify.ModeOff,
		notify.DownloadError: notify.ModeOff, notify.StreamStarted: notify.ModeOff,
	}
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	if dst.resetDls != 1 || dst.resetStream != 1 {
		t.Fatalf("resets while Off = %d, %d; want 1, 1", dst.resetDls, dst.resetStream)
	}
	dst.modes = nil
	src.trOK, src.pxOK = false, false
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	if dst.resetDls != 2 || dst.resetStream != 2 {
		t.Fatalf("resets while not set up = %d, %d; want 2, 2", dst.resetDls, dst.resetStream)
	}
}

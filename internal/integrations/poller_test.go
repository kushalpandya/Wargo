// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kushalpandya/wargo/internal/integrations/transmission/transmissiontest"
	"github.com/kushalpandya/wargo/internal/notify"
)

var pollerT0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

// subscribe attaches a browser-like subscriber to the transmission topic
// and returns a channel of the messages it receives.
func subscribe(t *testing.T, h *harness) <-chan string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(h.s.hub.ServeWS))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "?topics=" + Topic
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	msgs := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			msgs <- string(b)
		}
	}()
	t.Cleanup(func() {
		cancel()
		conn.CloseNow()
		<-done
	})
	deadline := time.Now().Add(5 * time.Second)
	for h.s.hub.Subscribers(Topic) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscriber never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return msgs
}

// stepEvery runs pollerStep once a second for n steps from pollerT0.
func stepEvery(h *harness, n int) {
	for i := 0; i < n; i++ {
		h.s.pollerStep(context.Background(), pollerT0.Add(time.Duration(i)*time.Second), false)
	}
}

func count(calls []string, method string) int {
	n := 0
	for _, c := range calls {
		if c == method {
			n++
		}
	}
	return n
}

func TestPaceWatched(t *testing.T) {
	h, ft := newWatchHarness(t)
	sink := &countingSink{}
	h.s.downloads = sink
	clock(h)
	subscribe(t, h)
	ctx := context.Background()
	for i, wantPoll := range []bool{true, false, true} {
		h.s.pollerStep(ctx, pollerT0.Add(time.Duration(i)*time.Second), false)
		got := takeCalls(ft)
		if wantPoll && (count(got, "torrent-get") != 1 || count(got, "session-stats") != 1) {
			t.Fatalf("t+%ds calls = %v; want a full poll", i, got)
		}
		if !wantPoll && len(got) != 0 {
			t.Fatalf("t+%ds calls = %v; want none", i, got)
		}
	}
	// Downloads are reported from the full snapshots.
	if sink.setDls != 2 || len(sink.lists[1]) != 1 || sink.lists[1][0].Name != "debian.iso" {
		t.Fatalf("SetDownloads = %d, %+v", sink.setDls, sink.lists)
	}
}

func TestPaceBackground(t *testing.T) {
	h, ft := newWatchHarness(t)
	h.s.downloads = &countingSink{}
	clock(h)
	stepEvery(h, 11)
	if got := ft.Methods(); !reflect.DeepEqual(got, []string{"torrent-get", "torrent-get"}) {
		t.Fatalf("calls over 10 s = %v; want two light polls", got)
	}
	if got := ft.LastFields(); !reflect.DeepEqual(got, downloadFields) {
		t.Fatalf("fields = %v; want %v", got, downloadFields)
	}
}

func allDownloadsOff() map[notify.Category]notify.Mode {
	return map[notify.Category]notify.Mode{
		notify.DownloadStarted: notify.ModeOff, notify.DownloadFinished: notify.ModeOff, notify.DownloadError: notify.ModeOff,
	}
}

func TestPaceIdle(t *testing.T) {
	h, ft := newWatchHarness(t)
	sink := &countingSink{}
	sink.modes = allDownloadsOff()
	h.s.downloads = sink
	clock(h)
	stepEvery(h, 60)
	if got := ft.Methods(); len(got) != 0 {
		t.Fatalf("calls while idle = %v; want none", got)
	}
	if sink.resetDls == 0 {
		t.Fatal("ResetDownloads never called while idle")
	}
}

func TestBackoff(t *testing.T) {
	h, ft := newWatchHarness(t)
	h.s.downloads = &countingSink{}
	clock(h)
	subscribe(t, h)
	ft.Fail("torrent-get", "boom")
	ctx := context.Background()
	var attempts []int
	for i := 0; i <= 130; i++ {
		h.s.pollerStep(ctx, pollerT0.Add(time.Duration(i)*time.Second), false)
		if len(takeCalls(ft)) > 0 {
			attempts = append(attempts, i)
		}
	}
	want := []int{0, 2, 6, 14, 30, 62, 122}
	if !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempts at %v; want %v", attempts, want)
	}
	ft.ClearFail("torrent-get")
	h.s.pollerStep(ctx, pollerT0.Add(182*time.Second), false)
	if got := takeCalls(ft); count(got, "torrent-get") != 1 {
		t.Fatalf("recovery calls = %v", got)
	}
	h.s.pollerStep(ctx, pollerT0.Add(183*time.Second), false)
	if got := takeCalls(ft); len(got) != 0 {
		t.Fatalf("+1 s after recovery = %v; want none", got)
	}
	h.s.pollerStep(ctx, pollerT0.Add(184*time.Second), false)
	if got := takeCalls(ft); count(got, "torrent-get") != 1 {
		t.Fatalf("+2 s after recovery = %v; want a poll", got)
	}
}

// A failed poll shows on the page, and no download report follows.
func TestFailedPollPublishesError(t *testing.T) {
	h, ft := newWatchHarness(t)
	sink := &countingSink{}
	h.s.downloads = sink
	clock(h)
	msgs := subscribe(t, h)
	ft.Fail("torrent-get", "boom")
	h.s.pollerStep(context.Background(), pollerT0, false)
	select {
	case m := <-msgs:
		if !strings.Contains(m, `"error"`) {
			t.Fatalf("message = %s; want an error payload", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no error published")
	}
	if sink.setDls != 0 {
		t.Fatalf("SetDownloads after a failed poll = %d; want 0", sink.setDls)
	}
}

// orderSink waits for the page to have been published before it takes
// the downloads, so a wrong order shows up as a failed wait.
type orderSink struct {
	countingSink
	published <-chan string
	mu        sync.Mutex
	order     []string
}

func (o *orderSink) SetDownloads(ctx context.Context, list []notify.Download) {
	select {
	case <-o.published:
		o.add("publish")
	case <-time.After(5 * time.Second):
	}
	o.add("downloads")
	o.countingSink.SetDownloads(ctx, list)
}

func (o *orderSink) add(s string) {
	o.mu.Lock()
	o.order = append(o.order, s)
	o.mu.Unlock()
}

func TestStepOrder(t *testing.T) {
	h, _ := newWatchHarness(t)
	clock(h)
	msgs := subscribe(t, h)
	sink := &orderSink{published: msgs}
	h.s.downloads = sink
	h.s.pollerStep(context.Background(), pollerT0, false)
	if want := []string{"publish", "downloads"}; !reflect.DeepEqual(sink.order, want) {
		t.Fatalf("order = %v; want %v", sink.order, want)
	}
}

func TestWokenPollsNow(t *testing.T) {
	h, ft := newWatchHarness(t)
	clock(h)
	subscribe(t, h)
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, false)
	takeCalls(ft)
	h.s.wakePoller()
	h.s.wakePoller() // does not block
	h.s.pollerStep(ctx, pollerT0.Add(time.Second), true)
	if got := takeCalls(ft); count(got, "torrent-get") != 1 {
		t.Fatalf("woken calls = %v; want a poll", got)
	}
}

// A woken step does not poll when nothing needs polling (no page open,
// every download category Off, auto-remove off).
func TestWokenSkipsWithoutDemand(t *testing.T) {
	h, ft := newWatchHarness(t)
	sink := &countingSink{}
	sink.modes = allDownloadsOff()
	h.s.downloads = sink
	clock(h)
	h.s.pollerStep(context.Background(), pollerT0, true)
	if got := ft.Methods(); len(got) != 0 {
		t.Fatalf("calls = %v; want none", got)
	}
	if sink.setDls != 0 {
		t.Fatalf("SetDownloads with every category Off = %d; want 0", sink.setDls)
	}
}

// Notifications fetch only the fields they use.
func TestWatchFetchesOnlyTorrents(t *testing.T) {
	h, ft := newWatchHarness(t)
	dst := &countingSink{}
	h.s.downloads = dst
	clock(h)
	h.s.pollerStep(context.Background(), pollerT0, false)
	if got := ft.Methods(); len(got) != 1 || got[0] != "torrent-get" {
		t.Fatalf("calls = %v; want one torrent-get", got)
	}
	want := map[string]bool{
		"hashString": true, "name": true, "status": true, "percentDone": true,
		"rateDownload": true, "eta": true, "error": true, "errorString": true, "addedDate": true,
	}
	fields := ft.LastFields()
	if len(fields) != len(want) {
		t.Fatalf("fields = %v; want %d", fields, len(want))
	}
	for _, f := range fields {
		if !want[f] {
			t.Fatalf("unexpected field %q in %v", f, fields)
		}
	}
	if dst.setDls != 1 || len(dst.lists[0]) != 1 || dst.lists[0][0].Name != "debian.iso" || dst.lists[0][0].Status != "downloading" {
		t.Fatalf("SetDownloads = %d, %+v", dst.setDls, dst.lists)
	}
	if got := dst.lists[0][0].Added; !got.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("Added = %v; want Transmission's addedDate", got)
	}
}

// Stats, session or free space failing says nothing about torrents.
func TestWatchIgnoresStatsFailure(t *testing.T) {
	h, ft := newWatchHarness(t)
	ft.Fail("session-stats", "stats unavailable")
	ft.Fail("session-get", "session unavailable")
	ft.Fail("free-space", "no space info")
	dst := &countingSink{}
	h.s.downloads = dst
	clock(h)
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, false)
	if dst.setDls != 1 {
		t.Fatalf("SetDownloads = %d; want 1", dst.setDls)
	}
	ft.Fail("torrent-get", "torrents unavailable")
	h.s.pollerStep(ctx, pollerT0.Add(time.Minute), false)
	if dst.setDls != 1 {
		t.Fatalf("SetDownloads after a failed torrent-get = %d; want 1", dst.setDls)
	}
}

// Pointed at another Transmission, the next report is a baseline, so the
// torrents that one already has are not announced.
func TestWatchResetsOnClientChange(t *testing.T) {
	h, _ := newWatchHarness(t)
	sink := &countingSink{}
	h.s.downloads = sink
	clock(h)
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, false)
	h.s.pollerStep(ctx, pollerT0.Add(10*time.Second), false)
	if sink.resetDls != 0 || sink.setDls != 2 {
		t.Fatalf("same Transmission: resets %d, Set %d; want 0, 2", sink.resetDls, sink.setDls)
	}
	other := transmissiontest.New("kushal", "s3cret")
	t.Cleanup(other.Close)
	h.s.mu.Lock()
	h.s.tr.URL = other.RPCURL()
	h.s.mu.Unlock()
	h.s.pollerStep(ctx, pollerT0.Add(20*time.Second), false)
	if sink.resetDls != 1 || sink.setDls != 3 {
		t.Fatalf("another Transmission: resets %d, Set %d; want 1, 3", sink.resetDls, sink.setDls)
	}
	h.s.pollerStep(ctx, pollerT0.Add(30*time.Second), false)
	if sink.resetDls != 1 {
		t.Fatalf("resets after the change = %d; want 1", sink.resetDls)
	}
}

// A step that does not poll because every category is Off or Transmission
// is not set up makes the next report a baseline; a failed poll does not.
func TestWatchSkipResetsBaseline(t *testing.T) {
	h, ft := newWatchHarness(t)
	sink := &countingSink{}
	h.s.downloads = sink
	clock(h)
	ctx := context.Background()
	ft.Fail("torrent-get", "boom")
	h.s.pollerStep(ctx, pollerT0, false)
	ft.ClearFail("torrent-get")
	h.s.pollerStep(ctx, pollerT0.Add(20*time.Second), false)
	if sink.resetDls != 0 || sink.setDls != 1 {
		t.Fatalf("while polling: resets %d, Set %d; want 0, 1", sink.resetDls, sink.setDls)
	}
	sink.modes = allDownloadsOff()
	h.s.pollerStep(ctx, pollerT0.Add(21*time.Second), false)
	if sink.resetDls != 1 {
		t.Fatalf("resets while Off = %d; want 1", sink.resetDls)
	}
	sink.modes = nil
	h.s.mu.Lock()
	h.s.tr.URL = ""
	h.s.mu.Unlock()
	h.s.pollerStep(ctx, pollerT0.Add(22*time.Second), false)
	if sink.resetDls != 2 {
		t.Fatalf("resets while not set up = %d; want 2", sink.resetDls)
	}
}

// Ticks arrive a little off the second; a tick a hair before the due time
// must poll rather than wait a whole extra second.
func TestPaceToleratesTickJitter(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	run := func(t *testing.T, h *harness, ft *transmissiontest.Fake, offs []time.Duration) []time.Duration {
		var polled []time.Duration
		for _, off := range offs {
			h.s.pollerStep(context.Background(), pollerT0.Add(off), false)
			if len(takeCalls(ft)) > 0 {
				polled = append(polled, off)
			}
		}
		return polled
	}
	t.Run("watched 2 s", func(t *testing.T) {
		h, ft := newWatchHarness(t)
		h.s.downloads = &countingSink{}
		clock(h)
		subscribe(t, h)
		got := run(t, h, ft, []time.Duration{0, ms(1001), ms(1999), ms(3000), ms(3998)})
		if want := []time.Duration{0, ms(1999), ms(3998)}; !reflect.DeepEqual(got, want) {
			t.Fatalf("polled at %v; want %v", got, want)
		}
	})
	t.Run("background 10 s", func(t *testing.T) {
		h, ft := newWatchHarness(t)
		h.s.downloads = &countingSink{}
		clock(h)
		got := run(t, h, ft, []time.Duration{0, ms(5000), ms(9999), ms(15000), ms(19998)})
		if want := []time.Duration{0, ms(9999), ms(19998)}; !reflect.DeepEqual(got, want) {
			t.Fatalf("polled at %v; want %v", got, want)
		}
	})
}

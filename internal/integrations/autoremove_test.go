// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/integrations/transmission/transmissiontest"
	"github.com/kushalpandya/wargo/internal/notify"
)

func TestFinished(t *testing.T) {
	cases := []struct {
		status string
		pct    float64
		err    int
		want   bool
	}{
		{"seeding", 1, 0, true},
		{"seed-wait", 1, 0, true},
		{"stopped", 1, 0, true},
		{"stopped", 0.99, 0, false},
		{"checking", 1, 0, false},
		{"check-wait", 1, 0, false},
		{"seeding", 1, 3, false},
		{"seeding", 1, 2, true},
		{"downloading", 0.5, 0, false},
		{"downloading", 1, 0, false},
		{"download-wait", 1, 0, false},
		{"unknown", 1, 0, false},
		{"", 1, 0, false},
	}
	for _, c := range cases {
		got := finished(TorrentView{Status: c.status, PercentDone: c.pct, Error: c.err})
		if got != c.want {
			t.Errorf("finished(%s, %v, error %d) = %v; want %v", c.status, c.pct, c.err, got, c.want)
		}
	}
}

// removeHarness is a harness with auto-remove turned on and a clock shared
// with the poller steps.
func removeHarness(t *testing.T, on bool) (*harness, *transmissiontest.Fake) {
	t.Helper()
	h, ft := newWatchHarness(t)
	h.s.downloads = &countingSink{}
	now := pollerT0
	h.s.now = func() time.Time { return now }
	if on {
		if w, _ := h.do(t, "PUT", "/api/transmission/settings", map[string]any{"removeFinished": true}); w.Code != 200 {
			t.Fatalf("turn on: %d %s", w.Code, w.Body)
		}
		// drain the wake the PUT queued
		select {
		case <-h.s.wake:
		default:
		}
	}
	return h, ft
}

func auditCount(h *harness, where string) int {
	var n int
	h.st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE ` + where).Scan(&n)
	return n
}

func TestAutoRemoveOff(t *testing.T) {
	h, ft := removeHarness(t, false)
	ft.AddTorrent("done.iso", 6, 1)
	h.s.pollerStep(context.Background(), pollerT0, true)
	if got := ft.Methods(); count(got, "torrent-remove") != 0 || count(got, "torrent-get") == 0 {
		t.Fatalf("calls = %v; want a poll and no removal", got)
	}
}

func TestAutoRemoveOn(t *testing.T) {
	h, ft := removeHarness(t, true)
	a := ft.AddTorrent("a.iso", 4, 0.5)
	b := ft.AddTorrent("b.iso", 6, 1)
	c := ft.AddTorrent("c.iso", 0, 1)
	h.s.pollerStep(context.Background(), pollerT0, true)
	if n := count(ft.Methods(), "torrent-remove"); n != 1 {
		t.Fatalf("torrent-remove calls = %d; want 1", n)
	}
	for _, id := range []int64{b.ID, c.ID} {
		got, _ := ft.Get(id)
		if !got.Removed || got.DataDeleted {
			t.Fatalf("torrent %d: removed %v, data deleted %v; want removed, data kept", id, got.Removed, got.DataDeleted)
		}
	}
	if got, _ := ft.Get(a.ID); got.Removed {
		t.Fatal("unfinished torrent was removed")
	}
	if n := auditCount(h, `action = 'transmission.remove' AND details LIKE '%"automatic":true%' AND details LIKE '%"deleteData":false%' AND source = 'system'`); n != 2 {
		t.Fatalf("audit rows = %d; want 2", n)
	}
}

type removeOrderSink struct {
	countingSink
	ft     *transmissiontest.Fake
	atSets []int // removals made before each SetDownloads
	pct    []float64
}

func (o *removeOrderSink) SetDownloads(ctx context.Context, list []notify.Download) {
	o.atSets = append(o.atSets, count(o.ft.Methods(), "torrent-remove"))
	for _, d := range list {
		if d.Name == "going.iso" {
			o.pct = append(o.pct, d.Percent)
		}
	}
	o.countingSink.SetDownloads(ctx, list)
}

func TestAutoRemoveAfterNotify(t *testing.T) {
	h, ft := removeHarness(t, true)
	g := ft.AddTorrent("going.iso", 4, 0.5)
	sink := &removeOrderSink{ft: ft}
	h.s.downloads = sink
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, true)
	ft.Update(g.ID, func(t *transmissiontest.Torrent) { t.Status, t.PercentDone = 6, 1 })
	h.s.pollerStep(ctx, pollerT0.Add(10*time.Second), false)
	if len(sink.pct) != 2 || sink.pct[1] != 1 {
		t.Fatalf("notifications saw %v; want 0.5 then 1", sink.pct)
	}
	if sink.atSets[1] != 0 {
		t.Fatal("torrent removed before the notifications saw it finish")
	}
	if got, _ := ft.Get(g.ID); !got.Removed {
		t.Fatal("finished torrent was not removed after the notifications")
	}
}

func TestAutoRemoveRetries(t *testing.T) {
	h, ft := removeHarness(t, true)
	d := ft.AddTorrent("done.iso", 6, 1)
	ft.Fail("torrent-remove", "x")
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, true)
	if got, _ := ft.Get(d.ID); got.Removed {
		t.Fatal("removed despite the failure")
	}
	failed := `action = 'transmission.autoremove' AND result != 'ok'`
	if n := auditCount(h, failed); n != 1 {
		t.Fatalf("failure rows = %d; want 1", n)
	}
	h.s.pollerStep(ctx, pollerT0.Add(20*time.Second), true)
	if n := auditCount(h, failed); n != 1 {
		t.Fatalf("failure rows after a second failure = %d; want still 1", n)
	}
	if n := auditCount(h, `action = 'transmission.remove'`); n != 0 {
		t.Fatalf("removal rows while failing = %d; want 0", n)
	}
	ft.ClearFail("torrent-remove")
	h.s.pollerStep(ctx, pollerT0.Add(40*time.Second), true)
	if got, _ := ft.Get(d.ID); !got.Removed || got.DataDeleted {
		t.Fatalf("after clearing: removed %v, data deleted %v", got.Removed, got.DataDeleted)
	}
	if n := auditCount(h, `action = 'transmission.remove'`); n != 1 {
		t.Fatalf("removal rows = %d; want 1", n)
	}
}

func TestAutoRemoveShortList(t *testing.T) {
	h, ft := removeHarness(t, true)
	a := ft.AddTorrent("a.iso", 4, 0.5)
	b := ft.AddTorrent("b.iso", 4, 0.7)
	sink := &countingSink{}
	h.s.downloads = sink
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, true)
	resets := sink.resetDls
	// Transmission answers with a partial list: only a.iso, unfinished.
	for _, tt := range knownIDs(ft) {
		if tt != a.ID {
			ft.Update(tt, func(t *transmissiontest.Torrent) { t.Removed = true })
		}
	}
	h.s.pollerStep(ctx, pollerT0.Add(10*time.Second), false)
	for _, tt := range knownIDs(ft) {
		if tt != a.ID {
			ft.Update(tt, func(t *transmissiontest.Torrent) { t.Removed = false })
		}
	}
	if n := count(ft.Methods(), "torrent-remove"); n != 0 {
		t.Fatalf("torrent-remove calls = %d; want 0", n)
	}
	if sink.resetDls != resets {
		t.Fatal("a short list reset the notifications")
	}
	for _, id := range []int64{a.ID, b.ID} {
		if got, _ := ft.Get(id); got.Removed {
			t.Fatalf("torrent %d was removed", id)
		}
	}
}

func TestSettingsEndpoint(t *testing.T) {
	h, ft := removeHarness(t, false)
	_ = ft
	if _, out := h.do(t, "GET", "/api/transmission/settings", nil); out["removeFinished"] != false {
		t.Fatalf("default = %v; want false", out)
	}
	if w, out := h.do(t, "PUT", "/api/transmission/settings", map[string]any{"removeFinished": true}); w.Code != 200 || out["removeFinished"] != true {
		t.Fatalf("PUT: %d %v", w.Code, out)
	}
	if _, out := h.do(t, "GET", "/api/transmission/settings", nil); out["removeFinished"] != true {
		t.Fatalf("after PUT = %v; want true", out)
	}
	_, name := h.s.Transmission().Capabilities(context.Background())
	if m, _ := name.(map[string]any); m["removeFinished"] != true {
		t.Fatalf("capabilities = %v; want removeFinished true", name)
	}
	if w, _ := h.do(t, "PUT", "/api/transmission/settings", map[string]any{}); w.Code != 400 {
		t.Fatalf("PUT {} = %d; want 400", w.Code)
	}
	if n := auditCount(h, `action = 'transmission.settings' AND details LIKE '%"removeFinished":true%'`); n != 1 {
		t.Fatalf("audit rows = %d; want 1", n)
	}
	// The setting survives a restart.
	s2 := New(config.Config{}, h.st, nil, h.s.audit)
	if !s2.removeFinished() {
		t.Fatal("setting not loaded after restart")
	}
}

func TestTurnOnRemovesSoon(t *testing.T) {
	h, ft := removeHarness(t, false)
	sink := &countingSink{}
	sink.modes = allDownloadsOff()
	h.s.downloads = sink
	d := ft.AddTorrent("done.iso", 6, 1)
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, false)
	if len(ft.Methods()) != 0 {
		t.Fatalf("idle poller called %v", ft.Methods())
	}
	if w, _ := h.do(t, "PUT", "/api/transmission/settings", map[string]any{"removeFinished": true}); w.Code != 200 {
		t.Fatalf("PUT: %d", w.Code)
	}
	select {
	case <-h.s.wake:
	default:
		t.Fatal("turning on did not wake the poller")
	}
	h.s.pollerStep(ctx, pollerT0.Add(time.Second), true)
	if got, _ := ft.Get(d.ID); !got.Removed {
		t.Fatal("finished torrent not removed after turning on")
	}
}

// A status Wargo does not know is never taken as finished, even at 100%.
func TestAutoRemoveSkipsUnknownStatus(t *testing.T) {
	h, ft := removeHarness(t, true)
	u := ft.AddTorrent("odd.iso", 9, 1)
	h.s.pollerStep(context.Background(), pollerT0, true)
	if n := count(ft.Methods(), "torrent-remove"); n != 0 {
		t.Fatalf("torrent-remove calls = %d; want 0", n)
	}
	if got, _ := ft.Get(u.ID); got.Removed {
		t.Fatal("torrent with an unknown status was removed")
	}
}

// A Transmission that answers torrent-remove with success but keeps the
// torrent is asked once, not on every poll, until the memo expires.
func TestAutoRemoveMemo(t *testing.T) {
	h, ft := removeHarness(t, true)
	now := clock(h)
	base := *now
	ft.IgnoreRemoves(true)
	ft.AddTorrent("stuck.iso", 6, 1)
	ctx := context.Background()
	removes := func() int { return count(ft.Methods(), "torrent-remove") }
	rows := func() int { return auditCount(h, `action = 'transmission.remove'`) }
	h.s.pollerStep(ctx, pollerT0, true)
	for i := 1; i <= 5; i++ {
		*now = base.Add(time.Duration(i) * 10 * time.Second)
		h.s.pollerStep(ctx, pollerT0.Add(time.Duration(i)*10*time.Second), false)
	}
	if removes() != 1 || rows() != 1 {
		t.Fatalf("within a minute: %d torrent-remove, %d audit rows; want 1, 1", removes(), rows())
	}
	*now = base.Add(removedMemo)
	h.s.pollerStep(ctx, pollerT0.Add(60*time.Second), false)
	if removes() != 2 || rows() != 2 {
		t.Fatalf("after the memo expired: %d torrent-remove, %d audit rows; want 2, 2", removes(), rows())
	}
}

// A removed hash is forgotten once Transmission stops listing it, so the
// same torrent added again is removed again when it finishes.
func TestAutoRemoveReadded(t *testing.T) {
	h, ft := removeHarness(t, true)
	now := clock(h)
	base := *now
	d := ft.AddTorrent("again.iso", 6, 1)
	ctx := context.Background()
	h.s.pollerStep(ctx, pollerT0, true)
	if got, _ := ft.Get(d.ID); !got.Removed {
		t.Fatal("finished torrent not removed")
	}
	*now = base.Add(10 * time.Second)
	h.s.pollerStep(ctx, pollerT0.Add(10*time.Second), false) // absent: forgotten
	ft.Update(d.ID, func(t *transmissiontest.Torrent) { t.Removed = false })
	*now = base.Add(20 * time.Second)
	h.s.pollerStep(ctx, pollerT0.Add(20*time.Second), false)
	if n := count(ft.Methods(), "torrent-remove"); n != 2 {
		t.Fatalf("torrent-remove calls = %d; want 2", n)
	}
	if got, _ := ft.Get(d.ID); !got.Removed {
		t.Fatal("re-added torrent not removed again")
	}
}

// The real loop, with the page open and wakes coming in, asks a
// Transmission that keeps a "removed" torrent to remove it once.
func TestRunPollerRemovesOnce(t *testing.T) {
	h, ft := removeHarness(t, true)
	h.s.now = time.Now
	ft.IgnoreRemoves(true)
	ft.AddTorrent("stuck.iso", 6, 1)
	msgs := subscribe(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Keep reading the page's messages so publishing never waits on them.
	go func() {
		for {
			select {
			case <-msgs:
			case <-ctx.Done():
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.s.runPoller(ctx)
	}()
	deadline := time.Now().Add(4500 * time.Millisecond)
	for time.Now().Before(deadline) {
		h.s.wakePoller()
		time.Sleep(250 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("runPoller did not stop; torrent-remove calls so far = %d", count(ft.Methods(), "torrent-remove"))
	}
	calls := ft.Methods()
	if n := count(calls, "torrent-remove"); n != 1 {
		t.Fatalf("torrent-remove calls = %d; want 1", n)
	}
	if n := auditCount(h, `action = 'transmission.remove'`); n != 1 {
		t.Fatalf("audit rows = %d; want 1", n)
	}
	if n := count(calls, "torrent-get"); n < 3 {
		t.Fatalf("torrent-get calls = %d; want the loop to keep polling", n)
	}
}

// knownIDs lists the ids of the fake's torrents (they are numbered from 0).
func knownIDs(ft *transmissiontest.Fake) []int64 {
	var ids []int64
	for id := int64(0); id < 50; id++ {
		if _, ok := ft.Get(id); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestTurnOffStops(t *testing.T) {
	h, ft := removeHarness(t, true)
	d := ft.AddTorrent("done.iso", 6, 1)
	h.do(t, "PUT", "/api/transmission/settings", map[string]any{"removeFinished": false})
	h.s.pollerStep(context.Background(), pollerT0, true)
	if got, _ := ft.Get(d.ID); got.Removed || strings.Contains(strings.Join(ft.Methods(), ","), "torrent-remove") {
		t.Fatal("removed while off")
	}
}

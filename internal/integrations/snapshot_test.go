// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/integrations/transmission/transmissiontest"
)

// clock makes h.s.now controllable.
func clock(h *harness) *time.Time {
	now := time.Unix(1800000000, 0)
	h.s.now = func() time.Time { return now }
	return &now
}

var seenCalls = map[*transmissiontest.Fake]int{}

// takeCalls returns the methods the fake recorded since the last call.
func takeCalls(ft *transmissiontest.Fake) []string {
	all := ft.Methods()
	got := all[seenCalls[ft]:]
	seenCalls[ft] = len(all)
	return got
}

func TestPollFullCalls(t *testing.T) {
	h, ft := newWatchHarness(t)
	now := clock(h)
	ctx := context.Background()
	full := []string{"torrent-get", "session-stats", "session-get", "free-space"}
	if _, err := h.s.poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := takeCalls(ft); !reflect.DeepEqual(got, full) {
		t.Fatalf("first = %v; want %v", got, full)
	}
	*now = now.Add(30 * time.Second)
	if _, err := h.s.poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := takeCalls(ft); !reflect.DeepEqual(got, full[:2]) {
		t.Fatalf("+30s = %v; want %v", got, full[:2])
	}
	*now = now.Add(31 * time.Second)
	if _, err := h.s.poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := takeCalls(ft); !reflect.DeepEqual(got, full) {
		t.Fatalf("+61s = %v; want %v", got, full)
	}
}

func TestPollLightCalls(t *testing.T) {
	h, ft := newWatchHarness(t)
	snap, err := h.s.poll(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := ft.Methods(); !reflect.DeepEqual(got, []string{"torrent-get"}) {
		t.Fatalf("calls = %v", got)
	}
	if got := ft.LastFields(); !reflect.DeepEqual(got, downloadFields) {
		t.Fatalf("fields = %v; want %v", got, downloadFields)
	}
	if snap.full || len(snap.torrents) != 1 || h.s.latest() != snap {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestPollSlowDataFailure(t *testing.T) {
	h, ft := newWatchHarness(t)
	ft.Fail("free-space", "x")
	snap, err := h.s.poll(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.payload().Stats.FreeSpace != nil {
		t.Fatal("FreeSpace set despite failure")
	}
	h2, _ := newWatchHarness(t)
	snap, err = h2.s.poll(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.payload().Stats.FreeSpace == nil {
		t.Fatal("FreeSpace missing on success")
	}
}

func TestPagePayloadReuse(t *testing.T) {
	h, ft := newWatchHarness(t)
	now := clock(h)
	ctx := context.Background()
	if _, err := h.s.poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	takeCalls(ft)
	*now = now.Add(2 * time.Second)
	if _, err := h.s.pagePayload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := takeCalls(ft); len(got) != 0 {
		t.Fatalf("+2s calls = %v", got)
	}
	*now = now.Add(100 * time.Millisecond)
	if _, err := h.s.pagePayload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := takeCalls(ft); !reflect.DeepEqual(got, []string{"torrent-get", "session-stats"}) {
		t.Fatalf("+2.1s calls = %v", got)
	}
	// A light snapshot does not serve a page.
	if _, err := h.s.poll(ctx, false); err != nil {
		t.Fatal(err)
	}
	takeCalls(ft)
	if _, err := h.s.pagePayload(ctx); err != nil {
		t.Fatal(err)
	}
	if got := takeCalls(ft); len(got) == 0 || got[0] != "torrent-get" {
		t.Fatalf("after light = %v; want a full poll", got)
	}
}

func TestPagePayloadShared(t *testing.T) {
	h, ft := newWatchHarness(t)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.s.pagePayload(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	n := 0
	for _, m := range ft.Methods() {
		if m == "torrent-get" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("torrent-get called %d times; want 1", n)
	}
}

// A caller that gives up does not fail the others sharing its poll.
func TestPagePayloadLeaderCanceled(t *testing.T) {
	h, _ := newWatchHarness(t)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h.s.pagePayload(gone)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := h.s.pagePayload(context.Background())
			if err != nil || len(p.Torrents) != 1 {
				t.Errorf("follower = %+v, %v; want a good payload", p, err)
			}
		}()
	}
	wg.Wait()
}

func TestPagePayloadClientChange(t *testing.T) {
	h, _ := newWatchHarness(t)
	ctx := context.Background()
	if _, err := h.s.poll(ctx, true); err != nil {
		t.Fatal(err)
	}
	ft2 := transmissiontest.New("kushal", "s3cret")
	t.Cleanup(ft2.Close)
	ft2.AddTorrent("ubuntu.iso", 4, 0.1)
	h.s.mu.Lock()
	h.s.tr.URL = ft2.RPCURL()
	h.s.mu.Unlock()
	p, err := h.s.pagePayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Torrents) != 1 || p.Torrents[0].Name != "ubuntu.iso" {
		t.Fatalf("torrents = %+v", p.Torrents)
	}
	if got := ft2.Methods(); !reflect.DeepEqual(got, []string{"torrent-get", "session-stats", "session-get", "free-space"}) {
		t.Fatalf("calls = %v", got)
	}
}

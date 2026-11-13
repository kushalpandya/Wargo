// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/store"
)

// fakeClock is a settable clock for Center.now.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

type pushed struct {
	cat                    Category
	key, title, body, link string
}

type testEnv struct {
	c      *Center
	st     *store.Store
	clock  *fakeClock
	pushes []pushed
}

func newTestCenter(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return attach(t, st)
}

// attach builds a Center on st with a fake clock and a recording push hook.
func attach(t *testing.T, st *store.Store) *testEnv {
	t.Helper()
	c, err := New(st, events.NewHub(), testIdleCutoff)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{c: c, st: st, clock: &fakeClock{t: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}}
	c.now = e.clock.now
	c.started = e.clock.t
	c.push = func(_ context.Context, cat Category, key, title, body, link string) {
		e.pushes = append(e.pushes, pushed{cat, key, title, body, link})
	}
	return e
}

func (e *testEnv) snap(t *testing.T) Snapshot {
	t.Helper()
	s, err := e.c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *testEnv) raise(o Occurrence) bool {
	e.clock.advance(time.Second)
	return e.c.Raise(context.Background(), o)
}

func unhealthy(name string) Occurrence {
	return Occurrence{Category: ContainerUnhealthy, Key: name, Title: name + " is unhealthy", Link: "/containers/" + name}
}

func TestRaiseOncePerOccurrence(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	if !e.raise(unhealthy("gluetun")) {
		t.Fatal("first raise did not push")
	}
	if e.raise(unhealthy("gluetun")) {
		t.Fatal("second raise of the same occurrence pushed")
	}
	if got := len(e.snap(t).Recent); got != 1 {
		t.Fatalf("recent: got %d items, want 1", got)
	}
	if !e.c.Resolve(ctx, ContainerUnhealthy, "gluetun", "gluetun is healthy again") {
		t.Fatal("resolve of an open occurrence reported nothing resolved")
	}
	if e.c.Resolve(ctx, ContainerUnhealthy, "gluetun", "gluetun is healthy again") {
		t.Fatal("resolve with nothing open reported a resolve")
	}
	if !e.raise(unhealthy("gluetun")) {
		t.Fatal("raise after resolve did not push")
	}
	s := e.snap(t)
	if len(s.Recent) != 2 {
		t.Fatalf("recent: got %d items, want 2", len(s.Recent))
	}
	if s.Recent[0].ResolvedAt != nil || s.Recent[1].ResolvedAt == nil {
		t.Fatalf("want newest unresolved first, got %+v", s.Recent)
	}
	if len(e.pushes) != 2 || e.pushes[0].key != "gluetun" || e.pushes[0].cat != ContainerUnhealthy {
		t.Fatalf("pushes: %+v", e.pushes)
	}
}

func TestResolvedLeavesBadge(t *testing.T) {
	e := newTestCenter(t)
	e.raise(unhealthy("gluetun"))
	if b := e.snap(t).Badge; b != 1 {
		t.Fatalf("badge after raise: %d", b)
	}
	e.clock.advance(time.Minute)
	e.c.Resolve(context.Background(), ContainerUnhealthy, "gluetun", "gluetun is healthy again")
	s := e.snap(t)
	if s.Badge != 0 {
		t.Fatalf("badge after resolve: %d", s.Badge)
	}
	if len(s.Recent) != 1 {
		t.Fatalf("recent: %+v", s.Recent)
	}
	it := s.Recent[0]
	if it.ResolvedAt == nil || !it.ResolvedAt.Equal(e.clock.t) || it.Title != "gluetun is healthy again" {
		t.Fatalf("resolved item: %+v", it)
	}
}

func TestClearAndClearAll(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.c.live.downloads = []Active{{ID: "dl:abc", Category: DownloadStarted, Title: "ubuntu.iso", StartedAt: e.clock.t}}
	e.raise(unhealthy("a"))
	e.raise(unhealthy("b"))
	e.raise(unhealthy("c"))
	s := e.snap(t)
	if len(s.Recent) != 3 || s.Badge != 4 {
		t.Fatalf("before clear: %d recent, badge %d", len(s.Recent), s.Badge)
	}
	if err := e.c.Clear(ctx, s.Recent[0].ID); err != nil {
		t.Fatal(err)
	}
	s = e.snap(t)
	if len(s.Recent) != 2 || s.Recent[0].Key != "b" || s.Badge != 3 {
		t.Fatalf("after clear one: %+v badge %d", s.Recent, s.Badge)
	}
	if err := e.c.Clear(ctx, "nope"); err == nil {
		t.Fatal("clearing an unknown id succeeded")
	}
	if err := e.c.ClearAll(ctx); err != nil {
		t.Fatal(err)
	}
	s = e.snap(t)
	if len(s.Recent) != 0 || len(s.Active) != 1 || s.Badge != 1 {
		t.Fatalf("after clear all: %d recent, %d active, badge %d", len(s.Recent), len(s.Active), s.Badge)
	}
	// "a" is cleared but still unhealthy: raising it again stays hidden.
	if e.raise(unhealthy("a")) {
		t.Fatal("cleared, unresolved problem pushed again")
	}
	if got := len(e.snap(t).Recent); got != 0 {
		t.Fatalf("cleared problem came back: %d recent", got)
	}
}

// Move resolves the old key's open item and opens one under the new key
// without a push; one the owner cleared stays cleared under the new key,
// and is still open (raising it again does nothing).
func TestMoveKeepsCleared(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.raise(unhealthy("a"))
	e.raise(unhealthy("b"))
	pushes := len(e.pushes)
	if !e.c.Move(ctx, "a", "a was renamed to c", unhealthy("c")) {
		t.Fatal("Move of an open item = false")
	}
	if e.c.Move(ctx, "zz", "", unhealthy("zz2")) {
		t.Fatal("Move without an open item = true")
	}
	s := e.snap(t)
	keys := map[string]bool{}
	for _, it := range s.Recent {
		keys[it.Key] = it.ResolvedAt == nil
	}
	if len(keys) != 3 || !keys["c"] || keys["a"] || !keys["b"] || len(e.pushes) != pushes {
		t.Fatalf("after move: recent %+v pushes %d", s.Recent, len(e.pushes))
	}
	for _, it := range s.Recent {
		if it.Key == "b" {
			if err := e.c.Clear(ctx, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.c.Move(ctx, "b", "b was renamed to d", unhealthy("d"))
	s = e.snap(t)
	for _, it := range s.Recent {
		if it.Key == "d" || it.Key == "b" {
			t.Fatalf("cleared item shows after the move: %+v", it)
		}
	}
	if s.Badge != 1 {
		t.Fatalf("badge = %d, want 1 (c)", s.Badge)
	}
	if e.raise(unhealthy("d")) || len(e.snap(t).Recent) != 2 {
		t.Fatalf("moved cleared item is not open: raised again")
	}
}

func TestRetention(t *testing.T) {
	e := newTestCenter(t)
	for i := range 120 {
		e.raise(Occurrence{Category: DownloadFinished, Key: fmt.Sprint(i), Title: "Download finished"})
	}
	s := e.snap(t)
	if len(s.Recent) != 100 || s.Recent[0].Key != "119" || s.Recent[99].Key != "20" {
		t.Fatalf("got %d items, first %q last %q", len(s.Recent), s.Recent[0].Key, s.Recent[len(s.Recent)-1].Key)
	}
	var rows int
	e.st.DB.QueryRow(`SELECT COUNT(*) FROM notify_items`).Scan(&rows)
	if rows != 100 {
		t.Fatalf("stored rows: %d", rows)
	}

	e = newTestCenter(t)
	e.raise(Occurrence{Category: DownloadFinished, Key: "old", Title: "Download finished"})
	e.clock.advance(8 * 24 * time.Hour)
	if got := len(e.snap(t).Recent); got != 0 {
		t.Fatalf("8 day old item still listed: %d", got)
	}
	e.raise(Occurrence{Category: DownloadFinished, Key: "new", Title: "Download finished"})
	s = e.snap(t)
	if len(s.Recent) != 1 || s.Recent[0].Key != "new" {
		t.Fatalf("recent: %+v", s.Recent)
	}
	e.st.DB.QueryRow(`SELECT COUNT(*) FROM notify_items`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("stored rows after expiry: %d", rows)
	}
}

func TestOffHidesCategory(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.raise(unhealthy("gluetun"))
	e.raise(Occurrence{Category: DownloadFinished, Key: "h1", Title: "Download finished"})
	if err := e.c.SetModes(ctx, map[Category]Mode{ContainerUnhealthy: ModeOff}); err != nil {
		t.Fatal(err)
	}
	s := e.snap(t)
	for _, it := range s.Recent {
		if it.Category == ContainerUnhealthy {
			t.Fatalf("off category listed: %+v", it)
		}
	}
	if len(s.Recent) != 1 || s.Badge != 1 {
		t.Fatalf("off: %d recent, badge %d", len(s.Recent), s.Badge)
	}
	if e.c.Mode(ContainerUnhealthy) != ModeOff || s.Settings[ContainerUnhealthy] != ModeOff {
		t.Fatal("mode not reported as off")
	}
	if err := e.c.SetModes(ctx, map[Category]Mode{ContainerUnhealthy: ModePush}); err != nil {
		t.Fatal(err)
	}
	s = e.snap(t)
	if len(s.Recent) != 2 || s.Badge != 2 {
		t.Fatalf("back on: %d recent, badge %d", len(s.Recent), s.Badge)
	}
}

func TestModeAppDoesNotPush(t *testing.T) {
	e := newTestCenter(t)
	e.c.SetModes(context.Background(), map[Category]Mode{ContainerFailed: ModeApp})
	if e.raise(Occurrence{Category: ContainerFailed, Key: "x", Title: "x failed to start"}) || len(e.pushes) != 0 {
		t.Fatal("app mode pushed")
	}
	if e.raise(Occurrence{Category: ContainerUnhealthy, Key: "x", Title: "x", Baseline: true}) || len(e.pushes) != 0 {
		t.Fatal("baseline pushed")
	}
	if got := len(e.snap(t).Recent); got != 2 {
		t.Fatalf("recent: %d", got)
	}
}

func TestLiveOccurrencePushesWithoutRecent(t *testing.T) {
	e := newTestCenter(t)
	live := Occurrence{Category: StreamStarted, Key: "s1", Title: "alice is watching Dune", Link: "/media", Live: true}
	if !e.raise(live) {
		t.Fatal("live occurrence did not push")
	}
	if len(e.pushes) != 1 || e.pushes[0] != (pushed{StreamStarted, "s1", "alice is watching Dune", "", "/media"}) {
		t.Fatalf("pushes: %+v", e.pushes)
	}
	// No dedupe for live occurrences: the caller decides what is new.
	live.Baseline = true
	if e.raise(live) {
		t.Fatal("baseline live occurrence pushed")
	}
	e.c.SetModes(context.Background(), map[Category]Mode{StreamStarted: ModeApp})
	live.Baseline = false
	if e.raise(live) {
		t.Fatal("live occurrence pushed in app mode")
	}
	s := e.snap(t)
	if len(s.Recent) != 0 || s.Badge != 0 || len(e.pushes) != 1 {
		t.Fatalf("live left a trace: %d recent, badge %d, %d pushes", len(s.Recent), s.Badge, len(e.pushes))
	}
	var rows int
	e.st.DB.QueryRow(`SELECT COUNT(*) FROM notify_items`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("live occurrence stored %d rows", rows)
	}
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	e := newTestCenter(t)
	mux := http.NewServeMux()
	e.c.Register(mux)

	rec := do(t, mux, "GET", "/api/notifications/settings", "")
	var got map[Category]Mode
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("GET settings: %d %s", rec.Code, rec.Body)
	}
	if len(got) != len(Categories) {
		t.Fatalf("settings: %v", got)
	}
	for _, cat := range Categories {
		if got[cat] != ModePush || e.c.Mode(cat) != ModePush {
			t.Fatalf("%s defaults to %q", cat, got[cat])
		}
	}

	for _, body := range []string{`{"bogus":"app"}`, `{"stream.started":"loud"}`, `nope`} {
		if rec := do(t, mux, "PUT", "/api/notifications/settings", body); rec.Code != 400 {
			t.Fatalf("PUT %s: got %d", body, rec.Code)
		}
	}
	if e.c.Mode(StreamStarted) != ModePush {
		t.Fatal("rejected PUT changed settings")
	}

	rec = do(t, mux, "PUT", "/api/notifications/settings", `{"stream.started":"app"}`)
	if rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got[StreamStarted] != ModeApp || got[DownloadStarted] != ModePush {
		t.Fatalf("PUT response: %v", got)
	}

	again := attach(t, e.st)
	if again.c.Mode(StreamStarted) != ModeApp || again.c.Mode(DiskFull) != ModePush {
		t.Fatal("settings did not persist across New")
	}
}

func TestHTTPSnapshotAndClear(t *testing.T) {
	e := newTestCenter(t)
	mux := http.NewServeMux()
	e.c.Register(mux)

	rec := do(t, mux, "GET", "/api/notifications", "")
	if rec.Code != 200 {
		t.Fatalf("GET: %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if string(raw["active"]) != "[]" || string(raw["recent"]) != "[]" || string(raw["badge"]) != "0" || raw["settings"] == nil {
		t.Fatalf("empty snapshot: %s", rec.Body)
	}

	e.raise(unhealthy("a"))
	e.raise(unhealthy("b"))
	s := e.snap(t)
	rec = do(t, mux, "GET", "/api/notifications", "")
	var items struct {
		Recent []map[string]any `json:"recent"`
	}
	json.Unmarshal(rec.Body.Bytes(), &items)
	for _, k := range []string{"id", "category", "key", "title", "body", "link", "createdAt", "resolvedAt", "clearedAt"} {
		if _, ok := items.Recent[0][k]; !ok {
			t.Fatalf("item JSON lacks %q: %s", k, rec.Body)
		}
	}

	for _, body := range []string{`{}`, `{"id":"missing"}`} {
		if rec := do(t, mux, "POST", "/api/notifications/clear", body); rec.Code/100 != 4 {
			t.Fatalf("clear %s: %d", body, rec.Code)
		}
	}
	if rec := do(t, mux, "POST", "/api/notifications/clear", `{"id":"`+s.Recent[0].ID+`"}`); rec.Code != 200 {
		t.Fatalf("clear one: %d %s", rec.Code, rec.Body)
	}
	if got := len(e.snap(t).Recent); got != 1 {
		t.Fatalf("after clear one: %d", got)
	}
	if rec := do(t, mux, "POST", "/api/notifications/clear", `{"all":true}`); rec.Code != 200 {
		t.Fatalf("clear all: %d", rec.Code)
	}
	if got := len(e.snap(t).Recent); got != 0 {
		t.Fatalf("after clear all: %d", got)
	}
}

// An open problem outlives the retention limits: dropping or hiding it
// would raise and push it again on the next report.
func TestRetentionKeepsOpenProblems(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.disks(Disk{"/", 50}) // baseline
	e.disks(Disk{"/", 95})
	if n := e.countPushes(DiskFull); n != 1 {
		t.Fatalf("disk pushes = %d, want 1", n)
	}
	e.clock.advance(8 * 24 * time.Hour)
	for i := range 120 {
		e.raise(Occurrence{Category: DownloadFinished, Key: fmt.Sprint(i), Title: "Download finished"})
	}
	disk := func(s Snapshot) *Item {
		for i, it := range s.Recent {
			if it.Category == DiskFull {
				return &s.Recent[i]
			}
		}
		return nil
	}
	s := e.snap(t)
	if disk(s) == nil || len(s.Recent) != 101 {
		t.Fatalf("8 day old open disk item gone: %d recent", len(s.Recent))
	}
	e.disks(Disk{"/", 95})
	if n := e.countPushes(DiskFull); n != 1 {
		t.Fatalf("disk pushes after retention = %d, want 1", n)
	}

	// Cleared, it is hidden but still marks the problem as notified.
	if err := e.c.Clear(ctx, disk(s).ID); err != nil {
		t.Fatal(err)
	}
	e.clock.advance(8 * 24 * time.Hour)
	for i := range 120 {
		e.raise(Occurrence{Category: DownloadFinished, Key: fmt.Sprint("b", i), Title: "Download finished"})
	}
	e.disks(Disk{"/", 95})
	if n := e.countPushes(DiskFull); n != 1 {
		t.Fatalf("disk pushes after clear and retention = %d, want 1", n)
	}
	if disk(e.snap(t)) != nil {
		t.Fatal("cleared disk item shown again")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"testing"
	"time"
)

func (e *testEnv) updates(source string, automatic bool, names ...string) {
	e.clock.advance(1e9)
	e.c.ReportUpdates(context.Background(), source, names, automatic)
}

func (e *testEnv) disks(list ...Disk) {
	e.clock.advance(1e9)
	e.c.ReportDisks(context.Background(), list)
}

// baselineUpdates reports every source empty so later reports are not baseline.
func (e *testEnv) baselineUpdates() {
	for _, s := range []string{"containers", "system", "homebrew"} {
		e.updates(s, true)
	}
}

func TestUpdatesAggregate(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", true, "a", "b", "c")
	e.updates("system", true, "p1", "p2", "p3", "p4")
	e.updates("homebrew", true, "x", "y")
	s := e.snap(t)
	if len(s.Recent) != 1 {
		t.Fatalf("recent = %+v, want one item", s.Recent)
	}
	it := s.Recent[0]
	if it.Category != UpdatesFound || it.Key != "pending" || it.Title != "9 updates available" ||
		it.Body != "Containers 3 · System 4 · Homebrew 2" || it.Link != "/updates" {
		t.Errorf("item = %+v", it)
	}
	if len(e.pushes) != 3 {
		t.Errorf("pushes = %d, want 3 (one per source with new names)", len(e.pushes))
	}
	e.updates("system", true)
	it = e.snap(t).Recent[0]
	if it.Title != "5 updates available" || it.Body != "Containers 3 · Homebrew 2" {
		t.Errorf("after system cleared: %+v", it)
	}
}

func TestUpdatesBaselineNoPush(t *testing.T) {
	e := newTestCenter(t)
	e.updates("containers", true, "a")
	if len(e.pushes) != 0 {
		t.Fatalf("baseline pushed: %+v", e.pushes)
	}
	if s := e.snap(t); len(s.Recent) != 1 || s.Recent[0].Title != "1 update available" {
		t.Fatalf("recent = %+v", s.Recent)
	}
}

func TestUpdatesSameSetNoPush(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", true, "a", "b")
	e.updates("containers", true, "a", "b")
	e.updates("containers", true, "b")
	if len(e.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(e.pushes))
	}
}

func TestUpdatesManualNoPush(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", false, "a")
	if len(e.pushes) != 0 {
		t.Fatalf("manual check pushed: %+v", e.pushes)
	}
	if s := e.snap(t); len(s.Recent) != 1 || s.Recent[0].Body != "Containers 1" {
		t.Fatalf("recent = %+v", s.Recent)
	}
}

func TestUpdatesGoneWhenZero(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", true, "a")
	e.updates("homebrew", true, "x")
	e.updates("containers", true)
	if s := e.snap(t); len(s.Recent) != 1 {
		t.Fatalf("recent = %+v, want 1", s.Recent)
	}
	e.updates("homebrew", true)
	s := e.snap(t)
	if len(s.Recent) != 0 || s.Badge != 0 {
		t.Fatalf("recent = %+v badge %d, want gone", s.Recent, s.Badge)
	}
}

func TestUpdatesNewNamesShowClearedItemAgain(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.baselineUpdates()
	e.updates("containers", true, "a")
	id := e.snap(t).Recent[0].ID
	if err := e.c.Clear(ctx, id); err != nil {
		t.Fatal(err)
	}
	e.updates("containers", true, "a")
	if got := len(e.snap(t).Recent); got != 0 {
		t.Fatalf("same set un-cleared the item (%d recent)", got)
	}
	e.updates("containers", true, "a", "b")
	s := e.snap(t)
	if len(s.Recent) != 1 || s.Recent[0].Title != "2 updates available" || s.Badge != 1 {
		t.Fatalf("recent = %+v badge %d, want the item back", s.Recent, s.Badge)
	}
	if len(e.pushes) != 2 {
		t.Errorf("pushes = %d, want 2", len(e.pushes))
	}
}

func TestDiskHysteresis(t *testing.T) {
	e := newTestCenter(t)
	e.disks(Disk{"/", 50}) // baseline
	e.disks(Disk{"/", 89})
	if len(e.snap(t).Recent) != 0 || len(e.pushes) != 0 {
		t.Fatal("89% raised")
	}
	e.disks(Disk{"/", 91})
	s := e.snap(t)
	if len(s.Recent) != 1 || len(e.pushes) != 1 || s.Recent[0].Title != "/ is 91% full" || e.pushes[0].cat != DiskFull {
		t.Fatalf("91%%: recent %+v pushes %+v", s.Recent, e.pushes)
	}
	e.disks(Disk{"/", 87})
	if s := e.snap(t); s.Recent[0].ResolvedAt != nil || s.Badge != 1 {
		t.Fatal("87% resolved too early")
	}
	e.disks(Disk{"/", 84.4})
	s = e.snap(t)
	if s.Recent[0].ResolvedAt == nil || s.Recent[0].Title != "/ is back to 84%" || s.Badge != 0 {
		t.Fatalf("84%%: %+v badge %d", s.Recent[0], s.Badge)
	}
	e.disks(Disk{"/", 91})
	s = e.snap(t)
	if len(s.Recent) != 2 || len(e.pushes) != 2 || s.Badge != 1 {
		t.Fatalf("second raise: recent %d pushes %d badge %d", len(s.Recent), len(e.pushes), s.Badge)
	}
}

func TestDiskBaselineAndRestart(t *testing.T) {
	e := newTestCenter(t)
	e.disks(Disk{"/", 95}, Disk{"/data", 92}, Disk{"/mnt", 91})
	if len(e.pushes) != 0 || len(e.snap(t).Recent) != 3 {
		t.Fatalf("baseline: pushes %d recent %d", len(e.pushes), len(e.snap(t).Recent))
	}
	// Restart: "/" recovered while down, "/data" is still full.
	e2 := attach(t, e.st)
	// "/mnt" fell to 87%, inside the hysteresis band, so it stays open.
	e2.disks(Disk{"/", 60}, Disk{"/data", 93}, Disk{"/mnt", 87})
	s := e2.snap(t)
	if len(e2.pushes) != 0 || len(s.Recent) != 3 || s.Badge != 2 {
		t.Fatalf("restart: pushes %d recent %d badge %d", len(e2.pushes), len(s.Recent), s.Badge)
	}
	for _, it := range s.Recent {
		if it.Key == "/" && (it.ResolvedAt == nil || it.Title != "/ is back to 60%") {
			t.Errorf("/ not resolved: %+v", it)
		}
		if (it.Key == "/data" || it.Key == "/mnt") && it.ResolvedAt != nil {
			t.Errorf("/data resolved: %+v", it)
		}
	}
}

func TestUpdatesSwappedNameSameCountPushes(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", true, "a")
	e.updates("containers", true, "b")
	if len(e.pushes) != 2 {
		t.Fatalf("pushes = %d, want 2", len(e.pushes))
	}
}

func TestUpdatesPartialBaselineKeepsClearedItem(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.baselineUpdates()
	e.updates("containers", true, "a")
	e.updates("system", true, "p1", "p2", "p3", "p4")
	it := e.snap(t).Recent[0]
	if err := e.c.Clear(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	e.pushes = nil

	// Restart: containers (0 pending) reports first, then the others.
	e2 := attach(t, e.st)
	e2.updates("containers", true)
	if _, err := e2.c.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	e2.updates("system", true, "p1", "p2", "p3", "p4")
	e2.updates("homebrew", true)
	s := e2.snap(t)
	if len(s.Recent) != 0 || s.Badge != 0 {
		t.Fatalf("cleared item came back: %+v badge %d", s.Recent, s.Badge)
	}
	var title, body string
	err := e2.st.DB.QueryRowContext(ctx, `SELECT title, body FROM notify_items WHERE category = ?`, string(UpdatesFound)).Scan(&title, &body)
	if err != nil {
		t.Fatalf("item deleted or missing: %v", err)
	}
	if title != "5 updates available" && title != "4 updates available" {
		t.Errorf("title = %q", title)
	}
	if len(e2.pushes) != 0 {
		t.Errorf("pushes = %+v", e2.pushes)
	}
}

func TestUpdatesPartialBaselineDoesNotRewriteText(t *testing.T) {
	e := newTestCenter(t)
	ctx := context.Background()
	e.baselineUpdates()
	e.updates("containers", true, "a")
	e.updates("system", true, "p1", "p2")
	want := e.snap(t).Recent[0]
	e2 := attach(t, e.st)
	e2.updates("containers", true)
	got := e2.snap(t).Recent
	if len(got) != 1 || got[0].Title != want.Title || got[0].Body != want.Body {
		t.Fatalf("after partial report: %+v, want %q / %q", got, want.Title, want.Body)
	}
	_ = ctx
}

// A source that never reports (Docker installed, daemon never up) holds
// the updates item for at most updatesWindow after New.
func TestUpdatesWindowCloses(t *testing.T) {
	e := newTestCenter(t)
	e.updates("system", true, "p1")
	e.updates("homebrew", true)
	e.clock.advance(11 * time.Minute)
	e.updates("system", true, "p1", "p2")
	s := e.snap(t)
	if len(s.Recent) != 1 || s.Recent[0].Title != "2 updates available" || s.Recent[0].Body != "System 2" {
		t.Fatalf("after the window: %+v", s.Recent)
	}
	if len(e.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(e.pushes))
	}
	e.updates("system", true)
	if s := e.snap(t); len(s.Recent) != 0 {
		t.Fatalf("nothing pending, item kept: %+v", s.Recent)
	}
	// Containers reports at last: its first report never pushes.
	e.updates("containers", true, "a")
	if s := e.snap(t); len(s.Recent) != 1 || s.Recent[0].Title != "1 update available" || len(e.pushes) != 1 {
		t.Fatalf("late first report: %+v pushes %d", s.Recent, len(e.pushes))
	}
}

// The open disk item follows the current percentage, without a push and
// without coming back once cleared.
func TestDiskTitleFollowsPercent(t *testing.T) {
	e := newTestCenter(t)
	e.disks(Disk{"/", 50})
	e.disks(Disk{"/", 91})
	e.disks(Disk{"/", 94})
	s := e.snap(t)
	if len(s.Recent) != 1 || s.Recent[0].Title != "/ is 94% full" || len(e.pushes) != 1 {
		t.Fatalf("94%%: %+v pushes %d", s.Recent, len(e.pushes))
	}
	e.disks(Disk{"/", 88})
	if s := e.snap(t); s.Recent[0].Title != "/ is 88% full" || s.Recent[0].ResolvedAt != nil {
		t.Fatalf("88%%: %+v", s.Recent[0])
	}
	if err := e.c.Clear(context.Background(), s.Recent[0].ID); err != nil {
		t.Fatal(err)
	}
	e.disks(Disk{"/", 97})
	if s := e.snap(t); len(s.Recent) != 0 || len(e.pushes) != 1 {
		t.Fatalf("cleared item: recent %+v pushes %d", s.Recent, len(e.pushes))
	}
	var title string
	if err := e.st.DB.QueryRow(`SELECT title FROM notify_items WHERE category = ?`, string(DiskFull)).Scan(&title); err != nil || title != "/ is 97% full" {
		t.Fatalf("stored title = %q, %v", title, err)
	}
}

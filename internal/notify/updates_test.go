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

// settle waits until any held updates push has been sent.
func (e *testEnv) settle() { e.wait(updatesHoldMax) }

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
	if len(e.pushes) != 0 {
		t.Errorf("pushed %+v before the checks settled", e.pushes)
	}
	e.settle()
	want := pushed{UpdatesFound, "pending", "9 updates available", "Containers 3 · System 4 · Homebrew 2", "/updates"}
	if len(e.pushes) != 1 || e.pushes[0] != want {
		t.Errorf("pushes = %+v, want one combined push", e.pushes)
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
	e.settle()
	e.updates("containers", true, "a", "b")
	e.settle()
	e.updates("containers", true, "b")
	e.settle()
	if len(e.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1", len(e.pushes))
	}
}

func TestUpdatesManualNoPush(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", false, "a")
	e.settle()
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
	e.settle()
	id := e.snap(t).Recent[0].ID
	if err := e.c.Clear(ctx, id); err != nil {
		t.Fatal(err)
	}
	e.updates("containers", true, "a")
	if got := len(e.snap(t).Recent); got != 0 {
		t.Fatalf("same set un-cleared the item (%d recent)", got)
	}
	e.updates("containers", true, "a", "b")
	e.settle()
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
	e.settle()
	e.updates("containers", true, "b")
	e.settle()
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
	e2.settle()
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
	e.settle()
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
	e.settle()
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

// A name compares with the source's previous report: one that drops out
// and comes back is a new update and pushes again. Names seen in a manual
// check, or in the previous automatic one, do not push.
func TestUpdatesNotifiedSetRemembered(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", true, "a", "b")
	e.settle()
	e.updates("containers", true, "b")
	e.settle()
	e.updates("containers", true, "a", "b")
	e.settle()
	if len(e.pushes) != 2 {
		t.Fatalf("pushes = %d, want 2 (a came back: a new update)", len(e.pushes))
	}
	e.updates("homebrew", false, "a", "b", "c")
	e.settle()
	e.updates("homebrew", true, "a", "b", "c")
	e.settle()
	if len(e.pushes) != 2 {
		t.Fatalf("pushes = %d, want 2 (manual check already showed a, b, c)", len(e.pushes))
	}
	e.updates("system", true, "a", "b")
	e.settle()
	e.updates("system", true, "a", "b")
	e.settle()
	if len(e.pushes) != 3 {
		t.Fatalf("pushes = %d, want 3 (same automatic set again)", len(e.pushes))
	}
}

// A full disk that is no longer mounted is over once two reports in a row
// miss it; a disk still mounted keeps the 85% band. An empty report
// (metrics not read) changes nothing.
func TestUnmountedDiskResolves(t *testing.T) {
	e := newTestCenter(t)
	e.disks(Disk{"/", 50}, Disk{"/mnt", 50}, Disk{"/data", 50})
	e.disks(Disk{"/", 50}, Disk{"/mnt", 95}, Disk{"/data", 92})
	if s := e.snap(t); len(s.Recent) != 2 || s.Badge != 2 {
		t.Fatalf("raised: %+v badge %d", s.Recent, s.Badge)
	}
	e.disks()
	if s := e.snap(t); s.Badge != 2 {
		t.Fatalf("empty report resolved items: %+v", s.Recent)
	}
	e.disks(Disk{"/", 50}, Disk{"/data", 87})
	if s := e.snap(t); s.Badge != 2 {
		t.Fatalf("missed once: badge = %d, want 2 (/mnt kept open)", s.Badge)
	}
	e.disks()
	e.disks(Disk{"/", 50}, Disk{"/data", 87})
	s := e.snap(t)
	if s.Badge != 1 {
		t.Fatalf("badge = %d, want 1 (/data still open)", s.Badge)
	}
	for _, it := range s.Recent {
		if it.Key == "/mnt" && (it.ResolvedAt == nil || it.Title != "/mnt is no longer mounted") {
			t.Errorf("/mnt unmounted: %+v, want resolved as no longer mounted", it)
		}
		if it.Key == "/data" && it.ResolvedAt != nil {
			t.Errorf("/data at 87%% resolved: %+v", it)
		}
	}
}

// A network mount whose usage cannot be read once drops out of one report:
// its full item stays open and is not pushed again when it is back.
func TestFlakyMountNotRepushed(t *testing.T) {
	e := newTestCenter(t)
	e.disks(Disk{"/", 50}, Disk{"/nas", 50})
	e.disks(Disk{"/", 50}, Disk{"/nas", 92})
	e.disks(Disk{"/", 50})
	e.disks(Disk{"/", 50}, Disk{"/nas", 92})
	s := e.snap(t)
	if len(s.Recent) != 1 || s.Recent[0].ResolvedAt != nil || s.Recent[0].Title != "/nas is 92% full" {
		t.Fatalf("after one miss: recent %+v, want the same open item", s.Recent)
	}
	if n := e.countPushes(DiskFull); n != 1 {
		t.Fatalf("pushes = %d, want 1", n)
	}
	e.disks(Disk{"/", 50})
	if s := e.snap(t); s.Recent[0].ResolvedAt != nil {
		t.Fatalf("missed once more after being back: %+v, want still open", s.Recent[0])
	}
	e.disks(Disk{"/", 50})
	if s := e.snap(t); len(s.Recent) != 1 || s.Recent[0].ResolvedAt == nil || s.Badge != 0 {
		t.Fatalf("missed twice: recent %+v badge %d, want resolved", s.Recent, s.Badge)
	}
	if n := e.countPushes(DiskFull); n != 1 {
		t.Fatalf("pushes = %d, want 1", n)
	}
}

// The held push waits for an automatic check still running, then goes out
// once, after updatesHold of quiet, with every source's count.
func TestUpdatesPushWaitsForRunningChecks(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	endSystem := e.c.BeginUpdateCheck("system")
	endBrew := e.c.BeginUpdateCheck("homebrew")
	endContainers := e.c.BeginUpdateCheck("containers")
	e.updates("containers", true, "a")
	endContainers()
	e.wait(updatesHold - 2*time.Second)
	e.updates("homebrew", true, "x", "y")
	endBrew()
	e.wait(updatesHold + time.Minute) // system still running
	if len(e.pushes) != 0 {
		t.Fatalf("pushed %+v while a check was running", e.pushes)
	}
	e.updates("system", true, "p1")
	endSystem()
	endSystem() // a second call does nothing
	e.wait(updatesHold - time.Second)
	if len(e.pushes) != 0 {
		t.Fatalf("pushed %+v before updatesHold of quiet", e.pushes)
	}
	e.wait(time.Second)
	want := pushed{UpdatesFound, "pending", "4 updates available", "Containers 1 · System 1 · Homebrew 2", "/updates"}
	if len(e.pushes) != 1 || e.pushes[0] != want {
		t.Fatalf("pushes = %+v, want %+v", e.pushes, want)
	}
}

// A check that hangs holds the push for at most updatesHoldMax.
func TestUpdatesPushHoldIsBounded(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	end := e.c.BeginUpdateCheck("system")
	defer end()
	e.updates("containers", true, "a")
	e.wait(updatesHoldMax - time.Second)
	if len(e.pushes) != 0 {
		t.Fatalf("pushed %+v early", e.pushes)
	}
	e.wait(time.Second)
	if len(e.pushes) != 1 {
		t.Fatalf("pushes = %d, want 1 after updatesHoldMax", len(e.pushes))
	}
}

// A held push is dropped when, before it goes out, the new updates were
// installed, the owner cleared the item, or push was turned off.
func TestUpdatesHeldPushDropped(t *testing.T) {
	ctx := context.Background()
	for name, act := range map[string]func(t *testing.T, e *testEnv){
		"installed": func(t *testing.T, e *testEnv) { e.updates("containers", false) },
		"cleared": func(t *testing.T, e *testEnv) {
			if err := e.c.Clear(ctx, e.snap(t).Recent[0].ID); err != nil {
				t.Fatal(err)
			}
		},
		"push off": func(t *testing.T, e *testEnv) {
			if err := e.c.SetModes(ctx, map[Category]Mode{UpdatesFound: ModeApp}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newTestCenter(t)
			e.baselineUpdates()
			e.updates("containers", true, "a")
			act(t, e)
			e.settle()
			if len(e.pushes) != 0 {
				t.Fatalf("pushes = %+v, want none", e.pushes)
			}
		})
	}
}

// Updates already notified do not carry a held push whose new ones were
// installed during the hold.
func TestUpdatesHeldPushNeedsNewName(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("system", true, "p1", "p2")
	e.settle()
	e.pushes = nil
	e.updates("containers", true, "a")
	e.updates("containers", false) // a was updated during the hold
	e.settle()
	if len(e.pushes) != 0 {
		t.Fatalf("pushes = %+v, want none (only System, already notified, is left)", e.pushes)
	}
	if s := e.snap(t); len(s.Recent) != 1 || s.Recent[0].Body != "System 2" {
		t.Fatalf("recent = %+v", s.Recent)
	}
}

// After Close the held push is dropped and no new hold starts.
func TestUpdatesCloseDropsHeldPush(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	end := e.c.BeginUpdateCheck("system")
	e.updates("containers", true, "a")
	e.c.Close()
	e.updates("homebrew", true, "x")
	end()
	e.settle()
	if len(e.pushes) != 0 || len(e.timers) != 0 {
		t.Fatalf("pushes = %+v, timers = %d after Close, want none", e.pushes, len(e.timers))
	}
}

// A manual check during the hold changes nothing about the push: it goes
// out once, with the counts as they are when it is sent.
func TestUpdatesManualDuringHold(t *testing.T) {
	e := newTestCenter(t)
	e.baselineUpdates()
	e.updates("containers", true, "a")
	e.updates("system", false, "p1", "p2")
	e.settle()
	if len(e.pushes) != 1 || e.pushes[0].title != "3 updates available" {
		t.Fatalf("pushes = %+v", e.pushes)
	}
}

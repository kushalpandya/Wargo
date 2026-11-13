// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/notify"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestContainerEventMapping(t *testing.T) {
	tests := []struct {
		action, exit string
		cat          notify.Category
		resolve, ok  bool
	}{
		{"health_status: unhealthy", "", notify.ContainerUnhealthy, false, true},
		{"health_status: healthy", "", notify.ContainerUnhealthy, true, true},
		{"die", "1", notify.ContainerFailed, false, true},
		{"die", "255", notify.ContainerFailed, false, true},
		{"start", "", notify.ContainerFailed, true, true},
		// Stopped on purpose or ended normally: not a failure.
		{"die", "0", "", false, false},
		{"die", "137", "", false, false},
		{"die", "143", "", false, false},
		{"die", "", "", false, false},
		{"oom", "", "", false, false},
		{"health_status: starting", "", "", false, false},
		{"stop", "", "", false, false},
	}
	for _, tt := range tests {
		cat, resolve, ok := containerEvent(tt.action, tt.exit)
		if cat != tt.cat || resolve != tt.resolve || ok != tt.ok {
			t.Errorf("containerEvent(%q, %q) = (%q, %v, %v), want (%q, %v, %v)",
				tt.action, tt.exit, cat, resolve, ok, tt.cat, tt.resolve, tt.ok)
		}
	}
}

// fakeNotifier records what the containers feature reports. Like the real
// Center, a Raise while the same (category, key) is open does nothing.
type fakeNotifier struct {
	open     map[string]bool
	raised   []notify.Occurrence
	resolved []string // "category key: title"
	updates  []string // "source automatic: names"
}

func newFakeNotifier() *fakeNotifier { return &fakeNotifier{open: map[string]bool{}} }

func (f *fakeNotifier) Raise(_ context.Context, o notify.Occurrence) bool {
	k := string(o.Category) + " " + o.Key
	if f.open[k] {
		return false
	}
	f.open[k] = true
	f.raised = append(f.raised, o)
	return !o.Baseline
}

func (f *fakeNotifier) Resolve(_ context.Context, cat notify.Category, key, title string) bool {
	k := string(cat) + " " + key
	if !f.open[k] {
		return false
	}
	delete(f.open, k)
	f.resolved = append(f.resolved, k+": "+title)
	return true
}

func (f *fakeNotifier) Reconcile(ctx context.Context, cat notify.Category, current []string, title func(string) string) {
	keep := map[string]bool{}
	for _, k := range current {
		keep[k] = true
	}
	var stale []string
	for k := range f.open {
		if c, key, _ := cut(k); c == string(cat) && !keep[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t := ""
		if title != nil {
			t = title(key)
		}
		f.Resolve(ctx, cat, key, t)
	}
}

func (f *fakeNotifier) ReportUpdates(_ context.Context, source string, names []string, automatic bool) {
	f.updates = append(f.updates, fmt.Sprintf("%s %v: %s", source, automatic, strings.Join(names, ",")))
}

func cut(k string) (string, string, bool) {
	for i := range k {
		if k[i] == ' ' {
			return k[:i], k[i+1:], true
		}
	}
	return k, "", false
}

// fakeClock fires timers when advanced past their deadline.
type fakeClock struct {
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (c *fakeClock) afterFunc(d time.Duration, f func()) func() bool {
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		was := !t.stopped
		t.stopped = true
		return was
	}
}

func (c *fakeClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			t.stopped = true
			t.f()
		}
	}
}

func newTestWatch(n notifier, clock *fakeClock) *healthWatch {
	w := newHealthWatch(n)
	w.afterFunc = clock.afterFunc
	w.now = func() time.Time { return clock.now }
	return w
}

func TestDockerBaselineNoPush(t *testing.T) {
	ctx := context.Background()
	n := newFakeNotifier()
	// Left open from before the restart: one recovered while Wargo was down.
	n.open["container.unhealthy recovered"] = true
	n.open["container.failed gone"] = true
	w := newTestWatch(n, &fakeClock{})
	w.sync(ctx, []Container{
		{Name: "gluetun", State: "running", Status: "Up 2 hours (unhealthy)", Health: "unhealthy"},
		{Name: "recovered", State: "running", Status: "Up 2 hours (healthy)", Health: "healthy"},
		{Name: "crashed", State: "exited", Status: "Exited (1) 3 minutes ago"},
		{Name: "looping", State: "restarting", Status: "Restarting (2) 5 seconds ago"},
		{Name: "stopped", State: "exited", Status: "Exited (143) 2 days ago"},
		{Name: "done", State: "exited", Status: "Exited (0) 2 days ago"},
		{Name: "wargo", State: "exited", Status: "Exited (1) 1 minute ago", Self: true},
	}, true)

	got := map[string]bool{}
	for _, o := range n.raised {
		if !o.Baseline {
			t.Errorf("raised %s %s without Baseline: it would push", o.Category, o.Key)
		}
		got[string(o.Category)+" "+o.Key] = true
	}
	want := []string{"container.unhealthy gluetun", "container.failed crashed", "container.failed looping"}
	for _, k := range want {
		if !got[k] {
			t.Errorf("missing baseline item %q (raised %v)", k, got)
		}
	}
	if len(n.raised) != len(want) {
		t.Errorf("raised %d items, want %d: %v", len(n.raised), len(want), got)
	}
	titles := map[string]string{}
	for _, o := range n.raised {
		titles[o.Key] = o.Title
	}
	for key, want := range map[string]string{
		"gluetun": "gluetun is unhealthy",
		"crashed": "crashed stopped with an error",
		"looping": "looping keeps restarting",
	} {
		if titles[key] != want {
			t.Errorf("title of %s = %q, want %q", key, titles[key], want)
		}
	}
	wantResolved := map[string]bool{
		"container.unhealthy recovered: recovered is healthy again": true,
		"container.failed gone: ":                                   true,
	}
	for _, r := range n.resolved {
		if !wantResolved[r] {
			t.Errorf("unexpected resolve %q", r)
		}
		delete(wantResolved, r)
	}
	for r := range wantResolved {
		t.Errorf("missing resolve %q", r)
	}
}

func TestRestartLoopOneOccurrence(t *testing.T) {
	ctx := context.Background()
	n := newFakeNotifier()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w := newTestWatch(n, clock)

	// Crashes every 30 seconds, restarted by its restart policy. The event
	// path says "stopped with an error": only sync sees the restarting state.
	for range 5 {
		w.event(ctx, "flaky", "die", "1")
		clock.advance(time.Second)
		w.event(ctx, "flaky", "start", "")
		clock.advance(30 * time.Second)
	}
	if len(n.raised) != 1 || n.raised[0].Title != "flaky stopped with an error" || n.raised[0].Baseline {
		t.Fatalf("raised = %+v, want one pushed occurrence", n.raised)
	}
	if len(n.resolved) != 0 {
		t.Fatalf("resolved during the loop: %v", n.resolved)
	}

	// Stays up for 2 minutes: resolved once.
	w.event(ctx, "flaky", "die", "1")
	w.event(ctx, "flaky", "start", "")
	clock.advance(119 * time.Second)
	if len(n.resolved) != 0 {
		t.Fatalf("resolved before 2 minutes: %v", n.resolved)
	}
	clock.advance(time.Second)
	if len(n.resolved) != 1 || n.resolved[0] != "container.failed flaky: flaky is running again" {
		t.Fatalf("resolved = %v", n.resolved)
	}

	// Failing again later is a new occurrence.
	w.event(ctx, "flaky", "die", "1")
	if len(n.raised) != 2 {
		t.Fatalf("raised %d, want 2", len(n.raised))
	}
}

// A container stopped through Docker (a terminating kill, then die soon
// after) is not a failure, even when it exits with an error code. Other
// signals (a reload) and a die long after the kill are.
func TestStopIsNotAFailure(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name, signal string
		wait         time.Duration
		raised       bool
	}{
		{"reload signal", "1", time.Second, true},
		{"SIGTERM", "15", 10 * time.Second, false},
		{"SIGKILL", "9", time.Second, false},
		{"named signal", "SIGINT", time.Second, false},
		{"die long after", "15", 31 * time.Second, true},
	} {
		n := newFakeNotifier()
		clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
		w := newTestWatch(n, clock)
		w.event(ctx, "app", "kill", tt.signal)
		clock.advance(tt.wait)
		w.event(ctx, "app", "die", "1")
		if got := len(n.raised) == 1; got != tt.raised {
			t.Errorf("%s: raised %v, want %v", tt.name, got, tt.raised)
		}
	}
}

// withFakeNotifier wires a fakeNotifier into s.
func withFakeNotifier(s *Service) *fakeNotifier {
	n := newFakeNotifier()
	s.setNotifier(n)
	return n
}

// Wargo started before the Docker daemon: no empty report (that would drop
// the saved pending updates and push them again on the next check); the
// first look once Docker answers reports the saved names, without a push.
func TestStartupUpdatesWaitForDaemon(t *testing.T) {
	ctx := context.Background()
	f := &fakeDocker{containers: map[string]*fakeContainer{}}
	id := "2222222222222222222222222222222222222222222222222222222222222222"
	f.containers[id] = appContainer(id, "myapp", "bridge")
	sock := filepath.Join(shortTempDir(t), "docker.sock")
	s := startFake(t, &fakeDocker{containers: map[string]*fakeContainer{}})
	s.c = NewClient(sock) // nothing listening yet
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s.Configure(Options{Store: st})
	yes := true
	s.updates["app:latest|sha256:old"] = UpdateCheck{Image: "app:latest", UpdateAvailable: &yes}
	n := withFakeNotifier(s)
	s.dockerAbsent = func() bool { return false }

	s.reportStartupUpdates(ctx)
	if len(n.updates) != 0 {
		t.Fatalf("reported while the daemon was down: %v", n.updates)
	}
	serveFake(t, f, sock)
	s.syncProblems(ctx)
	if len(n.updates) != 1 || n.updates[0] != "containers false: myapp" {
		t.Fatalf("updates = %v, want the saved names once, not automatic", n.updates)
	}
	s.syncProblems(ctx) // a reconnect does not report again
	if len(n.updates) != 1 {
		t.Fatalf("updates = %v", n.updates)
	}
}

// Docker not installed: one empty report, so the updates item does not
// wait for containers.
func TestStartupUpdatesDockerAbsent(t *testing.T) {
	s := startFake(t, &fakeDocker{containers: map[string]*fakeContainer{}})
	s.c = NewClient(filepath.Join(shortTempDir(t), "missing.sock"))
	n := withFakeNotifier(s)
	s.dockerAbsent = func() bool { return true }
	s.reportStartupUpdates(context.Background())
	if len(n.updates) != 1 || n.updates[0] != "containers false: " {
		t.Fatalf("updates = %v", n.updates)
	}
}

func TestDockerAbsentDefault(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "docker.sock")
	if dockerMissing(missing, func(string) (string, error) { return "/usr/bin/docker", nil }) {
		t.Error("docker installed: not absent")
	}
	if !dockerMissing(missing, func(string) (string, error) { return "", os.ErrNotExist }) {
		t.Error("no socket and no docker: absent")
	}
	if !dockerMissing("", func(string) (string, error) { return "/usr/bin/docker", nil }) {
		t.Error("no socket configured: absent")
	}
}

// A flapping health check is one occurrence: unhealthy resolves only once
// the container has stayed healthy for stableAfter.
func TestFlappingHealthOneOccurrence(t *testing.T) {
	ctx := context.Background()
	n := newFakeNotifier()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w := newTestWatch(n, clock)
	for range 5 {
		w.event(ctx, "gluetun", "health_status: unhealthy", "")
		clock.advance(30 * time.Second)
		w.event(ctx, "gluetun", "health_status: healthy", "")
		clock.advance(time.Minute)
	}
	if len(n.raised) != 1 || len(n.resolved) != 0 {
		t.Fatalf("raised %d resolved %v, want one open occurrence", len(n.raised), n.resolved)
	}
	// A repeated healthy does not restart the count.
	w.event(ctx, "gluetun", "health_status: healthy", "")
	clock.advance(59 * time.Second)
	if len(n.resolved) != 0 {
		t.Fatalf("resolved before 2 minutes healthy: %v", n.resolved)
	}
	clock.advance(time.Second)
	if len(n.resolved) != 1 || n.resolved[0] != "container.unhealthy gluetun: gluetun is healthy again" {
		t.Fatalf("resolved = %v", n.resolved)
	}
}

// A removed container's problems are over, and so is the unhealthy state of
// one stopped on purpose.
func TestRemovedOrStoppedResolves(t *testing.T) {
	ctx := context.Background()
	n := newFakeNotifier()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w := newTestWatch(n, clock)
	w.event(ctx, "app", "health_status: unhealthy", "")
	w.event(ctx, "app", "die", "1")
	w.event(ctx, "app", "destroy", "")
	if len(n.open) != 0 {
		t.Fatalf("open after destroy: %v", n.open)
	}
	if len(n.resolved) != 2 || n.resolved[0] != "container.unhealthy app: app was removed" ||
		n.resolved[1] != "container.failed app: app was removed" {
		t.Fatalf("resolved = %v", n.resolved)
	}

	n = newFakeNotifier()
	w = newTestWatch(n, clock)
	w.event(ctx, "db", "health_status: unhealthy", "")
	w.event(ctx, "db", "kill", "15")
	w.event(ctx, "db", "die", "143")
	clock.advance(stableAfter)
	if len(n.resolved) != 1 || n.resolved[0] != "container.unhealthy db: db was stopped" {
		t.Fatalf("resolved = %v", n.resolved)
	}
}

func TestDeadContainerFailed(t *testing.T) {
	if !failedState(Container{Name: "x", State: "dead", Status: "Dead"}) {
		t.Error("dead container not failed")
	}
}

// Once the events watch ends, pending resolves are dropped.
func TestHealthWatchStop(t *testing.T) {
	ctx := context.Background()
	n := newFakeNotifier()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w := newTestWatch(n, clock)
	w.event(ctx, "a", "die", "1")
	w.event(ctx, "a", "start", "")
	w.event(ctx, "b", "health_status: unhealthy", "")
	w.event(ctx, "b", "health_status: healthy", "")
	w.stop()
	clock.advance(time.Hour)
	if len(n.resolved) != 0 {
		t.Fatalf("resolved after stop: %v", n.resolved)
	}
	w.event(ctx, "a", "start", "")
	clock.advance(time.Hour)
	if len(n.resolved) != 0 {
		t.Fatalf("timer started after stop: %v", n.resolved)
	}
}

// Updating a container through Wargo refreshes the updates item, without
// a push.
func TestContainerUpdateReportsUpdates(t *testing.T) {
	ctx := context.Background()
	f := &fakeDocker{containers: map[string]*fakeContainer{}}
	id := "3333333333333333333333333333333333333333333333333333333333333333"
	f.containers[id] = appContainer(id, "myapp", "bridge")
	s := startFake(t, f)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s.Configure(Options{Store: st})
	yes := true
	s.updates["app:latest|sha256:old"] = UpdateCheck{Image: "app:latest", UpdateAvailable: &yes}
	n := withFakeNotifier(s)
	s.markUpToDate("app:latest", "sha256:old")
	if len(n.updates) != 1 || n.updates[0] != "containers false: " {
		t.Fatalf("after markUpToDate: %v", n.updates)
	}
	s.recheckContainer(ctx, "myapp")
	if len(n.updates) != 2 || n.updates[1] != "containers false: " {
		t.Fatalf("after recheckContainer: %v", n.updates)
	}
}

// A restart (kill, die, start) keeps an unhealthy container's item open,
// so an autoheal loop pushes once. A stop with no start after it resolves
// once the hold-down has passed.
func TestRestartKeepsUnhealthyOpen(t *testing.T) {
	ctx := context.Background()
	n := newFakeNotifier()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w := newTestWatch(n, clock)
	w.event(ctx, "app", "health_status: unhealthy", "")
	for range 3 {
		w.event(ctx, "app", "kill", "15")
		w.event(ctx, "app", "die", "143")
		clock.advance(time.Second)
		w.event(ctx, "app", "start", "")
		clock.advance(30 * time.Second)
		w.event(ctx, "app", "health_status: unhealthy", "")
		clock.advance(stableAfter)
	}
	if len(n.raised) != 1 || len(n.resolved) != 0 || !n.open["container.unhealthy app"] {
		t.Fatalf("raised %d resolved %v open %v, want one open occurrence", len(n.raised), n.resolved, n.open)
	}

	w.event(ctx, "app", "kill", "15")
	w.event(ctx, "app", "die", "143")
	clock.advance(stableAfter - time.Second)
	if len(n.resolved) != 0 {
		t.Fatalf("resolved before the hold-down: %v", n.resolved)
	}
	clock.advance(time.Second)
	if len(n.resolved) != 1 || n.resolved[0] != "container.unhealthy app: app was stopped" {
		t.Fatalf("resolved = %v", n.resolved)
	}
}

// A reconnect that finds the container unhealthy again cancels a pending
// "healthy again".
func TestSyncCancelsPendingHealthy(t *testing.T) {
	ctx := context.Background()
	n := newFakeNotifier()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w := newTestWatch(n, clock)
	w.event(ctx, "gluetun", "health_status: unhealthy", "")
	w.event(ctx, "gluetun", "health_status: healthy", "")
	clock.advance(time.Minute)
	w.sync(ctx, []Container{
		{Name: "gluetun", State: "running", Status: "Up 2 hours (unhealthy)", Health: "unhealthy"},
	}, false)
	clock.advance(time.Hour)
	if len(n.resolved) != 0 || !n.open["container.unhealthy gluetun"] {
		t.Fatalf("resolved %v open %v, want still open", n.resolved, n.open)
	}
}

// A renamed container's problems move to its new name: the items under
// the old name are resolved, and the next event raises under the new one.
func TestRenameMovesProblem(t *testing.T) {
	if got := eventCode("rename", map[string]string{"name": "new", "oldName": "/old"}); got != "old" {
		t.Errorf("rename code = %q, want the old name", got)
	}
	if got := eventCode("kill", map[string]string{"signal": "15", "exitCode": "x"}); got != "15" {
		t.Errorf("kill code = %q", got)
	}
	if got := eventCode("die", map[string]string{"exitCode": "1"}); got != "1" {
		t.Errorf("die code = %q", got)
	}

	ctx := context.Background()
	n := newFakeNotifier()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w := newTestWatch(n, clock)
	w.event(ctx, "old", "health_status: unhealthy", "")
	w.event(ctx, "old", "die", "1")
	pushes := len(n.raised)
	w.event(ctx, "new", "rename", "old")
	if len(n.resolved) != 2 || n.resolved[0] != "container.unhealthy old: old was renamed to new" ||
		n.resolved[1] != "container.failed old: old was renamed to new" {
		t.Fatalf("resolved = %v", n.resolved)
	}
	// The open problems move to the new name at once, without a push.
	if len(n.open) != 2 || !n.open["container.unhealthy new"] || !n.open["container.failed new"] {
		t.Fatalf("open after rename: %v", n.open)
	}
	titles := map[string]string{}
	for _, o := range n.raised[pushes:] {
		if !o.Baseline {
			t.Errorf("rename pushed %s %s", o.Category, o.Key)
		}
		titles[string(o.Category)] = o.Title
	}
	if titles["container.unhealthy"] != "new is unhealthy" || titles["container.failed"] != "new stopped with an error" {
		t.Fatalf("titles after rename = %v", titles)
	}

	// Only the categories that were open move.
	n = newFakeNotifier()
	w = newTestWatch(n, clock)
	w.event(ctx, "a", "health_status: unhealthy", "")
	w.event(ctx, "b", "rename", "a")
	if len(n.open) != 1 || !n.open["container.unhealthy b"] {
		t.Fatalf("open after rename: %v", n.open)
	}

	// A stop in progress follows the container to its new name.
	n = newFakeNotifier()
	w = newTestWatch(n, clock)
	w.event(ctx, "s", "kill", "15")
	w.event(ctx, "t", "rename", "s")
	w.event(ctx, "t", "die", "1")
	if len(n.raised) != 0 {
		t.Fatalf("stop after a rename raised %+v", n.raised)
	}
}

// A kill with the container's own STOPSIGNAL is a stop through Docker,
// whether the signal is set by number or by name, with or without SIG.
func TestCustomStopSignalIsStop(t *testing.T) {
	ctx := context.Background()
	configured := map[string]string{"app": "SIGUSR1", "web": "WINCH", "num": "10"}
	for _, tt := range []struct {
		name, container, signal string
		raised                  bool
	}{
		{"named with SIG", "app", "10", false},
		{"named without SIG", "web", "28", false},
		{"by number", "num", "10", false},
		{"event by name", "num", "SIGUSR1", false},
		{"other signal", "app", "1", true},
		{"default still applies", "app", "15", false},
	} {
		n := newFakeNotifier()
		clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
		w := newTestWatch(n, clock)
		w.stopSignal = func(_ context.Context, name string) (string, bool) { return configured[name], true }
		w.event(ctx, tt.container, "kill", tt.signal)
		w.event(ctx, tt.container, "die", "1")
		if got := len(n.raised) == 1; got != tt.raised {
			t.Errorf("%s: raised %v, want %v", tt.name, got, tt.raised)
		}
	}

	// Looked up once per container, again after it is removed or renamed;
	// a failed lookup is not kept.
	n := newFakeNotifier()
	w := newTestWatch(n, &fakeClock{now: time.Unix(1_700_000_000, 0)})
	lookups := 0
	fail := true
	w.stopSignal = func(_ context.Context, name string) (string, bool) {
		lookups++
		return "SIGUSR1", !fail
	}
	w.event(ctx, "app", "kill", "10")
	fail = false
	w.event(ctx, "app", "kill", "10")
	w.event(ctx, "app", "kill", "10")
	if lookups != 2 {
		t.Fatalf("lookups = %d, want 2 (the failed one is retried, then cached)", lookups)
	}
	w.event(ctx, "app", "destroy", "")
	w.event(ctx, "app", "kill", "10")
	w.event(ctx, "app2", "rename", "app")
	w.event(ctx, "app2", "kill", "10")
	if lookups != 4 {
		t.Fatalf("lookups = %d, want 4 after destroy and rename", lookups)
	}

	// The service reads it from container inspect.
	f := &fakeDocker{containers: map[string]*fakeContainer{}}
	id := "5555555555555555555555555555555555555555555555555555555555555555"
	c := appContainer(id, "myapp", "bridge")
	c.inspect["Config"].(map[string]any)["StopSignal"] = "SIGUSR1"
	f.containers[id] = c
	s := startFake(t, f)
	sn := withFakeNotifier(s)
	if sig, ok := s.containerStopSignal(ctx, "myapp"); !ok || sig != "SIGUSR1" {
		t.Fatalf("containerStopSignal = %q, %v", sig, ok)
	}
	s.watch.event(ctx, "myapp", "kill", "10")
	s.watch.event(ctx, "myapp", "die", "1")
	if len(sn.raised) != 0 {
		t.Fatalf("stop with the container's STOPSIGNAL raised %+v", sn.raised)
	}
}

// An image whose registry check fails between two that find an update
// keeps its container in every updates report, so the update does not
// drop out and push again.
func TestFailedImageCarriesForward(t *testing.T) {
	ctx := context.Background()
	f := &fakeDocker{containers: map[string]*fakeContainer{}, repoDigests: []string{"app@sha256:local"}, remoteDigest: "sha256:remote"}
	id := "6666666666666666666666666666666666666666666666666666666666666666"
	f.containers[id] = appContainer(id, "myapp", "bridge")
	s := startFake(t, f)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s.Configure(Options{Store: st})
	n := withFakeNotifier(s)
	for i, fail := range []bool{false, true, true, false} {
		f.mu.Lock()
		f.registryFail = fail
		f.mu.Unlock()
		if err := s.CheckUpdates(ctx); err != nil {
			t.Fatal(err)
		}
		r := s.updatesFor([]Container{{ID: "c", Image: "app:latest", ImageID: "sha256:old"}})["c"]
		if fail != (r.Error != "") || r.UpdateAvailable == nil || !*r.UpdateAvailable {
			t.Fatalf("check %d (registry failing %v): result %+v", i, fail, r)
		}
		s.reportUpdates(ctx, true)
		if got := n.updates[len(n.updates)-1]; got != "containers true: myapp" {
			t.Fatalf("check %d (registry failing %v): report %q, want myapp", i, fail, got)
		}
	}
}

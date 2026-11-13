// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/notify"
)

// Container problems for the notifications panel: unhealthy containers and
// containers that exited with an error, keyed by container name. Wargo's
// own container is never reported.

// notifier is the part of notify.Center the containers feature uses.
type notifier interface {
	Raise(ctx context.Context, o notify.Occurrence) bool
	Resolve(ctx context.Context, cat notify.Category, key, title string) (resolved bool)
	Reconcile(ctx context.Context, cat notify.Category, current []string, resolvedTitle func(key string) string)
	ReportUpdates(ctx context.Context, source string, names []string, automatic bool)
}

// stableAfter is how long a container must stay up after a start before a
// failure counts as over, so a restart loop is one occurrence. Unhealthy
// works the same way: it is over once the container has stayed healthy
// this long, so a flapping health check is one occurrence.
const stableAfter = 2 * time.Minute

// containerEvent maps a Docker event to a notification: the category, and
// whether it resolves (true) or raises (false) it. ok is false for events
// that change nothing. A "start" resolves a failure, and "healthy" an
// unhealthy state, only once it has lasted stableAfter (see healthWatch).
// A "destroy" (handled by healthWatch) resolves both. Exits 0, 137
// and 143 (SIGKILL, SIGTERM: usually a stop or restart) are not failures.
func containerEvent(action, exit string) (cat notify.Category, resolve bool, ok bool) {
	switch action {
	case "health_status: unhealthy":
		return notify.ContainerUnhealthy, false, true
	case "health_status: healthy":
		return notify.ContainerUnhealthy, true, true
	case "die":
		if badExit(exit) {
			return notify.ContainerFailed, false, true
		}
	case "start":
		return notify.ContainerFailed, true, true
	}
	return "", false, false
}

func badExit(code string) bool {
	switch code {
	case "", "0", "137", "143":
		return false
	}
	return true
}

func problemTitle(cat notify.Category, name string) string {
	if cat == notify.ContainerUnhealthy {
		return name + " is unhealthy"
	}
	return name + " stopped with an error"
}

// failedTitle is the title of a failure found by sync: a container Docker
// is restarting keeps restarting, any other stopped with an error.
func failedTitle(c Container) string {
	if c.State == "restarting" {
		return c.Name + " keeps restarting"
	}
	return problemTitle(notify.ContainerFailed, c.Name)
}

func resolvedTitle(cat notify.Category, name string) string {
	if cat == notify.ContainerUnhealthy {
		return name + " is healthy again"
	}
	return name + " is running again"
}

// healthWatch turns Docker events into notifications.
type healthWatch struct {
	n notifier
	// afterFunc is time.AfterFunc returning Stop (replaced in tests).
	afterFunc func(d time.Duration, f func()) (stop func() bool)
	now       func() time.Time
	// stopSignal looks up a container's configured stop signal (its
	// STOPSIGNAL); ok is false when it could not be read. Nil means only
	// the usual signals count as a stop.
	stopSignal func(ctx context.Context, name string) (sig string, ok bool)

	mu sync.Mutex
	// stable holds the pending "running again" resolve per container,
	// healthy the pending "healthy again" one, and gone the pending "was
	// stopped" one (a start cancels it: a restart is not a stop).
	stable, healthy, gone map[string]*pending
	// stopped is set once the events watch has ended: no more timers.
	stopped bool
	// stopping holds when a container was sent a terminating signal: a die
	// within stopGrace after it is a stop through Docker, not a failure,
	// whatever the exit code.
	stopping map[string]time.Time
	// signals caches each container's stop signal number (0: none set)
	// until it is removed or renamed.
	signals map[string]int
}

func newHealthWatch(n notifier) *healthWatch {
	return &healthWatch{
		n: n, stable: map[string]*pending{}, healthy: map[string]*pending{}, gone: map[string]*pending{}, stopping: map[string]time.Time{},
		signals: map[string]int{}, now: time.Now,
		afterFunc: func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop },
	}
}

// stopGrace is how long after a terminating kill a die still counts as a
// stop through Docker.
const stopGrace = 30 * time.Second

// terminating reports whether a kill event's signal (number or name) is
// one Docker stops containers with: SIGKILL, SIGTERM, SIGINT, SIGQUIT or
// the container's own stop signal (stop, 0 if unknown).
func terminating(signal string, stop int) bool {
	n := signalNumber(signal)
	switch n {
	case 0:
		return false
	case 9, 15, 2, 3:
		return true
	}
	return n == stop
}

// linuxSignals numbers the signal names Docker accepts (Linux numbering:
// the daemon runs on Linux).
var linuxSignals = map[string]int{
	"HUP": 1, "INT": 2, "QUIT": 3, "ILL": 4, "TRAP": 5, "ABRT": 6, "IOT": 6, "BUS": 7,
	"FPE": 8, "KILL": 9, "USR1": 10, "SEGV": 11, "USR2": 12, "PIPE": 13, "ALRM": 14, "TERM": 15,
	"STKFLT": 16, "CHLD": 17, "CONT": 18, "STOP": 19, "TSTP": 20, "TTIN": 21, "TTOU": 22, "URG": 23,
	"XCPU": 24, "XFSZ": 25, "VTALRM": 26, "PROF": 27, "WINCH": 28, "IO": 29, "POLL": 29, "PWR": 30, "SYS": 31,
}

// signalNumber parses a signal given by number or by name, with or
// without the SIG prefix; 0 if it is neither.
func signalNumber(sig string) int {
	if n, err := strconv.Atoi(sig); err == nil && n > 0 {
		return n
	}
	return linuxSignals[strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(sig)), "SIG")]
}

// configuredStop is the number of name's own stop signal, 0 if it has none
// or it could not be read. A successful lookup is kept until the container
// is removed or renamed.
func (w *healthWatch) configuredStop(ctx context.Context, name string) int {
	if w.stopSignal == nil {
		return 0
	}
	w.mu.Lock()
	n, ok := w.signals[name]
	w.mu.Unlock()
	if ok {
		return n
	}
	sig, ok := w.stopSignal(ctx, name)
	if !ok {
		return 0
	}
	n = signalNumber(sig)
	w.mu.Lock()
	w.signals[name] = n
	w.mu.Unlock()
	return n
}

// event handles one container event (Wargo's own container already left
// out). code is the exit code of a die and the signal of a kill.
func (w *healthWatch) event(ctx context.Context, name, action, code string) {
	if name == "" {
		return
	}
	switch action {
	case "kill":
		stop := 0
		if !terminating(code, 0) {
			stop = w.configuredStop(ctx, name)
		}
		if terminating(code, stop) {
			w.mu.Lock()
			w.stopping[name] = w.now()
			w.mu.Unlock()
		}
		return
	case "die":
		w.mu.Lock()
		w.cancelLocked(w.stable, name)
		at, ok := w.stopping[name]
		delete(w.stopping, name)
		w.mu.Unlock()
		if ok && w.now().Sub(at) <= stopGrace {
			// Stopped on purpose: its health check no longer runs. Resolve
			// once it has stayed down for stableAfter, so a restart (an
			// autoheal loop) keeps the item open and pushes once.
			w.mu.Lock()
			w.cancelLocked(w.healthy, name)
			w.scheduleLocked(context.WithoutCancel(ctx), w.gone, notify.ContainerUnhealthy, name, name+" was stopped")
			w.mu.Unlock()
			return
		}
	case "destroy":
		w.mu.Lock()
		w.cancelLocked(w.stable, name)
		w.cancelLocked(w.healthy, name)
		w.cancelLocked(w.gone, name)
		delete(w.stopping, name)
		delete(w.signals, name)
		w.mu.Unlock()
		w.n.Resolve(ctx, notify.ContainerUnhealthy, name, name+" was removed")
		w.n.Resolve(ctx, notify.ContainerFailed, name, name+" was removed")
		return
	case "rename":
		// code is the old name. Its open items move to the new name: the
		// old ones are resolved and the same problems raised again under
		// the new name, without a push. A stop in progress carries over.
		old := code
		if old == "" || old == name {
			return
		}
		w.mu.Lock()
		w.cancelLocked(w.stable, old)
		w.cancelLocked(w.healthy, old)
		w.cancelLocked(w.gone, old)
		if at, ok := w.stopping[old]; ok {
			w.stopping[name] = at
			delete(w.stopping, old)
		}
		delete(w.signals, old)
		delete(w.signals, name)
		w.mu.Unlock()
		for _, cat := range []notify.Category{notify.ContainerUnhealthy, notify.ContainerFailed} {
			if w.n.Resolve(ctx, cat, old, old+" was renamed to "+name) {
				w.n.Raise(ctx, notify.Occurrence{Category: cat, Key: name, Title: problemTitle(cat, name), Link: containerLink(name), Baseline: true})
			}
		}
		return
	case "start":
		w.mu.Lock()
		w.cancelLocked(w.gone, name)
		w.mu.Unlock()
	case "health_status: unhealthy":
		w.mu.Lock()
		w.cancelLocked(w.healthy, name)
		w.mu.Unlock()
	}
	cat, resolve, ok := containerEvent(action, code)
	if !ok {
		return
	}
	switch {
	case !resolve:
		w.n.Raise(ctx, notify.Occurrence{Category: cat, Key: name, Title: problemTitle(cat, name), Link: containerLink(name)})
	default:
		w.holdDown(context.WithoutCancel(ctx), cat, name)
	}
}

// started resolves a failure of name once it has stayed up for stableAfter
// with no die in between.
func (w *healthWatch) started(ctx context.Context, name string) {
	w.holdDown(ctx, notify.ContainerFailed, name)
}

// holdDown resolves cat for name once stableAfter has passed with nothing
// in between to cancel it: a die for a failure, an unhealthy for unhealthy.
func (w *healthWatch) holdDown(ctx context.Context, cat notify.Category, name string) {
	timers := w.stable
	if cat == notify.ContainerUnhealthy {
		timers = w.healthy
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if cat == notify.ContainerFailed {
		delete(w.stopping, name)
	} else if timers[name] != nil {
		// Healthy again while already counting: it has been healthy since
		// the first one.
		return
	}
	w.scheduleLocked(ctx, timers, cat, name, resolvedTitle(cat, name))
}

// scheduleLocked resolves cat for name with title after stableAfter,
// unless the timer in timers is cancelled or replaced first.
func (w *healthWatch) scheduleLocked(ctx context.Context, timers map[string]*pending, cat notify.Category, name, title string) {
	w.cancelLocked(timers, name)
	if w.stopped {
		return
	}
	p := &pending{}
	p.stop = w.afterFunc(stableAfter, func() {
		w.mu.Lock()
		// A cancel or a later event may have replaced this timer.
		current := timers[name] == p
		if current {
			delete(timers, name)
		}
		w.mu.Unlock()
		if current {
			w.n.Resolve(ctx, cat, name, title)
		}
	})
	timers[name] = p
}

// pending is a scheduled resolve.
type pending struct{ stop func() bool }

func (w *healthWatch) cancelLocked(timers map[string]*pending, name string) {
	if p := timers[name]; p != nil {
		p.stop()
		delete(timers, name)
	}
}

// stop cancels every pending resolve and schedules no more. The events
// watch calls it when it ends.
func (w *healthWatch) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	for _, timers := range []map[string]*pending{w.stable, w.healthy, w.gone} {
		for name := range timers {
			w.cancelLocked(timers, name)
		}
	}
}

// sync matches the open items to the containers as they are now: problems
// that ended while nobody watched are resolved, current ones raised. At
// startup (baseline) nothing it raises is pushed.
func (w *healthWatch) sync(ctx context.Context, list []Container, baseline bool) {
	var unhealthy, running []string
	var failed []Container
	keepUnhealthy := []string{}
	healthy := map[string]bool{}
	for _, c := range list {
		if c.Self || c.Name == "" {
			continue
		}
		switch {
		case c.Health == "unhealthy":
			unhealthy = append(unhealthy, c.Name)
			keepUnhealthy = append(keepUnhealthy, c.Name)
		case c.Health == "starting":
			// Not known yet: its health event decides.
			keepUnhealthy = append(keepUnhealthy, c.Name)
		case c.Health == "healthy":
			healthy[c.Name] = true
		}
		if failedState(c) {
			failed = append(failed, c)
		} else if c.State == "running" {
			running = append(running, c.Name)
		}
	}
	w.n.Reconcile(ctx, notify.ContainerUnhealthy, keepUnhealthy, func(name string) string {
		if healthy[name] {
			return resolvedTitle(notify.ContainerUnhealthy, name)
		}
		return ""
	})
	// A running container keeps its failure until it has stayed up for
	// stableAfter: it may be in a restart loop.
	current := append([]string{}, running...)
	for _, c := range failed {
		current = append(current, c.Name)
	}
	w.n.Reconcile(ctx, notify.ContainerFailed, current, nil)
	// What Docker reports now overrides a pending resolve: an unhealthy
	// container is not healthy again, a running one was not stopped.
	w.mu.Lock()
	for _, name := range unhealthy {
		w.cancelLocked(w.healthy, name)
	}
	for _, name := range running {
		w.cancelLocked(w.gone, name)
	}
	w.mu.Unlock()
	for _, name := range unhealthy {
		w.n.Raise(ctx, notify.Occurrence{Category: notify.ContainerUnhealthy, Key: name,
			Title: problemTitle(notify.ContainerUnhealthy, name), Link: containerLink(name), Baseline: baseline})
	}
	for _, c := range failed {
		w.n.Raise(ctx, notify.Occurrence{Category: notify.ContainerFailed, Key: c.Name,
			Title: failedTitle(c), Link: containerLink(c.Name), Baseline: baseline})
	}
	for _, name := range running {
		w.started(context.WithoutCancel(ctx), name)
	}
}

var reExitStatus = regexp.MustCompile(`^(?:Exited|Restarting) \((\d+)\)`)

// failedState reports whether c exited with an error (or is restarting
// after one), from its status line ("Exited (1) 3 minutes ago"), or is
// dead (Docker could not stop or remove it).
func failedState(c Container) bool {
	if c.State == "dead" {
		return true
	}
	if c.State != "exited" && c.State != "restarting" {
		return false
	}
	m := reExitStatus.FindStringSubmatch(c.Status)
	return m != nil && badExit(m[1])
}

func containerLink(name string) string { return "/containers/" + url.PathEscape(name) }

// updateNames lists the containers with an image update available. A
// transient check failure keeps the previous answer (see merge), so a
// known update stays in the report.
func updateNames(l []Container) []string {
	var out []string
	for _, c := range l {
		if c.UpdateAvailable != nil && *c.UpdateAvailable {
			out = append(out, c.Name)
		}
	}
	return out
}

// setNotifier sends container problems and pending updates to n.
func (s *Service) setNotifier(n notifier) {
	s.notify = n
	s.watch = newHealthWatch(n)
	s.watch.stopSignal = s.containerStopSignal
}

// containerStopSignal reads name's configured stop signal (STOPSIGNAL)
// from container inspect; "" when none is set.
func (s *Service) containerStopSignal(ctx context.Context, name string) (string, bool) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var in struct {
		Config struct {
			StopSignal string `json:"StopSignal"`
		} `json:"Config"`
	}
	if err := s.c.call(cctx, http.MethodGet, "/containers/"+url.PathEscape(name)+"/json", nil, &in); err != nil {
		return "", false
	}
	return in.Config.StopSignal, true
}

// eventName is the container name of a Docker event ("name" attribute).
func eventName(attrs map[string]string) string {
	return strings.TrimPrefix(attrs["name"], "/")
}

// eventCode is what healthWatch.event takes as code for a Docker event:
// the signal of a kill, the old name of a rename, else the exit code.
func eventCode(action string, attrs map[string]string) string {
	switch action {
	case "kill":
		return attrs["signal"]
	case "rename":
		return strings.TrimPrefix(attrs["oldName"], "/")
	}
	return attrs["exitCode"]
}

// syncProblems runs sync against the current containers each time the
// events stream connects; the first time is the baseline. If the startup
// updates report is still owed (Docker was not up yet when Wargo
// started), it is sent now.
func (s *Service) syncProblems(ctx context.Context) {
	if s.notify == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	l, err := s.List(cctx, true)
	if err != nil {
		return
	}
	if s.takeStartupReport() {
		s.notify.ReportUpdates(ctx, "containers", updateNames(l), false)
	}
	s.mu.Lock()
	baseline := !s.synced
	s.synced = true
	s.mu.Unlock()
	s.watch.sync(ctx, l, baseline)
}

// reportStartupUpdates tells notifications which containers had an update
// pending when Wargo stopped, from the saved results, so the updates item
// is current before the first check. When Docker is not installed it
// sends an empty report; when the daemon is just not up yet it sends
// nothing, and syncProblems reports once Docker answers (an empty report
// now would drop the saved names and push them again on the next check).
func (s *Service) reportStartupUpdates(ctx context.Context) {
	if s.notify == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	l, err := s.List(cctx, true)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if !s.dockerAbsent() {
			return
		}
		l = nil
	}
	if s.takeStartupReport() {
		s.notify.ReportUpdates(ctx, "containers", updateNames(l), false)
	}
}

// takeStartupReport reports whether the startup updates report is still
// owed, and marks it sent.
func (s *Service) takeStartupReport() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	owed := !s.startupReported
	s.startupReported = true
	return owed
}

// reportUpdates tells notifications which containers have an update
// pending after a check that succeeded.
func (s *Service) reportUpdates(ctx context.Context, automatic bool) {
	if s.notify == nil {
		return
	}
	l, err := s.List(ctx, false)
	if err != nil {
		return
	}
	s.takeStartupReport()
	s.notify.ReportUpdates(ctx, "containers", updateNames(l), automatic)
}

// dockerMissing reports whether Docker is not installed here: no socket
// configured, or no socket and no docker CLI. A socket or CLI that exists
// means the daemon may just not be up yet.
func dockerMissing(socket string, lookPath func(string) (string, error)) bool {
	if socket == "" {
		return true
	}
	if _, err := os.Stat(socket); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	_, err := lookPath("docker")
	return err != nil
}

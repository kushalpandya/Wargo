// SPDX-License-Identifier: AGPL-3.0-only

// Package brew checks for and installs Homebrew updates on macOS and Linux.
// Homebrew refuses to run as root and belongs to the user who installed it,
// so Wargo runs it as its own user, and only when that user owns the
// Homebrew installation. Casks whose installers run sudo get the account
// password through SUDO_ASKPASS (see askpass.go) after the user approves.
package brew

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/pkg"
	"github.com/kushalpandya/wargo/internal/schedule"
	"github.com/kushalpandya/wargo/internal/server"
)

// Store keys.
const (
	keyLastCheck = "brew.lastCheck"
	keyRunning   = "brew.running"
	keyLastRun   = "brew.lastRun"
)

// Status is GET /api/brew/status and the sticky "brew" topic. Its fields
// mirror pkg.Status so the Updates page shares one set of components.
type Status struct {
	// Supported is false where Homebrew can't be managed (container mode).
	Supported bool `json:"supported"`
	// Available: a Homebrew installation was found.
	Available bool   `json:"available"`
	Backend   string `json:"backend"`
	Prefix    string `json:"prefix,omitempty"`
	Version   string `json:"version,omitempty"`
	// CanUpgrade is false when Wargo's user doesn't own Homebrew; Problem
	// says why.
	CanUpgrade     bool             `json:"canUpgrade"`
	Problem        string           `json:"problem,omitempty"`
	UpgradeCommand string           `json:"upgradeCommand,omitempty"`
	Checking       bool             `json:"checking"`
	LastCheck      *pkg.CheckResult `json:"lastCheck,omitempty"`
	Running        *jobs.Snapshot   `json:"running,omitempty"`
	LastRun        *pkg.RunRecord   `json:"lastRun,omitempty"`
	Fake           bool             `json:"fake,omitempty"`
}

type Service struct {
	d *server.Deps

	supported bool
	bin       string // absolute path of brew, "" when not installed
	prefix    string
	problem   string // why updates can't be installed, if they can't
	exe       string // this binary, for the askpass script
	logDir    string

	// fake (development only) reads canned check output from fakeDir and
	// runs fakeDir/upgrade.sh instead of brew upgrade.
	fake    bool
	fakeDir string

	mu       sync.Mutex
	checking bool
	starting bool // an update is being prepared (approval done, not yet running)
	version  string
	last     *pkg.CheckResult
	job      *jobs.Job
	cur      *pkg.RunRecord
	lastRun  *pkg.RunRecord

	sched *schedule.Hours
}

// candidates are the standard Homebrew locations. PATH is not searched: it
// can include user-writable directories, and these cover every supported
// install (Apple silicon, Intel Macs, Linux system-wide and per-user).
func candidates(home string) []string {
	if runtime.GOOS == "darwin" {
		return []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"}
	}
	c := []string{"/home/linuxbrew/.linuxbrew/bin/brew"}
	if home != "" {
		c = append(c, filepath.Join(home, ".linuxbrew/bin/brew"))
	}
	return c
}

// New builds the service. Set WARGO_BREW_FAKE=<dir> (development only) to
// read canned check output from dir and simulate upgrades with dir/upgrade.sh.
func New(d *server.Deps) *Service {
	s := &Service{d: d, supported: !host.Container(), logDir: d.Cfg.DataDir,
		sched: pkg.NewSchedule("brew.settings")}
	s.sched.Store, s.sched.Audit = d.Store, d.Audit
	s.exe, _ = os.Executable()
	if dir := os.Getenv("WARGO_BREW_FAKE"); dir != "" {
		s.fake, s.fakeDir = true, dir
		s.bin, s.prefix, s.version = "brew", "/opt/homebrew", "simulated"
		slog.Warn("!!! WARGO_BREW_FAKE: Homebrew checks and upgrades are SIMULATED; never set this in production !!!", "dir", dir)
	} else if s.supported {
		home, _ := os.UserHomeDir()
		s.detect(candidates(home))
	}
	if s.available() {
		cleanAskpassDirs()
	}
	ctx := context.Background()
	var lc pkg.CheckResult
	if ok, _ := d.Store.Get(ctx, keyLastCheck, &lc); ok {
		s.last = &lc
	}
	var lr pkg.RunRecord
	if ok, _ := d.Store.Get(ctx, keyLastRun, &lr); ok {
		s.lastRun = &lr
	}
	s.recoverInterrupted(ctx)
	d.Hub.Sticky("brew")
	return s
}

// detect finds brew and checks that this user may run it.
func (s *Service) detect(paths []string) {
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
			continue
		}
		s.bin = p
		s.prefix = filepath.Dir(filepath.Dir(p))
		break
	}
	if s.bin == "" {
		return
	}
	s.problem = ownerProblem(s.prefix, os.Getuid())
}

// ownerProblem explains why uid can't upgrade the Homebrew in prefix, or
// returns "". Homebrew must run as the user who owns its files.
func ownerProblem(prefix string, uid int) string {
	if uid == 0 {
		return "Homebrew can't be run as root."
	}
	dir := filepath.Join(prefix, "Cellar")
	if _, err := os.Stat(dir); err != nil {
		dir = prefix
	}
	st, err := os.Stat(dir)
	if err != nil {
		return "Homebrew's files can't be read."
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) == uid {
		return ""
	}
	owner := strconv.Itoa(int(sys.Uid))
	if u, err := user.LookupId(owner); err == nil {
		owner = u.Username
	}
	me := strconv.Itoa(uid)
	if u, err := user.LookupId(me); err == nil {
		me = u.Username
	}
	return fmt.Sprintf("Homebrew belongs to %s, but Wargo runs as %s, so it can list updates but not install them.", owner, me)
}

// recoverInterrupted closes an update that was running when Wargo stopped.
// brew upgrade runs as Wargo's child, so it ended with the old process.
func (s *Service) recoverInterrupted(ctx context.Context) {
	var rec pkg.RunRecord
	if ok, _ := s.d.Store.Get(ctx, keyRunning, &rec); !ok || rec.ID == "" {
		return
	}
	now := time.Now()
	rec.FinishedAt = &now
	rec.OK = false
	rec.Error = "Wargo stopped before the update finished. Check again to see what is still pending."
	if rec.Stage == "" {
		rec.Stage = StagePreparing
	}
	s.lastRun = &rec
	if s.last != nil {
		c := *s.last
		c.Stale = true
		s.last = &c
		s.d.Store.Set(ctx, keyLastCheck, s.last)
	}
	s.d.Store.Set(ctx, keyLastRun, &rec)
	s.d.Store.Delete(ctx, keyRunning)
}

func (s *Service) Capabilities(ctx context.Context) (string, any) {
	return "brew", map[string]any{"supported": s.supported, "available": s.available()}
}

func (s *Service) available() bool { return s.supported && s.bin != "" }

func (s *Service) status() Status {
	st := Status{Supported: s.supported, Available: s.available(), Backend: "brew", Fake: s.fake}
	if st.Available {
		st.Prefix = s.prefix
		st.CanUpgrade = s.problem == ""
		st.Problem = s.problem
		st.UpgradeCommand = "brew upgrade"
	}
	s.mu.Lock()
	st.Version = s.version
	st.Checking = s.checking
	st.LastCheck = s.last
	if s.job != nil {
		snap := s.job.Snapshot()
		st.Running = &snap
	}
	st.LastRun = s.lastRun
	s.mu.Unlock()
	return st
}

func (s *Service) publish() { s.d.Hub.Publish("brew", s.status()) }

// Run starts the background checks.
func (s *Service) Run(ctx context.Context) {
	if s.available() && !s.fake {
		go s.loadVersion(ctx)
	}
	s.publish()
	s.reportSaved(ctx)
	go s.sched.Every(ctx, "brew",
		s.available,
		func() time.Time {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.last == nil {
				return time.Time{}
			}
			return s.last.At
		},
		s.backgroundCheck)
	<-ctx.Done()
}

// reportSaved sends what the last check found (nothing where Homebrew is
// not available) to notifications as their baseline.
// A failed check keeps the list of the check before it; when there is none,
// nothing is sent and the next successful check is the baseline instead.
func (s *Service) reportSaved(ctx context.Context) {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if !s.available() {
		last = nil
	} else if last != nil && last.Error != "" && len(last.Updates) == 0 {
		return
	}
	s.reportUpdates(ctx, last, false)
}

// backgroundCheck lists updates unless a check or update is already
// running (schedule.ErrSkipped).
func (s *Service) backgroundCheck(ctx context.Context) error {
	if !s.begin() {
		return schedule.ErrSkipped
	}
	defer s.end()
	res, err := s.list(ctx, true)
	if err == nil {
		s.reportUpdates(ctx, res, true)
	}
	return err
}

// reportUpdates tells notifications which formulae and casks res has
// pending (pinned ones are left out). Only call it after a check that
// succeeded: an empty report would drop the updates item and push it
// again on the next good check.
func (s *Service) reportUpdates(ctx context.Context, res *pkg.CheckResult, automatic bool) {
	if s.d.Notify == nil {
		return
	}
	var names []string
	if res != nil {
		for _, u := range res.Updates {
			names = append(names, u.Name)
		}
	}
	s.d.Notify.ReportUpdates(ctx, "homebrew", names, automatic)
}

// recheck refreshes the list after an update, so the Updates page shows
// what is still outdated instead of the list from before it.
func (s *Service) recheck(runID string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if s.fake {
		// Simulated updates change nothing; pretend a successful one
		// installed everything (pinned formulae stay) so the page shows
		// what a real one would.
		if !ok {
			return
		}
		s.mu.Lock()
		if s.last != nil {
			c := *s.last
			c.At, c.Updates, c.Stale = time.Now(), []pkg.Update{}, false
			s.last = &c
		}
		s.mu.Unlock()
	} else if err := s.backgroundCheck(ctx); err != nil {
		if err != schedule.ErrSkipped {
			slog.Warn("brew: check after update failed", "err", err)
		}
		return
	}
	s.mu.Lock()
	last := pkg.TagAfterRun(&s.last, runID)
	s.mu.Unlock()
	if last != nil {
		s.d.Store.Set(ctx, keyLastCheck, last)
	}
	s.publish()
}

var reVersion = regexp.MustCompile(`^Homebrew (\S+)`)

func (s *Service) loadVersion(ctx context.Context) {
	out, _, err := s.brew(ctx, time.Minute, "--version")
	if err != nil {
		return
	}
	if m := reVersion.FindSubmatch(out); m != nil {
		s.mu.Lock()
		s.version = string(m[1])
		s.mu.Unlock()
		s.publish()
	}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/brew/status", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w, s.status()) })
	mux.HandleFunc("POST /api/brew/check", httpx.Handler(s.handleCheck))
	mux.HandleFunc("POST /api/brew/upgrade", httpx.Handler(s.handleUpgrade))
	mux.HandleFunc("GET /api/brew/log", httpx.Handler(s.handleLog))
	mux.HandleFunc("GET /api/brew/settings", httpx.Handler(s.sched.HandleGet))
	mux.HandleFunc("PUT /api/brew/settings", httpx.Handler(s.sched.HandlePut))
}

func (s *Service) requireAvailable() error {
	if !s.supported {
		return httpx.Errorf(http.StatusServiceUnavailable, "unsupported", "Homebrew can't be managed from a container")
	}
	if s.bin == "" {
		return httpx.Errorf(http.StatusServiceUnavailable, "unavailable", "Homebrew is not installed")
	}
	return nil
}

// begin marks a check as running unless a check or upgrade already is.
func (s *Service) begin() bool {
	s.mu.Lock()
	busy := s.checking || s.job != nil || s.starting
	if !busy {
		s.checking = true
	}
	s.mu.Unlock()
	if !busy {
		s.publish()
	}
	return !busy
}

func (s *Service) end() {
	s.mu.Lock()
	s.checking = false
	s.mu.Unlock()
	s.publish()
}

func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAvailable(); err != nil {
		return err
	}
	if !s.begin() {
		return httpx.Errorf(http.StatusConflict, "check_running", "A check or update is already running")
	}
	defer s.end()
	res, err := s.list(context.WithoutCancel(r.Context()), false)
	if err != nil {
		return httpx.Errorf(http.StatusBadGateway, "check_failed", err.Error())
	}
	s.reportUpdates(context.WithoutCancel(r.Context()), res, false)
	httpx.OK(w, res)
	return nil
}

// env is the environment brew runs with: Wargo's own, minus any askpass
// it inherited, with output that is easy to parse and no prompts.
func (s *Service) env(extra ...string) []string {
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "SUDO_ASKPASS", "PATH", "HOMEBREW_COLOR", "HOMEBREW_NO_AUTO_UPDATE":
			continue
		}
		out = append(out, e)
	}
	out = append(out,
		"PATH="+filepath.Join(s.prefix, "bin")+":"+filepath.Join(s.prefix, "sbin")+":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOMEBREW_NO_COLOR=1", "HOMEBREW_NO_EMOJI=1", "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_AUTO_UPDATE=1",
		"NONINTERACTIVE=1", "TERM=dumb",
	)
	return append(out, extra...)
}

// brew runs brew with args and returns stdout, or an error carrying the
// last line of stderr.
func (s *Service) brew(ctx context.Context, timeout time.Duration, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.bin, args...)
	cmd.Env = s.env()
	cmd.Dir = os.TempDir()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return out.Bytes(), errb.Bytes(), fmt.Errorf("brew %s timed out", args[0])
		}
		msg := lastLine(errb.Bytes())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), errb.Bytes(), fmt.Errorf("brew %s: %s", args[0], strings.TrimPrefix(msg, "Error: "))
	}
	return out.Bytes(), errb.Bytes(), nil
}

func lastLine(b []byte) string {
	l := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(l[len(l)-1])
}

// list refreshes Homebrew's package data (brew update) and asks which
// installed formulae and casks are outdated, then saves the result. A
// failed run keeps the previous list and only attaches the error.
func (s *Service) list(parent context.Context, background bool) (*pkg.CheckResult, error) {
	start := time.Now()
	updates, pinned, err := s.query(parent)
	res := &pkg.CheckResult{At: time.Now(), DurationMs: time.Since(start).Milliseconds(), Updates: updates, Extras: pinned}
	if res.Updates == nil {
		res.Updates = []pkg.Update{}
	}
	if res.Extras == nil {
		res.Extras = []pkg.Update{}
	}
	sortUpdates(res.Updates)
	sortUpdates(res.Extras)
	if background && parent.Err() != nil {
		return nil, parent.Err()
	}
	if err != nil {
		res.Error = err.Error()
		slog.Warn("brew: check failed", "err", err)
	}
	s.mu.Lock()
	if err != nil && s.last != nil {
		c := *s.last
		c.Error = res.Error
		res = &c
	}
	s.last = res
	s.mu.Unlock()
	if e := s.d.Store.Set(parent, keyLastCheck, res); e != nil {
		slog.Warn("brew: save check", "err", e)
	}
	return res, err
}

func (s *Service) query(ctx context.Context) (updates, pinned []pkg.Update, err error) {
	if s.fake {
		time.Sleep(800 * time.Millisecond)
		b, err := os.ReadFile(filepath.Join(s.fakeDir, "outdated.json"))
		if err != nil {
			return nil, nil, err
		}
		updates, pinned, err = parseOutdated(b)
		if info, e := os.ReadFile(filepath.Join(s.fakeDir, "info.json")); e == nil {
			applyInfo(updates, info)
			applyInfo(pinned, info)
		}
		return updates, pinned, err
	}
	// brew update writes to Homebrew's own files, which only its owner can.
	if s.problem == "" {
		if _, _, err := s.brew(ctx, 5*time.Minute, "update", "--quiet"); err != nil {
			return nil, nil, err
		}
	}
	out, _, err := s.brew(ctx, 2*time.Minute, "outdated", "--json=v2")
	if err != nil {
		return nil, nil, err
	}
	updates, pinned, err = parseOutdated(out)
	if err != nil {
		return nil, nil, fmt.Errorf("brew outdated: %w", err)
	}
	// Taps and password hints are extras: a failure here keeps the list.
	var formulae, casks []string
	for _, u := range append(append([]pkg.Update{}, updates...), pinned...) {
		if u.Source == "cask" {
			casks = append(casks, u.Name)
		} else {
			formulae = append(formulae, u.Name)
		}
	}
	if len(formulae) > 0 {
		if b, _, err := s.brew(ctx, 2*time.Minute, append([]string{"info", "--formula", "--json=v2"}, formulae...)...); err == nil {
			applyInfo(updates, b)
			applyInfo(pinned, b)
		}
	}
	if len(casks) > 0 {
		if b, _, err := s.brew(ctx, 2*time.Minute, append([]string{"info", "--cask", "--json=v2"}, casks...)...); err == nil {
			applyInfo(updates, b)
		}
	}
	return updates, pinned, nil
}

// sortUpdates lists formulae before casks, each by name.
func sortUpdates(u []pkg.Update) {
	sort.SliceStable(u, func(a, b int) bool {
		if u[a].Source != u[b].Source {
			return u[a].Source > u[b].Source // "formula" > "cask"
		}
		return u[a].Name < u[b].Name
	})
}

func (s *Service) handleUpgrade(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAvailable(); err != nil {
		return err
	}
	if s.problem != "" {
		return httpx.Errorf(http.StatusConflict, "not_owner", s.problem)
	}
	s.mu.Lock()
	busy := s.job != nil || s.checking || s.starting
	var pending []pkg.Update
	if s.last != nil && !s.last.Stale && s.last.Error == "" {
		pending = s.last.Updates
	}
	s.mu.Unlock()
	if busy {
		return httpx.Errorf(http.StatusConflict, "upgrade_running", "An update or check is already in progress")
	}
	if len(pending) == 0 {
		return httpx.Errorf(http.StatusConflict, "nothing_to_do", "Check for updates first")
	}
	pwNeeded := false
	for _, u := range pending {
		pwNeeded = pwNeeded || u.NeedsPassword
	}
	// Passwords only matter on macOS: Linux has no casks.
	pwNeeded = pwNeeded && (runtime.GOOS == "darwin" || s.fake)
	target := fmt.Sprintf("Install %d Homebrew update%s", len(pending), plural(len(pending)))

	var pw string
	if pwNeeded {
		if !elevate.Has(r) {
			return s.d.Elevate.ErrPasswordRequired("brew.upgrade", target)
		}
		if s.fake {
			pw = "simulated"
			s.d.Audit.Record(r, "brew.upgrade", target+" (simulated)", nil, map[string]any{"fake": true})
		} else {
			var err error
			if pw, err = s.d.Elevate.VerifyPassword(r, "brew.upgrade", target); err != nil {
				return err
			}
		}
	}

	// Claim the run before preparing it, so a second request or a
	// background check can't start in the meantime.
	s.mu.Lock()
	if s.job != nil || s.checking || s.starting {
		s.mu.Unlock()
		return httpx.Errorf(http.StatusConflict, "upgrade_running", "An update or check is already in progress")
	}
	s.starting = true
	s.mu.Unlock()
	started := false
	defer func() {
		if !started {
			s.mu.Lock()
			s.starting = false
			s.mu.Unlock()
		}
	}()

	if err := os.MkdirAll(s.logDir, 0o750); err != nil {
		return err
	}
	pruneLogs(s.logDir, 10)
	now := time.Now()
	id := now.Format("20060102-150405")
	rec := &pkg.RunRecord{ID: id, Log: filepath.Join(s.logDir, "brew-"+id+".log"), Backend: "brew",
		Packages: len(pending), StartedAt: now, Fake: s.fake}
	lf, err := os.OpenFile(rec.Log, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return httpx.Errorf(http.StatusConflict, "upgrade_running", "An update was just started")
	}
	ap, err := startAskpass(s.exe, pw)
	pw = ""
	if err != nil {
		lf.Close()
		os.Remove(rec.Log)
		return fmt.Errorf("prepare password helper: %w", err)
	}
	var cmd *exec.Cmd
	if s.fake {
		cmd = exec.Command("/bin/sh", filepath.Join(s.fakeDir, "upgrade.sh"))
	} else {
		cmd = exec.Command(s.bin, "upgrade")
	}
	cmd.Env = s.env("SUDO_ASKPASS=" + ap.script)
	cmd.Dir = os.TempDir()
	pr, pw2, err := os.Pipe()
	if err != nil {
		ap.Close()
		lf.Close()
		os.Remove(rec.Log)
		return err
	}
	// brew stays in Wargo's process group, so stopping Wargo (launchd,
	// systemd) stops the update too; recoverInterrupted then reports it.
	cmd.Stdout, cmd.Stderr = pw2, pw2
	if err := cmd.Start(); err != nil {
		pw2.Close()
		pr.Close()
		ap.Close()
		lf.Close()
		os.Remove(rec.Log)
		s.d.Audit.Record(r, "brew.upgrade", target, err, nil)
		return httpx.Errorf(http.StatusBadGateway, "upgrade_start_failed", "Could not start the update: "+err.Error())
	}
	pw2.Close()
	ap.setRoot(cmd.Process.Pid)
	if !pwNeeded {
		s.d.Audit.Record(r, "brew.upgrade", target, nil, map[string]any{"fake": s.fake})
	}
	rec.PID = cmd.Process.Pid
	if err := s.d.Store.Set(context.WithoutCancel(r.Context()), keyRunning, rec); err != nil {
		slog.Error("brew: persist running update", "err", err)
	}
	title := "Homebrew update"
	if s.fake {
		title += " (simulated)"
	}
	j := s.d.Jobs.Start("brew.upgrade", title, nil)
	j.Progress(StagePreparing, 0, "Starting")
	rec.JobID = j.ID()
	started = true
	s.mu.Lock()
	s.job, s.cur, s.starting = j, rec, false
	s.mu.Unlock()
	s.publish()
	go s.follow(rec, j, cmd, pr, lf, ap)
	httpx.OK(w, j.Snapshot())
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

const maxLineLen = 2000

// follow streams the update's output into the job and the log file until
// brew exits.
func (s *Service) follow(rec *pkg.RunRecord, j *jobs.Job, cmd *exec.Cmd, pr io.ReadCloser, lf *os.File, ap *askpass) {
	defer ap.Close()
	var (
		st    = progress{planned: rec.Packages}
		mu    sync.Mutex
		batch []string
	)
	flush := func() {
		mu.Lock()
		b := batch
		batch = nil
		p := st
		mu.Unlock()
		if len(b) > 0 {
			j.Log(strings.Join(b, "\n"))
		}
		j.Progress(p.Stage, p.Frac, p.Current)
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				flush()
			}
		}
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		raw := sc.Text()
		lf.WriteString(raw + "\n")
		line := pkg.CleanLine(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > maxLineLen {
			line = line[:maxLineLen]
		}
		mu.Lock()
		parseLine(line, &st)
		batch = append(batch, line)
		mu.Unlock()
	}
	pr.Close()
	waitErr := cmd.Wait()
	close(done)
	flush()
	lf.Close()

	var exit *int
	if cmd.ProcessState != nil {
		c := cmd.ProcessState.ExitCode()
		exit = &c
	}
	var runErr error
	switch {
	case st.sudoFail && waitErr != nil:
		runErr = errors.New("a cask needed an administrator password that wasn't accepted; update again and approve with your password")
	case waitErr != nil && exit != nil && *exit > 0:
		runErr = fmt.Errorf("brew exited with status %d", *exit)
	case waitErr != nil:
		runErr = waitErr
	}
	s.finish(rec, j, exit, runErr, st)
}

func (s *Service) finish(rec *pkg.RunRecord, j *jobs.Job, exit *int, runErr error, st progress) {
	now := time.Now()
	rec.FinishedAt = &now
	rec.ExitStatus = exit
	rec.OK = runErr == nil
	rec.Upgraded = st.Upgraded
	rec.Stage = st.Stage
	if runErr != nil {
		rec.Error = runErr.Error()
		j.Progress(st.Stage, st.Frac, "")
	} else {
		j.Progress(StageDone, 1, "")
	}
	j.Finish(runErr, map[string]any{"ok": rec.OK, "exitStatus": exit, "upgraded": rec.Upgraded, "log": rec.Log, "fake": rec.Fake})
	code := -1
	if exit != nil {
		code = *exit
	}
	slog.Info("brew: update finished", "ok", rec.OK, "exit", code, "fake", rec.Fake)
	result := "ok"
	if !rec.OK {
		result = "error: " + rec.Error
	}
	s.d.Audit.RecordSystem("brew.upgrade.finished", rec.ID, result)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.mu.Lock()
	if s.job == j {
		s.job, s.cur = nil, nil
	}
	s.lastRun = rec
	if s.last != nil {
		c := *s.last
		c.Stale = true
		s.last = &c
	}
	last := s.last
	s.mu.Unlock()
	s.d.Store.Set(ctx, keyLastRun, rec)
	s.d.Store.Delete(ctx, keyRunning)
	if last != nil {
		s.d.Store.Set(ctx, keyLastCheck, last)
	}
	s.publish()
	go s.recheck(rec.ID, rec.OK)
}

// handleLog returns the tail of the current or last update's log.
func (s *Service) handleLog(w http.ResponseWriter, r *http.Request) error {
	s.mu.Lock()
	rec := s.cur
	if rec == nil {
		rec = s.lastRun
	}
	s.mu.Unlock()
	if rec == nil {
		return httpx.ErrNotFound
	}
	if id := r.URL.Query().Get("id"); id != "" && id != rec.ID {
		return httpx.ErrNotFound
	}
	lines, err := pkg.TailLines(rec.Log, 256<<10)
	if err != nil {
		return httpx.ErrNotFound
	}
	httpx.OK(w, map[string]any{"id": rec.ID, "lines": lines})
	return nil
}

var logRe = regexp.MustCompile(`^brew-\d{8}-\d{6}\.log$`)

func pruneLogs(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range ents {
		if logRe.MatchString(e.Name()) {
			logs = append(logs, e.Name())
		}
	}
	sort.Strings(logs)
	for i := 0; i < len(logs)-keep; i++ {
		os.Remove(filepath.Join(dir, logs[i]))
	}
}

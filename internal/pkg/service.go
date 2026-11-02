// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/server"
)

// Store keys.
const (
	keyLastCheck = "pkg.lastCheck"
	keyRunning   = "pkg.running"
	keyLastRun   = "pkg.lastRun"
)

// exitMarker is printed by the wrapper shell after the upgrade command so
// the result is known even after systemd garbage-collects the unit.
const exitMarker = "[wargo] exit status "

// wrapperScript is a constant: the upgrade argv is passed as positional
// parameters, never interpolated into the script.
const wrapperScript = `"$@"; rc=$?; printf '\n` + exitMarker + `%d\n' "$rc"; exit "$rc"`

// CheckResult is the outcome of the last explicit update check.
type CheckResult struct {
	At           time.Time `json:"at"`
	DurationMs   int64     `json:"durationMs"`
	Updates      []Update  `json:"updates"`
	Extras       []Update  `json:"extras"`
	DownloadSize int64     `json:"downloadSize"`
	Error        string    `json:"error,omitempty"`
	// Stale is set after an upgrade ran; the list no longer reflects the host.
	Stale bool `json:"stale,omitempty"`
}

// RunRecord describes an upgrade run (persisted so Wargo can re-attach).
type RunRecord struct {
	ID             string       `json:"id"`
	Unit           string       `json:"unit"`
	Log            string       `json:"log"`
	Backend        string       `json:"backend"`
	Packages       int          `json:"packages"`
	StartedAt      time.Time    `json:"startedAt"`
	FinishedAt     *time.Time   `json:"finishedAt,omitempty"`
	Fake           bool         `json:"fake,omitempty"`
	PID            int          `json:"pid,omitempty"`
	JobID          string       `json:"jobId,omitempty"`
	OK             bool         `json:"ok"`
	Error          string       `json:"error,omitempty"`
	ExitStatus     *int         `json:"exitStatus,omitempty"`
	Upgraded       int          `json:"upgraded"`
	Stage          string       `json:"stage,omitempty"`
	RebootRequired bool         `json:"rebootRequired"`
	RebootReason   string       `json:"rebootReason,omitempty"`
	ConfigFiles    []ConfigFile `json:"configFiles,omitempty"`
}

// Reboot is the host's reboot-required state.
type Reboot struct {
	Required bool   `json:"required"`
	Reason   string `json:"reason,omitempty"`
}

// Status is GET /api/pkg/backend and the sticky "pkg" topic.
type Status struct {
	Distro           string         `json:"distro"`
	DistroID         string         `json:"distroId"`
	Backend          string         `json:"backend"`
	Available        bool           `json:"available"`
	Extras           []string       `json:"extras"`
	NeedsRootToCheck bool           `json:"needsRootToCheck"`
	UpgradeCommand   string         `json:"upgradeCommand,omitempty"`
	Checking         bool           `json:"checking"`
	LastCheck        *CheckResult   `json:"lastCheck,omitempty"`
	Running          *jobs.Snapshot `json:"running,omitempty"`
	LastRun          *RunRecord     `json:"lastRun,omitempty"`
	Reboot           Reboot         `json:"reboot"`
	Fake             bool           `json:"fake,omitempty"`
}

type Service struct {
	d       *server.Deps
	be      Backend
	osr     OSRelease
	run     Runner
	dataDir string
	extras  []string

	fake       bool
	fakeScript string

	ctx context.Context

	mu       sync.Mutex
	checking bool
	last     *CheckResult
	job      *jobs.Job
	cur      *RunRecord
	lastRun  *RunRecord
	reboot   Reboot
	rebootAt time.Time
}

// New builds the service. Set WARGO_PKG_FAKE=1 (development only) to
// simulate upgrades with a local script instead of the real package manager.
func New(d *server.Deps) *Service {
	s := &Service{
		d: d, osr: readOSRelease(), run: ExecRunner(5 * time.Minute),
		dataDir: filepath.Join(d.Cfg.DataDir, "pkg"), ctx: context.Background(),
	}
	s.be = Detect(s.osr, hasBin)
	if s.be != nil {
		switch b := s.be.(type) {
		case *pamacBackend:
			s.extras = append(s.extras, "aur")
		case *pacmanBackend:
			if b.aurHelper != "" {
				s.extras = append(s.extras, "aur")
			}
		}
	}
	if hasBin("flatpak") {
		s.extras = append(s.extras, "flatpak")
	}
	if os.Getenv("WARGO_PKG_FAKE") == "1" && os.Getenv("WARGO_PKG_FAKE_SCRIPT") == "" {
		slog.Error("WARGO_PKG_FAKE=1 ignored: WARGO_PKG_FAKE_SCRIPT is not set")
	} else if os.Getenv("WARGO_PKG_FAKE") == "1" {
		s.fake = true
		s.fakeScript = os.Getenv("WARGO_PKG_FAKE_SCRIPT")
		slog.Warn("!!! WARGO_PKG_FAKE=1: OS upgrades are SIMULATED with a local script; never set this in production !!!",
			"script", s.fakeScript)
		if f := os.Getenv("WARGO_PKG_FAKE_CHECK"); f != "" {
			s.run = fakeCheckRunner(s.run, f)
			slog.Warn("!!! WARGO_PKG_FAKE_CHECK: update checks read canned output !!!", "file", f)
		}
	}
	ctx := context.Background()
	var lc CheckResult
	if ok, _ := d.Store.Get(ctx, keyLastCheck, &lc); ok {
		s.last = &lc
	}
	var lr RunRecord
	if ok, _ := d.Store.Get(ctx, keyLastRun, &lr); ok {
		s.lastRun = &lr
	}
	d.Hub.Sticky("pkg")
	return s
}

// fakeCheckRunner answers `pamac checkupdates` from a file (dev only).
func fakeCheckRunner(next Runner, file string) Runner {
	return func(ctx context.Context, argv ...string) ([]byte, []byte, int, error) {
		if len(argv) > 1 && argv[0] == "pamac" && argv[1] == "checkupdates" {
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, nil, -1, err
			}
			time.Sleep(800 * time.Millisecond)
			return b, nil, 100, nil
		}
		return next(ctx, argv...)
	}
}

func (s *Service) Capabilities(ctx context.Context) (string, any) {
	v := map[string]any{"backend": "", "available": false, "distro": s.distro()}
	if s.be != nil {
		v["backend"] = s.be.Name()
		v["available"] = hasBin(s.be.Binary())
	}
	return "pkg", v
}

func (s *Service) distro() string {
	if s.osr.PrettyName != "" {
		return s.osr.PrettyName
	}
	return s.osr.ID
}

func (s *Service) status(ctx context.Context) Status {
	st := Status{Distro: s.distro(), DistroID: s.osr.ID, Extras: s.extras, Fake: s.fake}
	if st.Extras == nil {
		st.Extras = []string{}
	}
	if s.be != nil {
		st.Backend = s.be.Name()
		st.Available = hasBin(s.be.Binary())
		st.NeedsRootToCheck = s.be.CheckNeedsRoot()
		st.UpgradeCommand = strings.Join(s.be.UpgradeArgv(), " ")
	}
	s.mu.Lock()
	st.Checking = s.checking
	st.LastCheck = s.last
	if s.job != nil {
		snap := s.job.Snapshot()
		st.Running = &snap
	}
	st.LastRun = s.lastRun
	refresh := time.Since(s.rebootAt) > time.Minute
	st.Reboot = s.reboot
	s.mu.Unlock()
	if refresh && s.be != nil {
		req, reason := s.be.RebootRequired(ctx, s.run)
		s.mu.Lock()
		s.reboot, s.rebootAt = Reboot{Required: req, Reason: reason}, time.Now()
		st.Reboot = s.reboot
		s.mu.Unlock()
	}
	return st
}

func (s *Service) publish() {
	s.d.Hub.Publish("pkg", s.status(context.Background()))
}

// Run re-attaches to an upgrade that was running when Wargo stopped.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	var rec RunRecord
	if ok, _ := s.d.Store.Get(ctx, keyRunning, &rec); ok && rec.ID != "" {
		slog.Info("pkg: re-attaching to upgrade", "unit", rec.Unit, "log", rec.Log, "fake", rec.Fake)
		if rec.Fake && !s.fake {
			// A simulated run must never be resumed by a production instance.
			slog.Warn("pkg: discarding simulated upgrade record")
			s.d.Store.Delete(ctx, keyRunning)
		} else {
			s.attach(&rec)
		}
	}
	s.publish()
	<-ctx.Done()
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pkg/backend", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, s.status(r.Context()))
	})
	mux.HandleFunc("GET /api/pkg/updates", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		last := s.last
		s.mu.Unlock()
		httpx.OK(w, last)
	})
	mux.HandleFunc("POST /api/pkg/check", httpx.Handler(s.handleCheck))
	mux.HandleFunc("GET /api/pkg/precheck", httpx.Handler(s.handlePrecheck))
	mux.HandleFunc("POST /api/pkg/upgrade", httpx.Handler(s.handleUpgrade))
	mux.HandleFunc("GET /api/pkg/log", httpx.Handler(s.handleLog))
	mux.HandleFunc("POST /api/system/power", httpx.Handler(s.handlePower))
}

func (s *Service) requireBackend() error {
	if s.be == nil {
		return httpx.Errorf(http.StatusServiceUnavailable, "unsupported", "No supported package manager was found on this host")
	}
	if !hasBin(s.be.Binary()) {
		return httpx.Errorf(http.StatusServiceUnavailable, "unavailable", s.be.Binary()+" is not installed")
	}
	return nil
}

func (s *Service) handleCheck(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireBackend(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.job != nil {
		s.mu.Unlock()
		return httpx.Errorf(http.StatusConflict, "upgrade_running", "An upgrade is in progress")
	}
	if s.checking {
		s.mu.Unlock()
		return httpx.Errorf(http.StatusConflict, "check_running", "A check is already running")
	}
	s.checking = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.checking = false
		s.mu.Unlock()
		s.publish()
	}()

	if s.be.CheckNeedsRoot() {
		if !elevate.Has(r) {
			return s.d.Elevate.ErrRequired("pkg.refresh", "Refresh package databases")
		}
		s.publish()
		if _, err := s.d.Elevate.Run(r, "pkg.refresh", "Refresh package databases", s.be.RefreshArgv(), nil); err != nil {
			return err
		}
	} else {
		s.publish()
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	start := time.Now()
	ups, extras, err := s.be.Check(ctx, s.run)
	extras = append(extras, checkFlatpak(ctx, s.run)...)
	res := &CheckResult{At: time.Now(), DurationMs: time.Since(start).Milliseconds(), Updates: ups, Extras: extras}
	if res.Updates == nil {
		res.Updates = []Update{}
	}
	if res.Extras == nil {
		res.Extras = []Update{}
	}
	sortUpdates(res.Updates)
	sortUpdates(res.Extras)
	for _, u := range res.Updates {
		res.DownloadSize += u.Size
	}
	if err != nil {
		res.Error = err.Error()
		slog.Warn("pkg: check failed", "err", err)
	}
	s.mu.Lock()
	s.last = res
	s.rebootAt = time.Time{}
	s.mu.Unlock()
	if e := s.d.Store.Set(context.WithoutCancel(r.Context()), keyLastCheck, res); e != nil {
		slog.Warn("pkg: save check", "err", e)
	}
	if err != nil {
		return httpx.Errorf(http.StatusBadGateway, "check_failed", err.Error())
	}
	httpx.OK(w, res)
	return nil
}

func sortUpdates(u []Update) {
	sort.SliceStable(u, func(a, b int) bool {
		if u[a].Repo != u[b].Repo {
			return u[a].Repo < u[b].Repo
		}
		return u[a].Name < u[b].Name
	})
}

type precheck struct {
	Lock LockStatus `json:"lock"`
	Disk []DiskInfo `json:"disk"`
	// Blocked is set when the upgrade cannot start.
	Blocked string `json:"blocked,omitempty"`
}

func (s *Service) precheck() precheck {
	p := precheck{Disk: diskSpace()}
	if s.be != nil {
		p.Lock = checkLocks(s.be.LockFiles())
	}
	if p.Lock.Locked {
		p.Blocked = "Package manager is busy: " + p.Lock.Reason
	}
	for _, d := range p.Disk {
		if d.Free < minSpace {
			p.Blocked = fmt.Sprintf("Not enough free space on %s", d.Path)
		}
	}
	return p
}

func (s *Service) handlePrecheck(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireBackend(); err != nil {
		return err
	}
	httpx.OK(w, s.precheck())
	return nil
}

func (s *Service) handleUpgrade(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireBackend(); err != nil {
		return err
	}
	s.mu.Lock()
	busy := s.job != nil || s.checking
	pending := 0
	if s.last != nil {
		pending = len(s.last.Updates)
	}
	s.mu.Unlock()
	if busy {
		return httpx.Errorf(http.StatusConflict, "upgrade_running", "An upgrade or check is already in progress")
	}
	pc := s.precheck()
	if pc.Lock.Locked {
		return httpx.Errorf(http.StatusConflict, "locked", "Package manager is busy: "+pc.Lock.Reason).With(pc)
	}
	if pc.Blocked != "" {
		return httpx.Errorf(http.StatusConflict, "low_space", pc.Blocked).With(pc)
	}
	target := fmt.Sprintf("Install %d updates with %s", pending, s.be.Name())
	if !s.fake && !elevate.Has(r) {
		// Ask for approval before creating any files.
		return s.d.Elevate.ErrRequired("pkg.upgrade", target)
	}

	if err := os.MkdirAll(s.dataDir, 0o750); err != nil {
		return err
	}
	pruneLogs(s.dataDir, 10)
	now := time.Now()
	id := now.Format("20060102-150405")
	rec := &RunRecord{
		ID: id, Unit: "wargo-upgrade-" + id, Log: filepath.Join(s.dataDir, id+".log"),
		Backend: s.be.Name(), Packages: pending, StartedAt: now, Fake: s.fake,
	}
	lf, err := os.OpenFile(rec.Log, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return httpx.Errorf(http.StatusConflict, "upgrade_running", "An upgrade was just started")
	}

	if s.fake {
		slog.Warn("!!! WARGO_PKG_FAKE: running SIMULATED upgrade (no root, no package manager) !!!", "script", s.fakeScript)
		cmd := exec.Command("/bin/sh", "-c", wrapperScript, "wargo-upgrade", "/bin/sh", s.fakeScript)
		cmd.Stdout, cmd.Stderr = lf, lf
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		err = cmd.Start()
		lf.Close()
		s.d.Audit.Record(r, "pkg.upgrade", target+" (simulated)", err, map[string]any{"fake": true, "script": s.fakeScript})
		if err != nil {
			os.Remove(rec.Log)
			return err
		}
		rec.PID = cmd.Process.Pid
		go cmd.Wait()
	} else {
		lf.Close()
		argv := s.systemdRunArgv(rec)
		out, err := s.d.Elevate.Run(r, "pkg.upgrade", target, argv, nil)
		if err != nil {
			os.Remove(rec.Log)
			var he *httpx.Error
			if errors.As(err, &he) && he.Code == "command_failed" {
				return httpx.Errorf(http.StatusBadGateway, "upgrade_start_failed", "Could not start the upgrade: "+lastLine(out))
			}
			return err
		}
	}
	if err := s.d.Store.Set(context.WithoutCancel(r.Context()), keyRunning, rec); err != nil {
		slog.Error("pkg: persist running upgrade", "err", err)
	}
	j := s.attach(rec)
	httpx.OK(w, j.Snapshot())
	return nil
}

// systemdRunArgv builds the root command that starts the upgrade unit.
func (s *Service) systemdRunArgv(rec *RunRecord) []string {
	argv := []string{"systemd-run",
		"--unit=" + rec.Unit,
		"--collect",
		"--description=Wargo system upgrade (" + rec.Backend + ")",
		"--property=Type=exec",
		"--property=StandardOutput=append:" + rec.Log,
		"--property=StandardError=append:" + rec.Log,
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"--setenv=LC_ALL=C",
	}
	for _, e := range s.be.Env() {
		if !strings.HasPrefix(e, "DEBIAN_FRONTEND=") {
			argv = append(argv, "--setenv="+e)
		}
	}
	argv = append(argv, "--", "/bin/sh", "-c", wrapperScript, "wargo-upgrade")
	return append(argv, s.be.UpgradeArgv()...)
}

func pruneLogs(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, e.Name())
		}
	}
	sort.Strings(logs)
	for i := 0; i < len(logs)-keep; i++ {
		os.Remove(filepath.Join(dir, logs[i]))
	}
}

// attach starts a job that follows rec's log and unit until it finishes.
func (s *Service) attach(rec *RunRecord) *jobs.Job {
	title := "System upgrade (" + rec.Backend + ")"
	if rec.Fake {
		title += " — simulated"
	}
	j := s.d.Jobs.Start("pkg.upgrade", title, nil)
	j.Progress(StagePreparing, 0, "Starting")
	rec.JobID = j.ID()
	s.mu.Lock()
	s.job, s.cur = j, rec
	ctx := s.ctx
	s.mu.Unlock()
	s.publish()
	go s.follow(ctx, rec, j)
	return j
}

const maxLineLen = 2000

func (s *Service) follow(ctx context.Context, rec *RunRecord, j *jobs.Job) {
	f, err := os.Open(rec.Log)
	if err != nil {
		s.finish(rec, j, nil, fmt.Errorf("cannot read upgrade log: %w", err), ProgressState{})
		return
	}
	defer f.Close()
	var (
		st      ProgressState
		partial string
		exit    *int
		buf     = make([]byte, 64<<10)
		goneFor int
	)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	readAll := func() {
		for {
			n, err := f.Read(buf)
			if n > 0 {
				data := partial + string(buf[:n])
				parts := strings.Split(data, "\n")
				partial = parts[len(parts)-1]
				if len(partial) > 64<<10 {
					partial = partial[len(partial)-maxLineLen:]
				}
				var batch []string
				for _, raw := range parts[:len(parts)-1] {
					line := CleanLine(raw)
					if strings.HasPrefix(line, exitMarker) {
						if v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, exitMarker))); err == nil {
							exit = &v
						}
						continue
					}
					if strings.TrimSpace(line) == "" {
						continue
					}
					if len(line) > maxLineLen {
						line = line[:maxLineLen]
					}
					s.parse(rec, line, &st)
					batch = append(batch, line)
				}
				if len(batch) > 0 {
					j.Log(strings.Join(batch, "\n"))
				}
			}
			if err != nil || n < len(buf) {
				return
			}
		}
	}
	for {
		readAll()
		j.Progress(st.Stage, st.Frac, st.Current)
		if exit != nil {
			break
		}
		active, known := s.unitActive(ctx, rec)
		if known && !active {
			goneFor++
			// Give the final log write a moment to land.
			if goneFor >= 4 {
				readAll()
				break
			}
		} else {
			goneFor = 0
		}
		select {
		case <-ctx.Done():
			// Wargo is shutting down; the unit keeps running and the
			// record in the store lets the next start re-attach.
			return
		case <-tick.C:
		}
	}
	var runErr error
	switch {
	case exit == nil:
		runErr = errors.New("the upgrade ended without reporting an exit status; check the log")
	case *exit != 0:
		runErr = fmt.Errorf("the package manager exited with status %d", *exit)
	}
	s.finish(rec, j, exit, runErr, st)
}

func (s *Service) parse(rec *RunRecord, line string, st *ProgressState) {
	if s.be != nil && rec.Backend == s.be.Name() {
		s.be.ParseProgress(line, st)
		return
	}
	parsePacman(line, st)
}

// unitActive reports whether the upgrade is still running. known is false
// when the state could not be determined.
func (s *Service) unitActive(ctx context.Context, rec *RunRecord) (active, known bool) {
	if rec.Fake {
		if rec.PID <= 0 {
			return false, true
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", rec.PID))
		if err != nil {
			return false, true
		}
		// Field 3 is the state; Z = zombie (exited, not yet reaped).
		if i := strings.LastIndexByte(string(b), ')'); i > 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return false, true
		}
		return true, true
	}
	out, _, code, err := s.run(ctx, "systemctl", "show", rec.Unit+".service", "-p", "ActiveState,LoadState,SubState")
	if err != nil || code != 0 {
		return false, false
	}
	props := map[string]string{}
	for _, l := range lines(out) {
		if k, v, ok := strings.Cut(l, "="); ok {
			props[k] = v
		}
	}
	switch props["ActiveState"] {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true, true
	}
	return false, true
}

func (s *Service) finish(rec *RunRecord, j *jobs.Job, exit *int, runErr error, st ProgressState) {
	now := time.Now()
	rec.FinishedAt = &now
	rec.ExitStatus = exit
	rec.OK = runErr == nil
	rec.Upgraded = st.Upgraded
	rec.Stage = st.Stage
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if s.be != nil {
		rec.RebootRequired, rec.RebootReason = s.be.RebootRequired(ctx, s.run)
		rec.ConfigFiles = newOnly(findConfigFiles("/etc", s.be.ConfigSuffixes(), rec.StartedAt.Add(-2*time.Second)))
	}
	if runErr == nil {
		j.Progress(StageDone, 1, "")
	} else {
		j.Progress(st.Stage, st.Frac, "")
	}
	j.Finish(runErr, map[string]any{
		"ok": rec.OK, "exitStatus": exit, "upgraded": rec.Upgraded, "rebootRequired": rec.RebootRequired,
		"rebootReason": rec.RebootReason, "configFiles": rec.ConfigFiles, "log": rec.Log, "fake": rec.Fake,
	})
	slog.Info("pkg: upgrade finished", "unit", rec.Unit, "ok", rec.OK, "exit", exit, "fake", rec.Fake)
	s.d.Audit.RecordSystem("pkg.upgrade.finished", rec.Unit, resultText(rec))

	s.mu.Lock()
	if s.job == j {
		s.job, s.cur = nil, nil
	}
	s.lastRun = rec
	s.reboot, s.rebootAt = Reboot{Required: rec.RebootRequired, Reason: rec.RebootReason}, time.Now()
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
}

// newOnly keeps files created by this run, plus older ones as long as the
// list stays short (they still need review).
func newOnly(c []ConfigFile) []ConfigFile {
	var out []ConfigFile
	for _, f := range c {
		if f.New || len(out) < 20 {
			out = append(out, f)
		}
	}
	return out
}

func resultText(r *RunRecord) string {
	if r.OK {
		return "ok"
	}
	return "error: " + r.Error
}

// handleLog returns the tail of the current or last run's log.
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
	lines, err := tailLines(rec.Log, 256<<10)
	if err != nil {
		return httpx.ErrNotFound
	}
	httpx.OK(w, map[string]any{"id": rec.ID, "lines": lines})
	return nil
}

func tailLines(path string, limit int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(0, st.Size()-limit)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, err
	}
	raw := strings.Split(string(b), "\n")
	if off > 0 && len(raw) > 0 {
		raw = raw[1:]
	}
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = CleanLine(l)
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, exitMarker) {
			continue
		}
		if len(l) > maxLineLen {
			l = l[:maxLineLen]
		}
		out = append(out, l)
	}
	return out, nil
}

func (s *Service) handlePower(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Action string `json:"action"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if req.Action != "reboot" && req.Action != "poweroff" {
		return httpx.BadRequest("action must be reboot or poweroff")
	}
	s.mu.Lock()
	busy := s.job != nil
	s.mu.Unlock()
	if busy {
		return httpx.Errorf(http.StatusConflict, "upgrade_running", "An upgrade is in progress; wait for it to finish")
	}
	target := map[string]string{"reboot": "Reboot the server", "poweroff": "Power off the server"}[req.Action]
	if !elevate.Has(r) {
		return s.d.Elevate.ErrRequired("system.power", target)
	}
	// Elevate.Run records the audit entry (incl. failures).
	if _, err := s.d.Elevate.Run(r, "system.power", target, s.d.Elevate.Helper("power", req.Action), nil); err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"ok": true, "action": req.Action})
	return nil
}

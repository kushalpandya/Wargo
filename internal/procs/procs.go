// SPDX-License-Identifier: AGPL-3.0-only

// Package procs lists host processes from /proc, computes CPU usage between
// samples and lets the user signal them (elevated for foreign processes).
package procs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/helper"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Topic carries the process list while someone is watching.
const Topic = "procs"

// minInterval is the minimum time between two /proc scans; concurrent
// clients share the cached sample.
const minInterval = time.Second

// Proc is one row of the process table.
type Proc struct {
	PID       int     `json:"pid"`
	PPID      int     `json:"ppid"`
	Name      string  `json:"name"`
	User      string  `json:"user"`
	UID       int     `json:"uid"`
	State     string  `json:"state"`
	CPU       float64 `json:"cpu"` // percent of one core (top-style; may exceed 100)
	MemRSS    uint64  `json:"memRSS"`
	MemPct    float64 `json:"memPct"`
	Threads   int     `json:"threads"`
	StartTime int64   `json:"startTime"` // unix ms
	Command   string  `json:"command"`
	Nice      int     `json:"nice"`
	Kernel    bool    `json:"kernel,omitempty"` // kernel thread (no cmdline, child of kthreadd)

	startTicks uint64 // raw start time, to detect pid reuse
}

// List is the payload of GET /api/procs and the "procs" topic.
type List struct {
	Procs     []Proc `json:"procs"`
	UID       int    `json:"uid"`
	Self      int    `json:"self"` // Wargo's own pid
	TotalMem  uint64 `json:"totalMem"`
	CPUs      int    `json:"cpus"`
	SampledAt int64  `json:"sampledAt"` // unix ms
}

// ContainerResolver maps a full container id to its name ("" if unknown).
type ContainerResolver func(ctx context.Context, id string) string

type Service struct {
	hub     *events.Hub
	audit   *audit.Log
	elevate *elevate.Service
	resolve ContainerResolver
	root    string // "/proc"

	mu       sync.Mutex
	last     *List
	stale    bool
	prev     map[int]cpuSample
	prevAt   time.Time
	users    map[int]string
	pageSize uint64
	bootTime int64
	scanBuf  [2048]byte // reused by scan (under mu)
}

type cpuSample struct {
	ticks uint64
	start uint64
}

func New(hub *events.Hub, al *audit.Log, el *elevate.Service, resolve ContainerResolver) *Service {
	return &Service{
		hub: hub, audit: al, elevate: el, resolve: resolve, root: "/proc",
		users: map[int]string{}, pageSize: uint64(os.Getpagesize()),
	}
}

func (s *Service) userName(uid int) string {
	if n, ok := s.users[uid]; ok {
		return n
	}
	n := strconv.Itoa(uid)
	if u, err := host.LookupUID(uid); err == nil {
		n = u.Name
	}
	s.users[uid] = n
	return n
}

// Sample returns a process list no older than minInterval.
func (s *Service) Sample() *List {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil && !s.stale && time.Since(time.UnixMilli(s.last.SampledAt)) < minInterval {
		return s.last
	}
	s.last, s.stale = s.scan(), false
	return s.last
}

func (s *Service) scan() *List {
	now := time.Now()
	if s.bootTime == 0 {
		s.bootTime = readBootTime(s.root)
	}
	l := &List{UID: host.AdminUID(), Self: os.Getpid(), TotalMem: readMeminfoTotal(s.root), CPUs: runtime.NumCPU(), SampledAt: now.UnixMilli()}
	names := readDirNames(s.root)
	cur := make(map[int]cpuSample, len(names))
	dt := now.Sub(s.prevAt).Seconds()
	l.Procs = make([]Proc, 0, len(names))
	// One buffer for every small /proc read of this scan: 500+ processes
	// are scanned every second or two, so per-file allocations add up.
	buf := s.scanBuf[:]
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		dir := s.root + "/" + name
		n, err := readInto(dir+"/stat", buf)
		if err != nil || n == len(buf) {
			continue // exited (or a stat line we can't trust)
		}
		st, err := parseStat(buf[:n])
		if err != nil {
			continue
		}
		uid, _ := ownerUID(dir)
		p := Proc{
			PID: pid, PPID: st.PPID, Name: st.Name, UID: uid, User: s.userName(uid), State: st.State,
			Threads: st.Threads, Nice: st.Nice,
			MemRSS:     uint64(max(st.RSSPages, 0)) * s.pageSize,
			StartTime:  (s.bootTime*1000 + int64(st.StartTime)*1000/clkTck),
			startTicks: st.StartTime,
		}
		if l.TotalMem > 0 {
			p.MemPct = float64(p.MemRSS) / float64(l.TotalMem) * 100
		}
		ticks := st.UTime + st.STime
		cur[pid] = cpuSample{ticks: ticks, start: st.StartTime}
		if prev, ok := s.prev[pid]; ok && prev.start == st.StartTime && dt > 0 && ticks >= prev.ticks {
			p.CPU = float64(ticks-prev.ticks) / clkTck / dt * 100
		}
		// Only the first maxCmdline bytes are shown; don't read megabytes
		// of arguments.
		if n, err := readInto(dir+"/cmdline", buf[:maxCmdline]); err == nil {
			p.Command = cleanCmdline(buf[:n], maxCmdline)
		}
		if p.Command == "" && (st.PPID == 2 || pid == 2) {
			p.Kernel = true
		}
		l.Procs = append(l.Procs, p)
	}
	s.prev, s.prevAt = cur, now
	return l
}

// Run publishes the list every 2s while someone is subscribed.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.hub.Subscribers(Topic) > 0 {
				s.hub.Publish(Topic, s.Sample())
			}
		}
	}
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/procs", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, s.Sample())
		return nil
	}))
	mux.HandleFunc("GET /api/procs/{pid}", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		pid, err := strconv.Atoi(r.PathValue("pid"))
		if err != nil || pid <= 0 {
			return httpx.BadRequest("invalid pid")
		}
		d, err := s.Details(r.Context(), pid)
		if err != nil {
			return err
		}
		httpx.OK(w, d)
		return nil
	}))
	mux.HandleFunc("POST /api/procs/{pid}/signal", httpx.Handler(s.handleSignal))
}

// Details is the payload of GET /api/procs/{pid}.
type Details struct {
	Proc
	Cmdline       string   `json:"cmdline"`
	Args          []string `json:"args,omitempty"`
	Exe           string   `json:"exe,omitempty"`
	Cwd           string   `json:"cwd,omitempty"`
	EnvCount      *int     `json:"envCount,omitempty"`
	FDCount       *int     `json:"fdCount,omitempty"`
	Cgroup        string   `json:"cgroup,omitempty"`
	ContainerID   string   `json:"containerId,omitempty"`
	ContainerName string   `json:"containerName,omitempty"`
	Children      []int    `json:"children"`
	Mem           struct {
		RSS    uint64 `json:"rss"`
		VMS    uint64 `json:"vms"`
		Shared uint64 `json:"shared"`
		Swap   uint64 `json:"swap"`
	} `json:"mem"`
	IO *struct {
		Read  uint64 `json:"read"`
		Write uint64 `json:"write"`
	} `json:"io,omitempty"`
	Status struct {
		RealUID        int    `json:"realUid"`
		VolCtxSwitches uint64 `json:"volCtxSwitches"`
		InvCtxSwitches uint64 `json:"invCtxSwitches"`
		ParentName     string `json:"parentName,omitempty"`
	} `json:"status"`
	Readable bool `json:"readable"` // whether privileged fields (cwd, fds, io) could be read
}

func (s *Service) find(pid int) (Proc, bool) {
	l := s.Sample()
	for _, p := range l.Procs {
		if p.PID == pid {
			return p, true
		}
	}
	return Proc{}, false
}

func (s *Service) Details(ctx context.Context, pid int) (*Details, error) {
	l := s.Sample()
	var d Details
	found := false
	names := map[int]string{}
	for _, p := range l.Procs {
		names[p.PID] = p.Name
		if p.PID == pid {
			d.Proc, found = p, true
		}
	}
	if !found {
		return nil, httpx.ErrNotFound
	}
	d.Children = []int{}
	for _, p := range l.Procs {
		if p.PPID == pid {
			d.Children = append(d.Children, p.PID)
		}
	}
	sort.Ints(d.Children)
	d.Status.ParentName = names[d.PPID]
	dir := filepath.Join(s.root, strconv.Itoa(pid))
	cbuf := make([]byte, 64<<10)
	if n, err := readInto(filepath.Join(dir, "cmdline"), cbuf); err == nil {
		raw := cbuf[:n]
		d.Cmdline = cleanCmdline(raw, 0)
		for _, a := range strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00") {
			if a != "" && len(d.Args) < 256 {
				d.Args = append(d.Args, strings.ToValidUTF8(a, "?"))
			}
		}
	}
	if v, err := os.Readlink(filepath.Join(dir, "exe")); err == nil {
		d.Exe = v
		d.Readable = true
	}
	if v, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil {
		d.Cwd = v
	}
	if b, err := os.ReadFile(filepath.Join(dir, "environ")); err == nil {
		n := 0
		for _, part := range strings.Split(string(b), "\x00") {
			if part != "" {
				n++
			}
		}
		d.EnvCount = &n
	}
	if f, err := os.Open(filepath.Join(dir, "fd")); err == nil {
		names, err := f.Readdirnames(-1)
		f.Close()
		if err == nil {
			n := len(names)
			d.FDCount = &n
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup")); err == nil {
		d.Cgroup = strings.TrimSpace(string(b))
		if len(d.Cgroup) > 2048 {
			d.Cgroup = d.Cgroup[:2048]
		}
		if id := containerFromCgroup(b); id != "" {
			d.ContainerID = id
			if s.resolve != nil {
				rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				d.ContainerName = s.resolve(rctx, id)
				cancel()
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "statm")); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 3 {
			vms, _ := strconv.ParseUint(f[0], 10, 64)
			rss, _ := strconv.ParseUint(f[1], 10, 64)
			sh, _ := strconv.ParseUint(f[2], 10, 64)
			d.Mem.VMS, d.Mem.RSS, d.Mem.Shared = vms*s.pageSize, rss*s.pageSize, sh*s.pageSize
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		kv := parseKV(b)
		d.Mem.Swap = kb(kv["VmSwap"])
		if f := strings.Fields(kv["Uid"]); len(f) > 0 {
			d.Status.RealUID, _ = strconv.Atoi(f[0])
		}
		d.Status.VolCtxSwitches, _ = strconv.ParseUint(kv["voluntary_ctxt_switches"], 10, 64)
		d.Status.InvCtxSwitches, _ = strconv.ParseUint(kv["nonvoluntary_ctxt_switches"], 10, 64)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "io")); err == nil {
		kv := parseKV(b)
		io := &struct {
			Read  uint64 `json:"read"`
			Write uint64 `json:"write"`
		}{}
		io.Read, _ = strconv.ParseUint(kv["read_bytes"], 10, 64)
		io.Write, _ = strconv.ParseUint(kv["write_bytes"], 10, 64)
		d.IO = io
	}
	return &d, nil
}

func (s *Service) handleSignal(w http.ResponseWriter, r *http.Request) error {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil {
		return httpx.BadRequest("invalid pid")
	}
	var req struct {
		Signal string `json:"signal"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	name := strings.ToUpper(strings.TrimPrefix(strings.ToUpper(req.Signal), "SIG"))
	sig, ok := helper.Signal(name)
	if !ok {
		return httpx.BadRequest("unsupported signal")
	}
	if pid <= 1 {
		return httpx.Errorf(http.StatusForbidden, "forbidden", "Refusing to signal PID 1 or below")
	}
	if pid == os.Getpid() || host.Container() && pid == os.Getppid() {
		return httpx.Errorf(http.StatusForbidden, "forbidden", "Refusing to signal Wargo itself")
	}
	p, ok := s.find(pid)
	if !ok {
		return httpx.ErrNotFound
	}
	target := fmt.Sprintf("SIG%s pid %d (%s, user %s)", name, pid, p.Name, p.User)
	err = signalVerified(pid, sig, func() error { return s.verifyTarget(p) })
	switch {
	case err == nil:
		s.audit.Record(r, "procs.signal", target, nil, map[string]any{"pid": pid, "signal": name})
	case errors.Is(err, syscall.ESRCH):
		return httpx.ErrNotFound
	case errors.Is(err, syscall.EPERM):
		if _, err := s.elevate.Run(r, "procs.signal", target, s.elevate.Helper("kill", name, strconv.Itoa(pid), strconv.FormatUint(p.startTicks, 10)), nil); err != nil {
			return err
		}
	default:
		s.audit.Record(r, "procs.signal", target, err, map[string]any{"pid": pid, "signal": name})
		return err
	}
	s.invalidate()
	httpx.OK(w, map[string]any{"ok": true, "pid": pid, "signal": name})
	return nil
}

// verifyTarget checks that the process now holding p.PID is the one the
// user picked (same start time, so the pid wasn't reused) and, in
// container mode, that it belongs to the admin user. Wargo is root there
// and could signal anything; keep the native rule that other users'
// processes need approval (EPERM routes to elevation).
func (s *Service) verifyTarget(p Proc) error {
	dir := filepath.Join(s.root, strconv.Itoa(p.PID))
	var buf [1024]byte
	n, err := readInto(filepath.Join(dir, "stat"), buf[:])
	if err != nil {
		return syscall.ESRCH
	}
	st, err := parseStat(buf[:n])
	if err != nil || st.StartTime != p.startTicks {
		return syscall.ESRCH
	}
	if host.Container() {
		if uid, ok := ownerUID(dir); !ok || uid != host.AdminUID() {
			return syscall.EPERM
		}
	}
	return nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.stale = true
	s.mu.Unlock()
}

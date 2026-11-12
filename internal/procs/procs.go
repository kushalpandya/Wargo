// SPDX-License-Identifier: AGPL-3.0-only

// Package procs lists host processes (Linux /proc, macOS libproc), computes CPU usage between
// samples and lets the user signal them (elevated for foreign processes).
package procs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
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
	scanArgs []byte     // darwin: reused kern.procargs2 buffer (under mu)
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
	s.platformDetails(ctx, &d)
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
	err = host.SignalVerified(pid, sig, func() error { return s.verifyTarget(p) })
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

func (s *Service) invalidate() {
	s.mu.Lock()
	s.stale = true
	s.mu.Unlock()
}

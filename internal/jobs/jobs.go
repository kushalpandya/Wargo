// SPDX-License-Identifier: AGPL-3.0-only

// Package jobs tracks long-running tasks (copies, archives, upgrades) and
// streams their progress on the "jobs" topic.
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
)

type State string

const (
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Canceled  State = "canceled"
)

const maxLogLines = 2000

// Snapshot is the JSON view of a job.
type Snapshot struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Title      string         `json:"title"`
	State      State          `json:"state"`
	Stage      string         `json:"stage,omitempty"`
	Progress   float64        `json:"progress"` // 0..1, -1 = indeterminate
	Current    string         `json:"current,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	LogLines   int            `json:"logLines"`
}

type Job struct {
	m      *Manager
	mu     sync.Mutex
	s      Snapshot
	log    []string
	cancel func()
	last   time.Time
}

type Manager struct {
	hub  *events.Hub
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewManager(hub *events.Hub) *Manager {
	return &Manager{hub: hub, jobs: map[string]*Job{}}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Start registers a new running job. cancel may be nil.
func (m *Manager) Start(kind, title string, cancel func()) *Job {
	j := &Job{m: m, cancel: cancel, s: Snapshot{
		ID: newID(), Kind: kind, Title: title, State: Running, Progress: -1, StartedAt: time.Now(),
	}}
	m.mu.Lock()
	m.jobs[j.s.ID] = j
	// Keep the most recent 50 finished jobs.
	if len(m.jobs) > 60 {
		var done []*Job
		for _, x := range m.jobs {
			if x.Snapshot().State != Running {
				done = append(done, x)
			}
		}
		sort.Slice(done, func(a, b int) bool { return done[a].s.StartedAt.Before(done[b].s.StartedAt) })
		for i := 0; i < len(done)-50; i++ {
			delete(m.jobs, done[i].s.ID)
		}
	}
	m.mu.Unlock()
	j.publish(true)
	return j
}

func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	out := make([]Snapshot, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.Snapshot())
	}
	m.mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].StartedAt.After(out[b].StartedAt) })
	return out
}

func (j *Job) ID() string { return j.s.ID }

func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.s
	s.LogLines = len(j.log)
	return s
}

// publish throttles progress events to ~10/s unless force is set.
func (j *Job) publish(force bool) {
	j.mu.Lock()
	if !force && time.Since(j.last) < 100*time.Millisecond {
		j.mu.Unlock()
		return
	}
	j.last = time.Now()
	s := j.s
	s.LogLines = len(j.log)
	j.mu.Unlock()
	j.m.hub.Publish("jobs", s)
}

// Progress updates stage, fraction (0..1, or -1) and current item.
func (j *Job) Progress(stage string, frac float64, current string) {
	j.mu.Lock()
	if stage != "" {
		j.s.Stage = stage
	}
	j.s.Progress = frac
	j.s.Current = current
	j.mu.Unlock()
	j.publish(false)
}

// Log appends output lines and streams them on "jobs.log".
func (j *Job) Log(text string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	j.mu.Lock()
	j.log = append(j.log, lines...)
	if len(j.log) > maxLogLines {
		j.log = j.log[len(j.log)-maxLogLines:]
	}
	j.mu.Unlock()
	j.m.hub.Publish("jobs.log", map[string]any{"id": j.s.ID, "lines": lines})
}

func (j *Job) Lines() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.log...)
}

// Finish marks the job done. err == nil means success.
func (j *Job) Finish(err error, result map[string]any) {
	now := time.Now()
	j.mu.Lock()
	if j.s.State == Running {
		j.s.State = Succeeded
		if err != nil {
			j.s.State = Failed
			j.s.Error = err.Error()
		}
	}
	j.s.FinishedAt = &now
	j.s.Result = result
	if j.s.State == Succeeded {
		j.s.Progress = 1
	}
	j.mu.Unlock()
	j.publish(true)
}

func (j *Job) Cancel() bool {
	j.mu.Lock()
	c := j.cancel
	running := j.s.State == Running
	if running && c != nil {
		j.s.State = Canceled
	}
	j.mu.Unlock()
	if running && c != nil {
		c()
		j.publish(true)
		return true
	}
	return false
}

// Register mounts the jobs API.
func (m *Manager) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, m.List())
	})
	mux.HandleFunc("GET /api/jobs/{id}", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		j := m.Get(r.PathValue("id"))
		if j == nil {
			return httpx.ErrNotFound
		}
		httpx.OK(w, map[string]any{"job": j.Snapshot(), "log": j.Lines()})
		return nil
	}))
	mux.HandleFunc("POST /api/jobs/{id}/cancel", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		j := m.Get(r.PathValue("id"))
		if j == nil {
			return httpx.ErrNotFound
		}
		httpx.OK(w, map[string]bool{"canceled": j.Cancel()})
		return nil
	}))
}

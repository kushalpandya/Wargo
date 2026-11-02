// SPDX-License-Identifier: AGPL-3.0-only

// Package sysevents gathers recent errors and warnings for System → Events:
// the systemd journal (system services and the kernel), the Docker
// daemon's recent container events, and Wargo's own audit log. Repeated
// messages are grouped so a device logging the same error thousands of
// times shows up once, with a count.
package sysevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Sources.
const (
	SrcSystem     = "system"
	SrcKernel     = "kernel"
	SrcContainers = "containers"
	SrcWargo      = "wargo"
)

// Levels.
const (
	LevelError   = "error"
	LevelWarning = "warning"
)

const (
	maxGroups  = 500
	maxMessage = 2000
	cacheTTL   = 20 * time.Second
)

var ranges = map[string]time.Duration{"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

// Event is a group of identical messages from one origin.
type Event struct {
	Key     string `json:"key"`
	Source  string `json:"source"`
	Level   string `json:"level"`
	Origin  string `json:"origin"` // program, container or Wargo area
	Unit    string `json:"unit,omitempty"`
	Message string `json:"message"`
	Count   int    `json:"count"`
	First   int64  `json:"first"` // unix ms
	Last    int64  `json:"last"`
}

// SourceStatus says whether a source could be read, and why not.
type SourceStatus struct {
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
}

// Response is the result of one query.
type Response struct {
	Events    []Event                 `json:"events"`
	Errors    int                     `json:"errors"`   // occurrences, not groups
	Warnings  int                     `json:"warnings"` // occurrences, not groups
	Sources   map[string]SourceStatus `json:"sources"`
	Since     int64                   `json:"since"`
	Truncated bool                    `json:"truncated,omitempty"`
}

// raw is one occurrence before grouping.
type raw struct {
	source, level, origin, unit, message string
	ts                                   time.Time
}

type Service struct {
	st     *store.Store
	docker *docker.Client

	// Overridable in tests.
	journal func(ctx context.Context, since time.Time, warnings bool) ([]raw, SourceStatus)
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at  time.Time
	res Response
}

func New(st *store.Store, dockerSocket string) *Service {
	s := &Service{st: st, journal: readJournal, now: time.Now, cache: map[string]cached{}}
	if dockerSocket != "" {
		s.docker = docker.Shared(dockerSocket)
	}
	return s
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/events", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		rng := r.URL.Query().Get("range")
		if rng == "" {
			rng = "24h"
		}
		if _, ok := ranges[rng]; !ok {
			return httpx.BadRequest("range must be 1h, 24h or 7d")
		}
		level := r.URL.Query().Get("level")
		if level == "" {
			level = LevelWarning
		}
		if level != LevelError && level != LevelWarning {
			return httpx.BadRequest("level must be error or warning")
		}
		httpx.OK(w, s.Query(r.Context(), rng, level == LevelWarning, r.URL.Query().Get("fresh") == "1"))
		return nil
	}))
}

// Query returns grouped events for the range; warnings includes warnings
// as well as errors. Results are cached briefly per range and level.
func (s *Service) Query(ctx context.Context, rng string, warnings bool, fresh bool) Response {
	key := rng
	if warnings {
		key += "+w"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[key]; ok && !fresh && s.now().Sub(c.at) < cacheTTL {
		return c.res
	}
	now := s.now()
	since := now.Add(-ranges[rng])
	res := Response{Sources: map[string]SourceStatus{}, Since: since.UnixMilli()}

	var all []raw
	j, st := s.journal(ctx, since, warnings)
	all = append(all, j...)
	res.Sources[SrcSystem] = st
	res.Sources[SrcKernel] = st

	c, st := s.containerEvents(ctx, since, now, warnings)
	all = append(all, c...)
	res.Sources[SrcContainers] = st

	a, st := s.auditEvents(ctx, since)
	all = append(all, a...)
	res.Sources[SrcWargo] = st

	res.Events, res.Errors, res.Warnings, res.Truncated = group(all, warnings)
	s.cache[key] = cached{at: s.now(), res: res}
	return res
}

func group(all []raw, warnings bool) (events []Event, errs, warns int, truncated bool) {
	by := map[string]*Event{}
	for _, e := range all {
		if e.level == LevelWarning && !warnings {
			continue
		}
		if e.level == LevelError {
			errs++
		} else {
			warns++
		}
		msg := clip(strings.TrimSpace(e.message), maxMessage)
		h := sha256.Sum256([]byte(e.source + "\x00" + e.origin + "\x00" + msg))
		k := hex.EncodeToString(h[:8])
		ms := e.ts.UnixMilli()
		g, ok := by[k]
		if !ok {
			g = &Event{Key: k, Source: e.source, Level: e.level, Origin: e.origin, Unit: e.unit, Message: msg, First: ms, Last: ms}
			by[k] = g
		}
		g.Count++
		g.First = min(g.First, ms)
		g.Last = max(g.Last, ms)
		if e.level == LevelError {
			g.Level = LevelError
		}
	}
	events = make([]Event, 0, len(by))
	for _, g := range by {
		events = append(events, *g)
	}
	slices.SortFunc(events, func(a, b Event) int {
		if a.Last != b.Last {
			if a.Last > b.Last {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
	if len(events) > maxGroups {
		events, truncated = events[:maxGroups], true
	}
	return
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

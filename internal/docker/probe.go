// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/dashicons"
	"github.com/kushalpandya/wargo/internal/netx"
)

// Probe is Wargo's own reachability check for a container that doesn't
// define a Docker HEALTHCHECK: a single small HTTP request to its web UI
// port. It says whether something answers, not whether the app is healthy.
type Probe struct {
	Status     string `json:"status"` // "up" | "down"
	Port       int    `json:"port"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	LatencyMs  int64  `json:"latencyMs,omitempty"`
	Error      string `json:"error,omitempty"`
	CheckedAt  int64  `json:"checkedAt"` // unix ms
	IntervalS  int    `json:"intervalS"` // seconds between checks
}

// Probe pacing. Checks are deliberately rare: one request per container
// every few minutes, spread out, and only while someone uses Wargo.
const (
	probeInterval   = 5 * time.Minute
	probeRetry      = time.Minute      // re-check once before reporting "down"
	probeFirstDelay = 30 * time.Second // after a container (or Wargo) starts
	probeResume     = 10 * time.Second // re-check restored results soon after a restart
	probeIdleAfter  = 15 * time.Minute // stop when nobody looked for this long
	probeTimeout    = 4 * time.Second
	probeTick       = 15 * time.Second
	probeParallel   = 2
	probeReadLimit  = 1 << 10
)

type probeState struct {
	addr  string // host:port probed
	port  int
	res   *Probe // last reported result (nil until the first conclusive one)
	fails int    // consecutive failures
	next  time.Time
}

type prober struct {
	mu         sync.Mutex
	states     map[string]*probeState // full container id -> state
	lastViewed time.Time
	client     *http.Client
	now        func() time.Time
}

func newProber() *prober {
	return &prober{
		states: map[string]*probeState{},
		client: &http.Client{
			Timeout:       probeTimeout,
			Transport:     netx.NewTransport(probeTimeout),
			CheckRedirect: netx.NoRedirects,
		},
		now: time.Now,
	}
}

func (p *prober) viewed() {
	p.mu.Lock()
	p.lastViewed = p.now()
	p.mu.Unlock()
}

// result returns a copy of the current result for a container, if any.
func (p *prober) result(id string) *Probe {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st := p.states[id]; st != nil && st.res != nil {
		r := *st.res
		return &r
	}
	return nil
}

// probeTarget picks the address to probe, or "" when the container should
// not be probed: not running, has its own health check, is Wargo, or has
// no recognisable web UI port.
func probeTarget(c Container) (addr string, port int) {
	if c.State != "running" || c.Health != "" || c.Self {
		return "", 0
	}
	slug := dashicons.GuessSlug(c.Image)
	if slug == "" {
		slug = dashicons.GuessSlug(c.Name)
	}
	known := dashicons.KnownPorts[slug]
	if c.NetworkMode == "host" {
		if known == 0 {
			return "", 0
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(known)), known
	}
	ports := c.Ports
	if c.NetworkContainer != "" {
		// Shares another container's network (e.g. behind a VPN): its UI is
		// published on that container. Only probe a port we know is ours.
		if known == 0 {
			return "", 0
		}
		ports = slices.DeleteFunc(slices.Clone(c.NetworkPorts), func(p Port) bool { return p.Private != known })
	}
	var best *Port
	bestRank := len(dashicons.WebPorts) + 1
	for i := range ports {
		p := &ports[i]
		if p.Type != "tcp" || p.Public == 0 {
			continue
		}
		rank := slices.Index(dashicons.WebPorts, p.Private)
		if known != 0 && p.Private == known {
			rank = -1
		}
		if rank < 0 && !(known != 0 && p.Private == known) {
			continue // not a web port: don't send HTTP to databases etc.
		}
		if best == nil || rank < bestRank {
			best, bestRank = p, rank
		}
	}
	if best == nil {
		return "", 0
	}
	ip := best.IP
	if ip == "" || ip == "0.0.0.0" || ip == "::" {
		ip = "127.0.0.1"
	}
	return net.JoinHostPort(ip, strconv.Itoa(best.Public)), best.Public
}

// check sends one GET and reports whether anything answered over HTTP.
// Any status code counts: a login page or 401 still means "responding".
func (p *prober) check(ctx context.Context, addr string, port int) Probe {
	start := p.now()
	res := Probe{Port: port, CheckedAt: start.UnixMilli(), IntervalS: int(probeInterval / time.Second)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		res.Status, res.Error = "down", "invalid address"
		return res
	}
	req.Header.Set("User-Agent", "Wargo-probe/1 (+reachability check)")
	resp, err := p.client.Do(req)
	res.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		res.Status, res.Error = "down", probeErr(err)
		return res
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, probeReadLimit))
	resp.Body.Close()
	res.Status, res.HTTPStatus = "up", resp.StatusCode
	return res
}

func probeErr(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.As(err, &ne) && ne.Timeout():
		return "no answer within " + probeTimeout.String()
	case strings.Contains(err.Error(), "malformed HTTP"):
		return "not an HTTP service"
	}
	return "unreachable"
}

func jitter(d time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(d))) }

// sync reconciles probe states with the current container list and
// returns the containers due for a check.
func (p *prober) sync(list []Container) []string {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[string]bool{}
	var due []string
	for _, c := range list {
		addr, port := probeTarget(c)
		if addr == "" {
			continue
		}
		seen[c.FullID] = true
		st := p.states[c.FullID]
		if st == nil || st.addr != addr {
			// New, restarted (stopped containers drop their state) or moved.
			st = &probeState{addr: addr, port: port, next: now.Add(probeFirstDelay + jitter(probeFirstDelay))}
			p.states[c.FullID] = st
		}
		if !now.Before(st.next) {
			due = append(due, c.FullID)
		}
	}
	for id := range p.states {
		if !seen[id] {
			delete(p.states, id)
		}
	}
	return due
}

// record stores a check result and returns whether the reported status changed.
func (p *prober) record(id string, r Probe) bool {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	st := p.states[id]
	if st == nil {
		return false
	}
	prev := ""
	if st.res != nil {
		prev = st.res.Status
	}
	if r.Status == "up" {
		st.fails = 0
		st.res = &r
		st.next = now.Add(probeInterval - probeInterval/10 + jitter(probeInterval/5))
	} else {
		st.fails++
		if st.fails < 2 {
			// One failure may be a blip: keep the previous result and retry soon.
			st.next = now.Add(probeRetry)
			return false
		}
		st.res = &r
		st.next = now.Add(probeInterval)
	}
	return prev != st.res.Status
}

const probesKey = "docker.probes"

type savedProbe struct {
	Addr string `json:"addr"`
	Res  Probe  `json:"res"`
}

// save persists the last conclusive results so badges survive restarts.
func (p *prober) save(ctx context.Context, set func(context.Context, string, any) error) {
	p.mu.Lock()
	out := map[string]savedProbe{}
	for id, st := range p.states {
		if st.res != nil {
			out[id] = savedProbe{Addr: st.addr, Res: *st.res}
		}
	}
	p.mu.Unlock()
	if err := set(ctx, probesKey, out); err != nil {
		slog.Debug("save probe results", "err", err)
	}
}

// restore seeds states from saved results; they're shown right away and
// re-checked soon (sync drops any whose container or port changed).
func (p *prober) restore(saved map[string]savedProbe) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, sp := range saved {
		if sp.Addr == "" {
			continue
		}
		r := sp.Res
		p.states[id] = &probeState{addr: sp.Addr, port: r.Port, res: &r, next: now.Add(probeResume + jitter(probeResume))}
	}
}

// probeLoop runs while Wargo runs; it only sends requests when someone
// has looked at containers recently.
func (s *Service) probeLoop(ctx context.Context) {
	p := s.probes
	if s.st != nil {
		var saved map[string]savedProbe
		if ok, err := s.st.Get(ctx, probesKey, &saved); ok && err == nil {
			p.restore(saved)
		}
	}
	t := time.NewTicker(probeTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p.mu.Lock()
		idle := p.now().Sub(p.lastViewed) > probeIdleAfter
		p.mu.Unlock()
		if idle && s.hub.Subscribers(TopicList) == 0 {
			continue
		}
		list, err := s.List(ctx, false)
		if err != nil {
			continue
		}
		due := p.sync(list)
		if len(due) == 0 {
			continue
		}
		changed := false
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, probeParallel)
		for _, id := range due {
			p.mu.Lock()
			st := p.states[id]
			var addr string
			var port int
			if st != nil {
				addr, port = st.addr, st.port
			}
			p.mu.Unlock()
			if addr == "" {
				continue
			}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				r := p.check(ctx, addr, port)
				if p.record(id, r) {
					mu.Lock()
					changed = true
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if s.st != nil {
			p.save(ctx, s.st.Set)
		}
		if changed {
			s.invalidate()
			s.publishList(ctx)
		}
	}
}

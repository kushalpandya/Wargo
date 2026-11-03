// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/schedule"
)

// Image update checks. Results are keyed by image ref + "|" + image id,
// kept in memory, persisted in the store (key "docker.updates") and
// refreshed in the background every checkInterval.

const updatesKey = "docker.updates"

type persistedUpdates struct {
	CheckedAt int64                  `json:"checkedAt"`
	Results   map[string]UpdateCheck `json:"results"`
}

// UpdatesStatus is the answer of GET /api/containers/updates and
// POST /api/containers/check-updates.
type UpdatesStatus struct {
	// CheckedAt is when the last full check finished (unix ms), null if never.
	CheckedAt *int64 `json:"checkedAt"`
	Checking  bool   `json:"checking"`
	// NextCheckAt is when the next background check is due (unix ms);
	// omitted when background checks are disabled.
	NextCheckAt int64 `json:"nextCheckAt,omitempty"`
	// IntervalMs is the background check interval (0 = disabled).
	IntervalMs int64 `json:"intervalMs"`
	// Results maps short container ids to the result for their image.
	Results map[string]UpdateCheck `json:"results"`
	// Error is set when the last check could not run at all (e.g. Docker down).
	Error string `json:"error,omitempty"`
}

func (s *Service) loadUpdates() {
	if s.st == nil {
		return
	}
	var p persistedUpdates
	ok, err := s.st.Get(context.Background(), updatesKey, &p)
	if err != nil {
		slog.Warn("docker: could not load update results", "err", err)
		return
	}
	if !ok {
		return
	}
	s.mu.Lock()
	for k, v := range p.Results {
		s.updates[k] = v
	}
	s.checkedAt = p.CheckedAt
	s.mu.Unlock()
}

// persistLocked snapshots results; call with s.mu held, then save.
func (s *Service) snapshotLocked() persistedUpdates {
	p := persistedUpdates{CheckedAt: s.checkedAt, Results: make(map[string]UpdateCheck, len(s.updates))}
	for k, v := range s.updates {
		p.Results[k] = v
	}
	return p
}

func (s *Service) save(p persistedUpdates) {
	if s.st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.st.Set(ctx, updatesKey, p); err != nil {
		slog.Warn("docker: could not save update results", "err", err)
	}
}

func (s *Service) updatesFor(l []Container) map[string]UpdateCheck {
	out := map[string]UpdateCheck{}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range l {
		if u, ok := s.updates[c.Image+"|"+c.ImageID]; ok {
			out[c.ID] = u
		}
	}
	return out
}

func (s *Service) updatesStatus(l []Container) UpdatesStatus {
	res := s.updatesFor(l)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := UpdatesStatus{Checking: s.checkDone != nil, IntervalMs: s.checkInterval.Milliseconds(), Results: res}
	if s.checkedAt > 0 {
		v := s.checkedAt
		st.CheckedAt = &v
	}
	if s.checkInterval > 0 {
		st.NextCheckAt = s.nextCheckAt
	}
	if s.checkErr != nil {
		st.Error = friendlyErr(s.checkErr)
	}
	return st
}

// registryErr turns distribution lookup failures into short messages.
func registryErr(err error) string {
	msg := err.Error()
	var ae *APIError
	if errors.As(err, &ae) {
		msg = ae.Message
	}
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "toomanyrequests") || strings.Contains(l, "rate limit") || strings.Contains(l, "429"):
		return "Registry rate limit reached; will retry on the next check"
	case strings.Contains(l, "unauthorized") || strings.Contains(l, "denied") || strings.Contains(l, "authentication required"):
		return "Registry denied access (private image or image no longer exists)"
	case strings.Contains(l, "manifest unknown") || strings.Contains(l, "not found"):
		return "Image tag not found in the registry"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(l, "timeout") || strings.Contains(l, "deadline"):
		return "Registry did not answer in time"
	}
	return truncate(msg, 300)
}

// merge keeps the previous result when a check failed transiently, so a
// rate limit doesn't hide a known update.
func merge(prev UpdateCheck, havePrev bool, cur UpdateCheck) UpdateCheck {
	if cur.UpdateAvailable == nil && cur.Error != "" && havePrev && prev.UpdateAvailable != nil {
		prev.Error = cur.Error
		return prev
	}
	return cur
}

// CheckUpdates checks every container image (3 at a time). Concurrent
// callers share one run, which continues even if ctx is canceled.
func (s *Service) CheckUpdates(ctx context.Context) error {
	s.mu.Lock()
	if done := s.checkDone; done != nil {
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.checkErr
	}
	done := make(chan struct{})
	s.checkDone = done
	s.mu.Unlock()

	result := make(chan error, 1)
	go func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		err := s.runCheck(rctx)
		s.mu.Lock()
		s.checkDone, s.checkErr = nil, err
		s.mu.Unlock()
		close(done)
		go s.publishList(context.Background())
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) runCheck(ctx context.Context) error {
	l, err := s.List(ctx, true)
	if err != nil {
		return err
	}
	type key struct{ ref, id string }
	uniq := map[key]bool{}
	for _, c := range l {
		uniq[key{c.Image, c.ImageID}] = true
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	results := map[string]UpdateCheck{}
	var mu sync.Mutex
	for k := range uniq {
		wg.Add(1)
		go func(k key) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			r := s.checkImage(cctx, k.ref, k.id)
			mu.Lock()
			results[k.ref+"|"+k.id] = r
			mu.Unlock()
		}(k)
	}
	wg.Wait()
	s.mu.Lock()
	next := make(map[string]UpdateCheck, len(results))
	for k, v := range results {
		prev, ok := s.updates[k]
		next[k] = merge(prev, ok, v)
	}
	// Images no longer used by any container are dropped.
	s.updates = next
	s.checkedAt = time.Now().UnixMilli()
	s.retryAt = time.Time{}
	if s.checkInterval > 0 {
		s.nextCheckAt = s.checkedAt + s.checkInterval.Milliseconds()
	}
	s.list = nil
	p := s.snapshotLocked()
	s.mu.Unlock()
	s.save(p)
	return nil
}

func (s *Service) handleCheckUpdates(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if err := s.CheckUpdates(ctx); err != nil {
		return dockerErr(err)
	}
	l, err := s.List(ctx, false)
	if err != nil {
		return dockerErr(err)
	}
	httpx.OK(w, s.updatesStatus(l))
	return nil
}

func (s *Service) markUpToDate(ref, imageID string) {
	f := false
	s.mu.Lock()
	s.updates[ref+"|"+imageID] = UpdateCheck{Image: ref, UpdateAvailable: &f, CheckedAt: time.Now().UnixMilli()}
	s.list = nil
	p := s.snapshotLocked()
	s.mu.Unlock()
	s.save(p)
}

// recheckContainer re-checks the image of the named container (after an
// update, whose new image id has no result yet).
func (s *Service) recheckContainer(ctx context.Context, name string) {
	l, err := s.List(ctx, true)
	if err != nil {
		return
	}
	i := findContainer(l, name)
	if i < 0 {
		return
	}
	c := l[i]
	s.mu.Lock()
	_, ok := s.updates[c.Image+"|"+c.ImageID]
	s.mu.Unlock()
	if ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	r := s.checkImage(cctx, c.Image, c.ImageID)
	cancel()
	s.mu.Lock()
	s.updates[c.Image+"|"+c.ImageID] = r
	s.list = nil
	p := s.snapshotLocked()
	s.mu.Unlock()
	s.save(p)
}

// autoCheck runs CheckUpdates in the background: first ~checkDelay after
// start (or when the persisted result is due, whichever is later), then
// every checkInterval. The interval can change at runtime (Settings →
// General): a change wakes the loop, and 0 pauses it.
func (s *Service) autoCheck(ctx context.Context) {
	first := true
	for {
		s.mu.Lock()
		interval := s.checkInterval
		var delay time.Duration
		if interval > 0 {
			delay = time.Minute
			if first {
				delay = s.checkDelay
			}
			if rem := time.Until(s.dueLocked()); rem > delay {
				delay = rem
			}
			s.nextCheckAt = time.Now().Add(delay).UnixMilli()
		} else {
			s.nextCheckAt = 0
		}
		s.mu.Unlock()

		if interval <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-s.sched.Wake():
				continue
			}
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.sched.Wake():
			t.Stop()
			continue
		case <-t.C:
		}
		first = false
		// A manual check since scheduling postpones the background one.
		s.mu.Lock()
		due := time.Until(s.dueLocked()) <= time.Minute
		s.mu.Unlock()
		if !due {
			continue
		}
		if err := s.CheckUpdates(ctx); err != nil {
			slog.Debug("docker: background update check failed", "err", err)
			if ctx.Err() != nil {
				return
			}
			// Retry sooner than the full interval, at most every 30 minutes.
			s.mu.Lock()
			s.retryAt = time.Now().Add(min(s.checkInterval, 30*time.Minute))
			s.mu.Unlock()
		}
	}
}

// dueLocked is when the next background check should run: after a failed
// one at retryAt, otherwise one interval after the last check.
func (s *Service) dueLocked() time.Time {
	if !s.retryAt.IsZero() {
		return s.retryAt
	}
	if s.checkedAt == 0 {
		return time.Time{}
	}
	return time.UnixMilli(s.checkedAt).Add(s.checkInterval)
}

// newSchedule builds the interval setting edited in Settings → General.
func (s *Service) newSchedule() *schedule.Hours {
	h := schedule.New("docker.settings", "containers.settings", 0, 0, 1, 3, 6, 12, 24, 72, 168)
	h.Current = func() int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return h.Ceil(s.checkInterval)
	}
	h.Apply = func(hours int) {
		s.mu.Lock()
		s.checkInterval = time.Duration(hours) * time.Hour
		s.mu.Unlock()
	}
	return h
}

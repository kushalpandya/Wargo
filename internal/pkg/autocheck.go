// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"context"
	"log/slog"
	"time"

	"github.com/kushalpandya/wargo/internal/schedule"
)

// Background update checks (Settings → General). They list pending updates
// without asking for admin approval: listing doesn't need root, and Debian
// and Ubuntu refresh their lists on their own (apt-daily). Some tools still
// contact mirrors: checkupdates syncs a private copy of the pacman database,
// and dnf and pamac refresh expired metadata. "Check for updates" on the
// Updates page refreshes the real databases first (with approval if needed).

func newSchedule() *schedule.Hours {
	return schedule.New("pkg.settings", "pkg.settings", 24, 0, 6, 12, 24, 72, 168)
}

// autoCheck lists updates every CheckHours, starting a few minutes after
// startup when the last check is older than that. A changed setting wakes it.
func (s *Service) autoCheck(ctx context.Context) {
	first := true
	var retryAt time.Time
	for {
		hours := s.sched.Load(ctx)
		if hours <= 0 || s.requireBackend() != nil {
			select {
			case <-ctx.Done():
				return
			case <-s.sched.Wake():
				continue
			}
		}
		interval := time.Duration(hours) * time.Hour
		s.mu.Lock()
		var last time.Time
		if s.last != nil {
			last = s.last.At
		}
		s.mu.Unlock()
		delay := time.Until(last.Add(interval))
		if first && delay < 3*time.Minute {
			delay = 3 * time.Minute // let the server settle after start
		}
		// After a failure, retry at most every 30 minutes.
		if r := time.Until(retryAt); r > delay {
			delay = r
		}
		if delay < time.Minute {
			delay = time.Minute
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
		s.mu.Lock()
		busy := s.checking || s.job != nil
		recent := s.last != nil && time.Since(s.last.At) < interval-time.Minute
		if !busy && !recent {
			s.checking = true
		}
		s.mu.Unlock()
		if busy || recent {
			continue
		}
		s.publish()
		retryAt = time.Time{}
		if _, err := s.list(ctx, true); err != nil {
			slog.Debug("pkg: background check failed", "err", err)
			retryAt = time.Now().Add(min(interval, 30*time.Minute))
		}
		s.mu.Lock()
		s.checking = false
		s.mu.Unlock()
		s.publish()
	}
}

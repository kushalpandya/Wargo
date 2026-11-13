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

// SettingsKey stores the background check interval. Homebrew shares it, so
// one setting covers every package source on the host.
const SettingsKey = "pkg.settings"

// NewSchedule returns the shared check interval setting.
func NewSchedule(action string) *schedule.Hours {
	return schedule.New(SettingsKey, action, 24, 0, 6, 12, 24, 72, 168)
}

func newSchedule() *schedule.Hours { return NewSchedule("pkg.settings") }

// autoCheck lists updates every CheckHours (see schedule.Hours.Every).
func (s *Service) autoCheck(ctx context.Context) {
	s.sched.Every(ctx, "pkg",
		func() bool { return s.requireBackend() == nil },
		func() time.Time {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.last == nil {
				return time.Time{}
			}
			return s.last.At
		},
		s.backgroundCheck)
}

// backgroundCheck lists updates without asking for approval, unless a check
// or upgrade is already running (schedule.ErrSkipped).
func (s *Service) backgroundCheck(ctx context.Context) error {
	s.mu.Lock()
	busy := s.checking || s.job != nil
	if !busy {
		s.checking = true
	}
	s.mu.Unlock()
	if busy {
		return schedule.ErrSkipped
	}
	s.publish()
	res, err := s.list(ctx, true)
	s.mu.Lock()
	s.checking = false
	s.mu.Unlock()
	s.publish()
	if err == nil {
		s.reportUpdates(ctx, res, true)
	}
	return err
}

// reportUpdates tells notifications which packages res has pending. Only
// call it after a check that succeeded: an empty report would drop the
// updates item and push it again on the next good check.
func (s *Service) reportUpdates(ctx context.Context, res *CheckResult, automatic bool) {
	if s.d.Notify == nil {
		return
	}
	var names []string
	if res != nil {
		for _, u := range res.Updates {
			names = append(names, u.Name)
		}
	}
	s.d.Notify.ReportUpdates(ctx, "system", names, automatic)
}

// recheck refreshes the list after an upgrade, so the Updates page shows
// what is still pending instead of the list from before it. The upgrade
// already refreshed the package databases, so no approval is needed.
func (s *Service) recheck(runID string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if s.fake {
		// Simulated upgrades change nothing; pretend a successful one
		// installed everything so the page shows what a real one would.
		if !ok {
			return
		}
		s.mu.Lock()
		if s.last != nil {
			c := *s.last
			c.At, c.Updates, c.DownloadSize, c.Stale = time.Now(), []Update{}, 0, false
			s.last = &c
		}
		s.mu.Unlock()
	} else if err := s.backgroundCheck(ctx); err != nil {
		if err != schedule.ErrSkipped {
			slog.Warn("pkg: check after upgrade failed", "err", err)
		}
		return
	}
	s.mu.Lock()
	last := TagAfterRun(&s.last, runID)
	s.mu.Unlock()
	if last != nil {
		s.d.Store.Set(ctx, keyLastCheck, last)
	}
	s.publish()
}

// TagAfterRun marks *last as the check that ran right after runID (see
// CheckResult.AfterRun) and returns it. The caller holds the lock.
func TagAfterRun(last **CheckResult, runID string) *CheckResult {
	if *last == nil {
		return nil
	}
	c := **last
	c.AfterRun = runID
	*last = &c
	return &c
}

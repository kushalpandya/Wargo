// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"context"
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
		func(ctx context.Context) error {
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
			_, err := s.list(ctx, true)
			s.mu.Lock()
			s.checking = false
			s.mu.Unlock()
			s.publish()
			return err
		})
}

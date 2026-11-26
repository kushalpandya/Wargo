// SPDX-License-Identifier: AGPL-3.0-only

package schedule

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ErrSkipped is returned by a check that didn't run because something else
// was busy; the loop then keeps its retry timing as it was.
var ErrSkipped = errors.New("check skipped")

// Every runs check in the background every N hours of h, at the fixed times
// Next gives, and a few minutes after startup when the last check (last)
// missed one (see Due and Plan). ready reports whether checking makes sense
// now (the tool exists); while it doesn't, or the setting is off, the loop
// waits for the setting to change. While the setting can't be read it
// doesn't check, and reads it again every minute. After a failed check it retries at most
// every 30 minutes, or at the next slot if that comes first. check decides
// for itself whether it is too busy to run, and returns ErrSkipped if so.
func (h *Hours) Every(ctx context.Context, name string, ready func() bool, last func() time.Time, check func(context.Context) error) {
	first := true
	var retryAt time.Time
	for {
		v, err := h.Get(ctx)
		if err != nil {
			// Not known whether checks are on: don't check, read again soon.
			slog.Debug(name+": reading the check setting failed", "err", err)
			t := time.NewTimer(time.Minute)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-h.Wake():
				t.Stop()
			case <-t.C:
			}
			continue
		}
		hours := v.Hours()
		if hours <= 0 || !ready() {
			select {
			case <-ctx.Done():
				return
			case <-h.Wake():
				continue
			}
		}
		interval := time.Duration(hours) * time.Hour
		wait := time.Minute
		if first {
			wait = 3 * time.Minute // let the server settle after start
			if h.firstWait > 0 {
				wait = h.firstWait
			}
		}
		now := time.Now()
		delay := Plan(now, Due(now, last(), retryAt, interval), interval, wait).Sub(now)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-h.Wake():
			t.Stop()
			continue
		case <-t.C:
		}
		first = false
		// A manual check meanwhile postpones the background one. A retry
		// runs regardless: a failed first check also set last.
		if retryAt.IsZero() && time.Until(Next(last(), interval)) > time.Minute {
			continue
		}
		switch err := check(ctx); {
		case errors.Is(err, ErrSkipped):
		case err != nil:
			slog.Debug(name+": background check failed", "err", err)
			retryAt = time.Now().Add(min(interval, 30*time.Minute))
			if ctx.Err() == nil {
				h.Audit.Outcome(name+".check", "background", err, map[string]any{"intervalHours": hours})
			}
		default:
			retryAt = time.Time{}
			h.Audit.Outcome(name+".check", "background", nil, nil)
		}
	}
}

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

// Every runs check in the background every N hours of h, starting a few
// minutes after startup when the last check (last) is older than that.
// ready reports whether checking makes sense now (the tool exists); while it
// doesn't, or the setting is off, the loop waits for the setting to change.
// After a failed check it retries at most every 30 minutes. check decides
// for itself whether it is too busy to run, and returns ErrSkipped if so.
func (h *Hours) Every(ctx context.Context, name string, ready func() bool, last func() time.Time, check func(context.Context) error) {
	first := true
	var retryAt time.Time
	for {
		hours := h.Load(ctx)
		if hours <= 0 || !ready() {
			select {
			case <-ctx.Done():
				return
			case <-h.Wake():
				continue
			}
		}
		interval := time.Duration(hours) * time.Hour
		delay := time.Until(last().Add(interval))
		if first && delay < 3*time.Minute {
			delay = 3 * time.Minute // let the server settle after start
		}
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
		case <-h.Wake():
			t.Stop()
			continue
		case <-t.C:
		}
		first = false
		if time.Since(last()) < interval-time.Minute {
			continue // a manual check ran meanwhile
		}
		switch err := check(ctx); {
		case errors.Is(err, ErrSkipped):
		case err != nil:
			slog.Debug(name+": background check failed", "err", err)
			retryAt = time.Now().Add(min(interval, 30*time.Minute))
		default:
			retryAt = time.Time{}
		}
	}
}

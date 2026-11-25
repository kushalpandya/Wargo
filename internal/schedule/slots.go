// SPDX-License-Identifier: AGPL-3.0-only

package schedule

import (
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

// Background checks run at fixed times of day in the host's time zone, not
// one interval after the last check: checks of every source with the same
// interval then start together (and send one updates push), however long
// each took and whenever a manual check or an upgrade's recheck last ran.
// The times are anchorHour plus whole intervals, counted from refDay so that
// 72 and 168 hours land on the same days for every source: every 24 hours
// at 09:00, every 6 hours at 03:00, 09:00, 15:00 and 21:00, every 168 hours
// on Mondays at 09:00. Hours are counted on the wall clock, so on the day
// DST ends an hourly check skips the repeated hour, and in a zone whose DST
// shift is not a whole hour a slot can land off the hour.
const anchorHour = 9

// refDay is the day the slots are counted from (a Monday), in days since
// the Unix epoch.
var refDay = time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC).Unix() / 86400

// location is the time zone of the slots (replaced in tests).
var location = host.Location

// Startup and retry checks run when they are due, not at a slot. One that
// would run less than snapWindow before a slot waits for the slot instead,
// so it doesn't run again right after (and starts with the other sources).
const snapWindow = 15 * time.Minute

// Next returns when the next background check is due after one that ran at
// last: the first slot after last. A zero last means no check ran yet, and
// Next returns the zero time (due now). An interval that does not fit a day
// (not whole hours, or neither dividing 24 hours nor a multiple of them)
// has no slots, and Next returns last plus interval.
func Next(last time.Time, interval time.Duration) time.Time {
	if last.IsZero() {
		return time.Time{}
	}
	if !slotted(interval) {
		return last.Add(interval)
	}
	h := int(interval / time.Hour)
	lt := last.In(location())
	y, m, d := lt.Date()
	// Days are counted on the calendar, so a DST change doesn't move the slots.
	day := int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()/86400 - refDay)
	for i := 0; i <= h/24+1; i++ {
		for hr := 0; hr < 24; hr++ {
			if (((day+i)*24+hr-anchorHour)%h+h)%h != 0 {
				continue
			}
			// time.Date moves an hour skipped by DST forward.
			if t := time.Date(y, m, d+i, hr, 0, 0, 0, lt.Location()); t.After(last) {
				return t
			}
		}
	}
	return last.Add(interval) // not reached
}

// slotted reports whether interval has slots (see Next).
func slotted(interval time.Duration) bool {
	h := int(interval / time.Hour)
	return h > 0 && interval%time.Hour == 0 && (24%h == 0 || h%24 == 0)
}

// Due returns when the next background check should run, given the last
// check (zero if none) and, after a failed one, when it may be retried
// (zero if the last one did not fail): the retry, or the next slot if that
// comes first, so a source whose check failed still joins the others.
// Otherwise the first slot after the last check. The zero time means now.
func Due(now, last, retryAt time.Time, interval time.Duration) time.Time {
	if !retryAt.IsZero() {
		if s := Next(now, interval); slotted(interval) && s.Before(retryAt) {
			return s
		}
		return retryAt
	}
	return Next(last, interval)
}

// Plan returns when a check loop should wake for a check due at due: not
// before now plus wait (the loop's minimum, longer right after startup),
// and when that falls just before a slot (see snapWindow), at the slot.
func Plan(now, due time.Time, interval, wait time.Duration) time.Time {
	at := now.Add(wait)
	if due.After(at) {
		at = due
	}
	if !slotted(interval) {
		return at
	}
	if s := Next(at, interval); s.Sub(at) < snapWindow {
		return s
	}
	return at
}

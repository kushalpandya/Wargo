// SPDX-License-Identifier: AGPL-3.0-only

package schedule

import (
	"testing"
	"time"
)

// inZone puts the slots in the named zone for the test. Tests using it must
// not run in parallel (location is a package variable).
func inZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skip("no zoneinfo for", name)
	}
	prev := location
	location = func() *time.Location { return loc }
	t.Cleanup(func() { location = prev })
	return loc
}

func TestNext(t *testing.T) {
	loc := inZone(t, "America/Toronto")
	at := func(y int, m time.Month, d, hr, min int) time.Time { return time.Date(y, m, d, hr, min, 0, 0, loc) }
	for _, c := range []struct {
		name     string
		last     time.Time
		interval time.Duration
		want     time.Time
	}{
		{"daily, before 09:00", at(2026, 11, 25, 8, 59), 24 * time.Hour, at(2026, 11, 25, 9, 0)},
		{"daily, a slot's own check", at(2026, 11, 25, 9, 0), 24 * time.Hour, at(2026, 11, 26, 9, 0)},
		{"daily, after an upgrade's recheck", at(2026, 11, 24, 14, 28), 24 * time.Hour, at(2026, 11, 25, 9, 0)},
		{"daily, across the year", at(2026, 12, 31, 10, 0), 24 * time.Hour, at(2027, 1, 1, 9, 0)},
		{"6h", at(2026, 11, 25, 9, 30), 6 * time.Hour, at(2026, 11, 25, 15, 0)},
		{"6h, on a slot", at(2026, 11, 25, 15, 0), 6 * time.Hour, at(2026, 11, 25, 21, 0)},
		{"6h, past midnight", at(2026, 11, 25, 21, 1), 6 * time.Hour, at(2026, 11, 26, 3, 0)},
		{"12h", at(2026, 11, 25, 10, 0), 12 * time.Hour, at(2026, 11, 25, 21, 0)},
		{"3h", at(2026, 11, 25, 1, 0), 3 * time.Hour, at(2026, 11, 25, 3, 0)},
		{"2h", at(2026, 11, 25, 9, 0), 2 * time.Hour, at(2026, 11, 25, 11, 0)},
		{"1h", at(2026, 11, 25, 9, 59), time.Hour, at(2026, 11, 25, 10, 0)},
		{"1h, on a slot", at(2026, 11, 25, 10, 0), time.Hour, at(2026, 11, 25, 11, 0)},
		// 2026-01-05 is the first day of the 72-hour cycle; 2026-11-25 is 324 days later.
		{"72h, on a cycle day", at(2026, 11, 25, 8, 0), 72 * time.Hour, at(2026, 11, 25, 9, 0)},
		{"72h, after it", at(2026, 11, 25, 9, 0), 72 * time.Hour, at(2026, 11, 28, 9, 0)},
		{"72h, before the reference day", at(2025, 12, 31, 10, 0), 72 * time.Hour, at(2026, 1, 2, 9, 0)},
		{"48h", at(2026, 11, 25, 9, 0), 48 * time.Hour, at(2026, 11, 27, 9, 0)},
		{"weekly, Mondays", at(2026, 11, 25, 12, 0), 168 * time.Hour, at(2026, 11, 30, 9, 0)},
		{"weekly, years before the reference day", at(2020, 2, 29, 12, 0), 168 * time.Hour, at(2020, 3, 2, 9, 0)},
		{"daily across spring forward", at(2026, 3, 7, 10, 0), 24 * time.Hour, at(2026, 3, 8, 9, 0)},
		{"daily across fall back", at(2026, 10, 31, 10, 0), 24 * time.Hour, at(2026, 11, 1, 9, 0)},
		// 02:00 doesn't exist on 2026-03-08; time.Date moves it to 03:00.
		{"1h, the skipped hour", at(2026, 3, 8, 1, 30), time.Hour, at(2026, 3, 8, 3, 0)},
		{"odd interval keeps last plus interval", at(2026, 11, 25, 10, 17), 5 * time.Hour, at(2026, 11, 25, 15, 17)},
		{"sub-hour interval keeps last plus interval", at(2026, 11, 25, 10, 17), 30 * time.Minute, at(2026, 11, 25, 10, 47)},
		{"36h keeps last plus interval", at(2026, 11, 25, 10, 0), 36 * time.Hour, at(2026, 11, 26, 22, 0)},
		{"never checked", time.Time{}, 24 * time.Hour, time.Time{}},
	} {
		if got := Next(c.last, c.interval); !got.Equal(c.want) {
			t.Errorf("%s: Next(%v, %v) = %v, want %v", c.name, c.last, c.interval, got.In(loc), c.want)
		}
	}
}

// On the day DST ends, 01:00 happens twice; hourly slots step from the
// first 01:00 to 02:00 (wall clock), so they never go backwards or stall.
func TestNextFallBack(t *testing.T) {
	loc := inZone(t, "America/Toronto")
	first := time.Date(2026, 11, 1, 1, 0, 0, 0, loc) // 01:00 EDT
	got := Next(first, time.Hour)
	if want := time.Date(2026, 11, 1, 2, 0, 0, 0, loc); !got.Equal(want) || !got.After(first) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Checks that finished minutes apart (a slower check, an upgrade's recheck)
// are next due at the same time.
func TestNextAligns(t *testing.T) {
	loc := inZone(t, "America/Toronto")
	a := Next(time.Date(2026, 11, 24, 14, 25, 24, 0, loc), 24*time.Hour)
	b := Next(time.Date(2026, 11, 24, 14, 28, 10, 0, loc), 24*time.Hour)
	if !a.Equal(b) {
		t.Fatalf("%v and %v differ", a, b)
	}
}

// The slots follow the host's zone, not UTC.
func TestNextUsesLocation(t *testing.T) {
	loc := inZone(t, "Asia/Kolkata")
	got := Next(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC), 24*time.Hour)
	if want := time.Date(2026, 11, 25, 9, 0, 0, 0, loc); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got.In(loc), want)
	}
}

func TestDue(t *testing.T) {
	loc := inZone(t, "America/Toronto")
	at := func(hr, min int) time.Time { return time.Date(2026, 11, 25, hr, min, 0, 0, loc) }
	day := 24 * time.Hour
	for _, c := range []struct {
		name               string
		now, last, retryAt time.Time
		interval           time.Duration
		want               time.Time
	}{
		{"never checked", at(12, 0), time.Time{}, time.Time{}, day, time.Time{}},
		{"after a check", at(12, 0), at(9, 0), time.Time{}, day, at(9, 0).Add(day)},
		{"missed slot", at(12, 0), at(9, 0).Add(-day), time.Time{}, day, at(9, 0)},
		{"retry, last success yesterday", at(12, 0), at(9, 0).Add(-day), at(12, 20), day, at(12, 20)},
		{"retry after a failed first check", at(12, 0), at(11, 50), at(12, 20), day, at(12, 20)},
		{"slot before the retry", at(8, 40), at(8, 40), at(9, 10), day, at(9, 0)},
		{"retry, interval without slots", at(8, 40), at(8, 40), at(9, 10), 5 * time.Hour, at(9, 10)},
	} {
		if got := Due(c.now, c.last, c.retryAt, c.interval); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPlan(t *testing.T) {
	loc := inZone(t, "America/Toronto")
	at := func(hr, min, sec int) time.Time { return time.Date(2026, 11, 25, hr, min, sec, 0, loc) }
	day := 24 * time.Hour
	for _, c := range []struct {
		name     string
		now, due time.Time
		interval time.Duration
		wait     time.Duration
		want     time.Time
	}{
		{"waits for the slot", at(12, 0, 0), at(9, 0, 0).Add(day), day, time.Minute, at(9, 0, 0).Add(day)},
		{"startup, overdue", at(12, 0, 0), at(9, 0, 0), day, 3 * time.Minute, at(12, 3, 0)},
		// Started at 08:56:30: the check would run at 08:59:30 and again at
		// 09:00. It waits for 09:00 instead.
		{"startup just before a slot", at(8, 56, 30), at(9, 0, 0).Add(-day), day, 3 * time.Minute, at(9, 0, 0)},
		{"startup 14 minutes before a slot", at(8, 43, 0), time.Time{}, day, 3 * time.Minute, at(9, 0, 0)},
		{"startup 16 minutes before a slot", at(8, 41, 0), time.Time{}, day, 3 * time.Minute, at(8, 44, 0)},
		{"retry just before a slot", at(8, 30, 0), at(8, 50, 0), day, time.Minute, at(9, 0, 0)},
		{"never before the minimum wait", at(8, 59, 50), at(9, 0, 0), day, time.Minute, at(9, 0, 50)},
		{"interval without slots", at(8, 56, 30), time.Time{}, 5 * time.Hour, 3 * time.Minute, at(8, 59, 30)},
		{"hourly, just before the hour", at(10, 50, 0), time.Time{}, time.Hour, 3 * time.Minute, at(11, 0, 0)},
	} {
		if got := Plan(c.now, c.due, c.interval, c.wait); !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got.In(loc), c.want)
		}
	}
}

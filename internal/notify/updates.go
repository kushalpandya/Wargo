// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
)

const (
	updatesKey  = "pending"
	updatesLink = "/updates"

	// A disk is full at fullPct and used, and recovers below recoveredPct.
	fullPct      = 90
	recoveredPct = 85

	// updatesWindow bounds the wait for every update source to report
	// after New. A source still silent after it (Docker installed but its
	// daemon never up) counts as nothing pending; its first report later
	// is still a baseline.
	updatesWindow = 10 * time.Minute

	// An automatic check that finds new updates does not push at once: the
	// push waits until no automatic check has started or finished for
	// updatesHold and none is running, so checks of every source that run
	// together send one push with all of their counts. It waits no longer
	// than updatesHoldMax after it became due.
	updatesHold    = 3 * time.Minute
	updatesHoldMax = 20 * time.Minute
)

// updateSources lists the sources ReportUpdates takes, in body order.
var updateSources = []struct{ id, label string }{
	{"containers", "Containers"},
	{"system", "System"},
	{"homebrew", "Homebrew"},
}

// updatesState is what ReportUpdates remembers. Guarded by Center.mu.
type updatesState struct {
	// names lists, per source, the names its last report held.
	names map[string]map[string]bool
	// running counts the automatic checks under way, per source.
	running map[string]int
	// due is when the held push became due; zero when none is held.
	due time.Time
	// fresh lists, per source, the new names behind the held push: it is
	// only sent if one of them is still pending.
	fresh map[string]map[string]bool
	// timer sends the held push; gen tells a stale timer from the current one.
	timer func() bool
	gen   int
	// closed is set by Close: nothing is held or sent after it.
	closed bool
}

// Disk is one filesystem and how full it is (0..100).
type Disk struct {
	Mount   string
	UsedPct float64
}

// diskState is what ReportDisks remembers. Guarded by Center.mu.
type diskState struct {
	seen bool
	// open lists the mounts whose items the last non-empty report kept
	// open; missing counts the non-empty reports in a row that left one of
	// them out (a network mount whose usage could not be read drops out of
	// one report).
	open    []string
	missing map[string]int
}

// ReportUpdates records what source (containers, system or homebrew) has
// pending. All sources feed one Recent item, "N updates available", whose
// body counts each source; it is rewritten as the counts change and deleted
// when nothing is pending. It pushes only when automatic is true and the
// source reports a name it did not report last time; a manual check
// (automatic false) updates the item and never pushes. The first report per
// source after New is the baseline: it is shown, never pushed. When the
// owner cleared the item and new names then appear, it shows again.
//
// The push is held until the automatic checks running together are done
// (see updatesHold and BeginUpdateCheck), then sent once with the counts of
// every source.
func (c *Center) ReportUpdates(ctx context.Context, source string, names []string, automatic bool) {
	known := false
	for _, s := range updateSources {
		known = known || s.id == source
	}
	if !known {
		slog.Error("notifications: unknown update source", "source", source)
		return
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.upd.names == nil {
		c.upd.names = map[string]map[string]bool{}
	}
	prev, seen := c.upd.names[source]
	var added []string
	for n := range set {
		if !prev[n] {
			added = append(added, n)
		}
	}
	fresh := len(added) > 0
	c.upd.names[source] = set
	baseline := !seen
	o, total := c.updatesItemLocked()
	// Until every source has reported since New, the totals are partial:
	// never delete or rewrite the stored item from them (that would drop
	// what a source not yet heard from contributed, and a later report
	// would bring it back uncleared). Only create it if there is none,
	// or rewrite it when a source reports names it did not have before.
	// The wait ends updatesWindow after New.
	window := len(c.upd.names) < len(updateSources) && c.now().Sub(c.started) < updatesWindow
	if total == 0 && window {
		return
	}
	if total == 0 {
		changed, err := c.deleteLocked(ctx, UpdatesFound, updatesKey)
		if err != nil {
			slog.Error("notifications: updates", "err", err)
		}
		if changed {
			c.publishLocked(ctx)
		}
		return
	}
	var changed bool
	var err error
	if window && !(fresh && !baseline) {
		changed, err = c.addLocked(ctx, o)
	} else {
		changed, err = c.upsertLocked(ctx, o, fresh && !baseline)
	}
	if err != nil {
		slog.Error("notifications: updates", "err", err)
	}
	if changed {
		c.publishLocked(ctx)
	}
	if fresh && automatic && !baseline && c.modeLocked(UpdatesFound) == ModePush {
		if c.upd.due.IsZero() {
			c.upd.due = c.now()
		}
		if c.upd.fresh == nil {
			c.upd.fresh = map[string]map[string]bool{}
		}
		if c.upd.fresh[source] == nil {
			c.upd.fresh[source] = map[string]bool{}
		}
		for _, n := range added {
			c.upd.fresh[source][n] = true
		}
		c.holdPushLocked()
	}
}

// updatesItemLocked returns the updates item for what every source has
// pending now, and how many updates that is.
func (c *Center) updatesItemLocked() (Occurrence, int) {
	total := 0
	var parts []string
	for _, s := range updateSources {
		if n := len(c.upd.names[s.id]); n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%s %d", s.label, n))
		}
	}
	o := Occurrence{
		Category: UpdatesFound, Key: updatesKey, Link: updatesLink,
		Title: fmt.Sprintf("%d updates available", total),
		Body:  strings.Join(parts, " · "),
	}
	if total == 1 {
		o.Title = "1 update available"
	}
	return o, total
}

// BeginUpdateCheck tells notifications that an automatic check of source
// started, so a held updates push waits for it; call the returned func when
// the check is over, after its ReportUpdates if it succeeded. Manual checks
// do not call it.
func (c *Center) BeginUpdateCheck(source string) (end func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.upd.running == nil {
		c.upd.running = map[string]int{}
	}
	c.upd.running[source]++
	c.holdPushLocked()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.upd.running[source]--; c.upd.running[source] <= 0 {
				delete(c.upd.running, source)
			}
			c.holdPushLocked()
		})
	}
}

// holdPushLocked (re)starts the timer of the held updates push, if one is
// held: updatesHold from now, or the end of updatesHoldMax while an
// automatic check is still running.
func (c *Center) holdPushLocked() {
	if c.upd.timer != nil {
		c.upd.timer()
		c.upd.timer = nil
	}
	if c.upd.due.IsZero() || c.upd.closed {
		return
	}
	now := c.now()
	last := c.upd.due.Add(updatesHoldMax)
	at := now.Add(updatesHold)
	if len(c.upd.running) > 0 || at.After(last) {
		at = last
	}
	c.upd.gen++
	gen := c.upd.gen
	c.upd.timer = c.afterFunc(max(at.Sub(now), 0), func() { c.sendHeldPush(gen) })
}

// sendHeldPush sends the held updates push, with the counts of every source
// as they are now. Nothing is sent when none of the new names behind it is
// still pending (they were installed meanwhile), push was turned off, or the
// owner already cleared the item.
func (c *Center) sendHeldPush(gen int) {
	ctx := context.Background()
	c.mu.Lock()
	if gen != c.upd.gen || c.upd.due.IsZero() || c.upd.closed {
		c.mu.Unlock()
		return
	}
	fresh := c.upd.fresh
	c.upd.due, c.upd.fresh, c.upd.timer = time.Time{}, nil, nil
	o, _ := c.updatesItemLocked()
	pending := false
	for source, names := range fresh {
		for n := range names {
			pending = pending || c.upd.names[source][n]
		}
	}
	due := pending && c.modeLocked(UpdatesFound) == ModePush
	if due {
		var n int
		err := c.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notify_items
			WHERE category = ? AND key = ? AND cleared_at IS NULL AND resolved_at IS NULL`,
			string(UpdatesFound), updatesKey).Scan(&n)
		if err != nil {
			slog.Error("notifications: updates", "err", err)
		}
		due = err == nil && n > 0
	}
	push := c.push
	c.mu.Unlock()
	if due && push != nil {
		push(ctx, o.Category, o.Key, o.Title, o.Body, o.Link)
	}
}

// ReportDisks records how full each filesystem is. A disk at 90% or more
// raises one occurrence that lasts until it falls below 85%; while it lasts,
// its title follows the current percentage. A disk missing from two
// non-empty lists in a row (unmounted) is resolved. The first call after
// New is the baseline: disks that recovered while Wargo was down, or are
// not mounted, are resolved, and full ones are shown without a push.
func (c *Center) ReportDisks(ctx context.Context, list []Disk) {
	// Disks still in the hysteresis band keep their open item.
	var open []string
	present := make(map[string]bool, len(list))
	for _, d := range list {
		present[d.Mount] = true
		if d.UsedPct >= recoveredPct {
			open = append(open, d.Mount)
		}
	}
	c.mu.Lock()
	first := !c.disk.seen
	c.disk.seen = true
	if !first && len(list) > 0 {
		if c.disk.missing == nil {
			c.disk.missing = map[string]int{}
		}
		for _, m := range c.disk.open {
			if present[m] {
				continue
			}
			c.disk.missing[m]++
			if c.disk.missing[m] < 2 {
				open = append(open, m)
			} else {
				delete(c.disk.missing, m)
			}
		}
		for m := range present {
			delete(c.disk.missing, m)
		}
	}
	if first || len(list) > 0 {
		c.disk.open = open
	}
	c.mu.Unlock()

	if first || len(list) > 0 {
		pct := make(map[string]float64, len(list))
		for _, d := range list {
			pct[d.Mount] = d.UsedPct
		}
		c.Reconcile(ctx, DiskFull, open, func(mount string) string {
			p, ok := pct[mount]
			if !ok {
				return mount + " is no longer mounted"
			}
			return fmt.Sprintf("%s is back to %d%%", mount, int(math.Round(p)))
		})
	}
	for _, d := range list {
		title := fmt.Sprintf("%s is %d%% full", d.Mount, int(math.Round(d.UsedPct)))
		switch {
		case d.UsedPct >= fullPct:
			c.Raise(ctx, Occurrence{Category: DiskFull, Key: d.Mount, Link: "/system", Baseline: first, Title: title})
			c.retitle(ctx, DiskFull, d.Mount, title)
		case d.UsedPct >= recoveredPct:
			c.retitle(ctx, DiskFull, d.Mount, title)
		case d.UsedPct < recoveredPct:
			c.Resolve(ctx, DiskFull, d.Mount, fmt.Sprintf("%s is back to %d%%", d.Mount, int(math.Round(d.UsedPct))))
		}
	}
}

// retitle sets the title of the open item of (cat, key), if there is one,
// leaving it where it is: no push, and a cleared item stays cleared.
func (c *Center) retitle(ctx context.Context, cat Category, key, title string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.st.DB.ExecContext(ctx,
		`UPDATE notify_items SET title = ? WHERE category = ? AND key = ? AND resolved_at IS NULL AND title != ?`,
		title, string(cat), key, title)
	if err != nil {
		slog.Error("notifications: retitle", "category", cat, "key", key, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		c.publishLocked(ctx)
	}
}

// stopHeldPushLocked drops the held updates push for good (Close).
func (c *Center) stopHeldPushLocked() {
	c.upd.closed = true
	c.upd.due, c.upd.fresh = time.Time{}, nil
	c.upd.gen++
	if c.upd.timer != nil {
		c.upd.timer()
		c.upd.timer = nil
	}
}

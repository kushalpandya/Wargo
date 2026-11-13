// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
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

	// notifiedTTL is how long a source remembers a name it notified after
	// the name stops being reported (see ReportUpdates).
	notifiedTTL = 7 * 24 * time.Hour
)

// updateSources lists the sources ReportUpdates takes, in body order.
var updateSources = []struct{ id, label string }{
	{"containers", "Containers"},
	{"system", "System"},
	{"homebrew", "Homebrew"},
}

// updatesState is what ReportUpdates remembers per source. Guarded by
// Center.mu.
type updatesState struct {
	// current holds the names each source's last report held.
	current map[string]map[string]bool
	// notified holds, per source, every name it reported (pushed or not,
	// automatic or manual) and when it last reported it. A name pushes
	// only when it is not in here. A name is forgotten once it has been
	// missing from the source's reports for notifiedTTL, so a later
	// update of the same package or container is news again.
	notified map[string]map[string]time.Time
}

// Disk is one filesystem and how full it is (0..100).
type Disk struct {
	Mount   string
	UsedPct float64
}

// diskState is what ReportDisks remembers. Guarded by Center.mu.
type diskState struct{ seen bool }

// ReportUpdates records what source (containers, system or homebrew) has
// pending. All sources feed one Recent item, "N updates available", whose
// body counts each source; it is rewritten as the counts change and deleted
// when nothing is pending. It pushes only when automatic is true and the
// source reports a name it has not reported within notifiedTTL (see
// updatesState.notified), so a name that drops out and comes back soon
// after does not push again; a manual check
// (automatic false) updates the item and never pushes. The first report per
// source after New is the baseline: it is shown, never pushed. When the
// owner cleared the item and new names then appear, it shows again.
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
	now := c.now()
	if c.upd.current == nil {
		c.upd.current = map[string]map[string]bool{}
		c.upd.notified = map[string]map[string]time.Time{}
	}
	_, seen := c.upd.current[source]
	notified := c.upd.notified[source]
	if notified == nil {
		notified = map[string]time.Time{}
		c.upd.notified[source] = notified
	}
	fresh := false
	for n := range set {
		if _, ok := notified[n]; !ok {
			fresh = true
		}
		notified[n] = now
	}
	for n, last := range notified {
		if now.Sub(last) > notifiedTTL {
			delete(notified, n)
		}
	}
	c.upd.current[source] = set
	baseline := !seen
	total := 0
	var parts []string
	for _, s := range updateSources {
		if n := len(c.upd.current[s.id]); n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%s %d", s.label, n))
		}
	}
	// Until every source has reported since New, the totals are partial:
	// never delete or rewrite the stored item from them (that would drop
	// what a source not yet heard from contributed, and a later report
	// would bring it back uncleared). Only create it if there is none,
	// or rewrite it when a source reports names it did not have before.
	// The wait ends updatesWindow after New.
	window := len(c.upd.current) < len(updateSources) && now.Sub(c.started) < updatesWindow
	if total == 0 && window {
		c.mu.Unlock()
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
		c.mu.Unlock()
		return
	}
	o := Occurrence{
		Category: UpdatesFound, Key: updatesKey, Link: updatesLink,
		Title: fmt.Sprintf("%d updates available", total),
		Body:  strings.Join(parts, " · "),
	}
	if total == 1 {
		o.Title = "1 update available"
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
	due := fresh && automatic && !baseline && c.modeLocked(UpdatesFound) == ModePush
	push := c.push
	c.mu.Unlock()
	if due && push != nil {
		push(ctx, o.Category, o.Key, o.Title, o.Body, o.Link)
	}
}

// ReportDisks records how full each filesystem is. A disk at 90% or more
// raises one occurrence that lasts until it falls below 85%; while it lasts,
// its title follows the current percentage. A disk missing from a
// non-empty list (unmounted) is resolved. The first call after New is the
// baseline: disks that recovered while Wargo was down are resolved, and
// full ones are shown without a push.
func (c *Center) ReportDisks(ctx context.Context, list []Disk) {
	c.mu.Lock()
	first := !c.disk.seen
	c.disk.seen = true
	c.mu.Unlock()

	if first || len(list) > 0 {
		// Disks still in the hysteresis band keep their open item.
		var open []string
		for _, d := range list {
			if d.UsedPct >= recoveredPct {
				open = append(open, d.Mount)
			}
		}
		pct := make(map[string]float64, len(list))
		for _, d := range list {
			pct[d.Mount] = d.UsedPct
		}
		c.Reconcile(ctx, DiskFull, open, func(mount string) string {
			p, ok := pct[mount]
			if !ok {
				return ""
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

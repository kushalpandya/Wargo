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
)

// updateSources lists the sources ReportUpdates takes, in body order.
var updateSources = []struct{ id, label string }{
	{"containers", "Containers"},
	{"system", "System"},
	{"homebrew", "Homebrew"},
}

// updatesState is what ReportUpdates remembers per source: the names the
// last report held. Guarded by Center.mu.
type updatesState map[string]map[string]bool

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
// source reports a name it did not report last time; a manual check
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
	if c.upd == nil {
		c.upd = updatesState{}
	}
	prev, seen := c.upd[source]
	fresh := false
	for n := range set {
		if !prev[n] {
			fresh = true
		}
	}
	c.upd[source] = set
	baseline := !seen
	total := 0
	var parts []string
	for _, s := range updateSources {
		if n := len(c.upd[s.id]); n > 0 {
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
	window := len(c.upd) < len(updateSources) && c.now().Sub(c.started) < updatesWindow
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
// its title follows the current percentage. The first call
// after New is the baseline: disks that recovered while Wargo was down are
// resolved, and full ones are shown without a push.
func (c *Center) ReportDisks(ctx context.Context, list []Disk) {
	c.mu.Lock()
	first := !c.disk.seen
	c.disk.seen = true
	c.mu.Unlock()

	if first {
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

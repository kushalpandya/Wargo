// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
)

// Recent keeps the last maxAge or maxItems items, whichever is smaller.
// The limits only apply to items that are over: resolved ones and plain
// records (download.finished, which never resolves). An open problem stays,
// cleared or not, for as long as it lasts: it is what stops the same
// problem from being raised and pushed again.
const (
	maxAge   = 7 * 24 * time.Hour
	maxItems = 100
)

// expirable is the SQL condition for an item the retention limits apply to.
const expirable = `(resolved_at IS NOT NULL OR category = '` + string(DownloadFinished) + `')`

// Occurrence is one happening of something worth a notification.
type Occurrence struct {
	Category Category
	// Key identifies what it is about within the category (a container
	// name, a torrent hash). One occurrence per key lasts until resolved.
	Key               string
	Title, Body, Link string
	// Baseline: it already existed when Wargo started; store it, never push.
	Baseline bool
	// Live: it is shown in Active (a download or stream starting); push
	// it per the category mode but never add a Recent item. The caller
	// decides what counts as new.
	Live bool
}

// Raise records o in Recent unless the same (category, key) has an item
// that is not resolved yet (cleared or not), in which case it does nothing.
// download.finished is a plain record that never resolves, so it is always
// recorded: SetDownloads raises it once per download, and a torrent added
// again is a new download.
// It reports whether the occurrence was due a push: its category mode is
// push and it is not a baseline. Items are stored even while their category
// is Off, so they show up if it is turned back on.
func (c *Center) Raise(ctx context.Context, o Occurrence) (pushed bool) {
	c.mu.Lock()
	due := c.modeLocked(o.Category) == ModePush && !o.Baseline
	if !o.Live {
		added, err := c.addLocked(ctx, o)
		if err != nil {
			slog.Error("notifications: raise", "category", o.Category, "key", o.Key, "err", err)
		}
		if !added {
			c.mu.Unlock()
			return false
		}
		c.publishLocked(ctx)
	}
	push := c.push
	c.mu.Unlock()
	if due && push != nil {
		push(ctx, o.Category, o.Key, o.Title, o.Body, o.Link)
	}
	return due
}

func (c *Center) addLocked(ctx context.Context, o Occurrence) (bool, error) {
	if o.Category != DownloadFinished {
		var one int
		err := c.st.DB.QueryRowContext(ctx,
			`SELECT 1 FROM notify_items WHERE category = ? AND key = ? AND resolved_at IS NULL LIMIT 1`,
			string(o.Category), o.Key).Scan(&one)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	now := c.now()
	if _, err := c.st.DB.ExecContext(ctx,
		`INSERT INTO notify_items(id, category, key, title, body, link, created_at) VALUES(?,?,?,?,?,?,?)`,
		rand.Text(), string(o.Category), o.Key, o.Title, o.Body, o.Link, now.UnixMilli()); err != nil {
		return false, err
	}
	return true, c.pruneLocked(ctx, now)
}

// pruneLocked applies the retention limits to the expirable items.
func (c *Center) pruneLocked(ctx context.Context, now time.Time) error {
	if _, err := c.st.DB.ExecContext(ctx, `DELETE FROM notify_items WHERE `+expirable+` AND created_at < ?`,
		now.Add(-maxAge).UnixMilli()); err != nil {
		return err
	}
	_, err := c.st.DB.ExecContext(ctx, `DELETE FROM notify_items WHERE `+expirable+` AND id NOT IN (
		SELECT id FROM notify_items WHERE `+expirable+` ORDER BY created_at DESC, rowid DESC LIMIT ?)`, maxItems)
	return err
}

// Reconcile resolves every open occurrence of cat whose key is not in
// current, the keys still happening now. Producers call it on their first
// look after a restart, so problems that ended while Wargo was down stop
// counting. resolvedTitle gives each resolved item its new title; nil (or
// an empty title) keeps the old one.
func (c *Center) Reconcile(ctx context.Context, cat Category, current []string, resolvedTitle func(key string) string) {
	keep := make(map[string]bool, len(current))
	for _, k := range current {
		keep[k] = true
	}
	c.mu.Lock()
	rows, err := c.st.DB.QueryContext(ctx,
		`SELECT DISTINCT key FROM notify_items WHERE category = ? AND resolved_at IS NULL`, string(cat))
	if err != nil {
		c.mu.Unlock()
		slog.Error("notifications: reconcile", "category", cat, "err", err)
		return
	}
	var stale []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			slog.Error("notifications: reconcile", "category", cat, "err", err)
			break
		}
		if !keep[k] {
			stale = append(stale, k)
		}
	}
	rows.Close()
	c.mu.Unlock()
	for _, k := range stale {
		title := ""
		if resolvedTitle != nil {
			title = resolvedTitle(k)
		}
		c.Resolve(ctx, cat, k, title)
	}
}

// Resolve marks the open occurrence of (cat, key) as resolved, so it stops
// counting toward the badge, and replaces its title when title is not
// empty ("gluetun is healthy again"). It does nothing if none is open.
func (c *Center) Resolve(ctx context.Context, cat Category, key, title string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.st.DB.ExecContext(ctx,
		`UPDATE notify_items SET resolved_at = ?, title = CASE WHEN ? = '' THEN title ELSE ? END
		WHERE category = ? AND key = ? AND resolved_at IS NULL`,
		c.now().UnixMilli(), title, title, string(cat), key)
	if err != nil {
		slog.Error("notifications: resolve", "category", cat, "key", key, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		c.publishLocked(ctx)
	}
}

// Clear hides one Recent item. An unknown or already cleared id is
// httpx.ErrNotFound.
func (c *Center) Clear(ctx context.Context, id string) error {
	return c.clear(ctx, `AND id = ?`, id)
}

// ClearAll hides every Recent item. Active rows stay.
func (c *Center) ClearAll(ctx context.Context) error {
	err := c.clear(ctx, ``)
	if errors.Is(err, httpx.ErrNotFound) {
		return nil
	}
	return err
}

func (c *Center) clear(ctx context.Context, where string, args ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, err := c.st.DB.ExecContext(ctx,
		`UPDATE notify_items SET cleared_at = ? WHERE cleared_at IS NULL `+where,
		append([]any{c.now().UnixMilli()}, args...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	c.publishLocked(ctx)
	return nil
}

// recentLocked lists the visible Recent items, newest first: not cleared,
// open or within retention, and in a category that is not Off. pruneLocked
// keeps the expirable items within maxItems.
func (c *Center) recentLocked(ctx context.Context) ([]Item, error) {
	rows, err := c.st.DB.QueryContext(ctx,
		`SELECT id, category, key, title, COALESCE(body, ''), COALESCE(link, ''), created_at, resolved_at
		FROM notify_items WHERE cleared_at IS NULL AND (NOT `+expirable+` OR created_at >= ?)
		ORDER BY created_at DESC, rowid DESC`,
		c.now().Add(-maxAge).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var created int64
		var resolved sql.NullInt64
		if err := rows.Scan(&it.ID, &it.Category, &it.Key, &it.Title, &it.Body, &it.Link, &created, &resolved); err != nil {
			return nil, err
		}
		if c.modeLocked(it.Category) == ModeOff {
			continue
		}
		it.CreatedAt = time.UnixMilli(created).UTC()
		if resolved.Valid {
			t := time.UnixMilli(resolved.Int64).UTC()
			it.ResolvedAt = &t
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// upsertLocked sets the title and body of the open item for (cat, key),
// creating it when there is none. With reshow, a cleared item becomes
// visible again and moves to the top (it is news). It reports whether the
// item was created or changed.
func (c *Center) upsertLocked(ctx context.Context, o Occurrence, reshow bool) (bool, error) {
	now := c.now()
	res, err := c.st.DB.ExecContext(ctx,
		`UPDATE notify_items SET title = ?, body = ?, link = ?,
			cleared_at = CASE WHEN ? THEN NULL ELSE cleared_at END,
			created_at = CASE WHEN ? THEN ? ELSE created_at END
		WHERE category = ? AND key = ? AND resolved_at IS NULL
			AND (title != ? OR body != ? OR link != ? OR (? AND cleared_at IS NOT NULL))`,
		o.Title, o.Body, o.Link, reshow, reshow, now.UnixMilli(),
		string(o.Category), o.Key, o.Title, o.Body, o.Link, reshow)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	return c.addLocked(ctx, o)
}

// deleteLocked removes every item of (cat, key), resolved or not.
func (c *Center) deleteLocked(ctx context.Context, cat Category, key string) (bool, error) {
	res, err := c.st.DB.ExecContext(ctx, `DELETE FROM notify_items WHERE category = ? AND key = ?`, string(cat), key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

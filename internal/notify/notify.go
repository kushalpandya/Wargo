// SPDX-License-Identifier: AGPL-3.0-only

// Package notify owns Wargo's notifications: the Active and Recent items in
// the panel, the per-category settings (Off, In app, In app and push) and
// the live "notifications" hub topic. State is kept in SQLite so a clear on
// one device clears everywhere.
package notify

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Topic is the sticky hub topic carrying the Snapshot.
const Topic = "notifications"

const settingsKey = "notify.settings"

// Category is a kind of notification, with its own setting.
type Category string

const (
	DownloadStarted    Category = "download.started"
	DownloadFinished   Category = "download.finished"
	StreamStarted      Category = "stream.started"
	ContainerUnhealthy Category = "container.unhealthy"
	ContainerFailed    Category = "container.failed"
	UpdatesFound       Category = "updates.found"
	DownloadError      Category = "download.error"
	DiskFull           Category = "disk.full"
)

// Categories lists every category in settings order.
var Categories = []Category{
	DownloadStarted, DownloadFinished, StreamStarted, ContainerUnhealthy,
	ContainerFailed, UpdatesFound, DownloadError, DiskFull,
}

func (c Category) valid() bool {
	for _, k := range Categories {
		if c == k {
			return true
		}
	}
	return false
}

// Mode is how a category is delivered.
type Mode string

const (
	ModeOff  Mode = "off"  // nowhere
	ModeApp  Mode = "app"  // in the panel only
	ModePush Mode = "push" // in the panel and pushed to devices
)

func (m Mode) valid() bool { return m == ModeOff || m == ModeApp || m == ModePush }

// Item is a Recent row.
type Item struct {
	ID         string     `json:"id"`
	Category   Category   `json:"category"`
	Key        string     `json:"key"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	Link       string     `json:"link"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	ClearedAt  *time.Time `json:"clearedAt"`
}

// Active is a live row (a download running, a stream playing). It can't be
// cleared; it leaves when the thing it shows ends.
type Active struct {
	ID        string    `json:"id"`
	Category  Category  `json:"category"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Progress  float64   `json:"progress"`
	StartedAt time.Time `json:"startedAt"`
	Link      string    `json:"link"`
}

// Snapshot is GET /api/notifications and the Topic payload.
type Snapshot struct {
	Active   []Active          `json:"active"`
	Recent   []Item            `json:"recent"`
	Badge    int               `json:"badge"`
	Settings map[Category]Mode `json:"settings"`
}

// PushFunc delivers one notification to every device that enabled push.
type PushFunc func(ctx context.Context, cat Category, key, title, body, link string)

// Center holds notification state. Its methods are safe for concurrent use.
type Center struct {
	st  *store.Store
	hub *events.Hub

	// now is the clock (replaced in tests).
	now func() time.Time
	// push is called for occurrences whose category mode is push, outside
	// the lock. New sets it to sendPush (push.go).
	push PushFunc

	// Web Push (push.go): the VAPID signing key, the client for push
	// services, and the sends still running.
	vapid      *ecdsa.PrivateKey
	pushClient *http.Client
	sending    sync.WaitGroup
	// closeMu guards closed: after Close, nothing more is sent.
	closeMu sync.Mutex
	closed  bool
	// idleCutoff is auth's idle limit: a session that is not remembered
	// and was last seen before idleCutoff(now) has timed out.
	idleCutoff func(now time.Time) time.Time
	// started is when New ran (see updatesWindow).
	started time.Time

	mu       sync.Mutex
	settings map[Category]Mode
	// live downloads and streams behind the Active rows (active.go).
	live liveState
	// what ReportUpdates and ReportDisks remember (updates.go).
	upd  updatesState
	disk diskState
}

// New loads the settings and the VAPID key, and publishes the first
// Snapshot. idleCutoff is auth.Service.IdleCutoff, used to tell which
// sessions may still receive push.
func New(st *store.Store, hub *events.Hub, idleCutoff func(now time.Time) time.Time) (*Center, error) {
	c := &Center{st: st, hub: hub, now: time.Now, settings: defaults(), pushClient: newPushClient(),
		idleCutoff: idleCutoff}
	c.push = c.sendPush
	c.started = c.now()
	ctx := context.Background()
	var err error
	if c.vapid, err = loadVAPID(ctx, st); err != nil {
		return nil, err
	}
	var saved map[Category]Mode
	if _, err := st.Get(ctx, settingsKey, &saved); err != nil {
		return nil, fmt.Errorf("notification settings: %w", err)
	}
	for k, m := range saved {
		if k.valid() && m.valid() {
			c.settings[k] = m
		}
	}
	hub.Sticky(Topic)
	c.mu.Lock()
	c.publishLocked(ctx)
	c.mu.Unlock()
	return c, nil
}

func defaults() map[Category]Mode {
	m := make(map[Category]Mode, len(Categories))
	for _, k := range Categories {
		m[k] = ModePush
	}
	return m
}

// Mode returns the setting for cat (push for an unknown category).
func (c *Center) Mode(cat Category) Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.modeLocked(cat)
}

func (c *Center) modeLocked(cat Category) Mode {
	if m, ok := c.settings[cat]; ok {
		return m
	}
	return ModePush
}

// Settings returns a copy of every category's mode.
func (c *Center) Settings() map[Category]Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settingsLocked()
}

func (c *Center) settingsLocked() map[Category]Mode {
	out := make(map[Category]Mode, len(c.settings))
	for k, m := range c.settings {
		out[k] = m
	}
	return out
}

// SetModes changes the categories in m, leaving the others as they are.
// Unknown categories or modes are a 400 error and nothing changes.
func (c *Center) SetModes(ctx context.Context, m map[Category]Mode) error {
	for k, v := range m {
		if !k.valid() {
			return httpx.BadRequest(fmt.Sprintf("unknown category %q", k))
		}
		if !v.valid() {
			return httpx.BadRequest(fmt.Sprintf("unknown mode %q for %s", v, k))
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.settingsLocked()
	for k, v := range m {
		next[k] = v
	}
	if err := c.st.Set(ctx, settingsKey, next); err != nil {
		return err
	}
	c.settings = next
	c.publishLocked(ctx)
	return nil
}

// Snapshot returns what the panel shows now.
func (c *Center) Snapshot(ctx context.Context) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked(ctx)
}

func (c *Center) snapshotLocked(ctx context.Context) (Snapshot, error) {
	s := Snapshot{Active: []Active{}, Settings: c.settingsLocked()}
	s.Active = append(s.Active, c.activeLocked()...)
	recent, err := c.recentLocked(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	s.Recent = recent
	s.Badge = len(s.Active)
	for _, it := range s.Recent {
		if it.ResolvedAt == nil {
			s.Badge++
		}
	}
	return s, nil
}

// publishLocked sends the current Snapshot on Topic. Call with c.mu held,
// so snapshots go out in the order the changes were made.
func (c *Center) publishLocked(ctx context.Context) {
	s, err := c.snapshotLocked(ctx)
	if err != nil {
		slog.Error("notifications: snapshot", "err", err)
		return
	}
	c.hub.Publish(Topic, s)
}

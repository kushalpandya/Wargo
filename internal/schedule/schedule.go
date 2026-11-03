// SPDX-License-Identifier: AGPL-3.0-only

// Package schedule holds the "check every N hours" setting shared by the
// background checks (OS updates, container images) in Settings → General.
package schedule

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Settings is the body of GET/PUT .../settings.
type Settings struct {
	// CheckHours: check in the background every N hours (0 = off).
	CheckHours int `json:"checkHours"`
}

// Hours is one interval setting persisted under Key.
type Hours struct {
	Key     string // store key
	Action  string // audit action recorded on PUT
	Default int    // used when nothing valid is saved
	Allowed []int  // ascending, 0 = off

	// Optional; set before serving.
	Store *store.Store
	Audit *audit.Log
	// Current reports the value for GET (default: the saved value).
	Current func() int
	// Apply runs after a new value was saved.
	Apply func(hours int)

	wake chan struct{}
}

// New creates the setting.
func New(key, action string, def int, allowed ...int) *Hours {
	return &Hours{Key: key, Action: action, Default: def, Allowed: allowed, wake: make(chan struct{}, 1)}
}

// Wake fires after the setting changed.
func (h *Hours) Wake() <-chan struct{} { return h.wake }

// Valid reports whether n is an allowed value.
func (h *Hours) Valid(n int) bool {
	for _, a := range h.Allowed {
		if a == n {
			return true
		}
	}
	return false
}

// Saved returns the stored value if there is a valid one.
func (h *Hours) Saved(ctx context.Context) (int, bool) {
	if h.Store == nil {
		return 0, false
	}
	var v Settings
	if ok, err := h.Store.Get(ctx, h.Key, &v); !ok || err != nil || !h.Valid(v.CheckHours) {
		return 0, false
	}
	return v.CheckHours, true
}

// Load returns the stored value or the default.
func (h *Hours) Load(ctx context.Context) int {
	if v, ok := h.Saved(ctx); ok {
		return v
	}
	return h.Default
}

// Ceil returns the smallest allowed value of at least d (0 stays 0;
// anything above the largest returns the largest).
func (h *Hours) Ceil(d time.Duration) int {
	if d <= 0 || len(h.Allowed) == 0 {
		return 0
	}
	for _, a := range h.Allowed {
		if a > 0 && time.Duration(a)*time.Hour >= d {
			return a
		}
	}
	return h.Allowed[len(h.Allowed)-1]
}

// HandleGet serves GET .../settings.
func (h *Hours) HandleGet(w http.ResponseWriter, r *http.Request) error {
	var v int
	if h.Current != nil {
		v = h.Current()
	} else {
		v = h.Load(r.Context())
	}
	httpx.OK(w, Settings{CheckHours: v})
	return nil
}

// HandlePut serves PUT .../settings.
func (h *Hours) HandlePut(w http.ResponseWriter, r *http.Request) error {
	var v Settings
	if err := httpx.Decode(r, &v); err != nil {
		return err
	}
	if !h.Valid(v.CheckHours) {
		return httpx.BadRequest("checkHours must be " + h.list())
	}
	if h.Store != nil {
		if err := h.Store.Set(r.Context(), h.Key, v); err != nil {
			return err
		}
	}
	if h.Apply != nil {
		h.Apply(v.CheckHours)
	}
	select {
	case h.wake <- struct{}{}:
	default:
	}
	if h.Audit != nil {
		h.Audit.Record(r, h.Action, fmt.Sprintf("check every %dh", v.CheckHours), nil, nil)
	}
	httpx.OK(w, v)
	return nil
}

// list renders Allowed as "0, 6, 12 or 24".
func (h *Hours) list() string {
	s := make([]string, len(h.Allowed))
	for i, a := range h.Allowed {
		s[i] = strconv.Itoa(a)
	}
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}

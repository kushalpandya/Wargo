// SPDX-License-Identifier: AGPL-3.0-only

// Package schedule holds the "check every N hours" setting shared by the
// background checks (OS updates, Homebrew, container images) in Settings →
// General.
package schedule

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Settings is the body of GET .../settings and of the PUT response.
type Settings struct {
	// Enabled: background checks run. Off keeps CheckHours, so switching
	// back on restores the frequency.
	Enabled bool `json:"enabled"`
	// CheckHours: how often to check while enabled (never 0).
	CheckHours int `json:"checkHours"`
}

// Hours is the effective interval: CheckHours while enabled, else 0.
func (v Settings) Hours() int {
	if !v.Enabled {
		return 0
	}
	return v.CheckHours
}

// saved is a stored value. Values saved before the on/off switch have no
// "enabled" and use checkHours 0 for off.
type saved struct {
	Enabled    *bool `json:"enabled"`
	CheckHours int   `json:"checkHours"`
}

// update is the body of PUT .../settings: either field may be left out to
// keep it as it is. checkHours 0 (the old "Off" choice) switches off and
// keeps the frequency.
type update struct {
	Enabled    *bool `json:"enabled"`
	CheckHours *int  `json:"checkHours"`
}

// UpdatesKey stores the one update-check setting shared by OS packages,
// Homebrew and container images (key kept from when only packages had it).
const UpdatesKey = "pkg.settings"

// Updates returns a handle on the shared update-check setting: daily by
// default, 6h to weekly, 0 accepted on PUT as "off".
func Updates(action string) *Hours {
	return New(UpdatesKey, action, 24, 0, 6, 12, 24, 72, 168)
}

// Hours is one interval setting persisted under Key.
type Hours struct {
	Key     string // store key
	Action  string // audit action recorded on PUT
	Default int    // frequency used when nothing valid is saved (0 = off)
	Allowed []int  // ascending, 0 = off

	// Optional; set before serving.
	Store *store.Store
	Audit *audit.Log
	// Apply runs with the new effective interval (0 when off) after the
	// value was saved through any setting sharing Key.
	Apply func(hours int)

	wake chan struct{}
	// firstWait is how long Every lets the server settle before its first
	// check (3 minutes when zero; shortened in tests).
	firstWait time.Duration
}

// registry lists every setting by store key, so a change saved through one
// wakes all the checks that share the key (OS packages, Homebrew and
// container images).
var registry = struct {
	sync.Mutex
	byKey map[string][]*Hours
}{byKey: map[string][]*Hours{}}

// saveMu serializes the read-modify-write of PUT and Adopt.
var saveMu sync.Mutex

// New creates the setting. Settings created with the same key share one
// stored value, and saving it through any of them wakes them all. Without
// a Store nothing is kept: GET always returns the default.
func New(key, action string, def int, allowed ...int) *Hours {
	h := &Hours{Key: key, Action: action, Default: def, Allowed: allowed, wake: make(chan struct{}, 1)}
	registry.Lock()
	registry.byKey[key] = append(registry.byKey[key], h)
	registry.Unlock()
	return h
}

// Wake fires after the setting changed.
func (h *Hours) Wake() <-chan struct{} { return h.wake }

// notify applies hours to and wakes every setting saved under h's key in
// the same store.
func (h *Hours) notify(hours int) {
	registry.Lock()
	all := append([]*Hours(nil), registry.byKey[h.Key]...)
	registry.Unlock()
	for _, x := range all {
		if x.Store != h.Store {
			continue
		}
		if x.Apply != nil {
			x.Apply(hours)
		}
		select {
		case x.wake <- struct{}{}:
		default:
		}
	}
}

// Valid reports whether n is an allowed value (0 included, meaning off).
func (h *Hours) Valid(n int) bool {
	for _, a := range h.Allowed {
		if a == n {
			return true
		}
	}
	return false
}

// frequency reports whether n is an allowed frequency (not 0).
func (h *Hours) frequency(n int) bool { return n > 0 && h.Valid(n) }

// defaults is the value used when nothing valid is saved.
func (h *Hours) defaults() Settings {
	if h.frequency(h.Default) {
		return Settings{Enabled: true, CheckHours: h.Default}
	}
	for _, a := range h.Allowed {
		if a > 0 {
			return Settings{CheckHours: a}
		}
	}
	return Settings{}
}

// normalize turns a stored value into Settings: without "enabled" it is on
// when checkHours > 0, and a frequency that isn't allowed (0 included)
// becomes the default one.
func (h *Hours) normalize(v saved) Settings {
	out := Settings{Enabled: v.CheckHours > 0, CheckHours: v.CheckHours}
	if v.Enabled != nil {
		out.Enabled = *v.Enabled
	}
	if !h.frequency(out.CheckHours) {
		out.CheckHours = h.defaults().CheckHours
	}
	return out
}

// stored reads key, reporting whether a readable value is saved there. A
// value that doesn't decode counts as missing, so the next save heals it;
// an error means the store couldn't be read and nothing is known.
func (h *Hours) stored(ctx context.Context, key string) (saved, bool, error) {
	var v saved
	if h.Store == nil {
		return v, false, nil
	}
	ok, err := h.Store.Get(ctx, key, &v)
	switch {
	case ok && err != nil: // saved but not decodable
		return saved{}, false, nil
	case err != nil:
		return saved{}, false, err
	}
	return v, ok, nil
}

// Get returns the stored setting, the default when nothing is saved, or
// the error when the store can't be read.
func (h *Hours) Get(ctx context.Context) (Settings, error) {
	v, ok, err := h.stored(ctx, h.Key)
	if err != nil {
		return Settings{}, err
	}
	if !ok {
		return h.defaults(), nil
	}
	return h.normalize(v), nil
}

// Load returns the effective interval: the frequency while enabled, else
// 0. It is also 0 when the store can't be read, so a read error never
// starts checks the owner may have switched off.
func (h *Hours) Load(ctx context.Context) int {
	v, err := h.Get(ctx)
	if err != nil {
		return 0
	}
	return v.Hours()
}

// Adopt moves a value saved under an older key into Key. It is copied only
// when Key has no value yet and the old value is on with a frequency (one
// that isn't allowed rounds up, see Ceil). An old "off" is dropped: it
// switched off only the old key's checks, so it must not switch off the
// others that share Key. The old key is deleted either way, also when it
// doesn't decode. When either key can't be read both are left alone, to
// retry on the next start.
func (h *Hours) Adopt(ctx context.Context, old string) {
	if h.Store == nil {
		return
	}
	v, ok, err := h.stored(ctx, old)
	if err != nil {
		return
	}
	if !ok { // absent, or not decodable: nothing to copy
		h.Store.Delete(ctx, old)
		return
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	_, has, err := h.stored(ctx, h.Key)
	if err != nil {
		return
	}
	if !has && v.CheckHours > 0 && (v.Enabled == nil || *v.Enabled) {
		if !h.frequency(v.CheckHours) {
			v.CheckHours = h.Ceil(time.Duration(v.CheckHours) * time.Hour)
		}
		s := h.normalize(v)
		if err := h.Store.Set(ctx, h.Key, s); err != nil {
			return
		}
		h.notify(s.Hours())
	}
	h.Store.Delete(ctx, old)
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
	v, err := h.Get(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, v)
	return nil
}

// HandlePut serves PUT .../settings ({enabled?, checkHours?}).
func (h *Hours) HandlePut(w http.ResponseWriter, r *http.Request) error {
	var u update
	if err := httpx.Decode(r, &u); err != nil {
		return err
	}
	if u.Enabled == nil && u.CheckHours == nil {
		return httpx.BadRequest("send enabled or checkHours")
	}
	if u.CheckHours != nil {
		if !h.Valid(*u.CheckHours) {
			return httpx.BadRequest("checkHours must be " + h.list())
		}
		if *u.CheckHours == 0 && u.Enabled != nil && *u.Enabled {
			return httpx.BadRequest("checkHours 0 means off")
		}
	}
	saveMu.Lock()
	v, err := h.Get(r.Context())
	if err != nil {
		// Saving defaults now would lose the remembered frequency.
		saveMu.Unlock()
		return err
	}
	if u.CheckHours != nil {
		if *u.CheckHours == 0 {
			v.Enabled = false
		} else {
			v.CheckHours = *u.CheckHours
		}
	}
	if u.Enabled != nil {
		v.Enabled = *u.Enabled
	}
	if h.Store != nil {
		if err := h.Store.Set(r.Context(), h.Key, v); err != nil {
			saveMu.Unlock()
			return err
		}
	}
	h.notify(v.Hours())
	saveMu.Unlock()
	if h.Audit != nil {
		msg := fmt.Sprintf("check every %dh", v.CheckHours)
		if !v.Enabled {
			msg = fmt.Sprintf("background checks off (every %dh kept)", v.CheckHours)
		}
		h.Audit.Record(r, h.Action, msg, nil, nil)
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

// SPDX-License-Identifier: AGPL-3.0-only

package schedule

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// newStore opens a throwaway store.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// do runs fn with body and returns the code and trimmed response body.
func do(t *testing.T, fn func(http.ResponseWriter, *http.Request) error, method, body string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	httpx.Handler(fn)(w, httptest.NewRequest(method, "/", strings.NewReader(body)))
	return w.Code, strings.TrimSpace(w.Body.String())
}

// get returns h's setting, failing the test when the store can't be read.
func get(t *testing.T, h *Hours) Settings {
	t.Helper()
	v, err := h.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// seed saves raw JSON values, failing the test on a store error.
func seed(t *testing.T, st *store.Store, saved map[string]string) {
	t.Helper()
	for k, v := range saved {
		if err := st.Set(context.Background(), k, json.RawMessage(v)); err != nil {
			t.Fatal(err)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigrateSavedValue(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		saved string
		want  Settings
		hours int
	}{
		{`{"checkHours":0}`, Settings{Enabled: false, CheckHours: 24}, 0},
		{`{"checkHours":12}`, Settings{Enabled: true, CheckHours: 12}, 12},
		{`{"enabled":false,"checkHours":6}`, Settings{Enabled: false, CheckHours: 6}, 0},
		{`{"enabled":true,"checkHours":5}`, Settings{Enabled: true, CheckHours: 24}, 24},
	} {
		st := newStore(t)
		h := New("m.settings", "m", 24, 0, 6, 12, 24)
		h.Store = st
		if err := st.Set(ctx, h.Key, json.RawMessage(c.saved)); err != nil {
			t.Fatal(err)
		}
		if got := get(t, h); got != c.want {
			t.Errorf("%s reads as %+v, want %+v", c.saved, got, c.want)
		}
		if got := h.Load(ctx); got != c.hours {
			t.Errorf("%s runs every %dh, want %d", c.saved, got, c.hours)
		}
		if _, body := do(t, h.HandleGet, http.MethodGet, ""); body != mustJSON(t, c.want) {
			t.Errorf("%s: GET %s", c.saved, body)
		}
	}
}

func TestSwitchOffKeepsFrequency(t *testing.T) {
	ctx := context.Background()
	h := New("off.settings", "off", 24, 0, 6, 12, 24)
	h.Store = newStore(t)
	if code, body := do(t, h.HandleGet, http.MethodGet, ""); code != 200 || body != `{"enabled":true,"checkHours":24}` {
		t.Fatalf("default GET %d %s", code, body)
	}
	if code, body := do(t, h.HandlePut, http.MethodPut, `{"checkHours":12}`); code != 200 || body != `{"enabled":true,"checkHours":12}` {
		t.Fatalf("frequency %d %s", code, body)
	}
	if code, body := do(t, h.HandlePut, http.MethodPut, `{"enabled":false}`); code != 200 || body != `{"enabled":false,"checkHours":12}` {
		t.Fatalf("off %d %s", code, body)
	}
	if h.Load(ctx) != 0 {
		t.Fatal("switched off still schedules")
	}
	// A frequency change while off is kept and leaves it off.
	if code, body := do(t, h.HandlePut, http.MethodPut, `{"checkHours":6}`); code != 200 || body != `{"enabled":false,"checkHours":6}` {
		t.Fatalf("frequency while off %d %s", code, body)
	}
	if code, body := do(t, h.HandlePut, http.MethodPut, `{"enabled":true}`); code != 200 || body != `{"enabled":true,"checkHours":6}` || h.Load(ctx) != 6 {
		t.Fatalf("on %d %s", code, body)
	}
	// The old "Off" choice (0) switches off and keeps the frequency.
	if code, body := do(t, h.HandlePut, http.MethodPut, `{"checkHours":0}`); code != 200 || body != `{"enabled":false,"checkHours":6}` {
		t.Fatalf("legacy off %d %s", code, body)
	}
	for _, bad := range []string{`{}`, `{"checkHours":5}`, `{"enabled":true,"checkHours":0}`} {
		if code, _ := do(t, h.HandlePut, http.MethodPut, bad); code != 400 {
			t.Errorf("%s accepted: %d", bad, code)
		}
	}
}

// Every doesn't check while switched off, and checks soon after switching
// on. 5 hours has no fixed slots (see Next), so with no check run yet the
// first one is due as soon as firstWait passes, at any time of day.
func TestEveryStopsWhenOff(t *testing.T) {
	h := New("every.settings", "every", 5, 0, 5)
	h.Store = newStore(t)
	h.firstWait = time.Millisecond
	if code, _ := do(t, h.HandlePut, http.MethodPut, `{"enabled":false}`); code != 200 {
		t.Fatal(code)
	}
	<-h.Wake()
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Every(ctx, "every", func() bool { return true }, func() time.Time { return time.Time{} },
			func(context.Context) error { ran <- struct{}{}; return nil })
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-ran:
		t.Fatal("checked while switched off")
	case <-time.After(200 * time.Millisecond):
	}
	if code, _ := do(t, h.HandlePut, http.MethodPut, `{"enabled":true}`); code != 200 {
		t.Fatal(code)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("no check after switching on")
	}
}

// failingStore saves the given values, then returns the store closed so
// every read fails; reopen gives a working store on the same data.
func failingStore(t *testing.T, saved map[string]string) (st *store.Store, reopen func() *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	seed(t, st, saved)
	st.Close()
	return st, func() *store.Store {
		s, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
}

// A read error must not save defaults over the remembered frequency.
func TestPutReadErrorSavesNothing(t *testing.T) {
	st, reopen := failingStore(t, map[string]string{"rerr.settings": `{"enabled":true,"checkHours":12}`})
	h := New("rerr.settings", "rerr", 24, 0, 12, 24)
	h.Store = st
	if code, _ := do(t, h.HandlePut, http.MethodPut, `{"enabled":false}`); code != 500 {
		t.Fatalf("PUT on an unreadable store: %d", code)
	}
	if code, _ := do(t, h.HandleGet, http.MethodGet, ""); code != 500 {
		t.Fatalf("GET on an unreadable store: %d", code)
	}
	// Not known whether checks are on, so none run.
	if got := h.Load(context.Background()); got != 0 {
		t.Fatalf("background load on an unreadable store: %d, want 0", got)
	}
	h.Store = reopen()
	if got := get(t, h); got != (Settings{Enabled: true, CheckHours: 12}) {
		t.Fatalf("saved value changed: %+v", got)
	}
}

// Every doesn't check while the setting can't be read.
func TestEveryWaitsOnReadError(t *testing.T) {
	st, _ := failingStore(t, map[string]string{"everr.settings": `{"enabled":false,"checkHours":5}`})
	h := New("everr.settings", "everr", 5, 0, 5)
	h.Store = st
	h.firstWait = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Every(ctx, "everr", func() bool { return true }, func() time.Time { return time.Time{} },
			func(context.Context) error { ran <- struct{}{}; return nil })
	}()
	defer func() { cancel(); <-done }()
	select {
	case <-ran:
		t.Fatal("checked while the setting couldn't be read")
	case <-time.After(200 * time.Millisecond):
	}
}

// A corrupt saved value counts as missing and heals on the next save.
func TestCorruptValueHeals(t *testing.T) {
	st := newStore(t)
	h := New("corrupt.settings", "corrupt", 24, 0, 6, 24)
	h.Store = st
	if err := st.Set(context.Background(), h.Key, "not an object"); err != nil {
		t.Fatal(err)
	}
	if code, body := do(t, h.HandlePut, http.MethodPut, `{"checkHours":6}`); code != 200 || body != `{"enabled":true,"checkHours":6}` {
		t.Fatalf("%d %s", code, body)
	}
	if v, ok, err := h.stored(context.Background(), h.Key); err != nil || !ok || h.normalize(v) != (Settings{Enabled: true, CheckHours: 6}) {
		t.Fatalf("stored %+v ok %v err %v", v, ok, err)
	}
}

func TestAdopt(t *testing.T) {
	ctx := context.Background()
	gone := func(t *testing.T, st *store.Store) {
		t.Helper()
		var raw json.RawMessage
		if ok, err := st.Get(ctx, "old.settings", &raw); err != nil || ok {
			t.Fatalf("old key kept (ok %v, err %v)", ok, err)
		}
	}
	t.Run("only old key", func(t *testing.T) {
		st := newStore(t)
		h := New("new1.settings", "new", 24, 0, 6, 12, 24, 72, 168)
		h.Store = st
		seed(t, st, map[string]string{"old.settings": `{"checkHours":3}`})
		h.Adopt(ctx, "old.settings")
		if got := get(t, h); got != (Settings{Enabled: true, CheckHours: 6}) {
			t.Fatalf("adopted %+v", got)
		}
		gone(t, st)
	})
	// An old "off" applied only to the old key's checks: it is dropped,
	// and the shared setting keeps its default.
	for _, old := range []string{`{"checkHours":0}`, `{"enabled":false,"checkHours":12}`} {
		t.Run("old off "+old, func(t *testing.T) {
			st := newStore(t)
			h := New("new2.settings", "new", 24, 0, 6, 12, 24)
			h.Store = st
			seed(t, st, map[string]string{"old.settings": old})
			h.Adopt(ctx, "old.settings")
			if _, has, err := h.stored(ctx, h.Key); err != nil || has {
				t.Fatalf("copied an off value (has %v, err %v)", has, err)
			}
			if got := get(t, h); got != (Settings{Enabled: true, CheckHours: 24}) {
				t.Fatalf("reads as %+v", got)
			}
			gone(t, st)
		})
	}
	t.Run("both keys", func(t *testing.T) {
		st := newStore(t)
		h := New("new3.settings", "new", 24, 0, 6, 12, 24)
		h.Store = st
		seed(t, st, map[string]string{h.Key: `{"checkHours":12}`, "old.settings": `{"checkHours":6}`})
		h.Adopt(ctx, "old.settings")
		if got := get(t, h); got != (Settings{Enabled: true, CheckHours: 12}) {
			t.Fatalf("kept %+v", got)
		}
		gone(t, st)
	})
	t.Run("old not decodable", func(t *testing.T) {
		st := newStore(t)
		h := New("new4.settings", "new", 24, 0, 6, 12, 24)
		h.Store = st
		if err := st.Set(ctx, "old.settings", "junk"); err != nil {
			t.Fatal(err)
		}
		h.Adopt(ctx, "old.settings")
		if _, has, err := h.stored(ctx, h.Key); err != nil || has {
			t.Fatalf("copied junk (has %v, err %v)", has, err)
		}
		gone(t, st)
	})
	t.Run("store unreadable", func(t *testing.T) {
		st, reopen := failingStore(t, map[string]string{"old.settings": `{"checkHours":12}`, "new5.settings": `{"checkHours":72}`})
		h := New("new5.settings", "new", 24, 0, 6, 12, 24, 72)
		h.Store = st
		h.Adopt(ctx, "old.settings")
		h.Store = reopen()
		if got := get(t, h); got != (Settings{Enabled: true, CheckHours: 72}) {
			t.Fatalf("new key changed: %+v", got)
		}
		if _, ok, _ := h.stored(ctx, "old.settings"); !ok {
			t.Fatal("old key deleted after a read error")
		}
	})
}

func TestSharedApply(t *testing.T) {
	st := newStore(t)
	a := New("apply.settings", "a", 24, 0, 6, 24)
	b := New("apply.settings", "b", 24, 0, 6, 24)
	a.Store, b.Store = st, st
	got := -1
	b.Apply = func(n int) { got = n }
	if code, _ := do(t, a.HandlePut, http.MethodPut, `{"checkHours":6}`); code != 200 || got != 6 {
		t.Fatalf("b applied %d", got)
	}
	if code, _ := do(t, a.HandlePut, http.MethodPut, `{"enabled":false}`); code != 200 || got != 0 {
		t.Fatalf("b applied %d after off", got)
	}
}

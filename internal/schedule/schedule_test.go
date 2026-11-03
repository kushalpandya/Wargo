// SPDX-License-Identifier: AGPL-3.0-only

package schedule

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestHours(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := New("x.settings", "x.settings", 24, 0, 6, 24, 168)
	h.Store = st
	applied := -1
	h.Apply = func(n int) { applied = n }
	if got := h.Load(context.Background()); got != 24 {
		t.Fatalf("default %d", got)
	}
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		httpx.Handler(h.HandlePut)(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)))
		return w
	}
	if w := put(`{"checkHours":2}`); w.Code != 400 || !strings.Contains(w.Body.String(), "0, 6, 24 or 168") {
		t.Fatalf("odd interval: %d %s", w.Code, w.Body)
	}
	if w := put(`{"checkHours":6}`); w.Code != 200 || applied != 6 || h.Load(context.Background()) != 6 {
		t.Fatalf("put: %d applied %d", w.Code, applied)
	}
	select {
	case <-h.Wake():
	default:
		t.Fatal("not woken")
	}
	w := httptest.NewRecorder()
	httpx.Handler(h.HandleGet)(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.TrimSpace(w.Body.String()) != `{"checkHours":6}` {
		t.Fatalf("get %s", w.Body)
	}
	for d, want := range map[time.Duration]int{0: 0, 30 * time.Minute: 6, 6 * time.Hour: 6, 7 * time.Hour: 24, 1000 * time.Hour: 168} {
		if got := h.Ceil(d); got != want {
			t.Errorf("Ceil(%v) = %d, want %d", d, got, want)
		}
	}
}

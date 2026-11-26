// SPDX-License-Identifier: AGPL-3.0-only

package procs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakePID is above any pid the host hands out, so nothing real is
// signalled.
const fakePID = 4194300

// withSample makes s answer from a sample holding one process.
func withSample(s *Service, p Proc) {
	s.mu.Lock()
	s.last = &List{Procs: []Proc{p}, SampledAt: time.Now().UnixMilli()}
	s.stale = false
	s.mu.Unlock()
}

// A signal request naming the process's start time is refused when the
// pid now belongs to another process; without one (the browser) it goes
// ahead as before.
func TestSignalRefusedWhenProcessReplaced(t *testing.T) {
	s := New(nil, nil, nil, nil)
	mux := http.NewServeMux()
	s.Register(mux)
	signal := func(body string) *httptest.ResponseRecorder {
		withSample(s, Proc{PID: fakePID, Name: "worker", User: "root", startTicks: 222})
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/procs/"+strconv.Itoa(fakePID)+"/signal", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}

	w := signal(`{"signal":"TERM","startTicks":111}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"process_changed"`) {
		t.Fatalf("replaced process: %d %s", w.Code, w.Body)
	}
	// Absent or matching, the handler goes on to signal: the fake pid has
	// no real process behind it, so it ends as not found.
	for _, body := range []string{`{"signal":"TERM"}`, `{"signal":"TERM","startTicks":222}`} {
		if w := signal(body); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
}

// The details carry the start time a signal request can name.
func TestDetailsCarryStartTicks(t *testing.T) {
	s := New(nil, nil, nil, nil)
	mux := http.NewServeMux()
	s.Register(mux)
	withSample(s, Proc{PID: fakePID, Name: "worker", startTicks: 222})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/procs/"+strconv.Itoa(fakePID), nil))
	var d struct {
		StartTicks uint64 `json:"startTicks"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &d) != nil || d.StartTicks != 222 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

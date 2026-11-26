// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func agentRequest(id, name, tool string) *http.Request {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	ctx := WithAgent(r.Context(), AgentRef{ID: id, Name: name, Tool: tool})
	ctx = WithSession(ctx, "abcd1234")
	return r.WithContext(ctx)
}

func listEntries(t *testing.T, l *Log, query string) []Entry {
	t.Helper()
	mux := http.NewServeMux()
	l.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/audit"+query, nil))
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", query, w.Code, w.Body)
	}
	var out []Entry
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAgentContextSetsSource(t *testing.T) {
	l, _ := newLog(t)
	id := l.Write(agentRequest("a1", "OpenClaw", "list_containers"), Event{Action: "docker.list"})
	e, _ := get(t, l, id)
	if e.Source != SourceAgent {
		t.Fatalf("source %q", e.Source)
	}
	if e.Agent == nil || *e.Agent != (AgentRef{"a1", "OpenClaw", "list_containers"}) {
		t.Fatalf("agent %+v", e.Agent)
	}
	if e.IP != "10.0.0.5" || e.Session != "abcd1234" {
		t.Fatalf("ip %q session %q", e.IP, e.Session)
	}
	plain, _ := get(t, l, l.Write(httptest.NewRequest("GET", "/x", nil), Event{Action: "x"}))
	if plain.Source != SourceUser || plain.Agent != nil {
		t.Fatalf("non-agent entry changed: %+v", plain)
	}
}

func TestAgentFilter(t *testing.T) {
	l, _ := newLog(t)
	l.Write(agentRequest("a1", "One", "t"), Event{Action: "x"})
	l.Write(agentRequest("a2", "Two", "t"), Event{Action: "x"})
	l.Write(agentRequest("a1", "One", "u"), Event{Action: "y"})
	l.Write(httptest.NewRequest("GET", "/x", nil), Event{Action: "x"})
	if n := len(listEntries(t, l, "?source=agent")); n != 3 {
		t.Fatalf("source=agent: %d", n)
	}
	got := listEntries(t, l, "?agent=a1")
	if len(got) != 2 {
		t.Fatalf("agent=a1: %d", len(got))
	}
	for _, e := range got {
		if e.Agent == nil || e.Agent.ID != "a1" {
			t.Fatalf("entry %+v", e)
		}
	}
}

func fakeClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	cur := start
	old := now
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = old })
	return &cur
}

func countOf(t *testing.T, e Entry) int {
	t.Helper()
	var d struct{ Count int }
	if err := json.Unmarshal(e.Details, &d); err != nil {
		t.Fatal(err)
	}
	return d.Count
}

func TestWriteFoldedFolds(t *testing.T) {
	l, _ := newLog(t)
	clock := fakeClock(t, time.Unix(1_800_000_000, 0))
	r := agentRequest("a1", "One", "list_containers")
	ev := Event{Action: "docker.list", Target: "all"}
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, l.WriteFolded(r, ev, 10*time.Minute))
		*clock = clock.Add(time.Second)
	}
	if ids[0] == 0 || ids[1] != ids[0] || ids[2] != ids[0] {
		t.Fatalf("ids %v", ids)
	}
	got := listEntries(t, l, "?agent=a1")
	if len(got) != 1 || countOf(t, got[0]) != 3 {
		t.Fatalf("entries %+v", got)
	}
	if want := time.Unix(1_800_000_002, 0); !got[0].TS.Equal(want) {
		t.Fatalf("ts %v, want %v", got[0].TS, want)
	}
	*clock = clock.Add(11 * time.Minute)
	if id := l.WriteFolded(r, ev, 10*time.Minute); id == ids[0] {
		t.Fatal("folded after the window")
	}
	got = listEntries(t, l, "?agent=a1")
	if len(got) != 2 || countOf(t, got[0]) != 1 || countOf(t, got[1]) != 3 {
		t.Fatalf("entries %+v", got)
	}
}

func TestWriteFoldedKeepsDistinctTargets(t *testing.T) {
	l, _ := newLog(t)
	fakeClock(t, time.Unix(1_800_000_000, 0))
	r := agentRequest("a1", "One", "t")
	a := l.WriteFolded(r, Event{Action: "docker.list", Target: "x"}, time.Minute)
	b := l.WriteFolded(r, Event{Action: "docker.list", Target: "y"}, time.Minute)
	c := l.WriteFolded(agentRequest("a2", "Two", "t"), Event{Action: "docker.list", Target: "x"}, time.Minute)
	if a == b || a == c || b == c {
		t.Fatalf("folded: %d %d %d", a, b, c)
	}
	if n := len(listEntries(t, l, "")); n != 3 {
		t.Fatalf("entries: %d", n)
	}
}

// WithRef links every entry written for the request to an earlier one,
// unless the event names its own Ref.
func TestWithRef(t *testing.T) {
	l, _ := newLog(t)
	first := l.Write(nil, Event{Action: "mcp.approval.requested"})
	r := agentRequest("a1", "One", "t")
	r = r.WithContext(WithRef(r.Context(), first))
	e, _ := get(t, l, l.Write(r, Event{Action: "thing.poke"}))
	if e.Ref != first {
		t.Fatalf("ref %d, want %d", e.Ref, first)
	}
	own, _ := get(t, l, l.Write(r, Event{Action: "thing.other", Ref: 99}))
	if own.Ref != 99 {
		t.Fatalf("event's own ref replaced: %d", own.Ref)
	}
	plain, _ := get(t, l, l.Write(agentRequest("a1", "One", "t"), Event{Action: "x"}))
	if plain.Ref != 0 {
		t.Fatalf("ref without WithRef: %d", plain.Ref)
	}
}

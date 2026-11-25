// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
)

// get fetches one entry through the API.
func get(t *testing.T, l *Log, id int64) (Entry, []Entry) {
	t.Helper()
	mux := http.NewServeMux()
	l.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/api/audit/%d", id), nil))
	if w.Code != 200 {
		t.Fatalf("GET entry %d: %d %s", id, w.Code, w.Body)
	}
	var out struct {
		Entry   Entry   `json:"entry"`
		Related []Entry `json:"related"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Entry, out.Related
}

// request builds a request as the mux would hand it to a handler.
func request(method, pattern, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Pattern = pattern
	r.Header.Set("User-Agent", "test-browser")
	ctx := httpx.WithRequestID(r.Context(), "req123")
	ctx = WithSession(ctx, "sess0001")
	return r.WithContext(ctx)
}

func TestWriteRecordsRequestContext(t *testing.T) {
	l, _ := newLog(t)
	l.Version = "9.9.9"
	r := request("POST", "POST /api/pkg/upgrade", "/api/pkg/upgrade?token=secret")
	r = r.WithContext(WithElevation(r.Context(), "passkey"))
	id := l.Write(r, Event{Action: "pkg.upgrade", Target: "Install 3 updates",
		Err: httpx.Errorf(409, "locked", "Package manager is busy"), Duration: 1500 * time.Millisecond})
	e, _ := get(t, l, id)
	if e.Source != SourceUser || e.Request != "req123" || e.Session != "sess0001" || e.Elevation != "passkey" ||
		e.Route != "POST /api/pkg/upgrade" || e.Version != "9.9.9" || e.DurationMs != 1500 || e.UA != "test-browser" {
		t.Fatalf("context not recorded: %+v", e)
	}
	if e.Code != "locked" || e.Result != "error: Package manager is busy" {
		t.Fatalf("code/result: %q %q", e.Code, e.Result)
	}
}

func TestNilLogIsSafe(t *testing.T) {
	var l *Log
	r := request("POST", "POST /api/x", "/api/x")
	span := l.Begin(r, "x", "y")
	if l.Write(r, Event{Action: "x"}) != 0 || l.End(span, Event{}) != 0 || l.FindByDetail("x", "job", "1") != 0 {
		t.Fatal("nil log wrote")
	}
	l.Record(r, "x", "", nil, nil)
	l.Outcome("x", "y", errors.New("e"), nil)
	l.AllowLogs(nil)
}

func TestSystemEntries(t *testing.T) {
	l, _ := newLog(t)
	id := l.Write(nil, Event{Action: "wargo.start", Target: "1.3.0"})
	e, _ := get(t, l, id)
	if e.Source != SourceSystem || e.IP != "local" || e.Result != "ok" {
		t.Fatalf("system entry: %+v", e)
	}
}

func TestOldEntriesGetASource(t *testing.T) {
	l, st := newLog(t)
	now := time.Now().Unix()
	for _, ip := range []string{"local", "10.0.0.5"} {
		if _, err := st.DB.Exec(`INSERT INTO audit_log(ts, ip, action, result) VALUES(?, ?, 'x', 'ok')`, now, ip); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := get(t, l, 1)
	b, _ := get(t, l, 2)
	if a.Source != SourceSystem || b.Source != SourceUser {
		t.Fatalf("sources: %q %q", a.Source, b.Source)
	}
}

func TestCodes(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{WithCode(errors.New("x"), "pkg.conflict"), "pkg.conflict"},
		{fmt.Errorf("wrapped: %w", httpx.Errorf(403, "elevation_failed", "no")), "elevation_failed"},
		{context.Canceled, "canceled"},
		{errors.New("plain"), ""},
	} {
		if got := codeOf(c.err); got != c.want {
			t.Errorf("codeOf(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	if WithCode(nil, "x") != nil {
		t.Error("WithCode(nil) should stay nil")
	}
}

func TestCanceledResult(t *testing.T) {
	l, _ := newLog(t)
	id := l.Write(nil, Event{Action: "files.copy", Err: fmt.Errorf("copy: %w", context.Canceled)})
	e, _ := get(t, l, id)
	if e.Result != resultCanceled || e.Code != "canceled" {
		t.Fatalf("canceled: %q %q", e.Result, e.Code)
	}
}

func TestRedaction(t *testing.T) {
	for in, want := range map[string]string{
		"https://bob:hunter2@example.com/x":                "https://[redacted]@example.com/x",
		"GET /api?apikey=abc123&q=ubuntu":                  "GET /api?apikey=[redacted]&q=ubuntu",
		"http://plex:32400/?X-Plex-Token=tok&x=1":          "http://plex:32400/?X-Plex-Token=[redacted]&x=1",
		"Authorization: Basic Ym9iOmh1bnRlcjI=":            "Authorization: [redacted]",
		"sent bearer eyJhbGciOi.eyJzdWIi.sig to it":        "sent bearer [redacted] to it",
		`body {"password": "hunter2", "user": "bob"}`:      `body {"password": "[redacted]", "user": "bob"}`,
		"X-Api-Key: 0123456789abcdef":                      "X-Api-Key: [redacted]",
		"error: failed to prepare transaction":             "error: failed to prepare transaction",
		"magnet:?xt=urn:btih:abc&dn=Ubuntu+24.04":          "magnet:?xt=urn:btih:abc&dn=Ubuntu+24.04",
		"plasma-workspace and plasma-x11-session conflict": "plasma-workspace and plasma-x11-session conflict",
		"helper --password=hunter2 --user bob":             "helper --password=[redacted] --user bob",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRedactionEverywhere(t *testing.T) {
	l, st := newLog(t)
	id := l.Write(nil, Event{
		Action: "integrations.prowlarr.update", Target: "http://u:p@prowlarr:9696/?apikey=k1",
		Err: errors.New("GET http://prowlarr/api?apikey=k2: refused"),
		Details: map[string]any{"apiKey": "k3", "apiKeyChanged": true, "url": "http://x/?token=k4",
			"nested": []any{map[string]any{"password": "k5"}}, "accessToken": "k7", "clientSecret": "k8", "inode": uint64(1) << 60},
		Output: &Output{Lines: []string{"curl -H 'Authorization: Bearer k6' ..."}},
	})
	var row string
	if err := st.DB.QueryRow(`SELECT target || result || details || output FROM audit_log WHERE id = ?`, id).Scan(&row); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "u:p@"} {
		if strings.Contains(row, secret) {
			t.Errorf("%q stored: %s", secret, row)
		}
	}
	if !strings.Contains(row, `"apiKeyChanged":true`) {
		t.Errorf("flag was masked: %s", row)
	}
	if !strings.Contains(row, `"inode":1152921504606846976`) {
		t.Errorf("large number changed: %s", row)
	}
}

func TestOutputLimits(t *testing.T) {
	l, _ := newLog(t)
	var lines []string
	for i := range 200 {
		lines = append(lines, fmt.Sprintf("line %03d %s", i, strings.Repeat("x", 50)))
	}
	lines = append(lines, strings.Repeat("y", 5000))
	id := l.Write(nil, Event{Action: "x", Err: errors.New("failed"), Output: &Output{Lines: lines, Log: "/var/log/wargo/1.log"}})
	e, _ := get(t, l, id)
	o := e.Output
	if o == nil || !o.Truncated || len(o.Lines) > maxOutputLines || o.Log != "/var/log/wargo/1.log" {
		t.Fatalf("output: %+v", o)
	}
	total := 0
	for _, l := range o.Lines {
		total += len(l)
		if len(l) > maxOutputLine+4 {
			t.Fatalf("line not clipped: %d", len(l))
		}
	}
	if total > maxOutputBytes {
		t.Fatalf("output bytes %d", total)
	}
	if last := o.Lines[len(o.Lines)-1]; !strings.HasPrefix(last, "yyy") {
		t.Fatalf("the last lines should be kept, got %q", last)
	}
}

func TestOutputBudget(t *testing.T) {
	l, st := newLog(t)
	old := maxOutputTotal
	maxOutputTotal = 20 << 10
	defer func() { maxOutputTotal = old }()
	big := []string{strings.Repeat("z", 1000), strings.Repeat("z", 1000), strings.Repeat("z", 1000)}
	for range 20 {
		l.Write(nil, Event{Action: "x", Err: errors.New("e"), Output: &Output{Lines: big}})
	}
	l.Prune()
	var total, kept, rows int
	st.DB.QueryRow(`SELECT COALESCE(SUM(LENGTH(output)),0), COUNT(output), COUNT(*) FROM audit_log`).Scan(&total, &kept, &rows)
	if total > maxOutputTotal || rows != 20 || kept == 0 {
		t.Fatalf("total %d kept %d rows %d", total, kept, rows)
	}
	var newest bool
	st.DB.QueryRow(`SELECT output IS NOT NULL FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&newest)
	if !newest {
		t.Fatal("the newest output was dropped instead of the oldest")
	}
}

func TestSpanLinksStartAndEnd(t *testing.T) {
	l, _ := newLog(t)
	r := request("POST", "POST /api/pkg/upgrade", "/api/pkg/upgrade")
	span := l.Begin(r, "pkg.upgrade", "Install 2 updates")
	// The approved command writes the start entry, elevated.
	start := l.Write(r.WithContext(WithElevation(r.Context(), "sudo")), Event{Action: "pkg.upgrade", Target: "Install 2 updates"})

	// The span is saved with the run and read back after a restart.
	b, _ := json.Marshal(span)
	var restored Span
	json.Unmarshal(b, &restored)
	restored.Started = time.Now().Add(-90 * time.Second)

	end := l.End(restored, Event{Target: "wargo-upgrade-1", Err: WithCode(errors.New("pacman exited with status 1"), "pkg.conflict"),
		Output: &Output{Lines: []string{":: a and b are in conflict"}}})
	e, related := get(t, l, end)
	if e.Action != "pkg.upgrade.finished" || e.Ref != start || e.Source != SourceJob || e.Elevation != "sudo" ||
		e.Session != "sess0001" || e.Request != "req123" || e.Code != "pkg.conflict" || e.DurationMs < 89_000 {
		t.Fatalf("end entry: %+v", e)
	}
	if len(related) != 1 || related[0].ID != start {
		t.Fatalf("related of end: %+v", related)
	}
	_, related = get(t, l, start)
	if len(related) != 1 || related[0].ID != end || !related[0].HasOutput {
		t.Fatalf("related of start: %+v", related)
	}
}

func TestSpanWithoutStart(t *testing.T) {
	l, _ := newLog(t)
	id := l.End(Span{Action: "brew.upgrade", Started: time.Now().Add(-time.Minute)}, Event{Target: "x"})
	e, _ := get(t, l, id)
	if e.Action != "brew.upgrade.finished" || e.Ref != 0 || e.IP != "local" || e.DurationMs < 59_000 {
		t.Fatalf("entry: %+v", e)
	}
}

func TestFindByDetail(t *testing.T) {
	l, _ := newLog(t)
	l.Write(nil, Event{Action: "containers.update.self.start", Details: map[string]any{"job": "aaa"}})
	want := l.Write(nil, Event{Action: "containers.update.self.start", Details: map[string]any{"job": "bbb"}})
	if got := l.FindByDetail("containers.update.self.start", "job", "bbb"); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
	if l.FindByDetail("containers.update.self.start", "job') OR 1=1 --", "bbb") != 0 {
		t.Fatal("key must be validated")
	}
}

func TestOutcomeOnlyRecordsChanges(t *testing.T) {
	l, st := newLog(t)
	count := func() (n int) {
		st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'pkg.check'`).Scan(&n)
		return
	}
	l.Outcome("pkg.check", "background", nil, nil) // fine from the start: nothing
	l.Outcome("pkg.check", "background", errors.New("mirror down"), nil)
	l.Outcome("pkg.check", "background", errors.New("mirror down"), nil)
	if n := count(); n != 1 {
		t.Fatalf("repeated failure: %d entries", n)
	}
	l.Outcome("pkg.check", "background", errors.New("no space"), nil)
	l.Outcome("pkg.check", "background", nil, nil)
	l.Outcome("pkg.check", "background", nil, nil)
	if n := count(); n != 3 {
		t.Fatalf("entries: %d", n)
	}
	var result, details string
	st.DB.QueryRow(`SELECT result, details FROM audit_log WHERE action = 'pkg.check' ORDER BY id DESC LIMIT 1`).Scan(&result, &details)
	if result != "ok" || !strings.Contains(details, `"recovered":true`) {
		t.Fatalf("recovery: %q %s", result, details)
	}
}

func TestLogEndpoint(t *testing.T) {
	l, _ := newLog(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pkg-1.log")
	other := filepath.Join(t.TempDir(), "secret.txt")
	for p, b := range map[string]string{logPath: "one\ntwo\nGET http://x/?apikey=abc\n", other: "nope"} {
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l.AllowLogs(func(p string) (*os.File, error) {
		if filepath.Dir(p) != dir {
			return nil, ErrNotMine
		}
		return os.Open(p)
	})
	mux := http.NewServeMux()
	l.Register(mux)
	fetch := func(id int64) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/api/audit/%d/log", id), nil))
		return w
	}
	ok := l.Write(nil, Event{Action: "pkg.upgrade.finished", Output: &Output{Log: logPath}})
	w := fetch(ok)
	var body struct {
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	if w.Code != 200 || len(body.Lines) != 3 || body.Lines[1] != "two" || strings.Contains(body.Lines[2], "abc") {
		t.Fatalf("log: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{other, filepath.Join(dir, "..", filepath.Base(filepath.Dir(other)), "secret.txt"), filepath.Join(dir, "gone.log")} {
		id := l.Write(nil, Event{Action: "x", Output: &Output{Log: p}})
		if w := fetch(id); w.Code != 404 {
			t.Errorf("%s: %d %s", p, w.Code, w.Body)
		}
	}
	if w := fetch(l.Write(nil, Event{Action: "x"})); w.Code != 404 {
		t.Errorf("entry without log: %d", w.Code)
	}
}

func TestListLeavesOutputOut(t *testing.T) {
	l, _ := newLog(t)
	l.Write(nil, Event{Action: "x", Err: errors.New("e"), Output: &Output{Lines: []string{"detail"}}})
	mux := http.NewServeMux()
	l.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/audit?source=system", nil))
	if strings.Contains(w.Body.String(), "detail") || !strings.Contains(w.Body.String(), `"hasOutput":true`) {
		t.Fatalf("list: %s", w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/audit?source=user", nil))
	if w.Body.String() != "[]\n" && w.Body.String() != "[]" {
		t.Fatalf("source filter: %s", w.Body)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/audit?source=nope", nil))
	if w.Code != 400 {
		t.Fatalf("bad source: %d", w.Code)
	}
}

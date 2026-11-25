// SPDX-License-Identifier: AGPL-3.0-only

// Package audit records security-relevant actions, and enough context
// about each to diagnose it later: who or what did it, the request and
// session it came from, how long it took, a stable error code and the
// output of a command that failed.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Sources of an entry.
const (
	SourceUser   = "user"   // a request from a signed-in (or signing-in) browser
	SourceSystem = "system" // Wargo itself: background checks, start and stop
	SourceJob    = "job"    // the end of a long-running job started earlier
)

// Results other than "ok" and "error: …".
const resultCanceled = "canceled"

type Entry struct {
	ID         int64           `json:"id"`
	TS         time.Time       `json:"ts"`
	IP         string          `json:"ip"`
	UA         string          `json:"ua"`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Result     string          `json:"result"`
	Elevation  string          `json:"elevation,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
	Source     string          `json:"source"`
	Code       string          `json:"code,omitempty"`
	Ref        int64           `json:"ref,omitempty"`
	Session    string          `json:"session,omitempty"`
	Request    string          `json:"request,omitempty"`
	Route      string          `json:"route,omitempty"`
	Version    string          `json:"version,omitempty"`
	DurationMs int64           `json:"durationMs,omitempty"`
	HasOutput  bool            `json:"hasOutput,omitempty"`
	Output     *Output         `json:"output,omitempty"`
}

// Output is what a failed command or crash printed: the lines that
// explain it, and the full log file while it is kept.
type Output struct {
	Lines     []string `json:"lines,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Log       string   `json:"log,omitempty"`
}

// Event is one entry to write.
type Event struct {
	Action, Target string
	// Err == nil records "ok"; context.Canceled records "canceled".
	Err error
	// Result overrides the result derived from Err.
	Result string
	// Code is a stable error code; by default the code of an *httpx.Error
	// or a Coded error in Err's chain.
	Code     string
	Ref      int64
	Duration time.Duration
	Details  any
	Output   *Output
	// Session overrides the request's session (a sign-in creates it).
	Session string
}

type Log struct {
	st    *store.Store
	proxy *httpx.ProxyTrust

	// Version is Wargo's version, stored with every entry.
	Version string

	mu       sync.Mutex
	inserts  int
	outcomes map[string]string
	logs     []LogOpener
}

// Limits. Retention: entries older than MaxAge are deleted, and at most
// MaxRows are kept, so the log can't grow without bound (several writers
// are reachable before sign-in). Output lines are kept for failures (a
// finished job may also point at its log file); when pruning (at start and
// every pruneEvery writes) finds more than maxOutputTotal bytes of output,
// the oldest output is dropped first and the entries stay.
const (
	MaxAge     = 180 * 24 * time.Hour
	MaxRows    = 50_000
	pruneEvery = 500

	maxTarget      = 512
	maxUA          = 512
	maxResult      = 4 << 10
	maxDetails     = 16 << 10
	maxOutputLines = 80
	maxOutputBytes = 8 << 10
	maxOutputLine  = 1 << 10
)

// maxRows and maxOutputTotal are variables so tests can use small caps.
var (
	maxRows        = MaxRows
	maxOutputTotal = 32 << 20
)

func New(st *store.Store, proxy *httpx.ProxyTrust) *Log {
	l := &Log{st: st, proxy: proxy, outcomes: map[string]string{}}
	l.Prune()
	return l
}

// Prune applies the retention policy.
func (l *Log) Prune() {
	cutoff := time.Now().Add(-MaxAge).Unix()
	if _, err := l.st.DB.Exec(`DELETE FROM audit_log WHERE ts < ?`, cutoff); err != nil {
		slog.Warn("audit prune", "err", err)
		return
	}
	if _, err := l.st.DB.Exec(`DELETE FROM audit_log WHERE id <= (SELECT id FROM audit_log ORDER BY id DESC LIMIT 1 OFFSET ?)`, maxRows); err != nil {
		slog.Warn("audit prune", "err", err)
	}
	l.pruneOutput()
}

// pruneOutput keeps the newest output that fits in maxOutputTotal and
// drops the rest.
func (l *Log) pruneOutput() {
	// LENGTH of a BLOB counts bytes (of TEXT, characters).
	_, err := l.st.DB.Exec(`UPDATE audit_log SET output = NULL WHERE id IN (
		SELECT id FROM (SELECT id, SUM(LENGTH(CAST(output AS BLOB))) OVER (ORDER BY id DESC) AS kept
			FROM audit_log WHERE output IS NOT NULL)
		WHERE kept > ?)`, maxOutputTotal)
	if err != nil {
		slog.Warn("audit prune", "err", err)
	}
}

func (l *Log) maybePrune() {
	l.mu.Lock()
	l.inserts++
	due := l.inserts%pruneEvery == 0
	l.mu.Unlock()
	if due {
		l.Prune()
	}
}

// clip shortens s to at most n bytes (on a rune boundary).
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

type (
	elevationKey struct{}
	sessionKey   struct{}
)

// WithElevation tags the request context with how it was elevated.
func WithElevation(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, elevationKey{}, method)
}

// WithSession tags the request context with the session's audit tag.
func WithSession(ctx context.Context, tag string) context.Context {
	return context.WithValue(ctx, sessionKey{}, tag)
}

// SessionTag is the short, non-secret tag an entry shows for a session,
// from its stored ID hash (itself a hash of the cookie).
func SessionTag(idHash string) string {
	if len(idHash) > 8 {
		return idHash[:8]
	}
	return idHash
}

// coded is an error with a stable audit code (see WithCode).
type coded struct {
	code string
	err  error
}

func (c *coded) Error() string { return c.err.Error() }
func (c *coded) Unwrap() error { return c.err }

// WithCode attaches an audit code to err (nil stays nil).
func WithCode(err error, code string) error {
	if err == nil {
		return nil
	}
	return &coded{code: code, err: err}
}

// codeOf returns the code given with WithCode or of an *httpx.Error in
// err's chain, "canceled" for a canceled context, or "".
func codeOf(err error) string {
	var c *coded
	if errors.As(err, &c) {
		return c.code
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	if errors.Is(err, context.Canceled) {
		return resultCanceled
	}
	return ""
}

// route is the handler pattern (never the query string, which may hold
// secrets), or the method and path when the request had no pattern.
func route(r *http.Request) string {
	if r.Pattern != "" {
		if strings.HasPrefix(r.Pattern, r.Method+" ") {
			return r.Pattern
		}
		return r.Method + " " + r.Pattern
	}
	return r.Method + " " + r.URL.Path
}

// context of an entry: who wrote it and from where.
type origin struct {
	source, ip, ua, elevation, session, request, route string
}

func (l *Log) originOf(r *http.Request) origin {
	if r == nil {
		return origin{source: SourceSystem, ip: "local"}
	}
	ctx := r.Context()
	o := origin{
		source:  SourceUser,
		ip:      l.proxy.ClientIP(r),
		ua:      r.UserAgent(),
		request: httpx.RequestID(ctx),
		route:   route(r),
	}
	o.elevation, _ = ctx.Value(elevationKey{}).(string)
	o.session, _ = ctx.Value(sessionKey{}).(string)
	return o
}

// Write records ev, with r's context (nil r: a system entry), and returns
// the new entry's ID (0 if it couldn't be written). Like every method of
// Log, it does nothing on a nil *Log, so features need no checks.
func (l *Log) Write(r *http.Request, ev Event) int64 {
	if l == nil {
		return 0
	}
	return l.write(l.originOf(r), ev)
}

func resultOf(ev Event) string {
	switch {
	case ev.Result != "":
		return ev.Result
	case ev.Err == nil:
		return "ok"
	case errors.Is(ev.Err, context.Canceled):
		return resultCanceled
	}
	return "error: " + ev.Err.Error()
}

func (l *Log) write(o origin, ev Event) int64 {
	result := resultOf(ev)
	code := ev.Code
	if code == "" && ev.Err != nil {
		code = codeOf(ev.Err)
	}
	if ev.Session != "" {
		o.session = ev.Session
	}
	var details, output []byte
	if ev.Details != nil {
		if d, err := json.Marshal(ev.Details); err == nil && string(d) != "null" {
			details = redactJSON(d)
			if len(details) > maxDetails {
				details, _ = json.Marshal(map[string]any{"truncated": clip(string(details), maxDetails)})
			}
		}
	}
	if out := clipOutput(ev.Output); out != nil {
		output, _ = json.Marshal(out)
	}
	target := clip(Redact(ev.Target), maxTarget)
	result = clip(Redact(result), maxResult)
	var dur sql.NullInt64
	if ev.Duration > 0 {
		dur = sql.NullInt64{Int64: ev.Duration.Milliseconds(), Valid: true}
	}
	res, err := l.st.DB.Exec(
		`INSERT INTO audit_log(ts, ip, ua, action, target, result, elevation, details,
			source, code, ref, session, request, route, version, duration_ms, output)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		time.Now().Unix(), o.ip, nullStr(clip(o.ua, maxUA)), ev.Action, target, result, nullStr(o.elevation), nullStr(string(details)),
		o.source, nullStr(code), nullInt(ev.Ref), nullStr(o.session), nullStr(o.request), nullStr(clip(o.route, maxTarget)),
		nullStr(l.Version), dur, nullStr(string(output)))
	var id int64
	if err != nil {
		slog.Error("audit write failed", "action", ev.Action, "err", err)
	} else {
		id, _ = res.LastInsertId()
	}
	attrs := []any{"id", id, "action", ev.Action, "target", target, "result", result, "source", o.source}
	if code != "" {
		attrs = append(attrs, "code", code)
	}
	if o.elevation != "" {
		attrs = append(attrs, "elevation", o.elevation)
	}
	if o.request != "" {
		attrs = append(attrs, "request", o.request)
	}
	slog.Info("audit", attrs...)
	l.maybePrune()
	return id
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
func nullInt(n int64) sql.NullInt64   { return sql.NullInt64{Int64: n, Valid: n != 0} }

// clipOutput applies the output limits: at most maxOutputLines lines of
// maxOutputLine bytes and maxOutputBytes in total, keeping the last lines.
func clipOutput(o *Output) *Output {
	if o == nil || (len(o.Lines) == 0 && o.Log == "") {
		return nil
	}
	c := &Output{Truncated: o.Truncated, Log: o.Log}
	lines := redactLines(o.Lines)
	if len(lines) > maxOutputLines {
		lines, c.Truncated = lines[len(lines)-maxOutputLines:], true
	}
	total := 0
	start := len(lines)
	for start > 0 {
		l := clip(lines[start-1], maxOutputLine)
		if total+len(l) > maxOutputBytes {
			c.Truncated = true
			break
		}
		total += len(l)
		lines[start-1] = l
		start--
	}
	c.Lines = lines[start:]
	return c
}

// OutputFrom keeps the last lines of text (nil when there are none).
func OutputFrom(text, log string) *Output {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 && log == "" {
		return nil
	}
	return &Output{Lines: lines, Log: log}
}

// Record writes an entry for r. err == nil records "ok".
func (l *Log) Record(r *http.Request, action, target string, err error, details any) {
	l.Write(r, Event{Action: action, Target: target, Err: err, Details: details})
}

// Outcome records the result of something Wargo does again and again on
// its own (background checks, push delivery) without flooding the log: a
// failure is written when it starts or its message changes, and the first
// success after a failure is written once. The last result is kept in
// memory only, so after a restart the first failure is written again and a
// recovery from a failure before the restart isn't.
func (l *Log) Outcome(action, target string, err error, details any) {
	if l == nil {
		return
	}
	key := action + "\x00" + target
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	l.mu.Lock()
	prev, seen := l.outcomes[key]
	l.outcomes[key] = msg
	l.mu.Unlock()
	switch {
	case err != nil && (!seen || prev != msg):
		l.Write(nil, Event{Action: action, Target: target, Err: err, Details: details})
	case err == nil && seen && prev != "":
		d := map[string]any{"recovered": true}
		if m, ok := details.(map[string]any); ok {
			for k, v := range m {
				d[k] = v
			}
		}
		l.Write(nil, Event{Action: action, Target: target, Details: d})
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

// Package audit records security-relevant actions.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

type Entry struct {
	ID        int64           `json:"id"`
	TS        time.Time       `json:"ts"`
	IP        string          `json:"ip"`
	UA        string          `json:"ua"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Result    string          `json:"result"`
	Elevation string          `json:"elevation,omitempty"`
	Details   json.RawMessage `json:"details,omitempty"`
}

type Log struct {
	st    *store.Store
	proxy *httpx.ProxyTrust

	mu      sync.Mutex
	inserts int
}

// Retention: entries older than MaxAge are deleted, and at most MaxRows
// are kept, so the log can't grow without bound (several writers are
// reachable before sign-in).
const (
	MaxAge     = 180 * 24 * time.Hour
	MaxRows    = 50_000
	pruneEvery = 500

	maxTarget  = 512
	maxUA      = 512
	maxResult  = 2048
	maxDetails = 16 << 10
)

// maxRows is MaxRows (a variable so tests can use a small cap).
var maxRows = MaxRows

func New(st *store.Store, proxy *httpx.ProxyTrust) *Log {
	l := &Log{st: st, proxy: proxy}
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

type ctxKey struct{}

// WithElevation tags the request context with how it was elevated.
func WithElevation(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, ctxKey{}, method)
}

// Record writes an entry. err == nil records "ok".
func (l *Log) Record(r *http.Request, action, target string, err error, details any) {
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	elev, _ := r.Context().Value(ctxKey{}).(string)
	var d []byte
	if details != nil {
		d, _ = json.Marshal(details)
		if len(d) > maxDetails {
			d, _ = json.Marshal(map[string]any{"truncated": clip(string(d), maxDetails)})
		}
	}
	target, result = clip(target, maxTarget), clip(result, maxResult)
	_, e := l.st.DB.Exec(
		`INSERT INTO audit_log(ts, ip, ua, action, target, result, elevation, details) VALUES(?,?,?,?,?,?,?,?)`,
		time.Now().Unix(), l.proxy.ClientIP(r), clip(r.UserAgent(), maxUA), action, target, result, elev, string(d))
	if e != nil {
		slog.Error("audit write failed", "err", e)
	}
	slog.Info("audit", "action", action, "target", target, "result", result, "elevation", elev)
	l.maybePrune()
}

// RecordSystem writes an entry not tied to an HTTP request.
func (l *Log) RecordSystem(action, target, result string) {
	_, _ = l.st.DB.Exec(`INSERT INTO audit_log(ts, ip, action, target, result) VALUES(?,?,?,?,?)`,
		time.Now().Unix(), "local", action, clip(target, maxTarget), clip(result, maxResult))
	l.maybePrune()
}

func (l *Log) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/audit", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		limit := min(max(httpx.QueryInt(r, "limit", 100), 1), 500)
		before := httpx.QueryInt(r, "before", 0)
		q := `SELECT id, ts, COALESCE(ip,''), COALESCE(ua,''), action, COALESCE(target,''), result,
			COALESCE(elevation,''), COALESCE(details,'') FROM audit_log`
		args := []any{}
		var where []string
		if before > 0 {
			where = append(where, `id < ?`)
			args = append(args, before)
		}
		// prefix narrows to one area, e.g. "files." for Home's feed.
		if p := r.URL.Query().Get("prefix"); p != "" {
			if len(p) > 40 || !validPrefix(p) {
				return httpx.BadRequest("invalid prefix")
			}
			// A range is exact and case-sensitive, unlike LIKE.
			where = append(where, `action >= ? AND action < ?`)
			args = append(args, p, p+"\xff")
		}
		if len(where) > 0 {
			q += ` WHERE ` + strings.Join(where, " AND ")
		}
		q += ` ORDER BY id DESC LIMIT ?`
		args = append(args, limit)
		rows, err := l.st.DB.QueryContext(r.Context(), q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		out := []Entry{}
		for rows.Next() {
			var e Entry
			var ts int64
			var d string
			if err := rows.Scan(&e.ID, &ts, &e.IP, &e.UA, &e.Action, &e.Target, &e.Result, &e.Elevation, &d); err != nil {
				return err
			}
			e.TS = time.Unix(ts, 0)
			if d != "" {
				e.Details = json.RawMessage(d)
			}
			out = append(out, e)
		}
		httpx.OK(w, out)
		return rows.Err()
	}))
}

// validPrefix allows the characters action names use (e.g. "files.").
func validPrefix(p string) bool {
	for _, c := range p {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

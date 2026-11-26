// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
)

// LogOpener opens a log file an entry points at (Output.Log). It returns
// ErrNotMine for paths it doesn't own, so each feature only serves its own
// log directory.
type LogOpener func(path string) (*os.File, error)

// ErrNotMine is returned by a LogOpener for a path it does not own.
var ErrNotMine = errors.New("not this feature's log")

// AllowLogs lets entries' log files be read through open.
func (l *Log) AllowLogs(open LogOpener) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.logs = append(l.logs, open)
	l.mu.Unlock()
}

// openLog finds the opener that owns path.
func (l *Log) openLog(path string) (*os.File, error) {
	l.mu.Lock()
	openers := append([]LogOpener(nil), l.logs...)
	l.mu.Unlock()
	for _, open := range openers {
		f, err := open(path)
		if errors.Is(err, ErrNotMine) {
			continue
		}
		return f, err
	}
	return nil, ErrNotMine
}

// maxLogTail is how much of a log file GET /api/audit/{id}/log returns.
const maxLogTail = 512 << 10

const entryColumns = `id, ts, COALESCE(ip,''), COALESCE(ua,''), action, COALESCE(target,''), result,
	COALESCE(elevation,''), COALESCE(details,''),
	COALESCE(source, CASE WHEN ip = 'local' THEN 'system' ELSE 'user' END),
	COALESCE(code,''), COALESCE(ref,0), COALESCE(session,''), COALESCE(request,''), COALESCE(route,''),
	COALESCE(version,''), COALESCE(duration_ms,0),
	COALESCE(agent_id,''), COALESCE(agent_name,''), COALESCE(tool,'')`

type scanner interface{ Scan(dest ...any) error }

// scanEntry reads entryColumns, plus the output column when withOutput is
// set (otherwise whether there is any).
func scanEntry(s scanner, withOutput bool) (Entry, error) {
	var e Entry
	var ts int64
	var d, agentID, agentName, tool string
	var out sql.NullString
	var has bool
	dest := []any{&e.ID, &ts, &e.IP, &e.UA, &e.Action, &e.Target, &e.Result, &e.Elevation, &d,
		&e.Source, &e.Code, &e.Ref, &e.Session, &e.Request, &e.Route, &e.Version, &e.DurationMs,
		&agentID, &agentName, &tool}
	if withOutput {
		dest = append(dest, &out)
	} else {
		dest = append(dest, &has)
	}
	if err := s.Scan(dest...); err != nil {
		return e, err
	}
	e.TS = time.Unix(ts, 0)
	if agentID != "" {
		e.Agent = &AgentRef{ID: agentID, Name: agentName, Tool: tool}
	}
	if d != "" {
		e.Details = json.RawMessage(d)
	}
	if out.Valid && out.String != "" {
		var o Output
		if json.Unmarshal([]byte(out.String), &o) == nil {
			e.Output = &o
		}
	}
	e.HasOutput = has || e.Output != nil
	return e, nil
}

func (l *Log) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/audit", httpx.Handler(l.handleList))
	mux.HandleFunc("GET /api/audit/{id}", httpx.Handler(l.handleGet))
	mux.HandleFunc("GET /api/audit/{id}/log", httpx.Handler(l.handleLog))
}

func (l *Log) handleList(w http.ResponseWriter, r *http.Request) error {
	limit := min(max(httpx.QueryInt(r, "limit", 100), 1), 500)
	before := httpx.QueryInt(r, "before", 0)
	q := `SELECT ` + entryColumns + `, output IS NOT NULL FROM audit_log`
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
	// action picks exact actions (repeatable), e.g. the finished updates
	// Home's Activity card lists.
	if acts := r.URL.Query()["action"]; len(acts) > 0 {
		if len(acts) > 10 {
			return httpx.BadRequest("too many actions")
		}
		for _, a := range acts {
			if len(a) > 40 || !validPrefix(a) {
				return httpx.BadRequest("invalid action")
			}
			args = append(args, a)
		}
		where = append(where, `action IN (?`+strings.Repeat(",?", len(acts)-1)+`)`)
	}
	switch src := r.URL.Query().Get("source"); src {
	case "":
	case SourceUser:
		where = append(where, `COALESCE(source, CASE WHEN ip = 'local' THEN 'system' ELSE 'user' END) = 'user'`)
	case SourceSystem, SourceJob, SourceAgent:
		where = append(where, `source = ?`)
		args = append(args, src)
	default:
		return httpx.BadRequest("invalid source")
	}
	if id := r.URL.Query().Get("agent"); id != "" {
		where = append(where, `agent_id = ?`)
		args = append(args, id)
	}
	if ref := httpx.QueryInt(r, "ref", 0); ref > 0 {
		where = append(where, `ref = ?`)
		args = append(args, ref)
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
		e, err := scanEntry(rows, false)
		if err != nil {
			return err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.OK(w, out)
	return nil
}

func (l *Log) entry(r *http.Request, id int64) (Entry, error) {
	e, err := scanEntry(l.st.DB.QueryRowContext(r.Context(),
		`SELECT `+entryColumns+`, output FROM audit_log WHERE id = ?`, id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return e, httpx.ErrNotFound
	}
	return e, err
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, httpx.BadRequest("invalid id")
	}
	return id, nil
}

// handleGet returns the whole entry, its output included, and the entries
// it belongs with: the one it points at and the ones that point at it.
func (l *Log) handleGet(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	e, err := l.entry(r, id)
	if err != nil {
		return err
	}
	related := []Entry{}
	rows, err := l.st.DB.QueryContext(r.Context(), `SELECT `+entryColumns+`, output IS NOT NULL FROM audit_log
		WHERE id = ? OR ref = ? ORDER BY id LIMIT 20`, e.Ref, e.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		x, err := scanEntry(rows, false)
		if err != nil {
			return err
		}
		related = append(related, x)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"entry": e, "related": related})
	return nil
}

// handleLog returns the end of the log file an entry points at, while the
// feature that wrote it still keeps it.
func (l *Log) handleLog(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	e, err := l.entry(r, id)
	if err != nil {
		return err
	}
	gone := httpx.Errorf(http.StatusNotFound, "log_gone", "This log is no longer kept")
	if e.Output == nil || e.Output.Log == "" {
		return httpx.Errorf(http.StatusNotFound, "no_log", "This entry has no log file")
	}
	f, err := l.openLog(e.Output.Log)
	if err != nil {
		return gone
	}
	defer f.Close()
	lines, truncated, err := TailFile(f, maxLogTail)
	if err != nil {
		return gone
	}
	httpx.OK(w, map[string]any{"path": e.Output.Log, "lines": redactLines(lines), "truncated": truncated})
	return nil
}

// TailFile reads the last max bytes of f as lines (dropping a partial
// first line when it starts mid-file, and replacing invalid UTF-8), and
// reports whether the start of the file was left out.
func TailFile(f *os.File, max int64) ([]string, bool, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, errors.New("not a regular file")
	}
	off := int64(0)
	if fi.Size() > max {
		off = fi.Size() - max
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, false, err
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, false, err
	}
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), int(max))
	for sc.Scan() {
		lines = append(lines, strings.ToValidUTF8(sc.Text(), "�"))
	}
	return lines, off > 0, sc.Err()
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

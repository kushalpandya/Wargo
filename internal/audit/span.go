// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"cmp"
	"database/sql"
	"net/http"
	"time"
)

// Span follows a long-running job from the request that started it to its
// end, which may come after Wargo restarted: it is plain data, saved with
// the job's own record. The start is the entry the request wrote for
// Action (for example the approved command); End writes the result with
// a link to it, the time it took, and the request's context.
type Span struct {
	Action    string    `json:"action"`
	Target    string    `json:"target,omitempty"`
	Started   time.Time `json:"started"`
	Request   string    `json:"request,omitempty"`
	Route     string    `json:"route,omitempty"`
	Session   string    `json:"session,omitempty"`
	IP        string    `json:"ip,omitempty"`
	UA        string    `json:"ua,omitempty"`
	Elevation string    `json:"elevation,omitempty"`
	// Ref is the start entry when it is already known.
	Ref int64 `json:"ref,omitempty"`
}

// Begin captures r's context for a job that ends later. It writes nothing:
// the request records its own start entry.
func (l *Log) Begin(r *http.Request, action, target string) Span {
	if l == nil {
		return Span{Action: action, Target: target, Started: time.Now()}
	}
	o := l.originOf(r)
	return Span{
		Action: action, Target: target, Started: time.Now(),
		Request: o.request, Route: o.route, Session: o.session, IP: o.ip, UA: o.ua, Elevation: o.elevation,
	}
}

// End writes the job's result: ev.Action defaults to the span's action
// with ".finished", and ev.Duration to the time since the span began. The
// entry links to the start entry and takes the start's elevation, address
// and session when the span doesn't have them.
func (l *Log) End(s Span, ev Event) int64 {
	if l == nil {
		return 0
	}
	if ev.Action == "" {
		ev.Action = s.Action + ".finished"
	}
	if ev.Target == "" {
		ev.Target = s.Target
	}
	if ev.Duration == 0 && !s.Started.IsZero() {
		ev.Duration = time.Since(s.Started)
	}
	o := origin{
		source: SourceJob, ip: s.IP, ua: s.UA, elevation: s.Elevation,
		session: s.Session, request: s.Request, route: s.Route,
	}
	ref := s.Ref
	if ref == 0 && s.Request != "" {
		var ip, ua, elev, sess sql.NullString
		err := l.st.DB.QueryRow(`SELECT id, ip, ua, elevation, session FROM audit_log
			WHERE request = ? AND action = ? ORDER BY id DESC LIMIT 1`, s.Request, s.Action).Scan(&ref, &ip, &ua, &elev, &sess)
		if err == nil {
			o.ip, o.ua = cmp.Or(o.ip, ip.String), cmp.Or(o.ua, ua.String)
			o.elevation, o.session = cmp.Or(o.elevation, elev.String), cmp.Or(o.session, sess.String)
		}
	}
	if o.ip == "" {
		o.ip = "local"
	}
	if ev.Ref == 0 {
		ev.Ref = ref
	}
	return l.write(o, ev)
}

// FindByDetail returns the newest entry for action whose details have key
// set to value (0 if none), for a job whose start entry can't be found by
// its request, such as one that a previous Wargo process started.
func (l *Log) FindByDetail(action, key, value string) int64 {
	if l == nil || value == "" || !validPrefix(key) {
		return 0
	}
	var id int64
	l.st.DB.QueryRow(`SELECT id FROM audit_log WHERE action = ? AND json_extract(details, '$.' || ?) = ?
		ORDER BY id DESC LIMIT 1`, action, key, value).Scan(&id)
	return id
}

// SPDX-License-Identifier: AGPL-3.0-only

package sysevents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

// maxJournal caps the entries read per query (newest first), so a host
// flooding its journal can't make a query slow or memory-hungry.
const maxJournal = 20000

// readJournal reads errors (priority 0-3) and optionally warnings (4)
// from the systemd journal. Natively it runs as Wargo's user, which sees
// system messages when in the systemd-journal, adm or wheel group; in
// container mode it runs on the host as root.
func readJournal(ctx context.Context, since time.Time, warnings bool) ([]raw, SourceStatus) {
	if _, err := host.LookPath("journalctl"); err != nil {
		return nil, SourceStatus{Note: "This host has no systemd journal (journalctl not found)."}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prio := "0..3"
	if warnings {
		prio = "0..4"
	}
	cmd := host.Command(ctx, "journalctl", "--no-pager", "--quiet", "--reverse",
		"--output=json", "--output-fields=MESSAGE,PRIORITY,SYSLOG_IDENTIFIER,_SYSTEMD_UNIT,_TRANSPORT,_COMM",
		"--since=@"+strconv.FormatInt(since.Unix(), 10), "--priority="+prio, "--lines="+strconv.Itoa(maxJournal))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, SourceStatus{Note: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return nil, SourceStatus{Note: "Could not run journalctl: " + err.Error()}
	}
	list := parseJournal(out)
	werr := cmd.Wait()
	st := SourceStatus{Available: true}
	msg := stderr.String()
	switch {
	case strings.Contains(msg, "not seeing messages from other users and the system"),
		strings.Contains(msg, "No journal files were opened due to insufficient permissions"):
		st.Note = fmt.Sprintf("Only messages from %s are visible. Add the account to the systemd-journal group to see system and kernel events.", host.AdminName())
	case werr != nil && len(list) == 0:
		st = SourceStatus{Note: "journalctl failed: " + firstLine(msg, werr)}
	}
	return list, st
}

func firstLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return err.Error()
	}
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func parseJournal(r interface{ Read([]byte) (int, error) }) []raw {
	var list []raw
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e struct {
			Message   json.RawMessage `json:"MESSAGE"`
			Priority  string          `json:"PRIORITY"`
			Ident     string          `json:"SYSLOG_IDENTIFIER"`
			Unit      string          `json:"_SYSTEMD_UNIT"`
			Transport string          `json:"_TRANSPORT"`
			Comm      string          `json:"_COMM"`
			TS        string          `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		us, _ := strconv.ParseInt(e.TS, 10, 64)
		ev := raw{source: SrcSystem, level: LevelError, origin: e.Ident, unit: e.Unit, message: journalMessage(e.Message), ts: time.UnixMicro(us)}
		if e.Priority == "4" {
			ev.level = LevelWarning
		}
		if e.Transport == "kernel" {
			ev.source, ev.origin, ev.unit = SrcKernel, "kernel", ""
		}
		if ev.origin == "" {
			ev.origin = e.Comm
		}
		if ev.origin == "" {
			ev.origin = strings.TrimSuffix(e.Unit, ".service")
		}
		list = append(list, ev)
	}
	return list
}

// journalMessage decodes MESSAGE, which journalctl emits as a string, or
// as an array of bytes when it isn't valid UTF-8.
func journalMessage(m json.RawMessage) string {
	var s string
	if json.Unmarshal(m, &s) == nil {
		return s
	}
	var b []byte
	var ints []int
	if json.Unmarshal(m, &ints) == nil {
		b = make([]byte, 0, len(ints))
		for _, i := range ints {
			b = append(b, byte(i))
		}
		return strings.ToValidUTF8(string(b), "\uFFFD")
	}
	return ""
}

// containerEvents reads crashes, out-of-memory kills and failing health
// checks from the Docker daemon's recent event history.
func (s *Service) containerEvents(ctx context.Context, since, until time.Time, warnings bool) ([]raw, SourceStatus) {
	if s.docker == nil {
		return nil, SourceStatus{Note: "Docker is not configured."}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	evs, err := s.docker.RecentEvents(ctx, since, until, "die", "oom", "health_status")
	if err != nil {
		return nil, SourceStatus{Note: "Docker is not reachable."}
	}
	var list []raw
	for _, e := range evs {
		r := raw{source: SrcContainers, origin: strings.TrimPrefix(e.Name, "/"), unit: e.Image, ts: e.Time}
		switch {
		case e.Action == "oom":
			r.level, r.message = LevelError, "Ran out of memory and was killed"
		case e.Action == "die":
			switch e.Exit {
			case "", "0":
				continue
			case "137", "143": // SIGKILL / SIGTERM: usually a stop or restart
				r.level, r.message = LevelWarning, "Stopped by a signal (exit code "+e.Exit+")"
			default:
				r.level, r.message = LevelError, "Exited with code "+e.Exit
			}
		case strings.HasPrefix(e.Action, "health_status") && strings.Contains(e.Action, "unhealthy"):
			r.level, r.message = LevelWarning, "Health check failing (unhealthy)"
		default:
			continue
		}
		list = append(list, r)
	}
	return list, SourceStatus{Available: true, Note: "Docker keeps only its most recent events in memory."}
}

// signInActions are audit actions for sign-in attempts.
var signInActions = map[string]string{
	"auth.password":      "Failed password sign-in",
	"auth.second_factor": "Failed two-factor code",
	"auth.passkey_login": "Failed passkey sign-in",
}

// auditEvents turns Wargo's audit log into events: failed sign-ins are
// warnings, any other failed action is an error.
func (s *Service) auditEvents(ctx context.Context, since time.Time) ([]raw, SourceStatus) {
	rows, err := s.st.DB.QueryContext(ctx,
		`SELECT ts, COALESCE(ip,''), action, COALESCE(target,''), result FROM audit_log
		 WHERE ts >= ? AND result LIKE 'error%' ORDER BY id DESC LIMIT 5000`, since.Unix())
	if err != nil {
		return nil, SourceStatus{Note: "Could not read Wargo's audit log."}
	}
	defer rows.Close()
	var list []raw
	for rows.Next() {
		var ts int64
		var ip, action, target, result string
		if rows.Scan(&ts, &ip, &action, &target, &result) != nil {
			continue
		}
		reason := strings.TrimSpace(strings.TrimPrefix(result, "error:"))
		r := raw{source: SrcWargo, level: LevelError, origin: action, ts: time.Unix(ts, 0)}
		if title, ok := signInActions[action]; ok {
			r.level, r.origin = LevelWarning, "sign-in"
			r.message = title
			if ip != "" {
				r.message += " from " + ip
			}
			if action == "auth.password" && target != "" {
				r.message += " as " + target
			}
		} else {
			r.message = reason
			if target != "" {
				r.message = target + ": " + reason
			}
		}
		list = append(list, r)
	}
	return list, SourceStatus{Available: true}
}

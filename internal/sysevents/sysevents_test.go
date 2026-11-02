// SPDX-License-Identifier: AGPL-3.0-only

package sysevents

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestParseJournal(t *testing.T) {
	in := strings.Join([]string{
		`{"MESSAGE":"usb 1-2.2: device descriptor read/64, error -32","PRIORITY":"3","_TRANSPORT":"kernel","SYSLOG_IDENTIFIER":"kernel","__REALTIME_TIMESTAMP":"1793643123960221"}`,
		`{"MESSAGE":"Failed to start Backup.","PRIORITY":"3","SYSLOG_IDENTIFIER":"systemd","_SYSTEMD_UNIT":"init.scope","__REALTIME_TIMESTAMP":"1793643123000000"}`,
		`{"MESSAGE":[104,105,255],"PRIORITY":"4","_COMM":"weird","__REALTIME_TIMESTAMP":"1793643122000000"}`,
		`not json`,
	}, "\n")
	got := parseJournal(strings.NewReader(in))
	if len(got) != 3 {
		t.Fatalf("got %d entries", len(got))
	}
	if got[0].source != SrcKernel || got[0].origin != "kernel" || got[0].level != LevelError {
		t.Errorf("kernel entry: %+v", got[0])
	}
	if got[1].source != SrcSystem || got[1].origin != "systemd" || got[1].unit != "init.scope" {
		t.Errorf("system entry: %+v", got[1])
	}
	if got[2].level != LevelWarning || got[2].origin != "weird" || got[2].message != "hi\uFFFD" {
		t.Errorf("binary message: %+v", got[2])
	}
}

func TestGroupCountsAndOrders(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	var all []raw
	for i := range 3204 {
		all = append(all, raw{source: SrcKernel, level: LevelError, origin: "kernel", message: "usb error", ts: t0.Add(time.Duration(i) * time.Second)})
	}
	all = append(all,
		raw{source: SrcSystem, level: LevelWarning, origin: "app", message: "slow", ts: t0.Add(time.Hour * 2)},
		raw{source: SrcSystem, level: LevelWarning, origin: "app", message: "slow", ts: t0},
	)
	ev, errs, warns, trunc := group(all, true)
	if errs != 3204 || warns != 2 || trunc || len(ev) != 2 {
		t.Fatalf("errs=%d warns=%d groups=%d trunc=%v", errs, warns, len(ev), trunc)
	}
	if ev[0].Origin != "app" || ev[0].Count != 2 || ev[0].First != t0.UnixMilli() {
		t.Errorf("newest group first, with first/last: %+v", ev[0])
	}
	if ev[1].Count != 3204 {
		t.Errorf("repeats grouped: %+v", ev[1])
	}
	if ev, _, warns, _ := group(all, false); len(ev) != 1 || warns != 0 {
		t.Errorf("errors only: %d groups, %d warnings", len(ev), warns)
	}
}

func fakeDocker(t *testing.T, events []string) *docker.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.43")
		case strings.HasSuffix(r.URL.Path, "/version"):
			fmt.Fprint(w, `{"ApiVersion":"1.43","Version":"24.0.0"}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			if r.URL.Query().Get("until") == "" {
				t.Error("events must be bounded by until")
			}
			fmt.Fprint(w, strings.Join(events, "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return docker.NewClient(sock)
}

func TestContainerEvents(t *testing.T) {
	ev := func(action, exit string) string {
		return fmt.Sprintf(`{"Action":%q,"timeNano":1800000000000000000,"Actor":{"ID":"abc","Attributes":{"name":"web","image":"nginx","exitCode":%q}}}`, action, exit)
	}
	s := &Service{docker: fakeDocker(t, []string{ev("die", "0"), ev("die", "1"), ev("die", "143"), ev("oom", ""), ev("health_status: unhealthy", ""), ev("health_status: healthy", "")})}
	list, st := s.containerEvents(context.Background(), time.Now().Add(-time.Hour), time.Now(), true)
	if !st.Available {
		t.Fatalf("status %+v", st)
	}
	var got []string
	for _, r := range list {
		got = append(got, r.level+":"+r.message)
	}
	want := "error:Exited with code 1|warning:Stopped by a signal (exit code 143)|error:Ran out of memory and was killed|warning:Health check failing (unhealthy)"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %q", strings.Join(got, "|"))
	}
	if list[0].origin != "web" || list[0].unit != "nginx" {
		t.Errorf("origin/unit: %+v", list[0])
	}
}

func TestAuditEventsAndQuery(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	for _, r := range [][]any{
		{now.Unix(), "10.0.0.5", "auth.password", "kushal", "error: invalid credentials"},
		{now.Unix(), "10.0.0.5", "auth.password", "kushal", "error: invalid credentials"},
		{now.Unix(), "10.0.0.5", "auth.second_factor", "totp", "error: invalid code"},
		{now.Unix(), "local", "containers.update", "web", "error: pull failed"},
		{now.Unix(), "local", "containers.update", "db", "ok"},
		{now.Add(-48 * time.Hour).Unix(), "local", "pkg.upgrade", "", "error: old"},
	} {
		if _, err := st.DB.Exec(`INSERT INTO audit_log(ts, ip, action, target, result) VALUES(?,?,?,?,?)`, r...); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{st: st, now: time.Now, cache: map[string]cached{},
		journal: func(context.Context, time.Time, bool) ([]raw, SourceStatus) {
			return nil, SourceStatus{Note: "no journal"}
		}}
	res := s.Query(context.Background(), "24h", true, false)
	if res.Errors != 1 || res.Warnings != 3 || len(res.Events) != 3 {
		t.Fatalf("errors=%d warnings=%d events=%+v", res.Errors, res.Warnings, res.Events)
	}
	msgs := map[string]int{}
	for _, e := range res.Events {
		msgs[e.Level+":"+e.Message] = e.Count
	}
	if msgs["warning:Failed password sign-in from 10.0.0.5 as kushal"] != 2 ||
		msgs["warning:Failed two-factor code from 10.0.0.5"] != 1 ||
		msgs["error:web: pull failed"] != 1 {
		t.Fatalf("messages: %v", msgs)
	}
	if res.Sources[SrcSystem].Available || res.Sources[SrcContainers].Available || !res.Sources[SrcWargo].Available {
		t.Errorf("sources: %+v", res.Sources)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/store"
)

var selfID = strings.Repeat("e", 64)

// startSelfFake runs the fake daemon with Wargo's own container (its
// Docker socket bind-mounted) plus a store and audit log.
func startSelfFake(t *testing.T, f *fakeDocker) *Service {
	t.Helper()
	if f.containers == nil {
		f.containers = map[string]*fakeContainer{}
	}
	self := appContainer(selfID, "wargo", "host")
	f.containers[selfID] = self
	s := startFake(t, f)
	self.inspect["Mounts"] = []any{
		map[string]any{"Type": "bind", "Source": "/", "Destination": "/host", "RW": true},
		map[string]any{"Type": "bind", "Source": "/run/docker.sock", "Destination": s.c.Socket(), "RW": true},
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s.st, s.audit = st, audit.New(st, httpx.NewProxyTrust(nil))
	s.selfID, s.version = selfID, "1.0.0"
	s.selfPoll, s.selfWait = 5*time.Millisecond, 2*time.Second
	return s
}

func waitJob(t *testing.T, s *Service, id string) jobs.Snapshot {
	t.Helper()
	for i := 0; i < 500; i++ {
		if sn := s.jobs.Get(id).Snapshot(); sn.State != jobs.Running {
			return sn
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return jobs.Snapshot{}
}

func postSelfUpdate(t *testing.T, s *Service) string {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/containers/wargo/update", nil))
	var res struct {
		JobID    string `json:"jobId"`
		Strategy string `json:"strategy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || res.Strategy != "self" || res.JobID == "" {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	return res.JobID
}

func TestHostSocketPath(t *testing.T) {
	m := []inspectMount{
		{Type: "bind", Source: "/", Destination: "/host"},
		{Type: "bind", Source: "/run/docker.sock", Destination: "/var/run/docker.sock"},
		{Type: "volume", Source: "/var/lib/docker/volumes/x", Destination: "/data"},
	}
	for sock, want := range map[string]string{
		"/var/run/docker.sock":      "/run/docker.sock",
		"/host/var/run/docker.sock": "/var/run/docker.sock",
		"/data/docker.sock":         "",
		"/other.sock":               "",
	} {
		if got := hostSocketPath(m, sock); got != want {
			t.Errorf("%s: got %q, want %q", sock, got, want)
		}
	}
}

func TestSelfUpdateStartsHelper(t *testing.T) {
	f := &fakeDocker{newImageID: "sha256:new"}
	s := startSelfFake(t, f)
	if _, caps := s.Capabilities(context.Background()); caps.(map[string]any)["selfUpdate"] != true {
		t.Fatalf("caps: %v", caps)
	}
	sn := waitJob(t, s, postSelfUpdate(t, s))
	if sn.State != jobs.Succeeded || sn.Result["handedOver"] != true || sn.Result["self"] != true {
		t.Fatalf("job: %+v\n%s", sn, strings.Join(s.jobs.Get(sn.ID).Lines(), "\n"))
	}
	f.mu.Lock()
	h := f.find(selfHelperName)
	self := f.containers[selfID]
	calls := append([]string(nil), f.calls...)
	f.mu.Unlock()
	if h == nil || !h.running || h.image != "sha256:new" {
		t.Fatalf("helper: %+v", h)
	}
	if self == nil || self.name != "wargo" || !self.running {
		t.Fatalf("self touched: %+v", self)
	}
	for _, c := range calls {
		if strings.Contains(c, selfID) && (strings.HasSuffix(c, "/stop") || strings.HasSuffix(c, "/rename")) {
			t.Fatalf("server stopped itself: %v", calls)
		}
	}
	cfg := h.inspect["Config"].(map[string]any)
	if got, want := cfg["Cmd"], []any{"self-update", "--target", selfID, "--socket", "/var/run/docker.sock"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cmd: %v", got)
	}
	if l := cfg["Labels"].(map[string]any); l[labelSelfUpdate] != "1" || l[labelSelfTarget] != selfID || l[labelSelfJob] != sn.ID {
		t.Fatalf("labels: %v", l)
	}
	hc := cfg["HostConfig"].(map[string]any)
	if !reflect.DeepEqual(hc["Binds"], []any{"/run/docker.sock:/var/run/docker.sock"}) || hc["NetworkMode"] != "none" || hc["Privileged"] != nil {
		t.Fatalf("host config: %v", hc)
	}
	var p selfPending
	if ok, _ := s.st.Get(context.Background(), keySelfPending, &p); !ok || p.HelperID != h.id || p.Target != selfID || p.FromVersion != "1.0.0" || p.ToImage != "sha256:new" || p.JobID != sn.ID {
		t.Fatalf("pending: %+v", p)
	}
	// A second request while pending is refused.
	mux := http.NewServeMux()
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/containers/wargo/update", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("second update: %d %s", w.Code, w.Body.String())
	}
}

func TestSelfUpdateUpToDate(t *testing.T) {
	f := &fakeDocker{newImageID: "sha256:old"}
	s := startSelfFake(t, f)
	sn := waitJob(t, s, postSelfUpdate(t, s))
	if sn.State != jobs.Succeeded || sn.Result["updated"] != false {
		t.Fatalf("job: %+v", sn)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "/containers/create") {
			t.Fatalf("helper created: %v", f.calls)
		}
	}
	if ok, _ := s.st.Get(context.Background(), keySelfPending, &selfPending{}); ok {
		t.Fatal("pending stored")
	}
}

func TestSelfUpdateNoSocketMount(t *testing.T) {
	f := &fakeDocker{newImageID: "sha256:new"}
	s := startSelfFake(t, f)
	f.containers[selfID].inspect["Mounts"] = []any{}
	if _, caps := s.Capabilities(context.Background()); caps.(map[string]any)["selfUpdate"] != false {
		t.Fatalf("caps: %v", caps)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/containers/wargo/update", nil))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "self_container") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func finishWith(t *testing.T, exit int, gone bool) (*Service, *fakeDocker, selfResult) {
	t.Helper()
	helperID := strings.Repeat("9", 64)
	f := &fakeDocker{newImageID: "sha256:new"}
	if !gone {
		f.containers = map[string]*fakeContainer{helperID: {id: helperID, name: selfHelperName, image: "sha256:new", exitCode: exit, inspect: map[string]any{}}}
	}
	s := startSelfFake(t, f)
	s.version = "2.0.0"
	p := selfPending{JobID: "j1", StartedAt: time.Now().UnixMilli(), Target: selfID, Name: "wargo", Ref: "app:latest",
		FromImage: "sha256:old", ToImage: "sha256:new", FromVersion: "1.0.0", ToVersion: "2.0.0-label", HelperID: helperID}
	ctx := context.Background()
	if err := s.st.Set(ctx, keySelfPending, p); err != nil {
		t.Fatal(err)
	}
	s.finishSelfUpdate(ctx)
	var res selfResult
	if ok, _ := s.st.Get(ctx, keySelfResult, &res); !ok {
		t.Fatal("no result")
	}
	if ok, _ := s.st.Get(ctx, keySelfPending, &selfPending{}); ok {
		t.Fatal("pending not cleared")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.containers[helperID] != nil {
		t.Fatal("helper not removed")
	}
	return s, f, res
}

func TestFinishSelfUpdateSuccess(t *testing.T) {
	_, _, res := finishWith(t, 0, false)
	if !res.OK || res.RolledBack || res.ToVersion != "2.0.0" || res.FromVersion != "1.0.0" || res.JobID != "j1" || res.FinishedAt == 0 {
		t.Fatalf("%+v", res)
	}
	if strings.Join(res.Log, "|") != "line one|line two|err line" {
		t.Fatalf("log: %v", res.Log)
	}
}

func TestFinishSelfUpdateRolledBack(t *testing.T) {
	_, _, res := finishWith(t, 1, false)
	if res.OK || !res.RolledBack || res.Error == "" || res.ExitCode == nil || *res.ExitCode != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestFinishSelfUpdateHelperGone(t *testing.T) {
	_, _, res := finishWith(t, 0, true)
	if res.OK || !strings.Contains(res.Error, "disappeared") {
		t.Fatalf("%+v", res)
	}
}

func TestSelfUpdateStatusEndpoint(t *testing.T) {
	s, _, _ := finishWith(t, 0, false)
	mux := http.NewServeMux()
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/containers/self-update", nil))
	var out struct {
		Pending *struct{} `json:"pending"`
		Result  *selfResult
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Pending != nil || out.Result == nil || !out.Result.OK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func recreateWithHealth(t *testing.T, health ...string) (*fakeDocker, *jobs.Job, error) {
	t.Helper()
	oldID := strings.Repeat("1", 64)
	f := &fakeDocker{newImageID: "sha256:new", newHealth: health, containers: map[string]*fakeContainer{oldID: appContainer(oldID, "app", "bridge")}}
	s := startFake(t, f)
	s.healthPoll = 5 * time.Millisecond
	job := s.jobs.Start("t", "t", nil)
	old, err := s.inspectFull(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.recreate(context.Background(), job, old, "", true)
	return f, job, err
}

func TestRecreateWaitsHealthy(t *testing.T) {
	f, job, err := recreateWithHealth(t, "starting", "starting", "healthy")
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(job.Lines(), "\n"))
	}
	if !strings.Contains(strings.Join(job.Lines(), "\n"), "Health: healthy") {
		t.Fatal(job.Lines())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if app := f.find("app"); len(f.containers) != 1 || app == nil || app.image != "sha256:new" {
		t.Fatalf("containers: %+v", f.containers)
	}
}

func TestRecreateUnhealthyRollsBack(t *testing.T) {
	f, job, err := recreateWithHealth(t, "starting", "unhealthy")
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("err = %v\n%s", err, strings.Join(job.Lines(), "\n"))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.containers[strings.Repeat("1", 64)]
	if len(f.containers) != 1 || old == nil || old.name != "app" || !old.running {
		t.Fatalf("not rolled back: %+v", f.containers)
	}
}

func TestSelfUpdateHelper(t *testing.T) {
	defer func(d, st time.Duration) { selfUpdateDelay, selfSettle = d, st }(selfUpdateDelay, selfSettle)
	selfUpdateDelay, selfSettle = 0, 10*time.Millisecond
	f := &fakeDocker{newImageID: "sha256:new", newHealth: []string{"healthy"}}
	s := startSelfFake(t, f)
	var out bytes.Buffer
	if err := SelfUpdate(context.Background(), s.c.Socket(), selfID, &out, 10*time.Second); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	f.mu.Lock()
	w := f.find("wargo")
	n := len(f.containers)
	f.mu.Unlock()
	if w == nil || w.id == selfID || w.image != "sha256:new" || n != 1 {
		t.Fatalf("not updated: %+v\n%s", w, out.String())
	}
	if !strings.Contains(out.String(), "Removed old container") {
		t.Fatal(out.String())
	}

	// Rolled back when the new one exits.
	f2 := &fakeDocker{newImageID: "sha256:new", exitAfter: true}
	s2 := startSelfFake(t, f2)
	out.Reset()
	if err := SelfUpdate(context.Background(), s2.c.Socket(), selfID, &out, 10*time.Second); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
}

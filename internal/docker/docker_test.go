// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/jobs"
)

const (
	gluetunID = "f9a0ad28e7b3a0e578027039c4d6b6ed40d48c7eac6831fad29a3e98f067bb55"
	transID   = "6f870339841d8c6cb4659c933f27f74d4a371d902de739c559819f2c0258568a"
)

const listJSON = `[
 {"Id":"` + transID + `","Names":["/transmission"],"Image":"linuxserver/transmission:latest","ImageID":"sha256:45e6",
  "State":"running","Status":"Up 18 hours","Created":1700000000,"Ports":[],
  "Labels":{"com.docker.compose.project":"transmissionvpn","com.docker.compose.service":"transmission",
   "com.docker.compose.project.working_dir":"/var/lib/casaos/apps/transmissionvpn","com.docker.compose.project.config_files":"/var/lib/casaos/apps/transmissionvpn/docker-compose.yml"},
  "HostConfig":{"NetworkMode":"container:` + gluetunID + `"}},
 {"Id":"` + gluetunID + `","Names":["/gluetun"],"Image":"qmcgaw/gluetun:latest","ImageID":"sha256:f7a9",
  "State":"running","Status":"Up 18 hours (healthy)","Created":1700000001,
  "Ports":[{"IP":"0.0.0.0","PrivatePort":9091,"PublicPort":9091,"Type":"tcp"},{"IP":"::","PrivatePort":9091,"PublicPort":9091,"Type":"tcp"},{"IP":"","PrivatePort":8888,"Type":"tcp"}],
  "Labels":{},"HostConfig":{"NetworkMode":"transmissionvpn_default"}},
 {"Id":"abc123abc123abc123","Names":["/db"],"Image":"mariadb:11","ImageID":"sha256:dae0","State":"exited","Status":"Exited (0) 2 days ago",
  "Created":1700000002,"Ports":[],"Labels":{"io.casaos.app":"x"},"HostConfig":{"NetworkMode":"bridge"}},
 {"Id":"def456def456def456","Names":["/web"],"Image":"nginx","ImageID":"sha256:aaaa","State":"running","Status":"Up 2 seconds (health: starting)",
  "Created":1700000003,"Ports":[],"Labels":{},"HostConfig":{"NetworkMode":"bridge"}}
]`

func TestParseHealth(t *testing.T) {
	tests := map[string]string{
		"Up 18 hours (healthy)":           "healthy",
		"Up 1 minute (unhealthy)":         "unhealthy",
		"Up 3 seconds (health: starting)": "starting",
		"Up 5 weeks":                      "",
		"Exited (1) 2 hours ago":          "",
	}
	for in, want := range tests {
		if got := parseHealth(in); got != want {
			t.Errorf("parseHealth(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConvertList(t *testing.T) {
	var raw []apiContainer
	if err := json.Unmarshal([]byte(listJSON), &raw); err != nil {
		t.Fatal(err)
	}
	l := convertList(raw)
	by := map[string]Container{}
	for _, c := range l {
		by[c.Name] = c
	}
	tr := by["transmission"]
	if tr.NetworkContainer != "gluetun" || tr.NetworkContainerID != "f9a0ad28e7b3" || len(tr.NetworkPorts) != 2 {
		t.Fatalf("network container not resolved: %+v", tr)
	}
	if tr.Compose == nil || tr.Compose.Project != "transmissionvpn" || !tr.CasaOS {
		t.Fatalf("compose/casaos: %+v", tr)
	}
	if tr.ID != transID[:12] || tr.FullID != transID || tr.Created != 1700000000000 {
		t.Fatalf("ids: %+v", tr)
	}
	g := by["gluetun"]
	if g.Health != "healthy" || len(g.Ports) != 2 || g.Ports[0].Public != 9091 || g.Ports[0].IP != "" {
		t.Fatalf("gluetun ports/health: %+v", g)
	}
	if len(g.NetworkUsers) != 1 || g.NetworkUsers[0] != "transmission" {
		t.Fatalf("network users: %+v", g.NetworkUsers)
	}
	if !by["db"].CasaOS || by["db"].Compose != nil {
		t.Fatalf("db: %+v", by["db"])
	}
	if by["web"].Health != "starting" {
		t.Fatalf("web: %+v", by["web"])
	}
	if l[0].Name != "db" {
		t.Fatalf("not sorted: %s", l[0].Name)
	}
}

func TestEnvAndLabels(t *testing.T) {
	env := envNames([]string{"PATH=/bin", "DB_PASSWORD=hunter2", "EMPTY="})
	if strings.Join(env, ",") != "DB_PASSWORD,EMPTY,PATH" {
		t.Fatal(env)
	}
	l := redactLabels(map[string]string{"traefik.http.middlewares.auth.basicauth.users": "a:b", "com.docker.compose.project": "x", "api_token": "t"})
	if l["com.docker.compose.project"] != "x" || l["api_token"] != "[redacted]" || l["traefik.http.middlewares.auth.basicauth.users"] != "[redacted]" {
		t.Fatal(l)
	}
}

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestLineReaderMux(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(frame(1, "2026-11-01T10:00:00.000000000Z hello\n2026-11-01T10:00:01.000000000Z wor"))
	buf.Write(frame(2, "2026-11-01T10:00:02.000000000Z oops\n"))
	buf.Write(frame(1, "ld\n"))
	buf.Write(frame(1, "tail-no-newline"))
	lr := NewLineReader(&buf, false, true)
	var got []LogLine
	for {
		l, err := lr.Next()
		if err != nil {
			break
		}
		got = append(got, l)
	}
	want := []LogLine{
		{TS: "2026-11-01T10:00:00.000000000Z", Stream: "out", Text: "hello"},
		{TS: "2026-11-01T10:00:02.000000000Z", Stream: "err", Text: "oops"},
		{TS: "2026-11-01T10:00:01.000000000Z", Stream: "out", Text: "world"},
		{Stream: "out", Text: "tail-no-newline"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLineReaderTTY(t *testing.T) {
	lr := NewLineReader(strings.NewReader("a\r\nb\nc"), true, false)
	var texts []string
	for {
		l, err := lr.Next()
		if err != nil {
			break
		}
		texts = append(texts, l.Text)
	}
	if strings.Join(texts, "|") != "a|b|c" {
		t.Fatal(texts)
	}
}

func TestComputeStats(t *testing.T) {
	a := &apiStats{}
	a.CPUStats.CPUUsage.TotalUsage = 2_000
	a.CPUStats.SystemUsage = 10_000
	a.MemoryStats.Usage = 1000
	a.MemoryStats.Limit = 4000
	a.MemoryStats.Stats = map[string]uint64{"inactive_file": 200}
	now := time.Now()
	prev := &statsPrev{at: now.Add(-2 * time.Second), cpu: 1_000, system: 5_000}
	st, _ := computeStats(a, prev, now)
	if st.CPU < 19.9 || st.CPU > 20.1 || st.Mem != 800 || st.MemLimit != 4000 {
		t.Fatalf("%+v", st)
	}
}

func TestStripImageDefaults(t *testing.T) {
	cfg := map[string]any{
		"Env":        []any{"PATH=/old", "TZ=UTC"},
		"Labels":     map[string]any{"img": "1", "com.docker.compose.project": "p"},
		"Entrypoint": []any{"/init"},
		"Cmd":        []any{"run"},
		"WorkingDir": "/app",
	}
	img := map[string]any{"Env": []any{"PATH=/old"}, "Labels": map[string]any{"img": "1"}, "Entrypoint": []any{"/init"}, "Cmd": []any{"run"}, "WorkingDir": "/app"}
	stripImageDefaults(cfg, img)
	if len(cfg["Env"].([]any)) != 1 || cfg["Env"].([]any)[0] != "TZ=UTC" {
		t.Fatal(cfg["Env"])
	}
	if _, ok := cfg["Labels"].(map[string]any)["img"]; ok {
		t.Fatal("image label kept")
	}
	for _, k := range []string{"Entrypoint", "Cmd", "WorkingDir"} {
		if _, ok := cfg[k]; ok {
			t.Fatalf("%s kept", k)
		}
	}
	// A user-set entrypoint keeps Cmd even when Cmd equals the image's.
	cfg = map[string]any{"Entrypoint": []any{"/custom"}, "Cmd": []any{"run"}}
	stripImageDefaults(cfg, img)
	if cfg["Cmd"] == nil || cfg["Entrypoint"] == nil {
		t.Fatal(cfg)
	}
}

// --- fake Docker daemon over a unix socket ---

type fakeContainer struct {
	id, name, image string
	running         bool
	exitCode        int
	inspect         map[string]any
	inspects        int // health reports served
}

type fakeDocker struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	calls      []string
	failStart  bool // starting a container with the new image fails
	exitAfter  bool // new containers stop right after starting
	newImageID string
	// newHealth is the health status sequence reported for running
	// containers of the new image (the last one repeats).
	newHealth []string
	seq       int
}

func (f *fakeDocker) find(ref string) *fakeContainer {
	for _, c := range f.containers {
		if c.id == ref || c.name == ref || strings.HasPrefix(c.id, ref) {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) inspectOf(c *fakeContainer) map[string]any {
	m := map[string]any{}
	for k, v := range c.inspect {
		m[k] = v
	}
	m["Id"], m["Name"], m["Image"] = c.id, "/"+c.name, c.image
	status := "exited"
	if c.running {
		status = "running"
	}
	st := map[string]any{"Status": status, "Running": c.running, "ExitCode": c.exitCode}
	if c.running && c.image == f.newImageID && len(f.newHealth) > 0 {
		st["Health"] = map[string]any{"Status": f.newHealth[min(c.inspects, len(f.newHealth)-1)]}
		c.inspects++
	}
	m["State"] = st
	return m
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if path == "/version" {
		json.NewEncoder(w).Encode(map[string]string{"Version": "29.0.0", "ApiVersion": "1.55"})
		return
	}
	path = strings.TrimPrefix(path, "/v1.47")
	f.calls = append(f.calls, r.Method+" "+path)
	notFound := func() { w.WriteHeader(404); w.Write([]byte(`{"message":"No such container"}`)) }
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == "POST" && path == "/images/create":
		enc := json.NewEncoder(w)
		enc.Encode(map[string]any{"status": "Pulling from library/app", "id": "latest"})
		enc.Encode(map[string]any{"status": "Downloading", "id": "l1", "progressDetail": map[string]int{"current": 5, "total": 10}})
		enc.Encode(map[string]any{"status": "Pull complete", "id": "l1"})
		enc.Encode(map[string]any{"status": "Status: Downloaded newer image for app:latest"})
	case r.Method == "GET" && parts[0] == "images":
		json.NewEncoder(w).Encode(map[string]any{"Id": f.newImageID, "Config": map[string]any{"Env": []string{"PATH=/bin"}}})
	case r.Method == "POST" && path == "/containers/create":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		name := r.URL.Query().Get("name")
		if f.find(name) != nil {
			w.WriteHeader(409)
			w.Write([]byte(`{"message":"name in use"}`))
			return
		}
		f.seq++
		id := strings.Repeat(string(rune('a'+f.seq)), 64)
		f.containers[id] = &fakeContainer{id: id, name: name, image: f.newImageID, inspect: map[string]any{"Config": body, "HostConfig": body["HostConfig"]}}
		json.NewEncoder(w).Encode(map[string]any{"Id": id})
	case r.Method == "GET" && path == "/containers/json":
		f.listContainers(w)
	case parts[0] == "containers" && len(parts) >= 2:
		c := f.find(parts[1])
		if c == nil {
			notFound()
			return
		}
		action := ""
		if len(parts) > 2 {
			action = parts[2]
		}
		switch {
		case r.Method == "GET" && action == "json":
			json.NewEncoder(w).Encode(f.inspectOf(c))
		case r.Method == "POST" && action == "rename":
			n := r.URL.Query().Get("name")
			if o := f.find(n); o != nil && o != c {
				w.WriteHeader(409)
				return
			}
			c.name = n
			w.WriteHeader(204)
		case r.Method == "POST" && action == "stop":
			c.running = false
			w.WriteHeader(204)
		case r.Method == "POST" && action == "start":
			if f.failStart && c.image == f.newImageID {
				w.WriteHeader(500)
				w.Write([]byte(`{"message":"port is already allocated"}`))
				return
			}
			c.running = !f.exitAfter || c.image != f.newImageID
			w.WriteHeader(204)
		case r.Method == "DELETE" && action == "":
			delete(f.containers, c.id)
			w.WriteHeader(204)
		case r.Method == "GET" && action == "logs":
			w.Write(frame(1, "line one\nline two\n"))
			w.Write(frame(2, "err line\n"))
		default:
			w.WriteHeader(400)
		}
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeDocker) listContainers(w http.ResponseWriter) {
	{
		var out []map[string]any
		for _, c := range f.containers {
			st := "exited"
			if c.running {
				st = "running"
			}
			hc, _ := c.inspect["HostConfig"].(map[string]any)
			out = append(out, map[string]any{"Id": c.id, "Names": []string{"/" + c.name}, "Image": "app:latest", "ImageID": c.image,
				"State": st, "Status": "Up", "HostConfig": map[string]any{"NetworkMode": hc["NetworkMode"]}})
		}
		json.NewEncoder(w).Encode(out)
	}
}

func startFake(t *testing.T, f *fakeDocker) *Service {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(f)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	hub := events.NewHub()
	s := New("", hub, jobs.NewManager(hub), nil, nil)
	s.c = NewClient(sock)
	s.settle = 10 * time.Millisecond
	s.composeAvailable = func() bool { return false }
	return s
}

func appContainer(id, name, netMode string) *fakeContainer {
	return &fakeContainer{id: id, name: name, image: "sha256:old", running: true, inspect: map[string]any{
		"Config": map[string]any{"Image": "app:latest", "Hostname": id[:12], "Env": []any{"PATH=/bin", "TZ=UTC"},
			"Labels": map[string]any{"com.docker.compose.project": "p"}},
		"HostConfig": map[string]any{"NetworkMode": netMode, "Binds": []any{"/data:/data"}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{
			"p_default": map[string]any{"Aliases": []any{"app", id[:12]}, "IPAMConfig": nil},
		}},
		"Mounts": []any{
			map[string]any{"Type": "bind", "Source": "/data", "Destination": "/data", "RW": true},
			map[string]any{"Type": "volume", "Name": "anonvol", "Destination": "/var/lib/app", "RW": true},
		},
	}}
}

func TestClientNegotiatesVersion(t *testing.T) {
	f := &fakeDocker{containers: map[string]*fakeContainer{}}
	s := startFake(t, f)
	if _, err := s.List(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if s.c.APIVersion() != "1.47" {
		t.Fatalf("api version %q", s.c.APIVersion())
	}
	if !IsNotFound(s.c.call(context.Background(), "GET", "/containers/nope/json", nil, nil)) {
		t.Fatal("expected not found")
	}
}

func TestLogsDemux(t *testing.T) {
	id := strings.Repeat("1", 64)
	f := &fakeDocker{containers: map[string]*fakeContainer{id: appContainer(id, "app", "bridge")}}
	s := startFake(t, f)
	lr, c, err := s.Logs(context.Background(), "app", 10, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var got []string
	for {
		l, err := lr.Next()
		if err != nil {
			break
		}
		got = append(got, l.Stream+":"+l.Text)
	}
	if strings.Join(got, "|") != "out:line one|out:line two|err:err line" {
		t.Fatal(got)
	}
}

func TestRecreateSuccess(t *testing.T) {
	oldID := strings.Repeat("1", 64)
	depID := strings.Repeat("2", 64)
	f := &fakeDocker{newImageID: "sha256:new", containers: map[string]*fakeContainer{
		oldID: appContainer(oldID, "app", "p_default"),
		depID: appContainer(depID, "sidecar", "container:"+oldID),
	}}
	s := startFake(t, f)
	job := s.jobs.Start("t", "t", nil)
	res, err := s.UpdateRecreate(context.Background(), job, "app")
	if err != nil {
		t.Fatalf("update: %v\n%s", err, strings.Join(job.Lines(), "\n"))
	}
	if res["updated"] != true {
		t.Fatal(res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.containers) != 2 {
		t.Fatalf("containers: %d", len(f.containers))
	}
	app := f.find("app")
	if app == nil || app.id == oldID || !app.running || app.image != "sha256:new" {
		t.Fatalf("new app: %+v", app)
	}
	cfg := app.inspect["Config"].(map[string]any)
	if _, ok := cfg["Hostname"]; ok {
		t.Fatal("default hostname should be dropped")
	}
	if env := cfg["Env"].([]any); len(env) != 1 || env[0] != "TZ=UTC" {
		t.Fatalf("env: %v", env)
	}
	hc := cfg["HostConfig"].(map[string]any)
	mounts, _ := hc["Mounts"].([]any)
	if len(mounts) != 1 || mounts[0].(map[string]any)["Source"] != "anonvol" {
		t.Fatalf("anonymous volume not kept: %v", hc["Mounts"])
	}
	eps := cfg["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)
	al := eps["p_default"].(map[string]any)["Aliases"].([]any)
	if len(al) != 1 || al[0] != "app" {
		t.Fatalf("aliases: %v", al)
	}
	side := f.find("sidecar")
	if side == nil || side.id == depID {
		t.Fatalf("dependent not recreated: %+v", side)
	}
	if nm := side.inspect["HostConfig"].(map[string]any)["NetworkMode"]; nm != "container:"+app.id {
		t.Fatalf("dependent network mode %v", nm)
	}
	if _, ok := side.inspect["Config"].(map[string]any)["NetworkingConfig"]; ok {
		t.Fatal("container network mode must not carry endpoints")
	}
}

func TestRecreateUpToDate(t *testing.T) {
	oldID := strings.Repeat("1", 64)
	f := &fakeDocker{newImageID: "sha256:old", containers: map[string]*fakeContainer{oldID: appContainer(oldID, "app", "bridge")}}
	s := startFake(t, f)
	res, err := s.UpdateRecreate(context.Background(), s.jobs.Start("t", "t", nil), "app")
	if err != nil || res["updated"] != false {
		t.Fatal(res, err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "rename") || strings.Contains(c, "create?") || strings.HasSuffix(c, "/stop") {
			t.Fatalf("unexpected mutation %s", c)
		}
	}
}

func testRollback(t *testing.T, f *fakeDocker) {
	oldID := strings.Repeat("1", 64)
	f.containers = map[string]*fakeContainer{oldID: appContainer(oldID, "app", "bridge")}
	f.newImageID = "sha256:new"
	s := startFake(t, f)
	job := s.jobs.Start("t", "t", nil)
	_, err := s.UpdateRecreate(context.Background(), job, "app")
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.containers) != 1 {
		t.Fatalf("new container not removed: %d", len(f.containers))
	}
	old := f.containers[oldID]
	if old == nil || old.name != "app" || !old.running {
		t.Fatalf("old container not restored: %+v\n%s", old, strings.Join(job.Lines(), "\n"))
	}
}

func TestRecreateRollbackOnStartFailure(t *testing.T) {
	testRollback(t, &fakeDocker{failStart: true})
}

func TestRecreateRollbackWhenNewExits(t *testing.T) {
	testRollback(t, &fakeDocker{exitAfter: true})
}

func TestPullProgressLogged(t *testing.T) {
	f := &fakeDocker{containers: map[string]*fakeContainer{}}
	s := startFake(t, f)
	job := s.jobs.Start("t", "t", nil)
	if err := s.pull(context.Background(), job, "app:latest"); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(job.Lines(), "\n")
	if !strings.Contains(log, "l1: Pull complete") || !strings.Contains(log, "Status: Downloaded newer image") {
		t.Fatal(log)
	}
	_ = io.EOF
}

func TestConvertRedactsCommand(t *testing.T) {
	c := convert(apiContainer{ID: gluetunID, Command: "app --db postgres://u:S3cret@db/x"})
	if strings.Contains(c.Command, "S3cret") {
		t.Fatalf("command leaks a password: %q", c.Command)
	}
	if !strings.Contains(c.Command, "app --db postgres://") || !strings.Contains(c.Command, "@db/x") {
		t.Fatalf("command over-redacted: %q", c.Command)
	}
}

// shortTempDir returns a temp dir with a short path: unix socket paths are
// limited to about 104 bytes on macOS, and its TMPDIR is long.
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "wd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestCLIEnvPinsSocket(t *testing.T) {
	got := cliEnv("/s.sock", []string{"PATH=/bin", "DOCKER_HOST=tcp://x", "DOCKER_CONTEXT=y"})
	if !slices.Equal(got, []string{"PATH=/bin", "DOCKER_HOST=unix:///s.sock"}) {
		t.Fatal(got)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/brew"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/integrations/transmission/transmissiontest"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/pkg"
	"github.com/kushalpandya/wargo/internal/procs"
	"github.com/kushalpandya/wargo/internal/server"
)

var (
	operateTools = []string{"container_action", "torrent_add", "torrent_action", "check_updates", "process_signal"}
	adminTools   = []string{"apply_os_updates", "upgrade_homebrew", "update_container"}
)

// actHarness is the approval harness (a signed-in browser, an admin agent)
// with Wargo's action tools, the capabilities each test names, and an
// operate agent.
type actHarness struct {
	*approvalHarness
	operate string
}

func newActHarness(t *testing.T, caps ...string) *actHarness {
	t.Helper()
	h := newApprovalHarness(t, nil)
	serveCaps(h.api, caps...)
	h.s.addActTools()
	addTool(h.s, torrentSearchSpec, h.s.torrentSearch)
	_, op := newAgent(t, h.s, LevelOperate)
	return &actHarness{approvalHarness: h, operate: op}
}

func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// toolOK calls a tool that must succeed and decodes its output into out.
func toolOK(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res := callTool(t, cs, name, args)
	text := res.Content[0].(*sdk.TextContent).Text
	if res.IsError {
		t.Fatalf("%s %v: error result %s", name, args, text)
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("%s: output %s: %v", name, text, err)
	}
}

// toolFails calls a tool that must fail with code.
func toolFails(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any, code string) httpx.Error {
	t.Helper()
	e := toolError(t, callTool(t, cs, name, args))
	if e.Code != code {
		t.Fatalf("%s %v: code %q (%s), want %q", name, args, e.Code, e.Message, code)
	}
	return e
}

// agentEntry is the newest audit entry with action: it must come from
// the agent through tool.
func (h *actHarness) agentEntry(t *testing.T, action, tool string) (target string) {
	t.Helper()
	var source, agentID, gotTool string
	err := h.s.st.DB.QueryRow(`SELECT target, source, COALESCE(agent_id, ''), COALESCE(tool, '') FROM audit_log
		WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(&target, &source, &agentID, &gotTool)
	if err != nil {
		t.Fatalf("no %s entry: %v", action, err)
	}
	if source != "agent" || agentID == "" || gotTool != tool {
		t.Fatalf("%s entry: source %q agent %q tool %q", action, source, agentID, gotTool)
	}
	return target
}

func (h *actHarness) pendingRequests() []Pending {
	var out []Pending
	for _, p := range h.s.approvals.List() {
		if p.State == StatePending {
			out = append(out, p)
		}
	}
	return out
}

// counter counts the requests a fake route receives and keeps their bodies.
type counter struct {
	mu     sync.Mutex
	bodies []string
	elev   []string
}

func (c *counter) seen(r *http.Request) {
	var b []byte
	if r.Body != nil {
		b, _ = io.ReadAll(r.Body)
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, string(b))
	c.elev = append(c.elev, r.Header.Get(elevate.Header))
	c.mu.Unlock()
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func (c *counter) body(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bodies[i]
}

func TestActToolsListed(t *testing.T) {
	h := newActHarness(t, "docker", "transmission", "prowlarr", "pkg", "brew")
	all := append(slices.Clone(operateTools), adminTools...)
	annotations := map[string][2]bool{ // destructive, idempotent
		"container_action": {true, false}, "torrent_add": {false, false}, "torrent_action": {true, true},
		"check_updates": {false, true}, "process_signal": {true, true},
		"apply_os_updates": {true, false}, "upgrade_homebrew": {true, false}, "update_container": {true, false},
	}
	for _, c := range []struct {
		token string
		want  []string
	}{{h.operate, operateTools}, {h.token, all}} {
		cs := h.client(t, c.token, false)
		res, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tl := range res.Tools {
			want, act := annotations[tl.Name]
			if !act {
				continue
			}
			names = append(names, tl.Name)
			a := tl.Annotations
			if a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint != want[0] || a.IdempotentHint != want[1] {
				t.Errorf("%s annotations %+v", tl.Name, a)
			}
			if tl.Description == "" || strings.ContainsAny(tl.Description, "—–") {
				t.Errorf("%s description %q", tl.Name, tl.Description)
			}
		}
		slices.Sort(names)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("sees %v, want %v", names, want)
		}
	}
	// Admin tools are hidden where their feature is unavailable.
	h2 := newActHarness(t)
	names := toolNames(t, h2.client(t, h2.token, false))
	for _, n := range []string{"apply_os_updates", "upgrade_homebrew", "update_container", "container_action", "torrent_action"} {
		if slices.Contains(names, n) {
			t.Errorf("%s listed without its feature", n)
		}
	}
}

// Every route an action tool declares is one Wargo's features register.
func TestActRoutesRegistered(t *testing.T) {
	st := testStore(t)
	hub := events.NewHub()
	al := audit.New(st, httpx.NewProxyTrust(nil))
	d := &server.Deps{Cfg: config.Config{DataDir: t.TempDir()}, Store: st, Hub: hub, Jobs: jobs.NewManager(hub), Audit: al}
	mux := http.NewServeMux()
	pkg.New(d).Register(mux)
	brew.New(d).Register(mux)
	docker.New("/nonexistent.sock", nil, nil, al, nil).Register(mux)
	procs.New(hub, al, nil, nil).Register(mux)
	serveIntegrations(&Service{st: st, audit: al}, mux, config.Config{})
	for _, spec := range []ToolSpec{containerActionSpec, torrentAddSpec, torrentActionSpec, checkUpdatesSpec, processSignalSpec,
		applyOSUpdatesSpec, applyOSUpdatesChecks, upgradeHomebrewSpec, updateContainerSpec, updateContainerChecks} {
		for _, rt := range spec.Routes {
			_, path, _ := strings.Cut(rt.Pattern, " ")
			for _, v := range []string{"{id}", "{action}", "{pid}"} {
				path = strings.ReplaceAll(path, v, "x")
			}
			if _, got := mux.Handler(httptest.NewRequest(rt.Method, path, nil)); got != rt.Pattern {
				t.Errorf("%s: %s reaches %q", spec.Name, rt.Pattern, got)
			}
		}
	}
}

func TestContainerAction(t *testing.T) {
	h := newActHarness(t, "docker")
	f := &fakeDaemon{}
	serveDocker(t, h.s, h.api, f)
	cs := h.client(t, h.operate, false)
	var out struct {
		Container string `json:"container"`
		Action    string `json:"action"`
		OK        bool   `json:"ok"`
	}
	toolOK(t, cs, "container_action", map[string]any{"id": "plex", "action": "restart"}, &out)
	if !out.OK || out.Container != "plex" || out.Action != "restart" {
		t.Fatalf("%+v", out)
	}
	f.mu.Lock()
	restarted := slices.Contains(f.requests, "POST /containers/plex/restart")
	f.mu.Unlock()
	if !restarted {
		t.Fatalf("daemon saw %v", f.requests)
	}
	if target := h.agentEntry(t, "containers.restart", "container_action"); target != "plex" {
		t.Fatalf("entry target %q", target)
	}
	toolFails(t, cs, "container_action", map[string]any{"id": "plex", "action": "update"}, "invalid_argument")
	toolFails(t, cs, "container_action", map[string]any{"id": "plex", "action": "kill"}, "invalid_argument")
	for _, bad := range []string{"../plex", "a/b", "", "-x", "plex?x=1"} {
		toolFails(t, cs, "container_action", map[string]any{"id": bad, "action": "stop"}, "invalid_argument")
	}
	// An unknown container is the handler's answer.
	toolFails(t, cs, "container_action", map[string]any{"id": "nope", "action": "stop"}, "not_found")
}

// fakeTransmission serves a fake Transmission (and, for results, a fake
// Prowlarr whose one result carries an info hash) through the real
// integrations.
func (h *actHarness) withTorrents(t *testing.T) *transmissiontest.Fake {
	t.Helper()
	ft := transmissiontest.New("kushal", "s3cret")
	t.Cleanup(ft.Close)
	fp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/status":
			w.Write([]byte(`{"appName":"Prowlarr","version":"2.6.5.5623"}`))
		case "/api/v1/indexer", "/api/v1/indexerstatus", "/api/v1/downloadclient", "/api/v1/health":
			w.Write([]byte(`[]`))
		case "/api/v1/search":
			w.Write([]byte(`[{"guid":"https://tpb.example/1","title":"Debian 12","indexerId":1,"indexer":"TPB","size":1000,
				"seeders":5,"protocol":"torrent","infoHash":"0123456789abcdef0123456789abcdef01234567"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fp.Close)
	cfg := config.Config{}
	cfg.Integrations.Transmission = config.Transmission{URL: ft.RPCURL(), Username: "kushal", Password: "s3cret"}
	cfg.Integrations.Prowlarr = config.Prowlarr{URL: fp.URL, APIKey: "secretkey0123456789"}
	serveIntegrations(h.s, h.api, cfg)
	return ft
}

func TestTorrentAdd(t *testing.T) {
	h := newActHarness(t, "transmission", "prowlarr")
	ft := h.withTorrents(t)
	cs := h.client(t, h.operate, false)
	type added struct {
		ID        int64  `json:"id"`
		Duplicate bool   `json:"duplicate"`
		Via       string `json:"via"`
	}
	var out added
	magnet := "magnet:?xt=urn:btih:89abcdef0123456789abcdef0123456789abcdef&dn=ubuntu"
	toolOK(t, cs, "torrent_add", map[string]any{"magnet": magnet}, &out)
	if out.ID == 0 {
		t.Fatalf("%+v", out)
	}
	if _, ok := ft.Get(out.ID); !ok {
		t.Fatal("not added to Transmission")
	}
	if target := h.agentEntry(t, "transmission.add", "torrent_add"); target != "89abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("entry target %q", target)
	}

	var search struct {
		Results []struct {
			ResultID string `json:"resultId"`
		} `json:"results"`
	}
	toolOK(t, cs, "torrent_search", map[string]any{"query": "debian"}, &search)
	if len(search.Results) != 1 {
		t.Fatalf("search %+v", search)
	}
	out = added{}
	toolOK(t, cs, "torrent_add", map[string]any{"resultId": search.Results[0].ResultID}, &out)
	if out.Via != "transmission" {
		t.Fatalf("queued %+v", out)
	}
	if target := h.agentEntry(t, "prowlarr.queue", "torrent_add"); target != "Debian 12" {
		t.Fatalf("entry target %q", target)
	}

	for _, args := range []map[string]any{
		{}, {"magnet": magnet, "resultId": search.Results[0].ResultID},
		{"magnet": "http://example.com/x.torrent"}, {"magnet": "magnet:?" + strings.Repeat("a", 8<<10)},
		{"resultId": "../x"}, {"resultId": "a/b"},
	} {
		toolFails(t, cs, "torrent_add", args, "invalid_argument")
	}
	toolFails(t, cs, "torrent_add", map[string]any{"resultId": "AAAAAAAAAAAAAAAAAAAA"}, "result_expired")
}

func TestTorrentAction(t *testing.T) {
	h := newActHarness(t, "transmission")
	ft := h.withTorrents(t)
	tr := ft.AddTorrent("debian-12.6.iso", 4, 0.42)
	cs := h.client(t, h.operate, false)
	id := strconv.FormatInt(tr.ID, 10)
	var out struct {
		OK bool `json:"ok"`
	}
	toolOK(t, cs, "torrent_action", map[string]any{"id": id, "action": "stop"}, &out)
	if got, _ := ft.Get(tr.ID); !out.OK || got.Status != 0 {
		t.Fatalf("stop: %+v status %d", out, got.Status)
	}
	h.agentEntry(t, "transmission.stop", "torrent_action")
	toolOK(t, cs, "torrent_action", map[string]any{"id": id, "action": "start"}, &out)
	if got, _ := ft.Get(tr.ID); got.Status != 4 {
		t.Fatalf("start: status %d", got.Status)
	}
	toolFails(t, cs, "torrent_action", map[string]any{"id": id, "action": "delete"}, "invalid_argument")
	for _, bad := range []string{"0", "-1", "abc", "1/2", "", "0123456789abcdef"} {
		toolFails(t, cs, "torrent_action", map[string]any{"id": bad, "action": "stop"}, "invalid_argument")
	}
}

// Removing keeps the downloaded data unless the agent asks for it to go,
// by hash only, and the audit entry says which.
func TestTorrentRemove(t *testing.T) {
	h := newActHarness(t, "transmission")
	ft := h.withTorrents(t)
	keep := ft.AddTorrent("debian-12.6.iso", 6, 1)
	drop := ft.AddTorrent("ubuntu-24.04.iso", 0, 0.01)
	cs := h.client(t, h.operate, false)
	var out struct {
		OK bool `json:"ok"`
	}
	toolFails(t, cs, "torrent_action", map[string]any{"id": keep.HashString, "action": "stop", "deleteData": true}, "invalid_argument")
	toolFails(t, cs, "torrent_action", map[string]any{"id": strconv.FormatInt(drop.ID, 10), "action": "remove", "deleteData": true}, "invalid_argument")
	if got, _ := ft.Get(keep.ID); got.Removed || got.Status != 6 {
		t.Fatalf("stop with deleteData changed the torrent: %+v", got)
	}
	if got, _ := ft.Get(drop.ID); got.Removed {
		t.Fatalf("deleteData by numeric id removed the torrent: %+v", got)
	}
	toolOK(t, cs, "torrent_action", map[string]any{"id": keep.HashString, "action": "remove"}, &out)
	toolOK(t, cs, "torrent_action", map[string]any{"id": drop.HashString, "action": "remove", "deleteData": true}, &out)
	for _, c := range []struct {
		id      int64
		deleted bool
	}{{keep.ID, false}, {drop.ID, true}} {
		if got, _ := ft.Get(c.id); !got.Removed || got.DataDeleted != c.deleted {
			t.Fatalf("torrent %d: removed %v, data deleted %v", c.id, got.Removed, got.DataDeleted)
		}
	}
	rows, err := h.s.st.DB.Query(`SELECT details FROM audit_log WHERE action = 'transmission.remove' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		details = append(details, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(details) != 2 || !strings.Contains(details[0], `"deleteData":false`) || !strings.Contains(details[1], `"deleteData":true`) {
		t.Fatalf("entry details %q", details)
	}
	h.agentEntry(t, "transmission.remove", "torrent_action")
}

type checkPartSeen struct {
	State    string       `json:"state"`
	Updates  *int         `json:"updates"`
	Error    string       `json:"error"`
	Approval *approvalRef `json:"approval"`
}

type checkSeen struct {
	OS         checkPartSeen `json:"os"`
	Homebrew   checkPartSeen `json:"homebrew"`
	Containers checkPartSeen `json:"containers"`
}

// Each part of check_updates stands alone: one that needs admin rights
// waits for the owner, one whose feature is missing is unavailable, and
// the others report what they found.
func TestCheckUpdatesParts(t *testing.T) {
	h := newActHarness(t, "docker")
	var osChecks counter
	h.api.HandleFunc("POST /api/pkg/check", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		osChecks.seen(r)
		if r.Header.Get(elevate.Header) == "" {
			return httpx.Errorf(http.StatusForbidden, "elevation_required", "Administrator approval required").
				With(map[string]any{"action": "pkg.refresh", "target": "Refresh package databases"})
		}
		httpx.OK(w, pkg.CheckResult{Updates: []pkg.Update{{Name: "linux"}, {Name: "mesa"}}})
		return nil
	}))
	h.api.HandleFunc("POST /api/brew/check", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		return httpx.Errorf(http.StatusServiceUnavailable, "unavailable", "Homebrew is not installed")
	}))
	yes, no := true, false
	h.api.HandleFunc("POST /api/containers/check-updates", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, docker.UpdatesStatus{Results: map[string]docker.UpdateCheck{
			"a": {UpdateAvailable: &yes}, "b": {UpdateAvailable: &no}, "c": {UpdateAvailable: &yes},
		}})
	})
	cs := h.client(t, h.operate, false)
	var out checkSeen
	toolOK(t, cs, "check_updates", nil, &out)
	if out.Containers.State != "checked" || out.Containers.Updates == nil || *out.Containers.Updates != 2 {
		t.Fatalf("containers %+v", out.Containers)
	}
	if out.Homebrew.State != "unavailable" || out.Homebrew.Approval != nil {
		t.Fatalf("homebrew %+v", out.Homebrew)
	}
	if out.OS.State != "waiting_for_approval" || out.OS.Approval == nil || out.OS.Approval.ID == "" {
		t.Fatalf("os %+v", out.OS)
	}
	p, ok := h.s.approvals.Get(out.OS.Approval.ID)
	if !ok || p.Method != "POST" || p.Path != "/api/pkg/check" || p.Body != nil || p.Tool != "check_updates" {
		t.Fatalf("stored %+v", p)
	}
	if w := h.approve(t, p.ID, map[string]string{elevate.Header: credential("sudo", "pw")}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if osChecks.count() != 2 || osChecks.elev[1] == "" {
		t.Fatalf("os check ran %d times", osChecks.count())
	}

	// Without the feature at all: unavailable, nothing pending.
	h2 := newActHarness(t)
	toolOK(t, h2.client(t, h2.operate, false), "check_updates", nil, &out)
	for name, part := range map[string]checkPartSeen{"os": out.OS, "homebrew": out.Homebrew, "containers": out.Containers} {
		if part.State != "unavailable" {
			t.Errorf("%s %+v", name, part)
		}
	}
	if n := len(h2.pendingRequests()); n != 0 {
		t.Fatalf("%d pending", n)
	}
}

func TestProcessSignal(t *testing.T) {
	h := newActHarness(t)
	procs.New(events.NewHub(), h.s.audit, elevate.New(h.s.audit, httpx.NewProxyTrust(nil), nil, ""), nil).Register(h.api)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	cs := h.client(t, h.operate, false)
	var out struct {
		PID    int    `json:"pid"`
		Signal string `json:"signal"`
		Sent   bool   `json:"sent"`
	}
	toolOK(t, cs, "process_signal", map[string]any{"pid": cmd.Process.Pid}, &out)
	if !out.Sent || out.Signal != "TERM" || out.PID != cmd.Process.Pid {
		t.Fatalf("%+v", out)
	}
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Fatalf("sleep ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process still running")
	}
	h.agentEntry(t, "procs.signal", "process_signal")
	for _, bad := range []int{1, 0, -5} {
		toolFails(t, cs, "process_signal", map[string]any{"pid": bad}, "invalid_argument")
	}
	toolFails(t, cs, "process_signal", map[string]any{"pid": os.Getpid()}, "forbidden")
	// TERM only: there is no signal to choose.
	if strings.Contains(inputSchema(t, cs, "process_signal"), "signal") {
		t.Fatal("process_signal takes a signal argument")
	}
}

// inputSchema is tool's input schema as JSON.
func inputSchema(t *testing.T, cs *sdk.ClientSession, tool string) string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == tool {
			b, _ := json.Marshal(tl.InputSchema)
			return string(b)
		}
	}
	t.Fatalf("%s not listed", tool)
	return ""
}

// fakeForeignProcess serves the process routes for pid 4242, a root
// process Wargo may not signal itself, following the real handler's rules:
// a signal naming another start time is refused with process_changed,
// and without a credential the answer is elevation_required.
type fakeForeignProcess struct {
	sig     counter
	mu      sync.Mutex
	ticks   uint64
	name    string
	signals int
}

func (h *actHarness) fakeForeignProcess() *fakeForeignProcess {
	f := &fakeForeignProcess{ticks: 111, name: "sshd"}
	h.api.HandleFunc("GET /api/procs/{pid}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		httpx.OK(w, map[string]any{"pid": 4242, "name": f.name, "user": "root", "startTicks": f.ticks})
	})
	h.api.HandleFunc("POST /api/procs/{pid}/signal", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		f.sig.seen(r)
		var req struct {
			Signal     string  `json:"signal"`
			StartTicks *uint64 `json:"startTicks"`
		}
		json.Unmarshal([]byte(f.sig.body(f.sig.count()-1)), &req)
		f.mu.Lock()
		defer f.mu.Unlock()
		if req.StartTicks != nil && *req.StartTicks != f.ticks {
			return httpx.Errorf(http.StatusConflict, "process_changed", "The process has exited and its PID was reused")
		}
		if r.Header.Get(elevate.Header) == "" {
			return httpx.Errorf(http.StatusForbidden, "elevation_required", "Administrator approval required").
				With(map[string]any{"action": "procs.signal", "target": "SIGTERM pid 4242 (" + f.name + ", user root)"})
		}
		f.signals++
		httpx.OK(w, map[string]any{"ok": true, "pid": 4242, "signal": req.Signal})
		return nil
	}))
	return f
}

func (f *fakeForeignProcess) signalled() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signals
}

type signalSeen struct {
	Sent     bool         `json:"sent"`
	Approval *approvalRef `json:"approval"`
	Message  string       `json:"message"`
}

// A process Wargo may not signal itself becomes a request for the owner,
// with the same stored request, naming the process's start time.
func TestProcessSignalNeedsApprovalOnEPERM(t *testing.T) {
	h := newActHarness(t)
	f := h.fakeForeignProcess()
	cs := h.client(t, h.operate, false)
	var out signalSeen
	toolOK(t, cs, "process_signal", map[string]any{"pid": 4242}, &out)
	if out.Sent || out.Approval == nil || out.Approval.State != StatePending || out.Message == "" {
		t.Fatalf("%+v", out)
	}
	p, _ := h.s.approvals.Get(out.Approval.ID)
	if p.Method != "POST" || p.Path != "/api/procs/4242/signal" || string(p.Body) != f.sig.body(0) || p.Tool != "process_signal" {
		t.Fatalf("stored %+v body %s, first sent %s", p, p.Body, f.sig.body(0))
	}
	if string(p.Body) != `{"signal":"TERM","startTicks":111}` {
		t.Fatalf("stored body %s", p.Body)
	}
	if p.Summary != "Send TERM to process 4242 (sshd, user root)" {
		t.Fatalf("summary %q", p.Summary)
	}
	w := h.approve(t, p.ID, nil)
	if w.Code != http.StatusForbidden || errorCode(t, w) != "elevation_required" {
		t.Fatalf("approve without credential: %d %s", w.Code, w.Body)
	}
	if w := h.approve(t, p.ID, map[string]string{elevate.Header: credential("sudo", "pw")}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	if f.sig.count() != 3 || f.sig.body(2) != f.sig.body(0) || f.signalled() != 1 {
		t.Fatalf("handler saw %d requests, signalled %d", f.sig.count(), f.signalled())
	}
}

// fakeContainers serves the containers list and details routes with plex
// and, marked as Wargo's own, wargo, and counts update requests.
func (h *actHarness) fakeContainers() *counter {
	var updates counter
	cs := []docker.Container{
		{ID: plexID[:12], FullID: plexID, Name: "plex"},
		{ID: sonarrID[:12], FullID: sonarrID, Name: "wargo", Self: true},
	}
	h.api.HandleFunc("GET /api/containers", func(w http.ResponseWriter, r *http.Request) { httpx.OK(w, cs) })
	h.api.HandleFunc("GET /api/containers/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, c := range cs {
			if c.Name == r.PathValue("id") || strings.HasPrefix(c.FullID, r.PathValue("id")) {
				// Details answered by inspect alone, without the list's
				// self mark.
				c.Self = false
				httpx.OK(w, docker.Details{Container: c})
				return
			}
		}
		httpx.Fail(w, r, httpx.ErrNotFound)
	})
	h.api.HandleFunc("POST /api/containers/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		updates.seen(r)
		// The real handler audits the action; so does the fake.
		h.s.audit.Record(r, "containers.update", r.PathValue("id"), nil, nil)
		httpx.OK(w, map[string]string{"jobId": "j1", "strategy": "recreate"})
	})
	return &updates
}

func TestUpdateContainerRefusesSelf(t *testing.T) {
	h := newActHarness(t, "docker")
	updates := h.fakeContainers()
	cs := h.client(t, h.token, false)
	for _, id := range []string{"wargo", sonarrID[:12], sonarrID} {
		toolFails(t, cs, "update_container", map[string]any{"id": id}, "self_container")
	}
	if n := len(h.pendingRequests()); n != 0 || updates.count() != 0 {
		t.Fatalf("%d pending, %d update requests", n, updates.count())
	}
	toolFails(t, cs, "update_container", map[string]any{"id": "../wargo"}, "invalid_argument")
	toolFails(t, cs, "update_container", map[string]any{"id": "nope"}, "not_found")
}

// Admin tools only ever create a request; nothing reaches the action's
// handler until the owner approves.
func TestAdminToolsCreatePendingFirst(t *testing.T) {
	h := newActHarness(t, "docker", "pkg", "brew")
	updates := h.fakeContainers()
	var upgrades, brews counter
	h.api.HandleFunc("GET /api/pkg/precheck", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]any{"lock": map[string]any{"locked": false}, "disk": []any{}})
	})
	h.api.HandleFunc("POST /api/pkg/upgrade", func(w http.ResponseWriter, r *http.Request) {
		upgrades.seen(r)
		httpx.OK(w, map[string]string{"id": "j2"})
	})
	h.api.HandleFunc("POST /api/brew/upgrade", func(w http.ResponseWriter, r *http.Request) {
		brews.seen(r)
		httpx.OK(w, map[string]string{"id": "j3"})
	})
	cs := h.client(t, h.token, false)
	want := map[string]struct {
		args          map[string]any
		path, body    string
		summaryHas    string
		handlerCalled *counter
	}{
		"apply_os_updates": {nil, "/api/pkg/upgrade", "", "OS updates", &upgrades},
		"upgrade_homebrew": {nil, "/api/brew/upgrade", "", "Homebrew", &brews},
		"update_container": {map[string]any{"id": "plex"}, "/api/containers/" + plexID + "/update", `{"strategy":"auto"}`, "plex", updates},
	}
	for name, w := range want {
		res := callTool(t, cs, name, w.args)
		if res.IsError {
			t.Fatalf("%s: %s", name, res.Content[0].(*sdk.TextContent).Text)
		}
		var out pendingOut
		json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &out)
		if out.Approval.State != StatePending || out.Message == "" {
			t.Fatalf("%s: %+v", name, out)
		}
		p, _ := h.s.approvals.Get(out.Approval.ID)
		if p.Method != "POST" || p.Path != w.path || string(p.Body) != w.body || p.Tool != name || !strings.Contains(p.Summary, w.summaryHas) {
			t.Fatalf("%s stored %+v body %s", name, p, p.Body)
		}
		if n := w.handlerCalled.count(); n != 0 {
			t.Fatalf("%s: handler ran %d times before approval", name, n)
		}
		if a := h.approve(t, p.ID, nil); a.Code != 200 {
			t.Fatalf("%s approve: %d %s", name, a.Code, a.Body)
		}
		if n := w.handlerCalled.count(); n != 1 || w.handlerCalled.body(0) != w.body {
			t.Fatalf("%s: handler ran %d times after approval", name, n)
		}
	}
}

func TestApplyOSUpdatesStopsOnBlockers(t *testing.T) {
	h := newActHarness(t, "pkg")
	var upgrades counter
	h.api.HandleFunc("GET /api/pkg/precheck", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]any{
			"lock":    map[string]any{"locked": true, "path": "/var/lib/pacman/db.lck", "reason": "pacman is running (pid 77)"},
			"disk":    []any{map[string]any{"path": "/", "free": 100, "low": true}},
			"blocked": "Not enough free space on /",
		})
	})
	h.api.HandleFunc("POST /api/pkg/upgrade", func(w http.ResponseWriter, r *http.Request) { upgrades.seen(r) })
	cs := h.client(t, h.token, false)
	e := toolFails(t, cs, "apply_os_updates", nil, "upgrade_blocked")
	b, _ := json.Marshal(e.Data)
	for _, s := range []string{"Not enough free space on /", "pacman is running (pid 77)", "/var/lib/pacman/db.lck"} {
		if !strings.Contains(string(b), s) {
			t.Errorf("blockers %s lack %q", b, s)
		}
	}
	if n := len(h.pendingRequests()); n != 0 || upgrades.count() != 0 {
		t.Fatalf("%d pending, %d upgrades", n, upgrades.count())
	}
	if n := h.countAudit(t, "mcp.approval.requested"); n != 0 {
		t.Fatalf("%d requests recorded", n)
	}
}

// No argument can make apply_os_updates ask for conflicting packages to
// be removed.
func TestApplyOSUpdatesNeverSendsResolveConflicts(t *testing.T) {
	h := newActHarness(t, "pkg")
	var upgrades counter
	h.api.HandleFunc("GET /api/pkg/precheck", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]any{"lock": map[string]any{"locked": false}, "disk": []any{}})
	})
	h.api.HandleFunc("POST /api/pkg/upgrade", func(w http.ResponseWriter, r *http.Request) {
		upgrades.seen(r)
		httpx.OK(w, map[string]string{"id": "j"})
	})
	cs := h.client(t, h.token, false)
	if strings.Contains(inputSchema(t, cs, "apply_os_updates"), "onflict") {
		t.Fatal("apply_os_updates takes an argument about conflicts")
	}
	// An argument it does not take is refused.
	res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "apply_os_updates", Arguments: map[string]any{"resolveConflicts": true}})
	if err == nil && !res.IsError {
		t.Fatalf("resolveConflicts accepted: %+v", res)
	}
	if n := len(h.pendingRequests()); n != 0 || upgrades.count() != 0 {
		t.Fatalf("%d pending, %d upgrades", n, upgrades.count())
	}
}

// Review Focus 4 for upgrade_homebrew, through the real (simulated)
// Homebrew feature: the approval page's first replay, without a
// credential, gets elevation_required with nothing recorded, claimed or
// started; with the credential the upgrade runs as the agent. The pkg and
// docker packages hold the same test for apply_os_updates and
// update_container.
func TestAdminReplayWithoutCredentialHasNoSideEffects(t *testing.T) {
	fake, err := filepath.Abs(filepath.Join("..", "brew", "testdata", "fake"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WARGO_BREW_FAKE", fake)
	t.Setenv("WARGO_BREW_FAKE_DELAY", "0")
	h := newActHarness(t, "brew")
	hub := events.NewHub()
	jm := jobs.NewManager(hub)
	logDir := t.TempDir()
	brew.New(&server.Deps{Cfg: config.Config{DataDir: logDir}, Store: h.s.st, Hub: hub, Jobs: jm, Audit: h.s.audit,
		Elevate: elevate.New(h.s.audit, httpx.NewProxyTrust(nil), nil, "")}).Register(h.api)
	cs := h.client(t, h.token, false)

	var checked checkSeen
	toolOK(t, cs, "check_updates", nil, &checked)
	if checked.Homebrew.State != "checked" || checked.Homebrew.Updates == nil || *checked.Homebrew.Updates != 6 {
		t.Fatalf("homebrew check %+v", checked.Homebrew)
	}
	h.agentEntry(t, "brew.check", "check_updates")

	var out pendingOut
	toolOK(t, cs, "upgrade_homebrew", nil, &out)
	nothingYet := func(when string) {
		t.Helper()
		if n := len(jm.List()); n != 0 {
			t.Fatalf("%s: %d jobs", when, n)
		}
		var n int
		h.s.st.DB.QueryRow(`SELECT count(*) FROM audit_log WHERE action LIKE 'brew.upgrade%'`).Scan(&n)
		if n != 0 {
			t.Fatalf("%s: %d brew.upgrade entries", when, n)
		}
		if logs, _ := filepath.Glob(filepath.Join(logDir, "brew-*.log")); len(logs) != 0 {
			t.Fatalf("%s: logs %v", when, logs)
		}
	}
	nothingYet("before approval")
	w := h.approve(t, out.Approval.ID, nil)
	if w.Code != http.StatusForbidden || errorCode(t, w) != "elevation_required" {
		t.Fatalf("first replay: %d %s", w.Code, w.Body)
	}
	nothingYet("after the first replay")
	if p := h.pending(t, out.Approval.ID); p.State != StatePending {
		t.Fatalf("state %s after the first replay", p.State)
	}

	w = h.approve(t, out.Approval.ID, map[string]string{elevate.Header: credential("sudo", "pw")})
	if w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	h.agentEntry(t, "brew.upgrade", "upgrade_homebrew")
	// The upgrade answers with its job; approval_status points at it.
	if p := h.pending(t, out.Approval.ID); p.State != StateApproved || len(jm.List()) != 1 || p.JobID != jm.List()[0].ID {
		t.Fatalf("approved %+v, jobs %+v", p, jm.List())
	}
	waitJobs(t, jm)
	if j := jm.List()[0]; j.State != jobs.Succeeded {
		t.Fatalf("upgrade %s: %s", j.State, j.Error)
	}
}

// waitJobs waits for every job to finish.
func waitJobs(t *testing.T, jm *jobs.Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		done := true
		for _, j := range jm.List() {
			done = done && j.FinishedAt != nil
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

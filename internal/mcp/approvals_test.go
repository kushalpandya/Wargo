// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/notify"
	"github.com/kushalpandya/wargo/internal/server"
)

const testOrigin = "https://wargo.example.com"

// pokeSpec is a test-only admin tool: it asks to POST a body to
// /api/things/{id}.
var pokeSpec = ToolSpec{Name: "poke_thing", Level: LevelAdmin, Admin: true,
	Routes: []route{{"POST", "POST /api/things/{id}"}}}

type pokeIn struct {
	ID  string `json:"id"`
	Raw string `json:"raw,omitempty"`
}

// received is what the test route saw on one request.
type received struct {
	method, path, body, elevation, origin, ip, ua string
}

// approvalHarness is an enabled Service with the poke_thing tool, a
// private API that serves /api/things/{id} and the approval endpoints, a
// signed-in browser (the dev bypass session, tag "dev") and an admin agent.
type approvalHarness struct {
	s       *Service
	api     *http.ServeMux
	browser http.Handler
	center  *notify.Center
	agent   Agent
	token   string
	ts      *httptest.Server

	mu    sync.Mutex
	got   []received
	clock time.Time
	// needElevation makes the route ask for a credential first.
	needElevation bool
	// reply is what the route answers (default {"poked": id}); fail, when
	// set, is its error instead, and boom makes it panic.
	reply any
	fail  *httpx.Error
	boom  bool
	// hold, when set, blocks the route until closed; entered is closed
	// once the route has started.
	hold, entered chan struct{}
}

func newApprovalHarness(t *testing.T, origins []string) *approvalHarness {
	t.Helper()
	st := testStore(t)
	cfg := config.Default()
	cfg.Auth.User = "tester"
	cfg.Auth.Origins = origins
	cfg.Listen = "127.0.0.1:2207"
	cfg.Dev.AuthBypass = true
	proxy := httpx.NewProxyTrust(nil)
	al := audit.New(st, proxy)
	au := auth.New(cfg, st, al, proxy)
	center, err := notify.New(st, events.NewHub(), func(t time.Time) time.Time { return t })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(center.Close)
	s, err := New(&server.Deps{Cfg: cfg, Store: st, Audit: al, Proxy: proxy, Auth: au, Notify: center})
	if err != nil {
		t.Fatal(err)
	}
	enable(t, s, NetworkLocal)
	s.tools = nil
	h := &approvalHarness{s: s, api: http.NewServeMux(), center: center, clock: time.Now()}
	s.approvals.now = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.clock
	}
	s.SetAPI(h.api)
	s.Register(h.api)
	h.browser = au.Require(h.api)
	h.api.HandleFunc("POST /api/things/{id}", httpx.Handler(h.thing))
	addTool(s, pokeSpec, func(ctx context.Context, req *sdk.CallToolRequest, in pokeIn) (*sdk.CallToolResult, pendingOut, error) {
		var body any = map[string]string{"id": in.ID}
		if in.Raw != "" {
			body = json.RawMessage(in.Raw)
		}
		out, err := s.waitForOwner(ctx, req, pokeSpec, in, "POST", "/api/things/"+in.ID, body, "Poke thing "+in.ID)
		return nil, out, err
	})
	addTool(s, approvalStatusSpec, s.approvalStatus)
	h.agent, h.token = newAgent(t, s, LevelAdmin)
	h.ts = httptest.NewServer(s.Handler())
	t.Cleanup(h.ts.Close)
	return h
}

func (h *approvalHarness) thing(w http.ResponseWriter, r *http.Request) error {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	need, reply, fail, boom, hold, entered := h.needElevation, h.reply, h.fail, h.boom, h.hold, h.entered
	h.mu.Unlock()
	if boom {
		panic("boom")
	}
	if entered != nil {
		close(entered)
	}
	if hold != nil {
		<-hold
	}
	elev := r.Header.Get(elevate.Header)
	if need && elev == "" {
		return httpx.Errorf(http.StatusForbidden, "elevation_required", "Administrator approval required").
			With(map[string]any{"action": "thing.poke", "target": r.PathValue("id"), "methods": map[string]bool{"sudo": true}})
	}
	if need && strings.Contains(decodeCredential(elev), "wrong") {
		return httpx.Errorf(http.StatusUnauthorized, "elevation_failed", "Incorrect sudo password")
	}
	h.mu.Lock()
	h.got = append(h.got, received{r.Method, r.URL.Path, string(b), elev, r.Header.Get("Origin"),
		strings.TrimSuffix(r.RemoteAddr, ":0"), r.UserAgent()})
	h.mu.Unlock()
	h.s.audit.Write(r, audit.Event{Action: "thing.poke", Target: r.PathValue("id")})
	if fail != nil {
		return fail
	}
	if reply == nil {
		reply = map[string]string{"poked": r.PathValue("id")}
	}
	httpx.OK(w, reply)
	return nil
}

func decodeCredential(h string) string {
	b, _ := base64.StdEncoding.DecodeString(h)
	return string(b)
}

func credential(method, password string) string {
	b, _ := json.Marshal(map[string]string{"method": method, "password": password})
	return base64.StdEncoding.EncodeToString(b)
}

func (h *approvalHarness) received() []received {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]received(nil), h.got...)
}

func (h *approvalHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.clock = h.clock.Add(d)
	h.mu.Unlock()
}

// client connects an MCP client for token; elicit declares URL elicitation.
func (h *approvalHarness) client(t *testing.T, token string, elicit bool) *sdk.ClientSession {
	t.Helper()
	return h.clientAt(t, token, elicit, "")
}

// clientAt is client speaking protocol version ("" for the newest).
func (h *approvalHarness) clientAt(t *testing.T, token string, elicit bool, version string) *sdk.ClientSession {
	t.Helper()
	var opts *sdk.ClientOptions
	if elicit {
		opts = &sdk.ClientOptions{
			Capabilities: &sdk.ClientCapabilities{Elicitation: &sdk.ElicitationCapabilities{URL: &sdk.URLElicitationCapabilities{}}},
			ElicitationHandler: func(context.Context, *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
				return &sdk.ElicitResult{Action: "cancel"}, nil
			},
		}
	}
	cs, err := connect(t, h.ts.URL, token, opts, version)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// request makes the agent ask to poke thing id with body raw ("" for the
// default body) and returns the pending result.
func (h *approvalHarness) request(t *testing.T, cs *sdk.ClientSession, id, raw string) pendingOut {
	t.Helper()
	var out pendingOut
	toolOK(t, cs, "poke_thing", map[string]any{"id": id, "raw": raw}, &out)
	return out
}

// browse sends a request from the signed-in browser.
func (h *approvalHarness) browse(t *testing.T, method, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = "127.0.0.1:2207"
	r.RemoteAddr = "192.168.1.20:4000"
	r.Header.Set("User-Agent", "Browser/1")
	r.Header.Set("Origin", "http://127.0.0.1:2207")
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.browser.ServeHTTP(w, r)
	return w
}

func (h *approvalHarness) approve(t *testing.T, id string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return h.browse(t, "POST", "/api/mcp/approvals/"+id+"/approve", header)
}

func (h *approvalHarness) pending(t *testing.T, id string) Pending {
	t.Helper()
	w := h.browse(t, "GET", "/api/mcp/approvals/"+id, nil)
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", id, w.Code, w.Body)
	}
	var p Pending
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e httpx.Error
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body %s: %v", w.Body, err)
	}
	return e.Code
}

// countAudit counts entries with action.
func (h *approvalHarness) countAudit(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := h.s.st.DB.QueryRow(`SELECT count(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Review Focus 3: approving runs exactly the stored request, and nothing
// reaches the handler before approval.
func TestApproveRunsStoredRequestUnchanged(t *testing.T) {
	h := newApprovalHarness(t, []string{testOrigin})
	cs := h.client(t, h.token, false)
	raw := `{"b":2,  "a":[1, 2], "s":"xé"}`
	first := h.request(t, cs, "t1", raw)
	// The agent asking again with other arguments makes another request;
	// it cannot change the first.
	second := h.request(t, cs, "t2", `{"evil":true}`)
	if first.Approval.ID == second.Approval.ID {
		t.Fatal("same id for two requests")
	}
	if n := len(h.received()); n != 0 {
		t.Fatalf("the handler ran %d times before approval", n)
	}
	p, ok := h.s.approvals.Get(first.Approval.ID)
	if !ok || string(p.Body) != raw || p.Method != "POST" || p.Path != "/api/things/t1" || p.Tool != "poke_thing" || p.State != StatePending {
		t.Fatalf("stored %+v body %s", p, p.Body)
	}

	w := h.approve(t, first.Approval.ID, nil)
	if w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	got := h.received()
	if len(got) != 1 {
		t.Fatalf("handler ran %d times", len(got))
	}
	if got[0].body != raw || got[0].method != "POST" || got[0].path != "/api/things/t1" {
		t.Fatalf("replayed %+v, want body %s", got[0], raw)
	}
	var after Pending
	json.Unmarshal(w.Body.Bytes(), &after)
	if after.State != StateApproved || string(after.Result) != `{"poked":"t1"}` || after.Status != 200 {
		t.Fatalf("after approve %+v result %s", after, after.Result)
	}
	if p := h.pending(t, second.Approval.ID); p.State != StatePending {
		t.Fatalf("other request %s", p.State)
	}
}

// Admin tools reach a handler only through an approved request.
func TestCallRefusesAdminSpec(t *testing.T) {
	h := newApprovalHarness(t, nil)
	ctx := withCaller(t.Context(), caller{agent: h.agent, ip: "127.0.0.1"})
	if _, err := h.s.call(ctx, pokeSpec, "POST", "/api/things/t1", nil); err == nil {
		t.Fatal("call ran an admin tool's route")
	}
	if n := len(h.received()); n != 0 {
		t.Fatalf("handler ran %d times", n)
	}
}

func TestApproveTwiceRefused(t *testing.T) {
	h := newApprovalHarness(t, nil)
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID
	if w := h.approve(t, id, nil); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	w := h.approve(t, id, nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "already_approved" {
		t.Fatalf("second approve: %d %s", w.Code, w.Body)
	}
	w = h.browse(t, "POST", "/api/mcp/approvals/"+id+"/deny", nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "already_approved" {
		t.Fatalf("deny after approve: %d %s", w.Code, w.Body)
	}
	if n := len(h.received()); n != 1 {
		t.Fatalf("handler ran %d times", n)
	}
}

// Two approvals at once: only one runs.
func TestApproveWhileRunningRefused(t *testing.T) {
	h := newApprovalHarness(t, nil)
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID
	h.mu.Lock()
	h.hold, h.entered = make(chan struct{}), make(chan struct{})
	hold, entered := h.hold, h.entered
	h.mu.Unlock()
	done := make(chan int)
	go func() {
		r := httptest.NewRequest("POST", "/api/mcp/approvals/"+id+"/approve", nil)
		r.Host = "127.0.0.1:2207"
		w := httptest.NewRecorder()
		h.browser.ServeHTTP(w, r)
		done <- w.Code
	}()
	<-entered
	h.mu.Lock()
	h.hold, h.entered = nil, nil
	h.mu.Unlock()
	// The approval page shows it running.
	if p := h.pending(t, id); !p.Running || p.State != StatePending {
		t.Fatalf("while running: %+v", p)
	}
	w := h.approve(t, id, nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "approval_in_progress" {
		t.Fatalf("approve while running: %d %s", w.Code, w.Body)
	}
	w = h.browse(t, "POST", "/api/mcp/approvals/"+id+"/deny", nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "approval_in_progress" {
		t.Fatalf("deny while running: %d %s", w.Code, w.Body)
	}
	close(hold)
	if code := <-done; code != 200 {
		t.Fatalf("first approve: %d", code)
	}
	if p := h.pending(t, id); p.Running || p.State != StateApproved {
		t.Fatalf("after running: %+v", p)
	}
	if n := len(h.received()); n != 1 {
		t.Fatalf("handler ran %d times", n)
	}
}

func TestApproveAfterExpiryRefused(t *testing.T) {
	h := newApprovalHarness(t, nil)
	h.s.mu.Lock()
	h.s.settings.ApprovalMinutes = 5
	h.s.mu.Unlock()
	cs := h.client(t, h.token, false)
	out := h.request(t, cs, "t1", "")
	id := out.Approval.ID
	p := h.pending(t, id)
	if got := p.Expires.Sub(p.Created); got != 5*time.Minute {
		t.Fatalf("time limit %v", got)
	}
	if !out.Approval.Expires.Equal(p.Expires) {
		t.Fatalf("agent told %v, stored %v", out.Approval.Expires, p.Expires)
	}
	h.advance(5*time.Minute + time.Second)
	w := h.approve(t, id, nil)
	if w.Code != http.StatusGone || errorCode(t, w) != "expired" {
		t.Fatalf("approve after expiry: %d %s", w.Code, w.Body)
	}
	w = h.browse(t, "POST", "/api/mcp/approvals/"+id+"/deny", nil)
	if w.Code != http.StatusGone || errorCode(t, w) != "expired" {
		t.Fatalf("deny after expiry: %d %s", w.Code, w.Body)
	}
	if p := h.pending(t, id); p.State != StateExpired {
		t.Fatalf("state %s", p.State)
	}
	h.s.approvals.sweep()
	h.browse(t, "GET", "/api/mcp/approvals", nil)
	if n := h.countAudit(t, "mcp.approval.expired"); n != 1 {
		t.Fatalf("%d expired entries", n)
	}
	if n := len(h.received()); n != 0 {
		t.Fatalf("handler ran %d times", n)
	}
	var st approvalStatusOut
	toolOK(t, cs, "approval_status", map[string]any{"id": id}, &st)
	if st.State != StateExpired {
		t.Fatalf("approval_status %+v", st)
	}
}

// The sweep writes expired entries without anyone looking, and decided
// requests are dropped after a while.
func TestSweepExpiresAndForgets(t *testing.T) {
	h := newApprovalHarness(t, nil)
	cs := h.client(t, h.token, false)
	a := h.request(t, cs, "t1", "").Approval.ID
	b := h.request(t, cs, "t2", "").Approval.ID
	if w := h.approve(t, b, nil); w.Code != 200 {
		t.Fatalf("approve: %d", w.Code)
	}
	h.advance(16 * time.Minute)
	h.s.approvals.sweep()
	if n := h.countAudit(t, "mcp.approval.expired"); n != 1 {
		t.Fatalf("%d expired entries", n)
	}
	if _, ok := h.s.approvals.Get(a); !ok {
		t.Fatal("expired request dropped at once")
	}
	h.advance(time.Hour)
	h.s.approvals.sweep()
	if _, ok := h.s.approvals.Get(a); ok {
		t.Fatal("expired request kept after an hour")
	}
	if _, ok := h.s.approvals.Get(b); ok {
		t.Fatal("approved request kept after an hour")
	}
	if n := h.countAudit(t, "mcp.approval.expired"); n != 1 {
		t.Fatalf("%d expired entries after forgetting", n)
	}
}

func TestDeny(t *testing.T) {
	h := newApprovalHarness(t, nil)
	cs := h.client(t, h.token, false)
	id := h.request(t, cs, "t1", "").Approval.ID
	w := h.browse(t, "GET", "/api/mcp/approvals", nil)
	var list []Pending
	if json.Unmarshal(w.Body.Bytes(), &list); w.Code != 200 || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	w = h.browse(t, "POST", "/api/mcp/approvals/"+id+"/deny", nil)
	if w.Code != 200 {
		t.Fatalf("deny: %d %s", w.Code, w.Body)
	}
	if p := h.pending(t, id); p.State != StateDenied {
		t.Fatalf("state %s", p.State)
	}
	w = h.approve(t, id, nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "already_denied" {
		t.Fatalf("approve after deny: %d %s", w.Code, w.Body)
	}
	if n := len(h.received()); n != 0 {
		t.Fatalf("handler ran %d times", n)
	}
	var ref int64
	var source string
	if err := h.s.st.DB.QueryRow(`SELECT ref, source FROM audit_log WHERE action = 'mcp.approval.denied'`).Scan(&ref, &source); err != nil {
		t.Fatal(err)
	}
	if p := h.pending(t, id); ref != p.Ref || source != audit.SourceUser {
		t.Fatalf("denied entry ref %d source %q, request entry %d", ref, source, p.Ref)
	}
	var st approvalStatusOut
	toolOK(t, cs, "approval_status", map[string]any{"id": id}, &st)
	if st.State != StateDenied {
		t.Fatalf("approval_status %+v", st)
	}
	if w := h.browse(t, "POST", "/api/mcp/approvals/nope/deny", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
}

// The handler wants a credential: the first approve hands its answer to
// the browser unchanged and the request stays pending; the browser retries
// with the credential and the approver's Origin, and it runs.
func TestElevationRequiredPassesThrough(t *testing.T) {
	h := newApprovalHarness(t, []string{testOrigin})
	h.needElevation = true
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID

	w := h.approve(t, id, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("first approve: %d %s", w.Code, w.Body)
	}
	var e struct {
		Code string         `json:"code"`
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Code != "elevation_required" || e.Data["action"] != "thing.poke" || e.Data["target"] != "t1" {
		t.Fatalf("first approve answer %s", w.Body)
	}
	if p := h.pending(t, id); p.State != StatePending {
		t.Fatalf("state after elevation_required: %s", p.State)
	}

	// A wrong password is also handed back, so the dialog can ask again.
	w = h.approve(t, id, map[string]string{elevate.Header: credential("sudo", "wrong")})
	if w.Code != http.StatusUnauthorized || errorCode(t, w) != "elevation_failed" {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body)
	}
	if p := h.pending(t, id); p.State != StatePending {
		t.Fatalf("state after elevation_failed: %s", p.State)
	}

	cred := credential("sudo", "right")
	w = h.approve(t, id, map[string]string{elevate.Header: cred, "Origin": testOrigin})
	if w.Code != 200 {
		t.Fatalf("approve with credential: %d %s", w.Code, w.Body)
	}
	got := h.received()
	if len(got) != 1 || got[0].elevation != cred || got[0].origin != testOrigin ||
		got[0].ip != "192.168.1.20" || got[0].ua != "Browser/1" {
		t.Fatalf("replayed %+v", got)
	}
	var elev string
	if err := h.s.st.DB.QueryRow(`SELECT elevation FROM audit_log WHERE action = 'mcp.approval.approved'`).Scan(&elev); err != nil {
		t.Fatal(err)
	}
	if elev != "sudo" {
		t.Fatalf("approved entry elevation %q", elev)
	}
}

// Review Focus 3: the approval endpoints answer only a browser session.
func TestApprovalEndpointsRefuseAgentContext(t *testing.T) {
	h := newApprovalHarness(t, nil)
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID
	endpoints := []struct{ method, path string }{
		{"GET", "/api/mcp/approvals"},
		{"GET", "/api/mcp/approvals/" + id},
		{"POST", "/api/mcp/approvals/" + id + "/approve"},
		{"POST", "/api/mcp/approvals/" + id + "/deny"},
	}
	for _, ep := range endpoints {
		// Signed in, but made on behalf of an agent.
		r := httptest.NewRequest(ep.method, ep.path, nil)
		r.Host = "127.0.0.1:2207"
		r = r.WithContext(audit.WithAgent(r.Context(), audit.AgentRef{ID: h.agent.ID, Name: h.agent.Name, Tool: "poke_thing"}))
		w := httptest.NewRecorder()
		h.browser.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s with an agent: %d", ep.method, ep.path, w.Code)
		}
		// No browser session at all.
		w = httptest.NewRecorder()
		h.api.ServeHTTP(w, httptest.NewRequest(ep.method, ep.path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d", ep.method, ep.path, w.Code)
		}
	}
	if p := h.pending(t, id); p.State != StatePending || len(h.received()) != 0 {
		t.Fatalf("state %s, handler ran %d times", p.State, len(h.received()))
	}
}

func TestApprovalStatusOnlyOwnRequests(t *testing.T) {
	h := newApprovalHarness(t, nil)
	h.reply = map[string]any{"jobId": "job42", "strategy": "pull"}
	mine := h.client(t, h.token, false)
	id := h.request(t, mine, "t1", "").Approval.ID
	_, otherToken := newAgent(t, h.s, LevelAdmin)
	other := h.client(t, otherToken, false)

	args := map[string]any{"id": id}
	toolFails(t, other, "approval_status", args, "not_found")
	var st approvalStatusOut
	toolOK(t, mine, "approval_status", args, &st)
	if st.State != StatePending || st.JobID != "" || st.Result != nil {
		t.Fatalf("pending status %+v", st)
	}
	if w := h.approve(t, id, nil); w.Code != 200 {
		t.Fatalf("approve: %d", w.Code)
	}
	st = approvalStatusOut{}
	toolOK(t, mine, "approval_status", args, &st)
	res, _ := st.Result.(map[string]any)
	if st.State != StateApproved || st.JobID != "job42" || res["strategy"] != "pull" || st.Status != 200 {
		t.Fatalf("approved status %+v", st)
	}
	toolFails(t, other, "approval_status", args, "not_found")
	// An id that is not one is refused as such.
	toolFails(t, mine, "approval_status", map[string]any{"id": "../x"}, "invalid_argument")
}

// How an admin tool answers: a client that declared URL elicitation gets
// the approval link as a URL elicitation, for the protocol revisions
// agents speak today as well as the newest (sessions are stateful for
// this); any other client, or any client when no origin is configured,
// gets a plain result, with the link only when there is one.
func TestPendingResultShapes(t *testing.T) {
	for _, c := range []struct {
		name    string
		origins []string
		elicit  bool
		version string
	}{
		{"elicitation, newest protocol", []string{testOrigin + "/", "https://second.example"}, true, ""},
		{"elicitation, 2025-11-25", []string{testOrigin}, true, "2025-11-25"},
		{"elicitation, 2025-06-18", []string{testOrigin}, true, "2025-06-18"},
		{"no elicitation", []string{testOrigin}, false, ""},
		{"elicitation without origins", nil, true, ""},
		{"neither", nil, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newApprovalHarness(t, c.origins)
			cs := h.clientAt(t, h.token, c.elicit, c.version)
			res, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: "poke_thing", Arguments: map[string]any{"id": "t1"}})
			list := h.s.approvals.List()
			if len(list) != 1 {
				t.Fatalf("%d requests", len(list))
			}
			id := list[0].ID
			link := ""
			if c.origins != nil {
				link = testOrigin + "/approve/" + id
			}
			if c.elicit && link != "" {
				var je *jsonrpc.Error
				if !errors.As(err, &je) || je.Code != sdk.CodeURLElicitationRequired {
					t.Fatalf("want URL elicitation required, got %v %+v", err, res)
				}
				var data struct {
					Elicitations []sdk.ElicitParams `json:"elicitations"`
				}
				if err := json.Unmarshal(je.Data, &data); err != nil || len(data.Elicitations) != 1 {
					t.Fatalf("data %s", je.Data)
				}
				el := data.Elicitations[0]
				if el.Mode != "url" || el.URL != link || el.ElicitationID != id || !strings.Contains(el.Message, "Poke thing t1") {
					t.Fatalf("elicitation %+v", el)
				}
				return
			}
			if err != nil || res.IsError {
				t.Fatalf("plain result: %v %+v", err, res)
			}
			var out pendingOut
			if err := json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &out); err != nil {
				t.Fatal(err)
			}
			a := out.Approval
			if a.ID != id || a.State != StatePending || a.URL != link || a.Expires.IsZero() {
				t.Fatalf("approval %+v", a)
			}
			if out.Message != "Waiting for the owner's approval in Wargo; check approval_status." {
				t.Fatalf("message %q", out.Message)
			}
		})
	}
}

// Once approved, a request is spent whatever its handler answered: a
// refusal is kept as its result, and so is an internal failure.
func TestApprovedRequestKeepsFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		fail   *httpx.Error
		boom   bool
		status int
		code   string
	}{
		{"handler refuses", httpx.Errorf(http.StatusConflict, "process_changed", "The process has exited and its PID was reused"), false, http.StatusConflict, "process_changed"},
		{"handler panics", nil, true, http.StatusInternalServerError, "internal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newApprovalHarness(t, nil)
			id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID
			h.mu.Lock()
			h.fail, h.boom = c.fail, c.boom
			h.mu.Unlock()
			h.approve(t, id, nil)
			p := h.pending(t, id)
			if p.State != StateApproved || p.Status != c.status || !strings.Contains(string(p.Result), `"`+c.code+`"`) {
				t.Fatalf("after approve %+v result %s", p, p.Result)
			}
			if w := h.approve(t, id, nil); errorCode(t, w) != "already_approved" {
				t.Fatalf("approved again: %d %s", w.Code, w.Body)
			}
			if n := h.countAudit(t, "mcp.approval.approved"); n != 1 {
				t.Fatalf("%d approved entries", n)
			}
		})
	}
}

// requested -> action entry -> approved, linked by Ref.
func TestApprovalAuditChain(t *testing.T) {
	h := newApprovalHarness(t, nil)
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID
	if w := h.approve(t, id, nil); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	type row struct {
		id, ref                                   int64
		action, source, agentID, tool, sess, elev string
	}
	rows, err := h.s.st.DB.Query(`SELECT id, COALESCE(ref,0), action, source, COALESCE(agent_id,''), COALESCE(tool,''),
		COALESCE(session,''), COALESCE(elevation,'') FROM audit_log
		WHERE action IN ('mcp.approval.requested','thing.poke','mcp.approval.approved') ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.ref, &r.action, &r.source, &r.agentID, &r.tool, &r.sess, &r.elev); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 3 {
		t.Fatalf("entries %+v", got)
	}
	req, act, ok := got[0], got[1], got[2]
	if req.action != "mcp.approval.requested" || req.source != audit.SourceAgent || req.agentID != h.agent.ID || req.tool != "poke_thing" {
		t.Fatalf("request entry %+v", req)
	}
	if p := h.pending(t, id); p.Ref != req.id {
		t.Fatalf("pending ref %d, entry %d", p.Ref, req.id)
	}
	if act.action != "thing.poke" || act.ref != req.id || act.source != audit.SourceAgent || act.agentID != h.agent.ID ||
		act.sess != "dev" || act.elev != "none" {
		t.Fatalf("action entry %+v", act)
	}
	if ok.action != "mcp.approval.approved" || ok.ref != req.id || ok.source != audit.SourceUser || ok.sess != "dev" || ok.elev != "none" {
		t.Fatalf("approved entry %+v", ok)
	}
}

// An agent cannot flood the owner (or memory) with requests: each agent
// has its own cap under the global one.
func TestTooManyPending(t *testing.T) {
	oldAll, oldAgent := maxPending, maxAgentPending
	maxPending, maxAgentPending = 3, 2
	t.Cleanup(func() { maxPending, maxAgentPending = oldAll, oldAgent })
	h := newApprovalHarness(t, nil)
	cs := h.client(t, h.token, false)
	first := h.request(t, cs, "t1", "").Approval.ID
	h.request(t, cs, "t2", "")
	toolFails(t, cs, "poke_thing", map[string]any{"id": "t3"}, "too_many_approvals")
	// Another agent still has room, until all requests together are full.
	_, otherToken := newAgent(t, h.s, LevelAdmin)
	other := h.client(t, otherToken, false)
	h.request(t, other, "o1", "")
	toolFails(t, other, "poke_thing", map[string]any{"id": "o2"}, "too_many_approvals")
	// A decided request no longer counts.
	if w := h.browse(t, "POST", "/api/mcp/approvals/"+first+"/deny", nil); w.Code != 200 {
		t.Fatalf("deny: %d", w.Code)
	}
	h.request(t, cs, "t3", "")
}

// An agent asking again for the same thing while it waits gets the same
// request: no second entry, no second notification.
func TestRepeatedRequestReturnsPending(t *testing.T) {
	h := newApprovalHarness(t, nil)
	cs := h.client(t, h.token, false)
	first := h.request(t, cs, "t1", `{"a":1}`).Approval.ID
	if again := h.request(t, cs, "t1", `{"a":1}`).Approval.ID; again != first {
		t.Fatalf("asking again made %s, want %s", again, first)
	}
	if other := h.request(t, cs, "t1", `{"a":2}`).Approval.ID; other == first {
		t.Fatal("another body got the same request")
	}
	if n := h.countAudit(t, "mcp.approval.requested"); n != 2 {
		t.Fatalf("%d requested entries", n)
	}
	snap, err := h.center.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range snap.Recent {
		if it.Category == notify.AgentApproval {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d notifications", n)
	}
	// Once decided, asking again is a new request.
	if w := h.browse(t, "POST", "/api/mcp/approvals/"+first+"/deny", nil); w.Code != 200 {
		t.Fatalf("deny: %d", w.Code)
	}
	if again := h.request(t, cs, "t1", `{"a":1}`).Approval.ID; again == first {
		t.Fatal("a denied request came back")
	}
}

// Revoking an agent in Settings denies what it still waits for, and the
// audit names the agent.
func TestRevokeDeniesPending(t *testing.T) {
	h := newApprovalHarness(t, nil)
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID
	if w := h.browse(t, "DELETE", "/api/mcp/agents/"+h.agent.ID, nil); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if p := h.pending(t, id); p.State != StateDenied {
		t.Fatalf("state %s", p.State)
	}
	var target, details string
	if err := h.s.st.DB.QueryRow(`SELECT target, details FROM audit_log WHERE action = 'mcp.agent.revoke'`).Scan(&target, &details); err != nil {
		t.Fatal(err)
	}
	if target != h.agent.Name || !strings.Contains(details, h.agent.ID) {
		t.Fatalf("revoke entry %q %s", target, details)
	}
	if n := h.countAudit(t, "mcp.approval.denied"); n != 1 {
		t.Fatalf("%d denied entries", n)
	}
	snap, _ := h.center.Snapshot(t.Context())
	for _, it := range snap.Recent {
		if it.Category == notify.AgentApproval && it.ResolvedAt == nil {
			t.Fatal("notification not resolved after revoke")
		}
	}
	if n := len(h.received()); n != 0 {
		t.Fatalf("handler ran %d times", n)
	}
}

func TestApprovalNotification(t *testing.T) {
	h := newApprovalHarness(t, nil)
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID
	snap, err := h.center.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var item *notify.Item
	for i := range snap.Recent {
		if snap.Recent[i].Category == notify.AgentApproval {
			item = &snap.Recent[i]
		}
	}
	if item == nil {
		t.Fatalf("no agent.approval item in %+v", snap.Recent)
	}
	if item.Key != id || item.Link != "/approve/"+id || item.Title != "Agent needs approval" ||
		!strings.Contains(item.Body, h.agent.Name) || !strings.Contains(item.Body, "Poke thing t1") || item.ResolvedAt != nil {
		t.Fatalf("item %+v", item)
	}
	if w := h.approve(t, id, nil); w.Code != 200 {
		t.Fatalf("approve: %d", w.Code)
	}
	snap, _ = h.center.Snapshot(t.Context())
	for _, it := range snap.Recent {
		if it.Category == notify.AgentApproval && it.Key == id && it.ResolvedAt == nil {
			t.Fatal("notification not resolved after approval")
		}
	}
}

// Requests live in memory: after a restart their notifications are
// resolved, not left open forever.
func TestRestartResolvesApprovalNotifications(t *testing.T) {
	h := newApprovalHarness(t, nil)
	h.request(t, h.client(t, h.token, false), "t1", "")
	if _, err := New(&server.Deps{Cfg: config.Default(), Store: h.s.st, Audit: h.s.audit, Notify: h.center}); err != nil {
		t.Fatal(err)
	}
	snap, err := h.center.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, it := range snap.Recent {
		if it.Category == notify.AgentApproval {
			n++
			if it.ResolvedAt == nil || it.Title != "Agent request dropped when Wargo restarted" {
				t.Fatalf("item %+v", it)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d agent.approval items", n)
	}
}

// An open session survives revoke and disable, so approving re-checks both
// and never runs the request for an agent that is gone.
func TestApproveRechecksAgentAndSettings(t *testing.T) {
	h := newApprovalHarness(t, nil)
	id := h.request(t, h.client(t, h.token, false), "t1", "").Approval.ID

	h.s.mu.Lock()
	h.s.settings.Enabled = false
	h.s.mu.Unlock()
	w := h.approve(t, id, nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "mcp_disabled" {
		t.Fatalf("approve while off: %d %s", w.Code, w.Body)
	}
	enable(t, h.s, NetworkLocal)

	level := LevelRead
	if err := h.s.agents.Update(t.Context(), h.agent.ID, nil, &level); err != nil {
		t.Fatal(err)
	}
	w = h.approve(t, id, nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "forbidden_level" {
		t.Fatalf("approve after lowering the level: %d %s", w.Code, w.Body)
	}

	if err := h.s.agents.Revoke(t.Context(), h.agent.ID); err != nil {
		t.Fatal(err)
	}
	w = h.approve(t, id, nil)
	if w.Code != http.StatusConflict || errorCode(t, w) != "agent_revoked" {
		t.Fatalf("approve after revoke: %d %s", w.Code, w.Body)
	}
	if n := len(h.received()); n != 0 {
		t.Fatalf("handler ran %d times", n)
	}
	if p := h.pending(t, id); p.State != StatePending {
		t.Fatalf("state %s", p.State)
	}
}

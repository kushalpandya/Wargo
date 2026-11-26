// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/notify"
)

// The states of a pending request.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateDenied   = "denied"
	StateExpired  = "expired"
)

const (
	// sweepEvery is how often expired requests are looked for while no one
	// asks about them.
	sweepEvery = time.Minute
	// keepDecided is how long a decided or expired request is remembered,
	// so the agent can still read its state.
	keepDecided = time.Hour
	// replayTimeout bounds an approved request's handler.
	replayTimeout = 15 * time.Minute
	// waitMessage tells an agent what to do with a pending request.
	waitMessage = "Waiting for the owner's approval in Wargo; check approval_status."
)

// maxPending bounds the requests waiting at once, and maxAgentPending
// those of one agent (variables for tests).
var (
	maxPending      = 50
	maxAgentPending = 10
)

// Pending is an agent's request waiting for the owner, and what came of it.
type Pending struct {
	ID      string         `json:"id"`
	Agent   audit.AgentRef `json:"agent"`
	AgentID string         `json:"agentId"`
	Tool    string         `json:"tool"`
	// Args are the tool's arguments, as the agent sent them.
	Args json.RawMessage `json:"args,omitempty"`
	// Method, Path and Body are the exact internal request that runs on
	// approval.
	Method  string          `json:"method"`
	Path    string          `json:"path"`
	Body    json.RawMessage `json:"body,omitempty"`
	Summary string          `json:"summary"`
	Created time.Time       `json:"created"`
	Expires time.Time       `json:"expires"`
	State   string          `json:"state"`
	// Status, Result and JobID are the handler's answer once approved.
	Status int             `json:"status,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	JobID  string          `json:"jobId,omitempty"`
	// Ref is the audit entry that recorded the request.
	Ref int64 `json:"ref"`
	// Running is set while the approved request is being served.
	Running bool `json:"running,omitempty"`
}

// errNoRequest answers for a request that is not (or no longer) known.
var errNoRequest = httpx.Errorf(http.StatusNotFound, "not_found", "No such request; it may have expired.")

// approval is a stored request with what only Approvals needs.
type approval struct {
	p    Pending
	spec ToolSpec
	// running is set while the approved request is being served.
	running bool
	// decided is when it left the pending state.
	decided time.Time
}

// Approvals holds the requests waiting for the owner. They live in memory
// only: a restart drops them, which the agent sees as not found.
type Approvals struct {
	s   *Service
	now func() time.Time

	mu    sync.Mutex
	items map[string]*approval
}

func newApprovals(s *Service) *Approvals {
	return &Approvals{s: s, now: time.Now, items: map[string]*approval{}}
}

// Create records the agent's request to run method and path with body
// for the tool spec, and tells the owner: an mcp.approval.requested audit
// entry (source agent, its id kept as Ref) and an agent.approval
// notification linking to the approval page. The agent is the caller in
// ctx, as for call. args are the tool's arguments, shown to the owner;
// summary is a plain description of the action. body is sent as JSON when
// approved (a json.RawMessage byte for byte). Nothing reaches a handler
// here.
//
// An agent asking again for the same tool, method, path and body while
// its first request still waits gets that request back, with no new entry
// or notification. Each agent may have maxAgentPending requests waiting,
// and all agents together maxPending.
func (a *Approvals) Create(ctx context.Context, spec ToolSpec, args any, method, path string, body any, summary string) (Pending, error) {
	c, ok := callerFrom(ctx)
	if !ok {
		return Pending{}, errors.New("mcp: approval request without an agent")
	}
	if _, err := a.s.reachable(spec, method, path); err != nil {
		return Pending{}, err
	}
	argsJSON, err := toJSON(args)
	if err != nil {
		return Pending{}, fmt.Errorf("mcp: tool %s: encoding arguments: %w", spec.Name, err)
	}
	bodyJSON, err := toJSON(body)
	if err != nil {
		return Pending{}, fmt.Errorf("mcp: tool %s: encoding body: %w", spec.Name, err)
	}
	b, err := randomBytes(12)
	if err != nil {
		return Pending{}, err
	}
	r, err := agentRequest(ctx, c, spec.Name, method, path, nil)
	if err != nil {
		return Pending{}, fmt.Errorf("mcp: tool %s: %w", spec.Name, err)
	}
	// Expired requests stop counting first.
	a.sweep()
	// One lock from the checks to the insert, so two calls at once can
	// neither both pass the caps nor both create the same request. The
	// audit write happens under it too: the request needs its Ref before
	// anyone can see it.
	a.mu.Lock()
	waiting, mine := 0, 0
	for _, e := range a.items {
		if e.p.State != StatePending {
			continue
		}
		if e.p.AgentID == c.agent.ID {
			if e.p.Tool == spec.Name && e.p.Method == method && e.p.Path == path && bytes.Equal(e.p.Body, bodyJSON) {
				p := e.p
				a.mu.Unlock()
				return p, nil
			}
			mine++
		}
		waiting++
	}
	if waiting >= maxPending || mine >= maxAgentPending {
		a.mu.Unlock()
		return Pending{}, httpx.Errorf(http.StatusTooManyRequests, "too_many_approvals",
			"Too many requests are waiting for the owner's approval; try again later.")
	}
	now := a.now()
	p := Pending{
		ID:      base64.RawURLEncoding.EncodeToString(b),
		Agent:   audit.AgentRef{ID: c.agent.ID, Name: c.agent.Name, Tool: spec.Name},
		AgentID: c.agent.ID, Tool: spec.Name, Args: argsJSON,
		Method: method, Path: path, Body: bodyJSON, Summary: summary,
		Created: now, Expires: now.Add(time.Duration(a.s.Settings().ApprovalMinutes) * time.Minute),
		State: StatePending,
	}
	p.Ref = a.s.audit.Write(r, audit.Event{Action: "mcp.approval.requested", Target: summary, Details: map[string]any{
		"id": p.ID, "tool": spec.Name, "method": method, "path": path, "args": argsJSON, "expires": p.Expires,
	}})
	a.items[p.ID] = &approval{p: p, spec: spec}
	a.mu.Unlock()
	if a.s.notify != nil {
		a.s.notify.Raise(context.WithoutCancel(ctx), notify.Occurrence{
			Category: notify.AgentApproval, Key: p.ID, Title: "Agent needs approval",
			Body: c.agent.Name + ": " + summary, Link: "/approve/" + p.ID,
		})
	}
	return p, nil
}

// toJSON encodes v (nil stays nil; a json.RawMessage is copied as is,
// and must be valid JSON).
func toJSON(v any) (json.RawMessage, error) {
	switch b := v.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		if b == nil {
			return nil, nil
		}
		if !json.Valid(b) {
			return nil, errors.New("not valid JSON")
		}
		return bytes.Clone(b), nil
	}
	return json.Marshal(v)
}

// Get returns request id.
func (a *Approvals) Get(id string) (Pending, bool) {
	a.sweep()
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.items[id]
	if !ok {
		return Pending{}, false
	}
	return e.view(), true
}

// view is the request as callers see it.
func (e *approval) view() Pending {
	p := e.p
	p.Running = e.running
	return p
}

// List returns every request still remembered, newest first.
func (a *Approvals) List() []Pending {
	a.sweep()
	a.mu.Lock()
	out := make([]Pending, 0, len(a.items))
	for _, e := range a.items {
		out = append(out, e.view())
	}
	a.mu.Unlock()
	slices.SortFunc(out, func(x, y Pending) int { return y.Created.Compare(x.Created) })
	return out
}

// sweep expires pending requests past their time, writing
// mcp.approval.expired once for each, and forgets requests decided more
// than keepDecided ago.
func (a *Approvals) sweep() {
	now := a.now()
	var expired []Pending
	a.mu.Lock()
	for id, e := range a.items {
		switch {
		case e.p.State == StatePending && !e.running && !now.Before(e.p.Expires):
			e.p.State = StateExpired
			e.decided = now
			expired = append(expired, e.p)
		case e.p.State != StatePending && now.Sub(e.decided) >= keepDecided:
			delete(a.items, id)
		}
	}
	a.mu.Unlock()
	for _, p := range expired {
		a.s.audit.Write(nil, audit.Event{Action: "mcp.approval.expired", Target: p.Summary, Ref: p.Ref, Details: decision(p)})
		a.resolve(p.ID)
	}
}

// decision is the details of an entry about a decided request.
func decision(p Pending) map[string]any {
	return map[string]any{"id": p.ID, "tool": p.Tool, "agent": map[string]string{"id": p.AgentID, "name": p.Agent.Name}}
}

// resolve marks the request's notification as dealt with.
func (a *Approvals) resolve(id string) {
	if a.s.notify != nil {
		a.s.notify.Resolve(context.Background(), notify.AgentApproval, id, "")
	}
}

// decidableLocked is request id if the owner can still approve or deny
// it, or the reason they cannot.
func (a *Approvals) decidableLocked(id string) (*approval, error) {
	e, ok := a.items[id]
	if !ok {
		return nil, errNoRequest
	}
	switch {
	case e.running:
		return nil, httpx.Errorf(http.StatusConflict, "approval_in_progress", "This request is already being approved.")
	case e.p.State == StateApproved:
		return nil, httpx.Errorf(http.StatusConflict, "already_approved", "This request was already approved.")
	case e.p.State == StateDenied:
		return nil, httpx.Errorf(http.StatusConflict, "already_denied", "This request was already denied.")
	case e.p.State == StateExpired || !a.now().Before(e.p.Expires):
		return nil, httpx.Errorf(http.StatusGone, "expired", "This request has expired; the agent can ask again.")
	}
	return e, nil
}

// Deny refuses request id for the owner's request r.
func (a *Approvals) Deny(r *http.Request, id string) (Pending, error) {
	a.sweep()
	a.mu.Lock()
	e, err := a.decidableLocked(id)
	if err != nil {
		a.mu.Unlock()
		return Pending{}, err
	}
	e.p.State = StateDenied
	e.decided = a.now()
	p := e.p
	a.mu.Unlock()
	a.s.audit.Write(r, audit.Event{Action: "mcp.approval.denied", Target: p.Summary, Ref: p.Ref, Details: decision(p)})
	a.resolve(p.ID)
	return p, nil
}

// denyAgent denies every request of agent id still waiting, for the
// owner's request r that revoked it: each gets its mcp.approval.denied
// entry and its notification is resolved. A request already running
// finishes.
func (a *Approvals) denyAgent(r *http.Request, id string) {
	now := a.now()
	var denied []Pending
	a.mu.Lock()
	for _, e := range a.items {
		if e.p.AgentID == id && e.p.State == StatePending && !e.running {
			e.p.State = StateDenied
			e.decided = now
			denied = append(denied, e.p)
		}
	}
	a.mu.Unlock()
	for _, p := range denied {
		details := decision(p)
		details["reason"] = "agent revoked"
		a.s.audit.Write(r, audit.Event{Action: "mcp.approval.denied", Target: p.Summary, Ref: p.Ref, Details: details})
		a.resolve(p.ID)
	}
}

// credentialAnswer says whether a handler's error code means it wants a
// (different) credential and did nothing: the browser's elevation dialog
// asks and sends the approval again, so the request stays pending.
func credentialAnswer(code string) bool {
	return code == "elevation_required" || code == "elevation_failed" || code == "throttled"
}

// Approve runs request id for the owner's request r: the stored method,
// path and body, unchanged, under the agent's identity, with r's
// elevation credential, Origin (passkey challenges are bound to it),
// client address and user agent, and r's session as the approver. The
// handler's own entries link to the request entry (Ref).
//
// If the handler wants a credential (elevation_required, elevation_failed,
// throttled), its answer comes back as is and the request stays pending,
// so the browser can prompt and approve again. Otherwise the request is
// approved, whatever the handler answered, its answer is kept, and
// mcp.approval.approved is written with the elevation method ("none"
// without one).
//
// Before running, MCP must still be on and the agent must still exist at a
// level that allows the tool; otherwise nothing runs, the request stays
// pending (it expires on its own) and the error says why.
func (a *Approvals) Approve(r *http.Request, id string) (*Response, error) {
	sess := auth.FromContext(r.Context())
	if sess == nil {
		return nil, httpx.ErrUnauthorized
	}
	a.sweep()
	a.mu.Lock()
	e, err := a.decidableLocked(id)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	e.running = true
	p, spec := e.p, e.spec
	a.mu.Unlock()
	release := func() {
		a.mu.Lock()
		e.running = false
		a.mu.Unlock()
	}

	c, err := a.replayCaller(r, p, spec)
	if err != nil {
		release()
		return nil, err
	}
	method := elevationMethod(r)
	tag := audit.SessionTag(sess.IDHash)
	prepare := func(rr *http.Request) *http.Request {
		if o := r.Header.Get("Origin"); o != "" {
			rr.Header.Set("Origin", o)
		}
		if h := r.Header.Get(elevate.Header); h != "" {
			rr.Header.Set(elevate.Header, h)
		}
		ctx := audit.WithSession(rr.Context(), tag)
		ctx = audit.WithRef(ctx, p.Ref)
		ctx = audit.WithElevation(ctx, method)
		return rr.WithContext(ctx)
	}
	var body any
	if p.Body != nil {
		body = p.Body
	}
	// Not r's context: once approved, the action runs to its end even if
	// the browser goes away, within a deadline of its own (handlers start
	// their long work as jobs, which this does not bound).
	ctx, cancel := context.WithTimeout(context.Background(), replayTimeout)
	defer cancel()
	resp, err := a.s.dispatch(withCaller(ctx, c), spec, p.Method, p.Path, body, prepare)
	if err == nil && resp.Err != nil && credentialAnswer(resp.Err.Code) {
		release()
		return resp, nil
	}

	a.mu.Lock()
	e.running = false
	e.p.State = StateApproved
	e.decided = a.now()
	if err != nil {
		e.p.Status = errInternal.Status
		e.p.Result, _ = json.Marshal(errInternal)
	} else {
		e.p.Status = resp.Status
		e.p.Result = resultJSON(resp.Body)
		e.p.JobID = jobIDOf(resp.Body)
	}
	p = e.p
	a.mu.Unlock()
	details := decision(p)
	details["status"] = p.Status
	a.s.audit.Write(r.WithContext(audit.WithElevation(r.Context(), method)),
		audit.Event{Action: "mcp.approval.approved", Target: p.Summary, Ref: p.Ref, Details: details})
	a.resolve(p.ID)
	return resp, err
}

// replayCaller is who an approved request runs for: the agent as it is
// now, from the approver's address and browser.
func (a *Approvals) replayCaller(r *http.Request, p Pending, spec ToolSpec) (caller, error) {
	if !a.s.Settings().Enabled {
		return caller{}, httpx.Errorf(http.StatusConflict, "mcp_disabled",
			"MCP is turned off. Turn it on in Settings to run this request.")
	}
	// Not r's cancellation: a browser going away is no reason to report
	// the agent as revoked.
	ag, ok, err := a.s.agents.ByID(context.WithoutCancel(r.Context()), p.AgentID)
	if err != nil {
		return caller{}, httpx.Errorf(http.StatusServiceUnavailable, "unavailable", "Wargo could not look up the agent; try again.")
	}
	if !ok {
		return caller{}, httpx.Errorf(http.StatusConflict, "agent_revoked", "The agent that asked for this has been revoked.")
	}
	if !ag.Level.Allows(spec.Level) {
		return caller{}, httpx.Errorf(http.StatusConflict, "forbidden_level",
			"The agent's access level no longer allows "+spec.Name+".")
	}
	return caller{agent: ag, ip: a.s.proxy.ClientIP(r), ua: r.UserAgent()}, nil
}

// elevationMethod is the method of r's elevation credential, or "none".
func elevationMethod(r *http.Request) string {
	h := r.Header.Get(elevate.Header)
	if h == "" {
		return "none"
	}
	var c struct {
		Method string `json:"method"`
	}
	b, err := base64.StdEncoding.DecodeString(h)
	if err != nil || json.Unmarshal(b, &c) != nil || c.Method == "" {
		return "unknown"
	}
	return c.Method
}

// resultJSON is a handler's response body as JSON (a body that is not
// JSON becomes a string).
func resultJSON(body []byte) json.RawMessage {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	if json.Valid(body) {
		return bytes.Clone(body)
	}
	b, _ := json.Marshal(string(body))
	return b
}

// jobIDOf is the job a handler started, from its {"jobId": ...} answer
// or, for the OS and Homebrew upgrades, the job itself ({"id", "kind", ...}).
func jobIDOf(body []byte) string {
	var v struct {
		JobID string `json:"jobId"`
		ID    string `json:"id"`
		Kind  string `json:"kind"`
	}
	_ = json.Unmarshal(body, &v)
	if v.JobID == "" && v.Kind != "" {
		return v.ID
	}
	return v.JobID
}

// approvalURL is the approval page for request id on the first of
// auth.origins, the address the owner's browsers use, or "" when none is
// configured (an agent's own address is no use on the owner's phone, and
// passkeys only work on their origin).
func (s *Service) approvalURL(id string) string {
	if len(s.origins) == 0 {
		return ""
	}
	return strings.TrimRight(s.origins[0], "/") + "/approve/" + url.PathEscape(id)
}

// pendingOut is what an admin tool returns: its request waits for the
// owner.
type pendingOut struct {
	Approval approvalRef `json:"approval"`
	Message  string      `json:"message"`
}

type approvalRef struct {
	ID      string    `json:"id"`
	State   string    `json:"state"`
	Expires time.Time `json:"expires"`
	URL     string    `json:"url,omitempty"`
}

// pendingResult is how an admin tool answers once it has created p. A
// client that declared URL elicitation gets the SDK's URL elicitation
// required error with the approval link, so it can show the owner the link
// at once; any other client, or any client when there is no link, gets a
// pendingOut. Admin tools declare pendingOut as their output and answer
// through waitForOwner.
func (s *Service) pendingResult(req *sdk.CallToolRequest, p Pending) (pendingOut, error) {
	link := s.approvalURL(p.ID)
	if link != "" && declaresURLElicitation(req) {
		return pendingOut{}, sdk.URLElicitationRequiredError([]*sdk.ElicitParams{{
			Mode: "url", URL: link, ElicitationID: p.ID,
			Message: "Approve in Wargo: " + p.Summary,
		}})
	}
	return pendingOut{Approval: refOf(p, link), Message: waitMessage}, nil
}

// refOf is how a tool names request p to the agent; link is its approval
// page ("" for none).
func refOf(p Pending, link string) approvalRef {
	return approvalRef{ID: p.ID, State: p.State, Expires: p.Expires, URL: link}
}

// waitForOwner is how an admin tool asks: it records the request
// (Approvals.Create) and answers with pendingResult. A request the agent
// already made and that still waits comes back as it is.
func (s *Service) waitForOwner(ctx context.Context, req *sdk.CallToolRequest, spec ToolSpec, args any, method, path string, body any, summary string) (pendingOut, error) {
	p, err := s.approvals.Create(ctx, spec, args, method, path, body, summary)
	if err != nil {
		return pendingOut{}, err
	}
	return s.pendingResult(req, p)
}

// declaresURLElicitation says whether the client behind req declared URL
// elicitation when it initialized (sessions are stateful for this).
func declaresURLElicitation(req *sdk.CallToolRequest) bool {
	caps := req.ClientCapabilities()
	return caps != nil && caps.Elicitation != nil && caps.Elicitation.URL != nil
}

// Run expires requests nobody asks about, so each gets its expired entry.
// Open MCP requests (an agent's event stream) end when ctx does, so they
// do not hold up shutdown.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.runCtx = ctx
	s.mu.Unlock()
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.approvals.sweep()
		}
	}
}

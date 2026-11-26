// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"runtime/debug"
	"strings"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// maxResponse caps a handler's response to a tool.
const maxResponse = 4 << 20

// caller is the agent a tool call is made for, with the client address and
// user agent of its /mcp request.
type caller struct {
	agent  Agent
	ip, ua string
}

type callerKey struct{}

func withCaller(ctx context.Context, c caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerFrom(ctx context.Context) (caller, bool) {
	c, ok := ctx.Value(callerKey{}).(caller)
	return c, ok
}

// Response is a handler's answer to a tool's request. Err is the decoded
// error body when Status is 400 or above.
type Response struct {
	Status int
	Body   json.RawMessage
	Err    *httpx.Error
}

// router is the private API mux: it can tell which pattern a request
// matches before serving it.
type router interface {
	http.Handler
	Handler(r *http.Request) (h http.Handler, pattern string)
}

// call serves method and path (with an optional query) through the private
// API for the caller in ctx, as a browser would send it but without a
// browser session: the request carries the agent's identity (source agent
// in audit, session tag "agent:<id prefix>"), the client address and user
// agent of its /mcp request, and a request ID of its own. body, unless nil,
// is sent as JSON (a json.RawMessage as is). A request that matches none
// of spec.Routes is refused with an error before any handler runs: that is
// a bug in the tool. So is calling it for an admin tool: those requests
// wait for the owner (Approvals.Create) and run only when approved.
func (s *Service) call(ctx context.Context, spec ToolSpec, method, target string, body any) (*Response, error) {
	if spec.Admin {
		return nil, fmt.Errorf("mcp: tool %s is an admin tool; its requests wait for approval", spec.Name)
	}
	return s.dispatch(ctx, spec, method, target, body, nil)
}

// dispatch is call without the admin check. prepare, unless nil, adjusts
// the request just before it is served (an approved request gets the
// approver's details).
func (s *Service) dispatch(ctx context.Context, spec ToolSpec, method, target string, body any, prepare func(*http.Request) *http.Request) (*Response, error) {
	c, ok := callerFrom(ctx)
	if !ok {
		return nil, errors.New("mcp: call without an agent")
	}
	mux, err := s.reachable(spec, method, target)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := toJSON(body)
		if err != nil {
			return nil, fmt.Errorf("mcp: tool %s: encoding body: %w", spec.Name, err)
		}
		rd = bytes.NewReader(b)
	}
	r, err := agentRequest(ctx, c, spec.Name, method, target, rd)
	if err != nil {
		return nil, fmt.Errorf("mcp: tool %s: %w", spec.Name, err)
	}
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if prepare != nil {
		r = prepare(r)
	}
	w := newCapture()
	if err := s.serve(mux, w, r); err != nil {
		return nil, fmt.Errorf("mcp: tool %s: %w", spec.Name, err)
	}
	if w.over {
		return nil, fmt.Errorf("mcp: tool %s: response to %s %s is over %d bytes", spec.Name, method, target, maxResponse)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	resp := &Response{Status: w.status, Body: w.body.Bytes()}
	if w.status >= http.StatusBadRequest {
		resp.Err = decodeError(w.status, resp.Body)
	}
	return resp, nil
}

// reachable checks that method and target reach one of spec's routes on
// the private API, and returns that API.
func (s *Service) reachable(spec ToolSpec, method, target string) (router, error) {
	mux, ok := s.api.(router)
	if !ok {
		return nil, errors.New("mcp: private API not set")
	}
	r, err := http.NewRequest(method, target, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: tool %s: %w", spec.Name, err)
	}
	if !canonical(r) || !declared(spec, method, mux, r) {
		return nil, fmt.Errorf("mcp: tool %s may not call %s %s", spec.Name, method, target)
	}
	return mux, nil
}

// serve runs the handler and recovers its panic like the server's own
// recoverer does: a tool call may run outside the HTTP request's goroutine,
// where nothing else would catch it.
func (s *Service) serve(h http.Handler, w http.ResponseWriter, r *http.Request) (err error) {
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				err = errors.New("handler aborted")
				return
			}
			err = s.panicked(r, v)
		}
	}()
	h.ServeHTTP(w, r)
	return nil
}

// panicked logs a recovered panic with its stack and audits it as
// server.panic with r's origin (the agent), as the server's recoverer
// does. Call it from the deferred function that recovered v.
func (s *Service) panicked(r *http.Request, v any) error {
	slog.Error("panic", "path", r.URL.Path, "request", httpx.RequestID(r.Context()), "err", v, "stack", string(debug.Stack()))
	err := fmt.Errorf("panic: %v", v)
	s.audit.Write(r, audit.Event{Action: "server.panic", Target: r.Method + " " + r.URL.Path, Err: err, Code: "panic"})
	return err
}

// agentRequest is a request made for caller c through tool: the agent's
// identity (source agent in audit, session tag "agent:<id prefix>"), its
// client address and user agent, and a request ID of its own.
func agentRequest(ctx context.Context, c caller, tool, method, target string, body io.Reader) (*http.Request, error) {
	id := c.agent.ID
	ctx = audit.WithAgent(ctx, audit.AgentRef{ID: id, Name: c.agent.Name, Tool: tool})
	ctx = audit.WithSession(ctx, "agent:"+id[:min(8, len(id))])
	ctx = httpx.WithRequestID(ctx, httpx.NewRequestID())
	r, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	// Only the address: forwarding headers are never copied, so the
	// handler's ProxyTrust sees the agent's address as the peer.
	r.RemoteAddr = net.JoinHostPort(c.ip, "0")
	if c.ua != "" {
		r.Header.Set("User-Agent", c.ua)
	}
	return r, nil
}

// canonical refuses a path the mux would redirect (dot segments, double
// slashes) and anything but a plain path.
func canonical(r *http.Request) bool {
	p := r.URL.EscapedPath()
	if r.URL.Scheme != "" || r.URL.Host != "" || !strings.HasPrefix(p, "/api/") {
		return false
	}
	clean := path.Clean(p)
	if strings.HasSuffix(p, "/") {
		clean += "/"
	}
	return clean == p
}

// declared says whether r reaches one of spec's routes on mux: the pattern
// the mux picks, not one the path merely fits, so a more specific route
// registered beside a declared one stays out of reach.
func declared(spec ToolSpec, method string, mux router, r *http.Request) bool {
	_, pattern := mux.Handler(r)
	for _, rt := range spec.Routes {
		if rt.Method == method && rt.Pattern == pattern {
			return true
		}
	}
	return false
}

// decodeError reads an httpx error body, or makes one from the status.
func decodeError(status int, body []byte) *httpx.Error {
	var e httpx.Error
	if json.Unmarshal(body, &e) != nil || e.Code == "" {
		return httpx.Errorf(status, "http_error", http.StatusText(status))
	}
	e.Status = status
	return &e
}

// capture is an in-memory ResponseWriter that keeps at most maxResponse
// bytes of body.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
	over   bool
}

func newCapture() *capture { return &capture{header: http.Header{}} }

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *capture) Write(p []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	if room := maxResponse - c.body.Len(); len(p) > room {
		c.body.Write(p[:room])
		c.over = true
		return len(p), nil
	}
	return c.body.Write(p)
}

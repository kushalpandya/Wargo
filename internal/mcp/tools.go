// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kushalpandya/wargo/internal/docker"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// route is one private API route a tool may call: its method and its
// pattern exactly as registered on the mux, e.g.
// {"POST", "POST /api/containers/{id}/{action}"}.
type route struct{ Method, Pattern string }

// ToolSpec describes a tool: who may see and call it, and the only routes
// it can reach.
type ToolSpec struct {
	Name, Description string
	// Level is the access level an agent needs to see and call the tool.
	Level Level
	// Admin tools wait for the owner's approval before calling a route.
	Admin bool
	// The tool's annotations.
	ReadOnly, Destructive, Idempotent bool
	// Needs is a capability key ("plex"); the tool is hidden and refused
	// while that capability is not available. "" if none.
	Needs  string
	Routes []route
}

// tool is a registered tool and how to add it to a server.
type tool struct {
	spec ToolSpec
	add  func(srv *sdk.Server)
}

// addTool registers a tool. It is a function, not a method, because the
// handler is generic. Register tools before serving starts. Each MCP server
// built for an agent gets only the tools its level and the host's
// capabilities allow, and every call checks both again before h runs (a
// session keeps the tool list it opened with). h finds its caller in ctx
// for call. A ReadOnly tool's admitted calls are audited as mcp.read,
// folded per agent and tool, and refused calls as mcp.refused, folded the
// same way. An *httpx.Error from h becomes an error result with its code;
// any other error is logged and reported as "internal".
func addTool[In, Out any](s *Service, spec ToolSpec, h sdk.ToolHandlerFor[In, Out]) {
	for _, rt := range spec.Routes {
		// Subtree patterns ("/api/x/") would allow every path beneath.
		if !strings.HasPrefix(rt.Pattern, rt.Method+" /api/") || strings.HasSuffix(rt.Pattern, "/") {
			panic(fmt.Sprintf("mcp: tool %s: invalid route %q", spec.Name, rt.Pattern))
		}
	}
	destructive := spec.Destructive
	t := &sdk.Tool{
		Name: spec.Name, Description: spec.Description,
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint: spec.ReadOnly, DestructiveHint: &destructive, IdempotentHint: spec.Idempotent,
		},
	}
	guarded := func(ctx context.Context, req *sdk.CallToolRequest, in In) (res *sdk.CallToolResult, out Out, err error) {
		// Tool calls run on the MCP connection's goroutine, out of the
		// server recoverer's reach, so a panic here would stop Wargo.
		defer func() {
			if v := recover(); v != nil {
				c, _ := callerOf(req)
				if r, rerr := agentRequest(ctx, c, spec.Name, http.MethodPost, Path, nil); rerr == nil {
					s.panicked(r, v)
				}
				var zero Out
				res, out, err = nil, zero, resultError{errInternal}
			}
		}()
		var zero Out
		ctx, err = s.admit(ctx, spec, req)
		if err != nil {
			var e *httpx.Error
			if c, ok := callerOf(req); ok && errors.As(err, &e) {
				if r, rerr := agentRequest(ctx, c, spec.Name, http.MethodPost, Path, nil); rerr == nil {
					s.auditRefused(r, spec.Name, e)
				}
			}
			return nil, zero, toolFailure(spec, err)
		}
		if spec.ReadOnly {
			s.auditRead(ctx, spec)
		}
		res, out, err = h(ctx, req, in)
		if err != nil {
			return nil, zero, toolFailure(spec, err)
		}
		return res, out, nil
	}
	s.tools = append(s.tools, tool{spec: spec, add: func(srv *sdk.Server) { sdk.AddTool(srv, t, guarded) }})
}

// visible says whether ag's server lists the tool.
func (s *Service) visible(spec ToolSpec, ag Agent) bool {
	return ag.Level.Allows(spec.Level) && (spec.Needs == "" || s.capable(spec.Needs))
}

// admit checks the agent behind req against spec on every call, and puts
// the caller in ctx. The agent comes from this very HTTP request, which was
// authenticated afresh, so its level is current.
func (s *Service) admit(ctx context.Context, spec ToolSpec, req *sdk.CallToolRequest) (context.Context, error) {
	c, ok := callerOf(req)
	if !ok {
		return ctx, httpx.ErrUnauthorized
	}
	if ag := c.agent; !ag.Level.Allows(spec.Level) {
		return ctx, httpx.Errorf(http.StatusForbidden, "forbidden_level",
			"This agent's access level does not allow "+spec.Name+". The owner can change it in Wargo's Settings.")
	}
	if spec.Needs != "" && !s.capable(spec.Needs) {
		return ctx, unavailable(spec.Name)
	}
	return withCaller(ctx, c), nil
}

// callerOf is the agent behind req with the client address and user agent
// of the HTTP request that carried it.
func callerOf(req *sdk.CallToolRequest) (caller, bool) {
	ag, ok := agentFrom(req)
	c := caller{agent: ag}
	if ex := req.GetExtra(); ex != nil {
		if ex.TokenInfo != nil {
			c.ip, _ = ex.TokenInfo.Extra[clientIPKey].(string)
		}
		c.ua = ex.Header.Get("User-Agent")
	}
	return c, ok
}

// resultError is an error result's text: the error as JSON, like the API's.
type resultError struct{ e *httpx.Error }

func (r resultError) Error() string {
	b, _ := json.Marshal(r.e)
	return string(b)
}

// toolFailure turns err into what the SDK sends back. Protocol errors
// (such as URL elicitation required) pass through, unwrapped, because the
// SDK only recognizes one by its type. Either kind may arrive wrapped.
func toolFailure(spec ToolSpec, err error) error {
	var je *jsonrpc.Error
	if errors.As(err, &je) {
		return je
	}
	return resultError{apiError(spec, err)}
}

// apiError is err as the agent sees it: an *httpx.Error as itself, any
// other error logged and reported as internal.
func apiError(spec ToolSpec, err error) *httpx.Error {
	var e *httpx.Error
	if !errors.As(err, &e) {
		slog.Error("mcp tool failed", "tool", spec.Name, "err", err)
		e = errInternal
	}
	return e
}

var errInternal = httpx.Errorf(http.StatusInternalServerError, "internal", "Internal error; Wargo's log has the details.")

// capsTTL is how long the capabilities answer is reused.
const capsTTL = 15 * time.Second

type capsCache struct {
	at    time.Time
	avail map[string]bool
}

// capable says whether capability key reports {available: true}, from
// GET /api/capabilities served through the private API. Only the first
// answer is waited for: after that, an expired answer is still used while
// one background refresh replaces it, so no agent request waits on it.
func (s *Service) capable(key string) bool {
	s.capsMu.Lock()
	c := s.caps
	if c != nil {
		if time.Since(c.at) >= capsTTL && !s.capsRefreshing {
			s.capsRefreshing = true
			go s.refreshCapabilities()
		}
		s.capsMu.Unlock()
		return c.avail[key]
	}
	s.capsMu.Unlock()
	if c = s.loadCapabilities(); c == nil {
		return false
	}
	s.capsMu.Lock()
	s.caps = c
	s.capsMu.Unlock()
	return c.avail[key]
}

// refreshCapabilities replaces the cached answer; a failed load keeps the
// old one.
func (s *Service) refreshCapabilities() {
	c := s.loadCapabilities()
	s.capsMu.Lock()
	if c != nil {
		s.caps = c
	}
	s.capsRefreshing = false
	s.capsMu.Unlock()
}

func (s *Service) loadCapabilities() *capsCache {
	if s.api == nil {
		return nil
	}
	// Not the agent's request context: the answer is shared.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(httpx.WithRequestID(ctx, httpx.NewRequestID()), http.MethodGet, "/api/capabilities", nil)
	if err != nil {
		return nil
	}
	w := newCapture()
	if err := s.serve(s.api, w, r); err != nil {
		return nil
	}
	var all map[string]json.RawMessage
	if w.status != http.StatusOK || w.over || json.Unmarshal(w.body.Bytes(), &all) != nil {
		slog.Warn("mcp: capabilities unavailable", "status", w.status)
		return nil
	}
	c := &capsCache{at: time.Now(), avail: map[string]bool{}}
	for k, v := range all {
		var cap struct {
			Available bool `json:"available"`
		}
		if json.Unmarshal(v, &cap) == nil && cap.Available {
			c.avail[k] = true
		}
	}
	return c
}

// maxUntrusted caps a third-party text field, in characters.
const maxUntrusted = 512

// untrusted prepares third-party text (names, log lines, command lines)
// for an agent: trimmed and capped, its meaning untouched.
func untrusted(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxUntrusted {
		return s
	}
	return string([]rune(s)[:maxUntrusted])
}

func invalidArg(msg string) *httpx.Error {
	return httpx.Errorf(http.StatusBadRequest, "invalid_argument", msg)
}

// get serves GET target through spec's routes and decodes the answer into
// out. A route the host does not register (a feature missing on this OS)
// is reported as unavailable, a handler's error as itself.
func (s *Service) get(ctx context.Context, spec ToolSpec, target string, out any) error {
	if !s.routed(spec, http.MethodGet, target) {
		return httpx.Errorf(http.StatusServiceUnavailable, "unavailable", spec.Name+" is not available on this host.")
	}
	body, err := s.send(ctx, spec, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("mcp: tool %s: decoding %s: %w", spec.Name, target, err)
	}
	return nil
}

// routed says whether method and target reach one of spec's routes.
func (s *Service) routed(spec ToolSpec, method, target string) bool {
	_, err := s.reachable(spec, method, target)
	return err == nil
}

// limit is n capped at most, or def when n is not positive.
func limit(n, def, most int) int {
	if n <= 0 {
		return def
	}
	return min(n, most)
}

// idRe accepts the ids Wargo makes (jobs, approval requests).
var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// containerRef refuses id unless it is a container id, id prefix or name
// (the docker feature's own rule).
func containerRef(id string) error {
	if !docker.ValidRef(id) {
		return invalidArg("id must be a container name or id")
	}
	return nil
}

// unavailable says a feature is not available on this host right now.
func unavailable(what string) *httpx.Error {
	return httpx.Errorf(http.StatusServiceUnavailable, "unavailable", what+" is not available on this host right now.")
}

func unixTime(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).Format(time.RFC3339)
}

func unixMilli(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format(time.RFC3339)
}

func timeOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.RFC3339)
}

func timePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return timeOf(*t)
}

// none is the input of a tool without arguments.
type none struct{}

// send serves method and target through spec's routes and returns the
// handler's answer, or its error.
func (s *Service) send(ctx context.Context, spec ToolSpec, method, target string, body any) (json.RawMessage, error) {
	resp, err := s.call(ctx, spec, method, target, body)
	if err != nil {
		return nil, err
	}
	if resp.Err != nil {
		return nil, resp.Err
	}
	return resp.Body, nil
}

func isElevationRequired(err error) bool {
	var e *httpx.Error
	return errors.As(err, &e) && e.Code == "elevation_required"
}

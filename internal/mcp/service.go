// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"net"
	"net/http"
	"os"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/notify"
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/internal/store"
)

// Service owns the agents, the settings and the /mcp endpoint.
type Service struct {
	st        *store.Store
	audit     *audit.Log
	agents    *Agents
	approvals *Approvals
	notify    *notify.Center
	// origins is auth.origins: the first one is where approval links point.
	origins []string

	proxy *httpx.ProxyTrust
	auth  *auth.Service
	// throttle counts wrong agent tokens, apart from browser sign-in.
	throttle     *auth.Throttle
	extraNets    []*net.IPNet
	instructions string
	handler      http.Handler
	// api is the private API mux, set once before serving starts.
	api http.Handler
	// tools are registered before serving starts (addTool); schemas
	// keeps their JSON schemas across the servers built per request.
	tools   []tool
	schemas *sdk.SchemaCache

	capsMu sync.Mutex
	caps   *capsCache
	// capsRefreshing is set while a background refresh runs.
	capsRefreshing bool

	mu       sync.Mutex
	settings Settings
	// runCtx is the context Run received: it ends when Wargo shuts down.
	runCtx context.Context
}

func New(d *server.Deps) (*Service, error) {
	s := &Service{
		st: d.Store, audit: d.Audit, agents: NewAgents(d.Store),
		proxy: d.Proxy, auth: d.Auth, throttle: auth.NewThrottle(),
		schemas: sdk.NewSchemaCache(), notify: d.Notify, origins: d.Cfg.Auth.Origins,
	}
	s.approvals = newApprovals(s)
	if s.notify != nil {
		// Requests live in memory, so none survived a restart: their
		// notifications are dealt with too.
		s.notify.Reconcile(context.Background(), notify.AgentApproval, nil,
			func(string) string { return "Agent request dropped when Wargo restarted" })
	}
	if s.proxy == nil {
		s.proxy = httpx.NewProxyTrust(d.Cfg.Auth.TrustedProxies)
	}
	// Config validation already refused anything that isn't a CIDR.
	for _, c := range d.Cfg.MCP.AllowedNetworks {
		if _, n, err := net.ParseCIDR(c); err == nil {
			s.extraNets = append(s.extraNets, n)
		}
	}
	// In container mode Wargo shares the host's UTS namespace, so this is
	// the host's name either way.
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "this machine"
	}
	s.instructions = instructions(hostname)
	s.handler = s.gate(s.requireAgent(sdk.NewStreamableHTTPHandler(s.serverFor, streamOptions())))

	cur, err := LoadSettings(context.Background(), d.Store)
	if err != nil {
		return nil, err
	}
	s.settings = cur
	s.addReadTools()
	s.addActTools()
	return s, nil
}

// Settings returns the current settings.
func (s *Service) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// Capabilities tells the UI whether MCP is on.
func (s *Service) Capabilities(ctx context.Context) (string, any) {
	return "mcp", map[string]any{"enabled": s.Settings().Enabled}
}

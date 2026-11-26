// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"net/http"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Register adds the browser API. An agent must not manage agents or
// approve its own requests, so every handler refuses a request made on
// behalf of one.
func (s *Service) Register(mux *http.ServeMux) {
	h := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
			if _, ok := audit.AgentFrom(r.Context()); ok {
				return httpx.ErrForbidden
			}
			return fn(w, r)
		})
	}
	mux.HandleFunc("GET /api/mcp/settings", h(s.handleGetSettings))
	mux.HandleFunc("PUT /api/mcp/settings", h(s.handlePutSettings))
	mux.HandleFunc("GET /api/mcp/agents", h(s.handleListAgents))
	mux.HandleFunc("POST /api/mcp/agents", h(s.handleCreateAgent))
	mux.HandleFunc("PATCH /api/mcp/agents/{id}", h(s.handleUpdateAgent))
	mux.HandleFunc("DELETE /api/mcp/agents/{id}", h(s.handleRevokeAgent))
	s.registerApprovals(mux, h)
}

func (s *Service) handleGetSettings(w http.ResponseWriter, r *http.Request) error {
	httpx.OK(w, s.Settings())
	return nil
}

func (s *Service) handlePutSettings(w http.ResponseWriter, r *http.Request) error {
	var in Settings
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	s.mu.Lock()
	err := SaveSettings(r.Context(), s.st, in)
	if err == nil {
		s.settings = in
	}
	s.mu.Unlock()
	s.audit.Record(r, "mcp.settings", "", err, in)
	if err != nil {
		return err
	}
	httpx.OK(w, in)
	return nil
}

func (s *Service) handleListAgents(w http.ResponseWriter, r *http.Request) error {
	list, err := s.agents.List(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, list)
	return nil
}

func (s *Service) handleCreateAgent(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name  string `json:"name"`
		Level Level  `json:"level"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	ag, token, err := s.agents.Create(r.Context(), in.Name, in.Level)
	target := in.Name
	if err == nil {
		target = ag.Name
	}
	s.audit.Record(r, "mcp.agent.create", target, err, map[string]any{"id": ag.ID, "level": in.Level})
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"agent": ag, "token": token})
	return nil
}

func (s *Service) handleUpdateAgent(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var in struct {
		Name  *string `json:"name"`
		Level *Level  `json:"level"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.Name == nil && in.Level == nil {
		return httpx.BadRequest("Nothing to change")
	}
	details := map[string]any{"id": id}
	if in.Name != nil {
		details["name"] = *in.Name
	}
	if in.Level != nil {
		details["level"] = *in.Level
	}
	target, err := s.agentName(r, id)
	if err != nil {
		return err
	}
	err = s.agents.Update(r.Context(), id, in.Name, in.Level)
	s.audit.Record(r, "mcp.agent.update", target, err, details)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{})
	return nil
}

// handleRevokeAgent deletes the agent and denies its requests still
// waiting for the owner.
func (s *Service) handleRevokeAgent(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	target, err := s.agentName(r, id)
	if err != nil {
		return err
	}
	err = s.agents.Revoke(r.Context(), id)
	s.audit.Record(r, "mcp.agent.revoke", target, err, map[string]any{"id": id})
	if err != nil {
		return err
	}
	s.approvals.denyAgent(r, id)
	httpx.OK(w, map[string]any{})
	return nil
}

// agentName is the name of agent id for its audit entries, or the id
// itself when there is no such agent (the change then fails as not found).
func (s *Service) agentName(r *http.Request, id string) (string, error) {
	ag, ok, err := s.agents.ByID(r.Context(), id)
	if err != nil || !ok {
		return id, err
	}
	return ag.Name, nil
}

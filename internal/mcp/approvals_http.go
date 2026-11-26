// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"net/http"

	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// registerApprovals adds the approval page's API. wrap refuses requests
// made for an agent; on top of it, every handler needs a browser session
// (the private API is already behind one; this keeps it so if that ever
// changes).
func (s *Service) registerApprovals(mux *http.ServeMux, wrap func(func(http.ResponseWriter, *http.Request) error) http.HandlerFunc) {
	h := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return wrap(func(w http.ResponseWriter, r *http.Request) error {
			if auth.FromContext(r.Context()) == nil {
				return httpx.ErrUnauthorized
			}
			return fn(w, r)
		})
	}
	mux.HandleFunc("GET /api/mcp/approvals", h(s.handleListApprovals))
	mux.HandleFunc("GET /api/mcp/approvals/{id}", h(s.handleGetApproval))
	mux.HandleFunc("POST /api/mcp/approvals/{id}/approve", h(s.handleApprove))
	mux.HandleFunc("POST /api/mcp/approvals/{id}/deny", h(s.handleDeny))
}

func (s *Service) handleListApprovals(w http.ResponseWriter, r *http.Request) error {
	httpx.OK(w, s.approvals.List())
	return nil
}

func (s *Service) handleGetApproval(w http.ResponseWriter, r *http.Request) error {
	p, ok := s.approvals.Get(r.PathValue("id"))
	if !ok {
		return errNoRequest
	}
	httpx.OK(w, p)
	return nil
}

// handleApprove answers with the request as it now is, or with the
// handler's own error when it wants a credential (elevation_required: the
// browser prompts and posts again with it).
func (s *Service) handleApprove(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	resp, err := s.approvals.Approve(r, id)
	if err != nil {
		return err
	}
	if resp.Err != nil && credentialAnswer(resp.Err.Code) {
		return resp.Err
	}
	return s.handleGetApproval(w, r)
}

func (s *Service) handleDeny(w http.ResponseWriter, r *http.Request) error {
	p, err := s.approvals.Deny(r, r.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.OK(w, p)
	return nil
}

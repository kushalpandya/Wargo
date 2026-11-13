// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"net/http"

	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Register adds the notification routes.
func (c *Center) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/notifications", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		s, err := c.Snapshot(r.Context())
		if err != nil {
			return err
		}
		httpx.OK(w, s)
		return nil
	}))
	mux.HandleFunc("POST /api/notifications/clear", httpx.Handler(c.handleClear))
	mux.HandleFunc("GET /api/notifications/settings", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, c.Settings())
	})
	mux.HandleFunc("PUT /api/notifications/settings", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		var m map[Category]Mode
		if err := httpx.Decode(r, &m); err != nil {
			return err
		}
		if err := c.SetModes(r.Context(), m); err != nil {
			return err
		}
		httpx.OK(w, c.Settings())
		return nil
	}))
	mux.HandleFunc("GET /api/notifications/push", httpx.Handler(c.handlePush))
	mux.HandleFunc("POST /api/notifications/push", httpx.Handler(c.handlePush))
	mux.HandleFunc("DELETE /api/notifications/push", httpx.Handler(c.handlePush))
}

// handlePush reports (GET), registers (POST, a PushSubscription) or removes
// (DELETE) this session's push registration, and answers with the VAPID
// public key and whether the session is registered.
func (c *Center) handlePush(w http.ResponseWriter, r *http.Request) error {
	sess := auth.FromContext(r.Context())
	if sess == nil {
		return httpx.ErrUnauthorized
	}
	switch r.Method {
	case http.MethodPost:
		var s pushSubscription
		if err := httpx.Decode(r, &s); err != nil {
			return err
		}
		if err := c.subscribe(r.Context(), sess.IDHash, s); err != nil {
			return err
		}
	case http.MethodDelete:
		if err := c.unsubscribe(r.Context(), sess.IDHash); err != nil {
			return err
		}
	}
	key, err := c.publicKey()
	if err != nil {
		return err
	}
	on, err := c.subscribed(r.Context(), sess.IDHash)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"publicKey": key, "subscribed": on})
	return nil
}

// handleClear takes {"id": "..."} to clear one Recent item or {"all": true}
// to clear them all, and answers with the new Snapshot.
func (c *Center) handleClear(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		ID  string `json:"id"`
		All bool   `json:"all"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	var err error
	switch {
	case req.All:
		err = c.ClearAll(r.Context())
	case req.ID != "":
		err = c.Clear(r.Context(), req.ID)
	default:
		return httpx.BadRequest("id or all is required")
	}
	if err != nil {
		return err
	}
	snap, err := c.Snapshot(r.Context())
	if err != nil {
		return err
	}
	httpx.OK(w, snap)
	return nil
}

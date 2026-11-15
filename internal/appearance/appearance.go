// SPDX-License-Identifier: AGPL-3.0-only

// Package appearance keeps the look settings that follow the user to every
// browser and device (the accent colour). The colour mode stays per device,
// in the browser.
package appearance

import (
	"context"
	"net/http"
	"slices"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

const settingsKey = "appearance"

// Accents are the accent colours the UI offers (web/src/lib/theme.ts).
var Accents = []string{"periwinkle", "lilac", "sky", "mint", "rose", "peach", "mono"}

// Settings is what GET and PUT /api/appearance carry.
type Settings struct {
	Accent string `json:"accent"`
}

// Service serves the appearance settings.
type Service struct {
	st    *store.Store
	audit *audit.Log
}

// New creates the service.
func New(st *store.Store, al *audit.Log) *Service {
	return &Service{st: st, audit: al}
}

func (s *Service) settings(ctx context.Context) Settings {
	var v Settings
	s.st.Get(ctx, settingsKey, &v)
	if !slices.Contains(Accents, v.Accent) {
		v.Accent = "periwinkle"
	}
	return v
}

// Register mounts the API.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/appearance", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, s.settings(r.Context()))
		return nil
	}))
	mux.HandleFunc("PUT /api/appearance", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		var v Settings
		if err := httpx.Decode(r, &v); err != nil {
			return err
		}
		if !slices.Contains(Accents, v.Accent) {
			return httpx.BadRequest("unknown accent colour")
		}
		err := s.st.Set(r.Context(), settingsKey, v)
		s.audit.Record(r, "appearance.settings", v.Accent, err, nil)
		if err != nil {
			return err
		}
		httpx.OK(w, v)
		return nil
	}))
}

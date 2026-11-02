// SPDX-License-Identifier: AGPL-3.0-only

// Package launcher implements the optional app launcher: user-defined
// tiles with locally cached icons, background health checks and
// suggestions from running containers.
package launcher

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/dashicons"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

const settingsKey = "launcher"

// Settings controls whether and where the launcher is shown.
type Settings struct {
	Enabled   bool   `json:"enabled"`
	Placement string `json:"placement"` // "overview" | "page"
}

// Tile is one launcher entry.
type Tile struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	URL       string  `json:"url"`
	Icon      string  `json:"icon,omitempty"`
	Group     string  `json:"group,omitempty"`
	Sort      int     `json:"sort"`
	NewTab    bool    `json:"newTab"`
	HealthURL string  `json:"healthUrl,omitempty"`
	Enabled   bool    `json:"enabled"`
	Status    *string `json:"status"`
}

// Service is the launcher feature.
type Service struct {
	st         *store.Store
	audit      *audit.Log
	iconDir    string
	dockerSock string

	health *healthChecker

	// iconBase is the CDN prefix for dashboard-icons (overridable in tests).
	iconBase   string
	iconClient *http.Client

	mu sync.Mutex
}

// New creates the launcher service. dataDir holds the icon cache.
func New(st *store.Store, al *audit.Log, dataDir, dockerSock string) *Service {
	if dockerSock == "" {
		dockerSock = "/var/run/docker.sock"
	}
	return &Service{
		st: st, audit: al, iconDir: dataDir + "/icons", dockerSock: dockerSock,
		health:     newHealthChecker(),
		iconBase:   dashicons.Base,
		iconClient: newIconClient(),
	}
}

func (s *Service) settings(ctx context.Context) Settings {
	v := Settings{Placement: "overview"}
	if ok, err := s.st.Get(ctx, settingsKey, &v); !ok || err != nil {
		return Settings{Placement: "overview"}
	}
	if v.Placement != "page" {
		v.Placement = "overview"
	}
	return v
}

// Capabilities implements server.Capable.
func (s *Service) Capabilities(ctx context.Context) (string, any) {
	return "launcher", s.settings(ctx)
}

// Register mounts the launcher API.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/launcher", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, s.settings(r.Context()))
		return nil
	}))
	mux.HandleFunc("PUT /api/launcher", httpx.Handler(s.handlePutSettings))
	mux.HandleFunc("GET /api/launcher/tiles", httpx.Handler(s.handleList))
	mux.HandleFunc("POST /api/launcher/tiles", httpx.Handler(s.handleCreate))
	mux.HandleFunc("POST /api/launcher/tiles/reorder", httpx.Handler(s.handleReorder))
	mux.HandleFunc("PUT /api/launcher/tiles/{id}", httpx.Handler(s.handleUpdate))
	mux.HandleFunc("DELETE /api/launcher/tiles/{id}", httpx.Handler(s.handleDelete))
	mux.HandleFunc("POST /api/launcher/icons", httpx.Handler(s.handleIconUpload))
	mux.HandleFunc("POST /api/launcher/icons/fetch", httpx.Handler(s.handleIconFetch))
	mux.HandleFunc("GET /api/launcher/icons/{name}", httpx.Handler(s.handleIconGet))
	mux.HandleFunc("GET /api/launcher/suggest", httpx.Handler(s.handleSuggest))
}

func (s *Service) handlePutSettings(w http.ResponseWriter, r *http.Request) error {
	var v Settings
	if err := httpx.Decode(r, &v); err != nil {
		return err
	}
	if v.Placement != "page" && v.Placement != "overview" {
		return httpx.BadRequest("placement must be \"overview\" or \"page\"")
	}
	err := s.st.Set(r.Context(), settingsKey, v)
	s.audit.Record(r, "launcher.settings", v.Placement, err, map[string]any{"enabled": v.Enabled})
	if err != nil {
		return err
	}
	if v.Enabled {
		s.health.kick()
	}
	httpx.OK(w, v)
	return nil
}

// ---- tiles ----

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// validURL accepts absolute http(s) URLs only.
func validURL(raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", errors.New("URL is required")
		}
		return "", nil
	}
	if len(raw) > 2048 {
		return "", errors.New("URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("URL must start with http:// or https://")
	}
	return u.String(), nil
}

type tileInput struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Icon      string `json:"icon"`
	Group     string `json:"group"`
	NewTab    *bool  `json:"newTab"`
	HealthURL string `json:"healthUrl"`
	Enabled   *bool  `json:"enabled"`
}

func (in tileInput) validate() (Tile, error) {
	var t Tile
	t.Name = strings.TrimSpace(in.Name)
	if t.Name == "" || len(t.Name) > 64 {
		return t, httpx.BadRequest("name is required (max 64 characters)")
	}
	var err error
	if t.URL, err = validURL(in.URL, true); err != nil {
		return t, httpx.BadRequest(err.Error())
	}
	if t.HealthURL, err = validURL(in.HealthURL, false); err != nil {
		return t, httpx.BadRequest("health " + err.Error())
	}
	t.Group = strings.TrimSpace(in.Group)
	if len(t.Group) > 48 {
		return t, httpx.BadRequest("group is too long")
	}
	t.Icon = strings.TrimSpace(in.Icon)
	if t.Icon != "" && !validIconName(t.Icon) {
		return t, httpx.BadRequest("invalid icon")
	}
	t.NewTab = in.NewTab == nil || *in.NewTab
	t.Enabled = in.Enabled == nil || *in.Enabled
	return t, nil
}

func (s *Service) list(ctx context.Context) ([]Tile, error) {
	rows, err := s.st.DB.QueryContext(ctx, `SELECT id, name, url, COALESCE(icon,''), COALESCE(group_name,''), sort,
		new_tab, COALESCE(health_url,''), enabled FROM launcher_tiles ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tile{}
	for rows.Next() {
		var t Tile
		var nt, en int
		if err := rows.Scan(&t.ID, &t.Name, &t.URL, &t.Icon, &t.Group, &t.Sort, &nt, &t.HealthURL, &en); err != nil {
			return nil, err
		}
		t.NewTab, t.Enabled = nt != 0, en != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) error {
	tiles, err := s.list(r.Context())
	if err != nil {
		return err
	}
	for i := range tiles {
		if tiles[i].HealthURL != "" {
			tiles[i].Status = s.health.status(tiles[i].ID, tiles[i].HealthURL)
		}
	}
	httpx.OK(w, tiles)
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Service) handleCreate(w http.ResponseWriter, r *http.Request) error {
	var in tileInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t, err := in.validate()
	if err != nil {
		return err
	}
	var n int
	s.st.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM launcher_tiles`).Scan(&n)
	if n >= 500 {
		return httpx.BadRequest("too many tiles")
	}
	t.ID = newID()
	s.mu.Lock()
	var maxSort sql.NullInt64
	s.st.DB.QueryRowContext(r.Context(), `SELECT MAX(sort) FROM launcher_tiles`).Scan(&maxSort)
	t.Sort = int(maxSort.Int64) + 1
	_, err = s.st.DB.ExecContext(r.Context(), `INSERT INTO launcher_tiles(id, name, url, icon, group_name, sort, new_tab, health_url, enabled)
		VALUES(?,?,?,?,?,?,?,?,?)`, t.ID, t.Name, t.URL, t.Icon, t.Group, t.Sort, b2i(t.NewTab), t.HealthURL, b2i(t.Enabled))
	s.mu.Unlock()
	s.audit.Record(r, "launcher.tile_create", t.Name, err, map[string]any{"url": t.URL})
	if err != nil {
		return err
	}
	// Adding the first app to a launcher that was never configured turns
	// it on (shown on Home); explicit choices, including "hidden", stay.
	if n == 0 {
		var cur Settings
		if ok, _ := s.st.Get(r.Context(), settingsKey, &cur); !ok {
			s.st.Set(r.Context(), settingsKey, Settings{Enabled: true, Placement: "overview"})
		}
	}
	if t.HealthURL != "" {
		s.health.kick()
	}
	httpx.OK(w, t)
	return nil
}

func (s *Service) handleUpdate(w http.ResponseWriter, r *http.Request) error {
	var in tileInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	t, err := in.validate()
	if err != nil {
		return err
	}
	t.ID = r.PathValue("id")
	res, err := s.st.DB.ExecContext(r.Context(), `UPDATE launcher_tiles SET name=?, url=?, icon=?, group_name=?, new_tab=?,
		health_url=?, enabled=? WHERE id=?`, t.Name, t.URL, t.Icon, t.Group, b2i(t.NewTab), t.HealthURL, b2i(t.Enabled), t.ID)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 0 {
			return httpx.ErrNotFound
		}
	}
	s.audit.Record(r, "launcher.tile_update", t.Name, err, map[string]any{"url": t.URL})
	if err != nil {
		return err
	}
	s.st.DB.QueryRowContext(r.Context(), `SELECT sort FROM launcher_tiles WHERE id=?`, t.ID).Scan(&t.Sort)
	if t.HealthURL != "" {
		s.health.kick()
	}
	httpx.OK(w, t)
	return nil
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var name string
	if err := s.st.DB.QueryRowContext(r.Context(), `SELECT name FROM launcher_tiles WHERE id=?`, id).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return httpx.ErrNotFound
		}
		return err
	}
	_, err := s.st.DB.ExecContext(r.Context(), `DELETE FROM launcher_tiles WHERE id=?`, id)
	s.audit.Record(r, "launcher.tile_delete", name, err, nil)
	if err != nil {
		return err
	}
	s.health.forget(id)
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

func (s *Service) handleReorder(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if len(req.IDs) > 1000 {
		return httpx.BadRequest("too many ids")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.st.DB.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range req.IDs {
		if _, err := tx.Exec(`UPDATE launcher_tiles SET sort=? WHERE id=?`, i, id); err != nil {
			return err
		}
	}
	err = tx.Commit()
	s.audit.Record(r, "launcher.tile_reorder", "", err, nil)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

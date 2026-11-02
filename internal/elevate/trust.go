// SPDX-License-Identifier: AGPL-3.0-only

package elevate

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/privproto"
	"github.com/kushalpandya/wargo/internal/store"
)

// WithStore gives the service access to Wargo's database (needed to
// export passkeys to wargo-privd). Returns s for chaining.
func (s *Service) WithStore(st *store.Store) *Service {
	s.store = st
	return s
}

// PrivdStatus describes the optional wargo-privd helper.
type PrivdStatus struct {
	Socket      string `json:"socket"`
	Installed   bool   `json:"installed"` // socket exists
	Reachable   bool   `json:"reachable"`
	Credentials int    `json:"credentials"`
	Error       string `json:"error,omitempty"`
	Executable  string `json:"executable"` // this wargo binary (for privd.toml)
	UID         int    `json:"uid"`
	Container   bool   `json:"container,omitempty"`
}

// Status queries privd.
func (c *PrivdClient) Status(ctx context.Context) PrivdStatus {
	st := PrivdStatus{Socket: c.socket}
	if _, err := os.Stat(c.socket); err != nil {
		return st
	}
	st.Installed = true
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := c.call(ctx, privproto.Request{Op: "status"}, 2*time.Second)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if !resp.OK {
		st.Error = resp.Error
		return st
	}
	st.Reachable = true
	st.Credentials = resp.Credentials
	return st
}

// privdStore mirrors privd.CredentialStore (kept separate to avoid a
// dependency from wargo on the privd server package).
type privdStore struct {
	UserHandle  string                `json:"userHandle"`
	RPIDs       []string              `json:"rpIds"`
	Credentials []webauthn.Credential `json:"credentials"`
}

func (s *Service) buildTrustStore(ctx context.Context, ids []string) (*privdStore, []string, error) {
	if s.store == nil {
		return nil, nil, httpx.ErrUnavailable
	}
	var handle string
	ok, err := s.store.Get(ctx, "webauthn_user_id", &handle)
	if err != nil {
		return nil, nil, err
	}
	if !ok || handle == "" {
		return nil, nil, httpx.Errorf(http.StatusConflict, "no_passkeys", "Register a passkey first")
	}
	if _, err := base64.RawURLEncoding.DecodeString(handle); err != nil {
		return nil, nil, fmt.Errorf("invalid stored user handle")
	}
	out := &privdStore{UserHandle: handle, RPIDs: []string{}, Credentials: []webauthn.Credential{}}
	var names []string
	for _, id := range ids {
		var data, name, rpID string
		err := s.store.DB.QueryRowContext(ctx, `SELECT data, name, rp_id FROM webauthn_credentials WHERE id = ?`, id).Scan(&data, &name, &rpID)
		if err != nil {
			return nil, nil, httpx.BadRequest("unknown passkey " + id)
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil, nil, fmt.Errorf("passkey %s: %w", id, err)
		}
		out.Credentials = append(out.Credentials, c)
		if !slices.Contains(out.RPIDs, rpID) {
			out.RPIDs = append(out.RPIDs, rpID)
		}
		names = append(names, name)
	}
	return out, names, nil
}

func (s *Service) registerPrivd(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/elevate/privd", func(w http.ResponseWriter, r *http.Request) {
		if s.containerMode() {
			// wargo-privd has no role in container mode.
			httpx.OK(w, PrivdStatus{Executable: s.exe, UID: os.Getuid(), Container: true})
			return
		}
		st := s.privd.Status(r.Context())
		st.Executable = s.exe
		st.UID = os.Getuid()
		httpx.OK(w, st)
	})
	mux.HandleFunc("POST /api/elevate/privd/trust", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			PasskeyIDs []string `json:"passkeyIds"`
		}
		if err := httpx.Decode(r, &req); err != nil {
			return err
		}
		if s.containerMode() {
			return httpx.Errorf(http.StatusConflict, "container_mode", "wargo-privd is not used in container mode")
		}
		if len(req.PasskeyIDs) == 0 || len(req.PasskeyIDs) > 32 {
			return httpx.BadRequest("select between 1 and 32 passkeys")
		}
		slices.Sort(req.PasskeyIDs)
		req.PasskeyIDs = slices.Compact(req.PasskeyIDs)
		cs, names, err := s.buildTrustStore(r.Context(), req.PasskeyIDs)
		if err != nil {
			return err
		}
		data, err := json.Marshal(cs)
		if err != nil {
			return err
		}
		target := fmt.Sprintf("Trust %d passkey(s) for admin approvals: %s", len(names), strings.Join(names, ", "))
		if _, err := s.Run(r, "privd.trust", target, s.Helper("privd-trust"), bytes.NewReader(data)); err != nil {
			return err
		}
		httpx.OK(w, map[string]any{"ok": true, "trusted": len(cs.Credentials)})
		return nil
	}))
}

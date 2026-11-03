// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/kushalpandya/wargo/internal/httpx"
)

// maxCeremonies bounds pending WebAuthn ceremonies kept in memory.
const maxCeremonies = 1024

type ceremony struct {
	data    webauthn.SessionData
	rpID    string
	expires time.Time
	ip      string
}

// waUser is the single Wargo account as seen by WebAuthn.
type waUser struct {
	id    []byte
	name  string
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.id }
func (u *waUser) WebAuthnName() string                       { return u.name }
func (u *waUser) WebAuthnDisplayName() string                { return u.name }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Service) userHandle(ctx context.Context) ([]byte, error) {
	var h string
	ok, err := s.st.Get(ctx, "webauthn_user_id", &h)
	if err != nil {
		return nil, err
	}
	if ok {
		return base64.RawURLEncoding.DecodeString(h)
	}
	b := make([]byte, 32)
	rand.Read(b)
	return b, s.st.Set(ctx, "webauthn_user_id", base64.RawURLEncoding.EncodeToString(b))
}

func (s *Service) loadUser(ctx context.Context, rpID string) (*waUser, error) {
	id, err := s.userHandle(ctx)
	if err != nil {
		return nil, err
	}
	u := &waUser{id: id, name: s.cfg.Auth.User}
	rows, err := s.st.DB.QueryContext(ctx, `SELECT data FROM webauthn_credentials WHERE rp_id = ?`, rpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(d), &c) == nil {
			u.creds = append(u.creds, c)
		}
	}
	return u, rows.Err()
}

// relyingParty builds a WebAuthn RP bound to the request's origin.
func (s *Service) relyingParty(r *http.Request) (*webauthn.WebAuthn, string, error) {
	origin := r.Header.Get("Origin")
	if origin == "" || !s.AllowedOrigin(r, origin) {
		return nil, "", httpx.Errorf(http.StatusForbidden, "bad_origin", "Passkeys need a request from an allowed origin")
	}
	u, err := url.Parse(origin)
	if err != nil {
		return nil, "", httpx.BadRequest("bad origin")
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" {
		return nil, "", httpx.Errorf(http.StatusBadRequest, "insecure_origin", "Passkeys require HTTPS (or localhost)")
	}
	rpID := u.Hostname()
	wa, err := webauthn.New(&webauthn.Config{
		RPID: rpID, RPDisplayName: "Wargo", RPOrigins: []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
		},
	})
	return wa, rpID, err
}

func (s *Service) storeCeremony(r *http.Request, rpID string, d *webauthn.SessionData) string {
	id := randToken(18)
	s.mu.Lock()
	s.gcPending()
	// Unauthenticated clients can start ceremonies: bound the map.
	if len(s.cerem) >= maxCeremonies {
		var oldest string
		for k, c := range s.cerem {
			if oldest == "" || c.expires.Before(s.cerem[oldest].expires) {
				oldest = k
			}
		}
		delete(s.cerem, oldest)
	}
	s.cerem[id] = ceremony{data: *d, rpID: rpID, expires: time.Now().Add(5 * time.Minute), ip: s.proxy.ClientIP(r)}
	s.mu.Unlock()
	return id
}

func (s *Service) takeCeremony(r *http.Request, id, rpID string) (webauthn.SessionData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cerem[id]
	delete(s.cerem, id)
	if !ok || time.Now().After(c.expires) || c.rpID != rpID || c.ip != s.proxy.ClientIP(r) {
		return webauthn.SessionData{}, httpx.Errorf(http.StatusBadRequest, "ceremony_expired", "Passkey request expired, try again")
	}
	return c.data, nil
}

func (s *Service) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) error {
	wa, rpID, err := s.relyingParty(r)
	if err != nil {
		return err
	}
	u, err := s.loadUser(r.Context(), rpID)
	if err != nil {
		return err
	}
	var excl []protocol.CredentialDescriptor
	for _, c := range u.creds {
		excl = append(excl, c.Descriptor())
	}
	opts, sd, err := wa.BeginRegistration(u, webauthn.WithExclusions(excl),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired))
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"ceremony": s.storeCeremony(r, rpID, sd), "options": opts})
	return nil
}

func (s *Service) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) error {
	wa, rpID, err := s.relyingParty(r)
	if err != nil {
		return err
	}
	sd, err := s.takeCeremony(r, r.URL.Query().Get("ceremony"), rpID)
	if err != nil {
		return err
	}
	u, err := s.loadUser(r.Context(), rpID)
	if err != nil {
		return err
	}
	cred, err := wa.FinishRegistration(u, sd, r)
	if err != nil {
		s.audit.Record(r, "auth.passkey_register", rpID, err, nil)
		return httpx.BadRequest("Passkey registration failed: " + err.Error())
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	data, _ := json.Marshal(cred)
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	_, err = s.st.DB.ExecContext(r.Context(),
		`INSERT INTO webauthn_credentials(id, name, data, rp_id, created_at) VALUES(?,?,?,?,?)`,
		id, name, string(data), rpID, time.Now().Unix())
	s.audit.Record(r, "auth.passkey_register", name+" @ "+rpID, err, nil)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]string{"id": id})
	return nil
}

func (s *Service) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) error {
	wa, rpID, err := s.relyingParty(r)
	if err != nil {
		return err
	}
	// Passkeys can't be brute-forced, so only the per-client backoff
	// applies (never the global budget): the real user can always sign in
	// with a passkey while someone hammers the password form.
	ip := s.proxy.ClientIP(r)
	if ok, wait := s.throttle.AllowLocal(ip); !ok {
		return httpx.Errorf(http.StatusTooManyRequests, "throttled", "Too many attempts. Try again in "+wait.Round(time.Second).String())
	}
	s.throttle.Release(ip)
	opts, sd, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"ceremony": s.storeCeremony(r, rpID, sd), "options": opts})
	return nil
}

// verifyAssertion validates a discoverable assertion in r's body and
// updates the credential's counter.
func (s *Service) verifyAssertion(r *http.Request, wa *webauthn.WebAuthn, rpID string, sd webauthn.SessionData) (string, error) {
	u, err := s.loadUser(r.Context(), rpID)
	if err != nil {
		return "", err
	}
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		if !bytes.Equal(userHandle, u.id) {
			return nil, errors.New("unknown user")
		}
		return u, nil
	}
	cred, err := wa.FinishDiscoverableLogin(handler, sd, r)
	if err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	data, _ := json.Marshal(cred)
	s.st.DB.ExecContext(r.Context(), `UPDATE webauthn_credentials SET data = ?, last_used = ? WHERE id = ?`,
		string(data), time.Now().Unix(), id)
	return id, nil
}

func (s *Service) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) error {
	wa, rpID, err := s.relyingParty(r)
	if err != nil {
		return err
	}
	ip := s.proxy.ClientIP(r)
	if ok, wait := s.throttle.AllowLocal(ip); !ok {
		return httpx.Errorf(http.StatusTooManyRequests, "throttled", "Too many attempts. Try again in "+wait.Round(time.Second).String())
	}
	sd, err := s.takeCeremony(r, r.URL.Query().Get("ceremony"), rpID)
	if err != nil {
		s.throttle.Release(ip)
		return err
	}
	id, err := s.verifyAssertion(r, wa, rpID, sd)
	s.audit.Record(r, "auth.passkey_login", rpID, err, nil)
	if err != nil {
		s.throttle.FailLocal(ip)
		return httpx.Errorf(http.StatusUnauthorized, "passkey_failed", "Passkey sign-in failed")
	}
	s.throttle.Success(ip)
	if _, err := s.createSession(w, r, MethodPasskey, r.URL.Query().Get("remember") == "1"); err != nil {
		return err
	}
	httpx.OK(w, map[string]string{"next": "done", "credential": id})
	return nil
}

// VerifyPasskeyStepUp is used by the elevation flow: it checks an assertion
// for a challenge created by BeginStepUp.
func (s *Service) BeginStepUp(r *http.Request) (any, string, error) {
	wa, rpID, err := s.relyingParty(r)
	if err != nil {
		return nil, "", err
	}
	u, err := s.loadUser(r.Context(), rpID)
	if err != nil {
		return nil, "", err
	}
	if len(u.creds) == 0 {
		return nil, "", httpx.Errorf(http.StatusConflict, "no_passkeys", "No passkeys registered for this origin")
	}
	opts, sd, err := wa.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	return opts, s.storeCeremony(r, rpID, sd), nil
}

// FinishStepUp verifies an assertion (JSON body) for ceremony id.
func (s *Service) FinishStepUp(r *http.Request, ceremonyID string, body []byte) (string, error) {
	wa, rpID, err := s.relyingParty(r)
	if err != nil {
		return "", err
	}
	sd, err := s.takeCeremony(r, ceremonyID, rpID)
	if err != nil {
		return "", err
	}
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	u, err := s.loadUser(r.Context(), rpID)
	if err != nil {
		return "", err
	}
	cred, err := wa.FinishLogin(u, sd, r2)
	if err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(cred.ID)
	data, _ := json.Marshal(cred)
	s.st.DB.ExecContext(r.Context(), `UPDATE webauthn_credentials SET data = ?, last_used = ? WHERE id = ?`,
		string(data), time.Now().Unix(), id)
	return id, nil
}

func (s *Service) handlePasskeyList(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.st.DB.QueryContext(r.Context(),
		`SELECT id, name, rp_id, created_at, COALESCE(last_used, 0) FROM webauthn_credentials ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pk struct {
		ID       string     `json:"id"`
		Name     string     `json:"name"`
		RPID     string     `json:"rpId"`
		Created  time.Time  `json:"createdAt"`
		LastUsed *time.Time `json:"lastUsed,omitempty"`
	}
	out := []pk{}
	for rows.Next() {
		var p pk
		var c, l int64
		if err := rows.Scan(&p.ID, &p.Name, &p.RPID, &c, &l); err != nil {
			return err
		}
		p.Created = time.Unix(c, 0)
		if l > 0 {
			t := time.Unix(l, 0)
			p.LastUsed = &t
		}
		out = append(out, p)
	}
	httpx.OK(w, out)
	return rows.Err()
}

func (s *Service) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := s.reauth(r, req.Password); err != nil {
		return err
	}
	id := r.PathValue("id")
	res, err := s.st.DB.ExecContext(r.Context(), `DELETE FROM webauthn_credentials WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	s.audit.Record(r, "auth.passkey_delete", id, nil, nil)
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

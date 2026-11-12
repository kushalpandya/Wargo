// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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

// maxCeremonies bounds pending registration and step-up ceremonies kept in
// memory. Those routes need a session; discoverable login, which anyone
// can start, uses sealed tokens instead (sealLoginCeremony).
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
	// Bound the map anyway, in case a session is used to flood it.
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

// loginCeremonyTTL is how long a discoverable login ceremony stays valid.
const loginCeremonyTTL = 5 * time.Minute

// maxLoginToken bounds the sealed login token accepted from a client.
const maxLoginToken = 4096

// loginPurpose tags sealed login tokens so no other sealed value can pass
// for one.
const loginPurpose = "passkey_login"

// sealedLogin is a discoverable login ceremony carried by the client.
// Anyone can start one, so nothing is kept server side until it succeeds.
type sealedLogin struct {
	Purpose string               `json:"purpose"`
	Data    webauthn.SessionData `json:"data"`
	RPID    string               `json:"rpID"`
	IP      string               `json:"ip"`
	Expires int64                `json:"expires"`
	Boot    string               `json:"boot"`
}

var errLoginCeremony = httpx.Errorf(http.StatusBadRequest, "ceremony_expired", "Passkey request expired, try again")

// sealLoginCeremony returns a URL-safe token holding sd, bound to rpID and
// the client's IP for loginCeremonyTTL.
func (s *Service) sealLoginCeremony(r *http.Request, rpID string, sd *webauthn.SessionData) (string, error) {
	return s.sealLogin(sealedLogin{
		Purpose: loginPurpose, Data: *sd, RPID: rpID, IP: s.proxy.ClientIP(r),
		Expires: time.Now().Add(loginCeremonyTTL).Unix(), Boot: s.bootID,
	})
}

func (s *Service) sealLogin(c sealedLogin) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sealed, err := s.st.Seal(string(b))
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// loginTokenKey decodes a sealed login token. The key is the SHA-256 of
// the decoded bytes, so other spellings of the same token share it.
func loginTokenKey(token string) ([32]byte, []byte, bool) {
	if token == "" || len(token) > maxLoginToken {
		return [32]byte{}, nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return [32]byte{}, nil, false
	}
	return sha256.Sum256(raw), raw, true
}

// openLoginCeremony checks a token from sealLoginCeremony: it must be
// unexpired, for rpID, from the same client IP, sealed by this process and
// not used before.
func (s *Service) openLoginCeremony(r *http.Request, token, rpID string) (webauthn.SessionData, error) {
	key, raw, ok := loginTokenKey(token)
	if !ok {
		return webauthn.SessionData{}, errLoginCeremony
	}
	plain, err := s.st.Unseal(base64.StdEncoding.EncodeToString(raw))
	if err != nil || plain == "" {
		return webauthn.SessionData{}, errLoginCeremony
	}
	var c sealedLogin
	if json.Unmarshal([]byte(plain), &c) != nil || c.Purpose != loginPurpose ||
		time.Now().Unix() > c.Expires || c.RPID != rpID || c.IP != s.proxy.ClientIP(r) || c.Boot != s.bootID {
		return webauthn.SessionData{}, errLoginCeremony
	}
	s.mu.Lock()
	_, used := s.usedLogin[key]
	s.mu.Unlock()
	if used {
		return webauthn.SessionData{}, errLoginCeremony
	}
	return c.Data, nil
}

// markLoginUsed records a token that signed someone in. It reports false
// when the token was already used (a parallel finish won the race).
func (s *Service) markLoginUsed(token string) bool {
	key, _, ok := loginTokenKey(token)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcPending()
	if _, used := s.usedLogin[key]; used {
		return false
	}
	// Kept a little past the token's own expiry, so clock rounding never
	// lets it back in.
	s.usedLogin[key] = time.Now().Add(loginCeremonyTTL + time.Minute)
	return true
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
	token, err := s.sealLoginCeremony(r, rpID, sd)
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"ceremony": token, "options": opts})
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
	token := r.URL.Query().Get("ceremony")
	sd, err := s.openLoginCeremony(r, token, rpID)
	if err != nil {
		s.throttle.Release(ip)
		return err
	}
	id, err := s.verifyAssertion(r, wa, rpID, sd)
	if err == nil && !s.markLoginUsed(token) {
		s.audit.Record(r, "auth.passkey_login", rpID, errors.New("ceremony already used"), nil)
		s.throttle.Release(ip)
		return errLoginCeremony
	}
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
	// Read before the transaction: the store has a single connection.
	totpOn := s.totpEnabled(r.Context())
	// Count and delete in one transaction so two parallel deletes cannot
	// both pass the last-factor check.
	tx, err := s.st.DB.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM webauthn_credentials`).Scan(&n); err != nil {
		return err
	}
	if s.cfg.Auth.RequireTOTP && n == 1 && !totpOn {
		return httpx.Errorf(http.StatusConflict, "last_second_factor", "This is your last second factor. Turn on two-factor codes before removing it.")
	}
	res, err := tx.ExecContext(r.Context(), `DELETE FROM webauthn_credentials WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit.Record(r, "auth.passkey_delete", id, nil, nil)
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

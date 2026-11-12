// SPDX-License-Identifier: AGPL-3.0-only

// Package auth implements host-account login (PAM), TOTP, passkeys,
// recovery codes, sessions and CSRF protection.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

const (
	SessionCookie = "wargo_session"
	CSRFCookie    = "wargo_csrf"
	// DeviceCookie is a random id for this browser, kept across sign-ins
	// so a new sign-in replaces the browser's earlier session.
	DeviceCookie = "wargo_device"
	CSRFHeader   = "X-Wargo-CSRF"
)

// Session methods.
const (
	MethodPassword     = "password"      // password only; TOTP setup pending
	MethodPasswordTOTP = "password+totp" // password + second factor
	MethodPasskey      = "passkey"
	MethodRecovery     = "password+recovery"
	MethodDev          = "dev"
)

type Session struct {
	IDHash    string
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
	IP        string
	UA        string
	Method    string
	// Remember: "Keep me signed in"; lasts SessionRemember since last use.
	Remember bool
}

type ctxKey struct{}

// FromContext returns the authenticated session, if any.
func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(ctxKey{}).(*Session)
	return s
}

type pendingLogin struct {
	ip       string
	expires  time.Time
	fails    int
	remember bool
}

// maxSecondFactorFails bounds TOTP/recovery guesses per password entry.
const maxSecondFactorFails = 5

type Service struct {
	cfg      config.Config
	st       *store.Store
	audit    *audit.Log
	proxy    *httpx.ProxyTrust
	throttle *Throttle
	bypass   bool
	// authenticate replaces the admin check and password verification in
	// checkPassword (tests only).
	authenticate func(username, password string) error

	mu       sync.Mutex
	pending  map[string]pendingLogin
	cerem    map[string]ceremony
	lastSeen map[string]time.Time
}

func New(cfg config.Config, st *store.Store, al *audit.Log, proxy *httpx.ProxyTrust) *Service {
	s := &Service{
		cfg: cfg, st: st, audit: al, proxy: proxy, throttle: NewThrottle(),
		pending: map[string]pendingLogin{}, cerem: map[string]ceremony{}, lastSeen: map[string]time.Time{},
	}
	if cfg.Dev.AuthBypass {
		if cfg.LoopbackOnly() {
			s.bypass = true
			slog.Warn("DEV MODE: authentication is bypassed. Never use this in production.")
		} else {
			slog.Error("dev.auth_bypass ignored: listen address is not loopback")
		}
	}
	return s
}

// Bypass reports whether dev auth bypass is active.
func (s *Service) Bypass() bool { return s.bypass }

// bypassFor reports whether the dev bypass applies to r (see bypassAllowed).
func (s *Service) bypassFor(r *http.Request) bool { return s.bypass && s.bypassAllowed(r) }

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// AdminGroup returns the configured or detected admin group.
func (s *Service) AdminGroup() string {
	if s.cfg.Auth.AdminGroup != "" {
		return s.cfg.Auth.AdminGroup
	}
	for _, g := range []string{"wheel", "sudo", "admin"} {
		if _, err := host.LookupGroup(g); err == nil {
			return g
		}
	}
	return "wheel"
}

func (s *Service) userIsAdmin(name string) error {
	u, err := host.LookupUser(name)
	if err != nil {
		return err
	}
	if host.Container() && u.UID == 0 {
		return errors.New("root cannot sign in")
	}
	g, err := host.LookupGroup(s.AdminGroup())
	if err != nil {
		return err
	}
	ids, err := host.UserGroups(u)
	if err != nil {
		return err
	}
	if u.GID == g.GID || slices.Contains(ids, g.GID) {
		return nil
	}
	return fmt.Errorf("user is not in %q", g.Name)
}

// passwordAuthenticate verifies the host account password: through PAM
// natively, against the host's shadow file in container mode.
func (s *Service) passwordAuthenticate(username, password string) error {
	if host.Container() {
		return VerifyHostPassword(username, password)
	}
	return pamAuthenticate(s.cfg.Auth.PAMService, username, password)
}

// passwordAvailable reports whether password sign-in can work.
func passwordAvailable() bool {
	if host.Container() {
		return cryptAvailable
	}
	return pamAvailable
}

// VerifyAdminPassword checks the configured admin account's password
// without throttling (container-mode elevation throttles separately).
func (s *Service) VerifyAdminPassword(password string) error {
	if err := s.userIsAdmin(s.cfg.Auth.User); err != nil {
		return err
	}
	return s.passwordAuthenticate(s.cfg.Auth.User, password)
}

func (s *Service) hasRecoveryCodes(ctx context.Context) bool {
	var n int
	s.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NULL`).Scan(&n)
	return n > 0
}

// HasPasskeys reports whether any passkey is registered.
func (s *Service) HasPasskeys(ctx context.Context) bool {
	var n int
	s.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM webauthn_credentials`).Scan(&n)
	return n > 0
}

// IsAdmin reports whether name is an allowed admin account.
func (s *Service) IsAdmin(name string) error { return s.userIsAdmin(name) }

// AllowedOrigin reports whether origin may talk to us for request r.
// Same-host origins are accepted, which is only safe because every request
// first passes HostAllowed (DNS-rebinding protection).
func (s *Service) AllowedOrigin(r *http.Request, origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) && s.HostAllowed(r) {
		return true
	}
	for _, o := range s.allOrigins() {
		if strings.EqualFold(strings.TrimRight(o, "/"), origin) {
			return true
		}
	}
	return false
}

// OriginPatterns returns host patterns for websocket.Accept.
func (s *Service) OriginPatterns() []string {
	var out []string
	for _, o := range s.allOrigins() {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			out = append(out, u.Host)
		}
	}
	return out
}

// ---- sessions ----

// cookieLife is how long the browser keeps the cookies of a remembered
// session: the browsers' upper limit. The server enforces the real expiry,
// which moves forward while the session is used.
const cookieLife = 400 * 24 * time.Hour

// setCookie sets one of our cookies; an empty value clears it.
func (s *Service) setCookie(w http.ResponseWriter, r *http.Request, name, value string, httpOnly bool, exp time.Time) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: httpOnly, Secure: s.proxy.IsSecure(r),
		SameSite: http.SameSiteStrictMode, Expires: exp}
	if value == "" {
		c.Expires, c.MaxAge = time.Time{}, -1
	}
	http.SetCookie(w, c)
}

// secureContext reports whether r can't be read on the network: HTTPS, or
// a browser on this machine. "Keep me signed in" is only honoured there.
func (s *Service) secureContext(r *http.Request) bool {
	if s.proxy.IsSecure(r) || isLocalhost(r) {
		return true
	}
	ip := net.ParseIP(s.proxy.ClientIP(r))
	return ip != nil && ip.IsLoopback()
}

func (s *Service) createSession(w http.ResponseWriter, r *http.Request, method string, remember bool) (*Session, error) {
	// A password-only session waiting for TOTP setup is never remembered,
	// nor is one whose cookie travels over plain HTTP.
	if method == MethodPassword && s.cfg.Auth.RequireTOTP || !s.secureContext(r) {
		remember = false
	}
	tok := randToken(32)
	now := time.Now()
	exp := now.Add(s.cfg.Auth.SessionMax.Duration)
	if remember {
		exp = now.Add(s.cfg.Auth.SessionRemember.Duration)
	}
	sess := &Session{
		IDHash: hashToken(tok), CSRF: randToken(24), CreatedAt: now, LastSeen: now,
		ExpiresAt: exp, IP: s.proxy.ClientIP(r), UA: r.UserAgent(), Method: method, Remember: remember,
	}
	device := ""
	if c, err := r.Cookie(DeviceCookie); err == nil && validDeviceID(c.Value) {
		device = c.Value
	} else {
		device = randToken(16)
	}
	ctx := r.Context()
	// This browser's earlier sessions go away; so do sessions from before
	// devices were tracked from the same browser and address (a one-time
	// cleanup), and every expired one.
	replaced, _ := s.deleteSessions(ctx, `device = ? OR (device IS NULL AND ua = ? AND ip = ?)`, device, sess.UA, sess.IP)
	s.pruneSessions(ctx)
	_, err := s.st.DB.ExecContext(ctx,
		`INSERT INTO sessions(id_hash, csrf, created_at, last_seen, expires_at, ip, ua, method, remember, device) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		sess.IDHash, sess.CSRF, now.Unix(), now.Unix(), sess.ExpiresAt.Unix(), sess.IP, sess.UA, method, remember, device)
	if err != nil {
		return nil, err
	}
	details := map[string]any{"remember": remember}
	if replaced > 0 {
		details["replaced"] = replaced
	}
	s.audit.Record(r, "auth.session", method, nil, details)
	cookieExp := sess.ExpiresAt
	if remember {
		cookieExp = now.Add(cookieLife)
	}
	s.setCookie(w, r, SessionCookie, tok, true, cookieExp)
	s.setCookie(w, r, CSRFCookie, sess.CSRF, false, cookieExp)
	s.setCookie(w, r, DeviceCookie, device, true, now.Add(cookieLife))
	return sess, nil
}

// validDeviceID accepts ids made by randToken(16) only.
func validDeviceID(v string) bool {
	if len(v) < 16 || len(v) > 64 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// deleteSessions deletes the sessions matching cond, forgets their
// in-memory last use and returns how many went.
func (s *Service) deleteSessions(ctx context.Context, cond string, args ...any) (int, error) {
	rows, err := s.st.DB.QueryContext(ctx, `DELETE FROM sessions WHERE `+cond+` RETURNING id_hash`, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var gone []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return len(gone), err
		}
		gone = append(gone, h)
	}
	s.mu.Lock()
	for _, h := range gone {
		delete(s.lastSeen, h)
	}
	s.mu.Unlock()
	return len(gone), rows.Err()
}

func (s *Service) deleteSession(ctx context.Context, h string) {
	s.deleteSessions(ctx, `id_hash = ?`, h)
}

// pruneSessions deletes sessions that expired or sat idle too long. The
// idle limit gets a minute's margin: last_seen in the database lags the
// in-memory value by up to that much.
func (s *Service) pruneSessions(ctx context.Context) {
	now := time.Now()
	s.deleteSessions(ctx, `expires_at < ? OR (remember = 0 AND last_seen < ?)`,
		now.Unix(), now.Add(-s.cfg.Auth.SessionIdle.Duration-time.Minute).Unix())
}

func (s *Service) lookup(r *http.Request) (*Session, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil, httpx.ErrUnauthorized
	}
	h := hashToken(c.Value)
	var sess Session
	var created, last, exp int64
	err = s.st.DB.QueryRowContext(r.Context(),
		`SELECT id_hash, csrf, created_at, last_seen, expires_at, COALESCE(ip,''), COALESCE(ua,''), method, remember FROM sessions WHERE id_hash = ?`, h).
		Scan(&sess.IDHash, &sess.CSRF, &created, &last, &exp, &sess.IP, &sess.UA, &sess.Method, &sess.Remember)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt, sess.LastSeen, sess.ExpiresAt = time.Unix(created, 0), time.Unix(last, 0), time.Unix(exp, 0)
	now := time.Now()
	s.mu.Lock()
	if t, ok := s.lastSeen[h]; ok && t.After(sess.LastSeen) {
		sess.LastSeen = t
	}
	s.mu.Unlock()
	idle, hardEnd := s.cfg.Auth.SessionIdle.Duration, sess.ExpiresAt
	if sess.Remember {
		idle = s.cfg.Auth.SessionRemember.Duration
		hardEnd = sess.CreatedAt.Add(s.cfg.Auth.SessionRememberMax.Duration)
	}
	if now.After(sess.ExpiresAt) || now.After(hardEnd) || now.Sub(sess.LastSeen) > idle {
		s.deleteSession(r.Context(), h)
		return nil, httpx.Errorf(http.StatusUnauthorized, "session_expired", "Session expired")
	}
	// Persist last_seen at most once a minute; keep finer value in memory.
	s.mu.Lock()
	s.lastSeen[h] = now
	s.mu.Unlock()
	if now.Sub(sess.LastSeen) > time.Minute || now.Sub(time.Unix(last, 0)) > time.Minute {
		if sess.Remember {
			// A remembered session lasts SessionRemember from its last
			// use, up to SessionRememberMax after sign-in.
			sess.ExpiresAt = now.Add(s.cfg.Auth.SessionRemember.Duration)
			if sess.ExpiresAt.After(hardEnd) {
				sess.ExpiresAt = hardEnd
			}
		}
		s.st.DB.Exec(`UPDATE sessions SET last_seen = ?, expires_at = ? WHERE id_hash = ?`, now.Unix(), sess.ExpiresAt.Unix(), h)
	}
	return &sess, nil
}

func (s *Service) devSession() *Session {
	return &Session{IDHash: "dev", Method: MethodDev, CreatedAt: time.Now(), LastSeen: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
}

// limited reports whether the session must finish TOTP enrolment first.
// Enabling TOTP upgrades the session that did it and deletes every other
// password-only one, so this doesn't depend on whether TOTP is on now.
func (s *Service) limited(sess *Session) bool {
	return sess.Method == MethodPassword && s.cfg.Auth.RequireTOTP
}

// limitedRoutes are the only endpoints a password-only session may use
// while TOTP enrolment is pending. In particular it can't register a
// passkey, regenerate recovery codes or open a WebSocket.
var limitedRoutes = map[string]bool{
	"GET /api/auth/me":           true,
	"POST /api/auth/totp/setup":  true,
	"POST /api/auth/totp/enable": true,
}

// isWebSocket reports whether r is a WebSocket upgrade.
func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// Require wraps next so it only runs for authenticated, CSRF-checked requests.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		// WebSocket upgrades are GETs but carry the session like a
		// state-changing request (terminal input!): check Origin for them
		// too, in addition to the check websocket.Accept does
		// (cross-site WebSocket hijacking).
		if (unsafe || isWebSocket(r)) && !s.AllowedOrigin(r, r.Header.Get("Origin")) {
			httpx.Fail(w, r, httpx.Errorf(http.StatusForbidden, "bad_origin", "Cross-origin request rejected"))
			return
		}
		var sess *Session
		if s.bypassFor(r) {
			sess = s.devSession()
		} else {
			var err error
			sess, err = s.lookup(r)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			if unsafe && subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(sess.CSRF)) != 1 {
				httpx.Fail(w, r, httpx.Errorf(http.StatusForbidden, "csrf", "Missing or invalid CSRF token"))
				return
			}
			if s.limited(sess) && !limitedRoutes[r.Method+" "+r.URL.Path] {
				httpx.Fail(w, r, httpx.Errorf(http.StatusForbidden, "totp_setup_required", "Set up two-factor authentication to continue"))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

func (s *Service) gcPending() {
	now := time.Now()
	for k, v := range s.pending {
		if now.After(v.expires) {
			delete(s.pending, k)
		}
	}
	for k, v := range s.cerem {
		if now.After(v.expires) {
			delete(s.cerem, k)
		}
	}
}

// checkPassword verifies the admin password via PAM with throttling.
func (s *Service) checkPassword(r *http.Request, username, password string) error {
	ip := s.proxy.ClientIP(r)
	if ok, wait := s.throttle.Allow(ip); !ok {
		return httpx.Errorf(http.StatusTooManyRequests, "throttled",
			fmt.Sprintf("Too many attempts. Try again in %s.", wait.Round(time.Second)))
	}
	invalid := httpx.Errorf(http.StatusUnauthorized, "invalid_credentials", "Invalid username or password")
	start := time.Now()
	var err error
	if username != s.cfg.Auth.User {
		err = errors.New("user not allowed")
	} else if s.authenticate != nil {
		err = s.authenticate(username, password)
	} else if err = s.userIsAdmin(username); err == nil {
		err = s.passwordAuthenticate(username, password)
	}
	// Even out response timing between unknown users and PAM failures.
	if d := 400*time.Millisecond - time.Since(start); d > 0 {
		time.Sleep(d)
	}
	if err != nil {
		s.throttle.Fail(ip)
		slog.Info("password check failed", "ip", ip, "err", err)
		return invalid
	}
	// Not Success: a correct password is only half of a sign-in when a
	// second factor follows, and clearing the backoff here would give
	// someone who knows the password fresh TOTP guesses after every
	// re-login. The in-flight slot stays reserved: every caller ends the
	// attempt exactly once, with Success once fully authenticated or with
	// Release when it stops short (a second factor follows, or it errors).
	return nil
}

// ---- HTTP ----

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// RegisterPublic mounts endpoints reachable without a session.
func (s *Service) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/state", httpx.Handler(s.handleState))
	mux.HandleFunc("POST /api/auth/login", httpx.Handler(s.handleLogin))
	mux.HandleFunc("POST /api/auth/second-factor", httpx.Handler(s.handleSecondFactor))
	mux.HandleFunc("POST /api/auth/passkey/login/begin", httpx.Handler(s.handlePasskeyLoginBegin))
	mux.HandleFunc("POST /api/auth/passkey/login/finish", httpx.Handler(s.handlePasskeyLoginFinish))
	mux.HandleFunc("POST /api/auth/logout", httpx.Handler(s.handleLogout))
}

// Register mounts endpoints that need a session (wrap mux with Require).
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/me", httpx.Handler(s.handleMe))
	mux.HandleFunc("POST /api/auth/totp/setup", httpx.Handler(s.handleTOTPSetup))
	mux.HandleFunc("POST /api/auth/totp/enable", httpx.Handler(s.handleTOTPEnable))
	mux.HandleFunc("POST /api/auth/totp/disable", httpx.Handler(s.handleTOTPDisable))
	mux.HandleFunc("POST /api/auth/recovery-codes", httpx.Handler(s.handleRecoveryRegenerate))
	mux.HandleFunc("GET /api/auth/passkeys", httpx.Handler(s.handlePasskeyList))
	mux.HandleFunc("POST /api/auth/passkeys/register/begin", httpx.Handler(s.handlePasskeyRegisterBegin))
	mux.HandleFunc("POST /api/auth/passkeys/register/finish", httpx.Handler(s.handlePasskeyRegisterFinish))
	mux.HandleFunc("DELETE /api/auth/passkeys/{id}", httpx.Handler(s.handlePasskeyDelete))
	mux.HandleFunc("GET /api/auth/sessions", httpx.Handler(s.handleSessions))
	mux.HandleFunc("POST /api/auth/sessions/revoke-others", httpx.Handler(s.handleRevokeOthers))
	mux.HandleFunc("DELETE /api/auth/sessions/{id}", httpx.Handler(s.handleRevoke))
}

func (s *Service) handleState(w http.ResponseWriter, r *http.Request) error {
	authed := s.bypassFor(r)
	if !authed {
		if _, err := s.lookup(r); err == nil {
			authed = true
		}
	}
	var n int
	s.st.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM webauthn_credentials`).Scan(&n)
	httpx.OK(w, map[string]any{
		"authenticated": authed,
		"dev":           s.bypassFor(r),
		"pam":           passwordAvailable(),
		"container":     host.Container(),
		"passkeys":      n > 0,
		"secure":        s.secureContext(r),
	})
	return nil
}

// isLocalhost reports whether the browser addressed this machine itself.
func isLocalhost(r *http.Request) bool {
	h := requestHostname(r)
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return h == "localhost"
}

var errBadOrigin = httpx.Errorf(http.StatusForbidden, "bad_origin", "Cross-origin request rejected")

// isThrottled reports whether err is the throttle's 429 (not audited: it
// carries no information and would let anyone flood the audit log).
func isThrottled(err error) bool {
	var e *httpx.Error
	return errors.As(err, &e) && e.Code == "throttled"
}

// needsSecondFactor reports whether a correct password alone must not
// yield a session: once any second factor exists (TOTP or a passkey), the
// password is only half of a sign-in. Without this, an attacker who knows
// the password of a passkey-only account would get a "TOTP setup" session
// and could enrol their own TOTP.
func (s *Service) needsSecondFactor(ctx context.Context) bool {
	return s.totpEnabled(ctx) || s.HasPasskeys(ctx)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) error {
	if !s.AllowedOrigin(r, r.Header.Get("Origin")) {
		return errBadOrigin
	}
	var req loginReq
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	user := strings.TrimSpace(req.Username)
	err := s.checkPassword(r, user, req.Password)
	if !isThrottled(err) {
		s.audit.Record(r, "auth.password", user, err, nil)
	}
	if err != nil {
		return err
	}
	if s.needsSecondFactor(r.Context()) {
		// The password half is done; the second factor takes its own slot.
		s.throttle.Release(s.proxy.ClientIP(r))
		if !s.totpEnabled(r.Context()) && !s.hasRecoveryCodes(r.Context()) {
			return httpx.Errorf(http.StatusConflict, "passkey_required", "This account signs in with a passkey. Use “Sign in with a passkey”.")
		}
		tok := randToken(24)
		s.mu.Lock()
		s.gcPending()
		s.pending[tok] = pendingLogin{ip: s.proxy.ClientIP(r), expires: time.Now().Add(5 * time.Minute), remember: req.Remember}
		s.mu.Unlock()
		httpx.OK(w, map[string]any{"next": "second-factor", "token": tok})
		return nil
	}
	if _, err := s.createSession(w, r, MethodPassword, req.Remember); err != nil {
		s.throttle.Release(s.proxy.ClientIP(r))
		return err
	}
	s.throttle.Success(s.proxy.ClientIP(r))
	next := "done"
	if s.cfg.Auth.RequireTOTP {
		next = "totp-setup"
	}
	httpx.OK(w, map[string]any{"next": next})
	return nil
}

func (s *Service) handleSecondFactor(w http.ResponseWriter, r *http.Request) error {
	if !s.AllowedOrigin(r, r.Header.Get("Origin")) {
		return errBadOrigin
	}
	var req struct {
		Token    string `json:"token"`
		Code     string `json:"code"`
		Recovery string `json:"recovery"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	ip := s.proxy.ClientIP(r)
	if ok, wait := s.throttle.Allow(ip); !ok {
		return httpx.Errorf(http.StatusTooManyRequests, "throttled", fmt.Sprintf("Too many attempts. Try again in %s.", wait.Round(time.Second)))
	}
	s.mu.Lock()
	p, ok := s.pending[req.Token]
	if ok && (time.Now().After(p.expires) || p.ip != ip) {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		s.throttle.Release(ip)
		return httpx.Errorf(http.StatusUnauthorized, "login_expired", "Login expired, start again")
	}
	method := MethodPasswordTOTP
	var err error
	if req.Recovery != "" {
		method = MethodRecovery
		err = s.useRecoveryCode(r.Context(), req.Recovery)
	} else {
		err = s.verifyTOTP(r.Context(), req.Code, false)
	}
	s.audit.Record(r, "auth.second_factor", method, err, nil)
	if err != nil {
		s.throttle.Fail(ip)
		// A pending login allows a few guesses only; then the password
		// must be entered again.
		s.mu.Lock()
		if p, ok := s.pending[req.Token]; ok {
			p.fails++
			if p.fails >= maxSecondFactorFails {
				delete(s.pending, req.Token)
			} else {
				s.pending[req.Token] = p
			}
		}
		s.mu.Unlock()
		return httpx.Errorf(http.StatusUnauthorized, "invalid_code", "Invalid code")
	}
	s.throttle.Success(ip)
	s.mu.Lock()
	delete(s.pending, req.Token)
	s.mu.Unlock()
	if _, err := s.createSession(w, r, method, p.remember); err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"next": "done"})
	return nil
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if !s.AllowedOrigin(r, r.Header.Get("Origin")) {
		return errBadOrigin
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		s.deleteSession(r.Context(), hashToken(c.Value))
	}
	s.setCookie(w, r, SessionCookie, "", true, time.Time{})
	s.setCookie(w, r, CSRFCookie, "", false, time.Time{})
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) error {
	sess := FromContext(r.Context())
	var n int
	s.st.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM webauthn_credentials`).Scan(&n)
	var codes int
	s.st.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NULL`).Scan(&codes)
	httpx.OK(w, map[string]any{
		"user":              s.cfg.Auth.User,
		"adminGroup":        s.AdminGroup(),
		"method":            sess.Method,
		"dev":               sess.Method == MethodDev,
		"csrf":              sess.CSRF,
		"totpEnabled":       s.totpEnabled(r.Context()),
		"totpSetupRequired": s.limited(sess),
		"passkeys":          n,
		"recoveryCodesLeft": codes,
		"expiresAt":         sess.ExpiresAt,
	})
	return nil
}

func (s *Service) handleSessions(w http.ResponseWriter, r *http.Request) error {
	cur := FromContext(r.Context())
	s.pruneSessions(r.Context())
	rows, err := s.st.DB.QueryContext(r.Context(),
		`SELECT id_hash, created_at, last_seen, COALESCE(ip,''), COALESCE(ua,''), method, remember FROM sessions ORDER BY last_seen DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type out struct {
		ID       string    `json:"id"`
		Created  time.Time `json:"createdAt"`
		LastSeen time.Time `json:"lastSeen"`
		IP       string    `json:"ip"`
		UA       string    `json:"ua"`
		Method   string    `json:"method"`
		Current  bool      `json:"current"`
		Remember bool      `json:"remember"`
	}
	res := []out{}
	for rows.Next() {
		var o out
		var c, l int64
		if err := rows.Scan(&o.ID, &c, &l, &o.IP, &o.UA, &o.Method, &o.Remember); err != nil {
			return err
		}
		o.Created, o.LastSeen = time.Unix(c, 0), time.Unix(l, 0)
		o.Current = o.ID == cur.IDHash
		o.ID = o.ID[:12]
		res = append(res, o)
	}
	httpx.OK(w, res)
	return rows.Err()
}

// handleRevoke signs out one other session, by the short id from the list.
func (s *Service) handleRevoke(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if !shortSessionID(id) {
		return httpx.BadRequest("invalid session id")
	}
	cur := FromContext(r.Context())
	if strings.HasPrefix(cur.IDHash, id) {
		return httpx.BadRequest("use Sign out to end this session")
	}
	// Hashes are lowercase hex, so prefix+"g" sorts after every hash
	// starting with prefix: a range the primary key index can serve.
	n, err := s.deleteSessions(r.Context(), `id_hash >= ? AND id_hash < ?`, id, id+"g")
	if err != nil {
		return err
	}
	if n == 0 {
		return httpx.ErrNotFound
	}
	s.audit.Record(r, "auth.revoke_session", id, nil, nil)
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

// shortSessionID accepts the 12 lowercase hex characters the list shows.
func shortSessionID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *Service) handleRevokeOthers(w http.ResponseWriter, r *http.Request) error {
	cur := FromContext(r.Context())
	n, err := s.deleteSessions(r.Context(), `id_hash != ?`, cur.IDHash)
	if err != nil {
		return err
	}
	s.audit.Record(r, "auth.revoke_sessions", fmt.Sprint(n), nil, nil)
	httpx.OK(w, map[string]int{"revoked": n})
	return nil
}

// reauth checks the password supplied for sensitive account changes.
func (s *Service) reauth(r *http.Request, password string) error {
	if sess := FromContext(r.Context()); sess != nil && sess.Method == MethodDev {
		return nil
	}
	if err := s.checkPassword(r, s.cfg.Auth.User, password); err != nil {
		return err
	}
	s.throttle.Success(s.proxy.ClientIP(r))
	return nil
}

// ResetAll wipes every second factor and session (used by `wargo admin reset-auth`).
func ResetAll(st *store.Store) error {
	for _, q := range []string{
		`DELETE FROM sessions`, `DELETE FROM webauthn_credentials`, `DELETE FROM recovery_codes`,
		`DELETE FROM settings WHERE key IN ('totp', 'webauthn_user_id')`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.Auth.User = "tester"
	proxy := httpx.NewProxyTrust(nil)
	return New(cfg, st, audit.New(st, proxy), proxy), st
}

func TestThrottleBackoff(t *testing.T) {
	th := NewThrottle()
	for i := 0; i < 3; i++ {
		if ok, _ := th.Allow("1.2.3.4"); !ok {
			t.Fatalf("attempt %d should be allowed", i)
		}
		th.Fail("1.2.3.4")
	}
	if ok, _ := th.Allow("1.2.3.4"); !ok {
		t.Fatal("3 failures should still allow (backoff starts after the 4th)")
	}
	th.Fail("1.2.3.4")
	if ok, wait := th.Allow("1.2.3.4"); ok || wait <= 0 {
		t.Fatalf("expected backoff after 4 failures, got ok=%v wait=%v", ok, wait)
	}
	if ok, _ := th.Allow("5.6.7.8"); !ok {
		t.Fatal("other IPs must not be affected")
	}
	th.Success("1.2.3.4")
	if ok, _ := th.Allow("1.2.3.4"); !ok {
		t.Fatal("success should clear backoff")
	}
}

func TestThrottleGlobalBudget(t *testing.T) {
	th := NewThrottle()
	th.GlobalPerMinute = 5
	for i := 0; i < 5; i++ {
		th.Fail(strings.Repeat("x", i+1))
	}
	if ok, _ := th.Allow("fresh"); ok {
		t.Fatal("global budget should block new IPs")
	}
}

func TestRequireRejectsWithoutSession(t *testing.T) {
	s, _ := newTestService(t)
	h := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
}

func sessionCookies(t *testing.T, s *Service, method string) (*http.Cookie, *Session) {
	t.Helper()
	rec := httptest.NewRecorder()
	sess, err := s.createSession(rec, httptest.NewRequest("POST", "/api/auth/login", nil), method, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatal("session cookie must be HttpOnly + SameSite=Strict")
			}
			return c, sess
		}
	}
	t.Fatal("no session cookie")
	return nil, nil
}

func TestRequireCSRFAndOrigin(t *testing.T) {
	s, _ := newTestService(t)
	s.cfg.Auth.RequireTOTP = false
	c, sess := sessionCookies(t, s, MethodPasswordTOTP)
	h := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))

	do := func(method, origin, token string) int {
		r := httptest.NewRequest(method, "http://wargo.test/api/x", nil)
		r.AddCookie(c)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set(CSRFHeader, token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if got := do("GET", "", ""); got != 204 {
		t.Fatalf("GET with session: %d", got)
	}
	if got := do("POST", "", ""); got != 403 {
		t.Fatalf("POST without CSRF: %d", got)
	}
	if got := do("POST", "", "wrong"); got != 403 {
		t.Fatalf("POST with bad CSRF: %d", got)
	}
	if got := do("POST", "http://evil.test", sess.CSRF); got != 403 {
		t.Fatalf("cross-origin POST: %d", got)
	}
	if got := do("POST", "http://wargo.test", sess.CSRF); got != 204 {
		t.Fatalf("same-origin POST: %d", got)
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	s, st := newTestService(t)
	c, sess := sessionCookies(t, s, MethodPasswordTOTP)
	old := time.Now().Add(-2 * time.Hour).Unix()
	st.DB.Exec(`UPDATE sessions SET last_seen = ? WHERE id_hash = ?`, old, sess.IDHash)
	s.lastSeen = map[string]time.Time{}
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(c)
	if _, err := s.lookup(r); err == nil {
		t.Fatal("idle session should expire")
	}
}

func TestLimitedSessionNeedsTOTPSetup(t *testing.T) {
	s, _ := newTestService(t)
	c, _ := sessionCookies(t, s, MethodPassword)
	h := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for path, want := range map[string]int{"/api/procs": 403, "/api/auth/me": 204} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("%s: got %d want %d", path, rec.Code, want)
		}
	}
}

func TestTOTPReplayRejected(t *testing.T) {
	s, st := newTestService(t)
	ctx := context.Background()
	key, _ := totp.Generate(totp.GenerateOpts{Issuer: "Wargo", AccountName: "tester"})
	sealed, _ := st.Seal(key.Secret())
	st.Set(ctx, "totp", totpState{Secret: sealed, Enabled: true})
	code, _ := totp.GenerateCode(key.Secret(), time.Now())
	if err := s.verifyTOTP(ctx, code, false); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := s.verifyTOTP(ctx, code, false); err == nil {
		t.Fatal("replayed code must be rejected")
	}
	if err := s.verifyTOTP(ctx, "000000", false); err == nil {
		t.Fatal("wrong code accepted")
	}
}

func TestRecoveryCodesSingleUse(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	codes, err := s.newRecoveryCodes(ctx)
	if err != nil || len(codes) != 10 {
		t.Fatalf("codes: %v %v", codes, err)
	}
	if err := s.useRecoveryCode(ctx, strings.ToUpper(codes[0])); err != nil {
		t.Fatalf("valid code (case-insensitive): %v", err)
	}
	if err := s.useRecoveryCode(ctx, codes[0]); err == nil {
		t.Fatal("recovery code reused")
	}
	if err := s.useRecoveryCode(ctx, "aaaa-bbbb-cccc"); err == nil {
		t.Fatal("unknown code accepted")
	}
}

func TestPasswordRejectsOtherUsers(t *testing.T) {
	s, _ := newTestService(t)
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	if err := s.checkPassword(r, "root", "whatever"); err == nil {
		t.Fatal("only the configured user may log in")
	}
}

func TestAllowedOrigin(t *testing.T) {
	s, _ := newTestService(t)
	s.cfg.Auth.Origins = []string{"https://wargo.example.com"}
	r := httptest.NewRequest("GET", "http://127.0.0.1:2207/", nil)
	for o, want := range map[string]bool{
		"":                           true,
		"http://127.0.0.1:2207":      true,
		"https://wargo.example.com":  true,
		"https://wargo.example.com/": false,
		"https://evil.example.com":   false,
	} {
		if got := s.AllowedOrigin(r, o); got != want {
			t.Errorf("%q: got %v want %v", o, got, want)
		}
	}
}

func TestDevBypassOnlyOnLoopback(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	defer st.Close()
	cfg := config.Default()
	cfg.Auth.User = "tester"
	cfg.Dev.AuthBypass = true
	cfg.Listen = "0.0.0.0:2207"
	proxy := httpx.NewProxyTrust(nil)
	if New(cfg, st, audit.New(st, proxy), proxy).Bypass() {
		t.Fatal("bypass must be ignored on non-loopback listen")
	}
	cfg.Listen = "127.0.0.1:2207"
	if !New(cfg, st, audit.New(st, proxy), proxy).Bypass() {
		t.Fatal("bypass expected on loopback")
	}
}

func TestThrottleParallelBurstIsBounded(t *testing.T) {
	th := NewThrottle()
	ok := 0
	for i := 0; i < 20; i++ {
		// Attempts in flight (no Fail yet) must count.
		if a, _ := th.Allow("1.2.3.4"); a {
			ok++
		}
	}
	if ok != th.MaxInflight {
		t.Fatalf("%d parallel attempts allowed, want %d", ok, th.MaxInflight)
	}
	th.Release("1.2.3.4")
	if a, _ := th.Allow("1.2.3.4"); !a {
		t.Fatal("released slot should be reusable")
	}
}

func TestThrottleGlobalCountsInflight(t *testing.T) {
	th := NewThrottle()
	th.GlobalPerMinute = 3
	for i := 0; i < 3; i++ {
		if a, _ := th.Allow(fmt.Sprintf("10.0.0.%d", i)); !a {
			t.Fatal("should be allowed")
		}
	}
	if a, _ := th.Allow("10.0.0.99"); a {
		t.Fatal("in-flight attempts must count towards the global budget")
	}
}

func TestThrottleIPv6Slash64(t *testing.T) {
	th := NewThrottle()
	for i := 1; i <= 4; i++ {
		ip := fmt.Sprintf("2001:db8:1:2::%x", i)
		th.Allow(ip)
		th.Fail(ip)
	}
	if a, _ := th.Allow("2001:db8:1:2:ffff::1"); a {
		t.Fatal("rotating addresses inside one /64 must share a bucket")
	}
	if a, _ := th.Allow("2001:db8:1:3::1"); !a {
		t.Fatal("another /64 must not be affected")
	}
}

func TestThrottleGlobalBudgetCannotLockOutKnownUser(t *testing.T) {
	th := NewThrottle()
	th.GlobalPerMinute = 5
	th.Allow("192.0.2.1")
	th.Success("192.0.2.1")
	for i := 0; i < 5; i++ {
		th.Fail(fmt.Sprintf("198.51.100.%d", i))
	}
	if a, _ := th.Allow("203.0.113.1"); a {
		t.Fatal("unknown client should hit the global budget")
	}
	if a, _ := th.Allow("192.0.2.1"); !a {
		t.Fatal("recently successful client must be exempt from the global budget")
	}
	th.Release("192.0.2.1")
	if a, _ := th.AllowLocal("203.0.113.1"); !a {
		t.Fatal("passkey sign-in must not be subject to the global budget")
	}
}

func TestHostAllowedDNSRebinding(t *testing.T) {
	s, _ := newTestService(t)
	s.cfg.Auth.Origins = []string{"https://wargo.example.com"}
	for host, want := range map[string]bool{
		"127.0.0.1:2207":         true,
		"[::1]:2207":             true,
		"192.168.1.10:2207":      true,
		"localhost:2207":         true,
		"nas":                    true,
		"nas.lan:2207":           true,
		"nas.home.arpa":          true,
		"wargo.example.com":      true,
		"WARGO.example.com:443":  true,
		"evil.example.net:2207":  false,
		"wargo.example.com.evil": false,
		"":                       false,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		if got := s.HostAllowed(r); got != want {
			t.Errorf("%q: got %v want %v", host, got, want)
		}
	}
	// The rebinding attack: Origin equals Host, both the attacker's name.
	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "evil.example.net:2207"
	if s.AllowedOrigin(r, "http://evil.example.net:2207") {
		t.Fatal("same-host origin on an unknown host must be rejected")
	}
}

func TestDevBypassOnlyForDirectLocalRequests(t *testing.T) {
	s, _ := newTestService(t)
	s.bypass = true
	h := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	do := func(host string, hdr map[string]string) int {
		r := httptest.NewRequest("GET", "/api/x", nil)
		r.Host = host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if got := do("127.0.0.1:2207", nil); got != 204 {
		t.Fatalf("direct local request: %d", got)
	}
	if got := do("localhost:5173", nil); got != 204 {
		t.Fatalf("localhost: %d", got)
	}
	// Published through cloudflared/nginx on the same host.
	if got := do("wargo.example.com", map[string]string{"X-Forwarded-For": "203.0.113.5"}); got != 401 {
		t.Fatalf("proxied request got the bypass: %d", got)
	}
	if got := do("127.0.0.1:2207", map[string]string{"CF-Connecting-IP": "203.0.113.5"}); got != 401 {
		t.Fatalf("tunnelled request got the bypass: %d", got)
	}
}

func TestLimitedSessionCannotEnrolOtherFactors(t *testing.T) {
	s, _ := newTestService(t)
	c, sess := sessionCookies(t, s, MethodPassword)
	h := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/auth/passkeys/register/begin", 403},
		{"POST", "/api/auth/recovery-codes", 403},
		{"POST", "/api/auth/sessions/revoke-others", 403},
		{"GET", "/ws/events", 403},
		{"POST", "/api/auth/totp/setup", 204},
		{"POST", "/api/auth/totp/enable", 204},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1"+tc.path, nil)
		r.AddCookie(c)
		r.Header.Set(CSRFHeader, sess.CSRF)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Errorf("%s %s: got %d want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestPasskeyAccountNeedsSecondFactor(t *testing.T) {
	s, st := newTestService(t)
	ctx := context.Background()
	if s.needsSecondFactor(ctx) {
		t.Fatal("fresh install: password + TOTP enrolment")
	}
	st.DB.Exec(`INSERT INTO webauthn_credentials(id, name, data, rp_id, created_at) VALUES('x','k','{}','example.com',0)`)
	if !s.needsSecondFactor(ctx) {
		t.Fatal("with a passkey registered a password alone must not create a session")
	}
}

func TestWebSocketUpgradeOriginChecked(t *testing.T) {
	s, _ := newTestService(t)
	s.cfg.Auth.RequireTOTP = false
	c, _ := sessionCookies(t, s, MethodPasswordTOTP)
	h := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	r := httptest.NewRequest("GET", "http://wargo.lan/ws/term", nil)
	r.AddCookie(c)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Origin", "https://other.lan")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatalf("cross-origin WebSocket upgrade: %d", rec.Code)
	}
}

func newTestRequest(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func addPasskey(st *store.Store, id string) {
	st.DB.Exec(`INSERT INTO webauthn_credentials(id, name, data, rp_id, created_at) VALUES(?,?,'{}','example.com',0)`, id, id)
}

func enableTOTP(t *testing.T, st *store.Store) string {
	t.Helper()
	key, _ := totp.Generate(totp.GenerateOpts{Issuer: "Wargo", AccountName: "tester"})
	sealed, _ := st.Seal(key.Secret())
	if err := st.Set(context.Background(), "totp", totpState{Secret: sealed, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return key.Secret()
}

func passkeyDelete(s *Service, id string) *httptest.ResponseRecorder {
	r := newTestRequest("DELETE", "/api/auth/passkeys/"+id, `{"password":"x"}`)
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	httpx.Handler(s.handlePasskeyDelete)(rec, r)
	return rec
}

func TestPasskeyDeleteRefusesLastFactorWhenRequired(t *testing.T) {
	s, st := newTestService(t)
	s.cfg.Auth.RequireTOTP = true
	s.bypass = true
	s.authenticate = func(string, string) error { return nil }
	addPasskey(st, "k1")
	rec := passkeyDelete(s, "k1")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "last_second_factor") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM webauthn_credentials`).Scan(&n)
	if n != 1 {
		t.Fatalf("passkey was removed: %d left", n)
	}
}

func TestPasskeyDeleteAllowedWithTOTP(t *testing.T) {
	s, st := newTestService(t)
	s.cfg.Auth.RequireTOTP = true
	s.authenticate = func(string, string) error { return nil }
	enableTOTP(t, st)
	addPasskey(st, "k1")
	if rec := passkeyDelete(s, "k1"); rec.Code != 200 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestTOTPEnableRefusedWhenEnabled(t *testing.T) {
	s, st := newTestService(t)
	ctx := context.Background()
	secret := enableTOTP(t, st)
	if _, err := s.newRecoveryCodes(ctx); err != nil {
		t.Fatal(err)
	}
	var before, after string
	st.DB.QueryRow(`SELECT group_concat(hash) FROM recovery_codes`).Scan(&before)
	code, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	rec := httptest.NewRecorder()
	httpx.Handler(s.handleTOTPEnable)(rec, newTestRequest("POST", "/api/auth/totp/enable", `{"code":"`+code+`"}`))
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "totp_already_enabled") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	st.DB.QueryRow(`SELECT group_concat(hash) FROM recovery_codes`).Scan(&after)
	if before == "" || before != after {
		t.Fatal("recovery codes changed")
	}
}

func TestUnconfirmedTOTPNotASecondFactor(t *testing.T) {
	s, st := newTestService(t)
	ctx := context.Background()
	key, _ := totp.Generate(totp.GenerateOpts{Issuer: "Wargo", AccountName: "tester"})
	sealed, _ := st.Seal(key.Secret())
	st.Set(ctx, "totp", totpState{Secret: sealed}) // setup without enable
	s.pending["tok"] = pendingLogin{ip: "192.0.2.1", expires: time.Now().Add(time.Minute)}
	code, _ := totp.GenerateCode(key.Secret(), time.Now())
	rec := httptest.NewRecorder()
	httpx.Handler(s.handleSecondFactor)(rec, newTestRequest("POST", "/api/auth/second-factor", `{"token":"tok","code":"`+code+`"}`))
	if rec.Code != 401 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

// Every Allow must be matched by exactly one Fail, Success or Release. A
// foreign in-flight slot reserved beforehand must survive each outcome.
func TestCheckPasswordReleasesOnce(t *testing.T) {
	const ip = "192.0.2.1"
	login := func(t *testing.T, s *Service, user, pw string) *httptest.ResponseRecorder {
		t.Helper()
		if ok, _ := s.throttle.Allow(ip); !ok { // another request's slot
			t.Fatal("setup Allow")
		}
		rec := httptest.NewRecorder()
		httpx.Handler(s.handleLogin)(rec, newTestRequest("POST", "/api/auth/login", fmt.Sprintf(`{"username":%q,"password":%q}`, user, pw)))
		return rec
	}
	inflight := func(s *Service) int { return len(s.throttle.inflight[throttleKey(ip)]) }
	setup := func(t *testing.T) (*Service, *store.Store) {
		s, st := newTestService(t)
		s.cfg.Auth.RequireTOTP = false
		s.authenticate = func(_, pw string) error {
			if pw != "good" {
				return errors.New("bad password")
			}
			return nil
		}
		return s, st
	}
	t.Run("password only success", func(t *testing.T) {
		s, _ := setup(t)
		if rec := login(t, s, "tester", "good"); rec.Code != 200 {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if n := inflight(s); n != 1 {
			t.Fatalf("in-flight slots = %d, want 1 (the foreign one)", n)
		}
	})
	t.Run("password then second factor", func(t *testing.T) {
		s, st := setup(t)
		secret := enableTOTP(t, st)
		rec := login(t, s, "tester", "good")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "second-factor") {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if n := inflight(s); n != 1 {
			t.Fatalf("after password step: in-flight slots = %d, want 1", n)
		}
		var tok string
		for k := range s.pending {
			tok = k
		}
		code, _ := totp.GenerateCode(secret, time.Now())
		rec = httptest.NewRecorder()
		httpx.Handler(s.handleSecondFactor)(rec, newTestRequest("POST", "/api/auth/second-factor", `{"token":"`+tok+`","code":"`+code+`"}`))
		if rec.Code != 200 {
			t.Fatalf("second factor: %d %s", rec.Code, rec.Body.String())
		}
		if n := inflight(s); n != 1 {
			t.Fatalf("after second factor: in-flight slots = %d, want 1", n)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		s, _ := setup(t)
		if rec := login(t, s, "tester", "bad"); rec.Code != 401 {
			t.Fatalf("got %d", rec.Code)
		}
		if n := inflight(s); n != 1 {
			t.Fatalf("in-flight slots = %d, want 1", n)
		}
	})
	t.Run("unknown user", func(t *testing.T) {
		s, _ := setup(t)
		if rec := login(t, s, "nobody", "good"); rec.Code != 401 {
			t.Fatalf("got %d", rec.Code)
		}
		if n := inflight(s); n != 1 {
			t.Fatalf("in-flight slots = %d, want 1", n)
		}
	})
}

// passkeyReq builds a passkey request from ip against the localhost RP.
func passkeyReq(method, target, ip, body string) *http.Request {
	r := httptest.NewRequest(method, "http://localhost:2207"+target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://localhost:2207")
	r.RemoteAddr = ip + ":40000"
	return r
}

// passkeyBegin starts a discoverable login from ip and returns the
// ceremony token and the challenge.
func passkeyBegin(t *testing.T, s *Service, ip string) (string, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	httpx.Handler(s.handlePasskeyLoginBegin)(rec, passkeyReq("POST", "/api/auth/passkey/login/begin", ip, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("begin from %s: %d %s", ip, rec.Code, rec.Body)
	}
	var out struct {
		Ceremony string `json:"ceremony"`
		Options  struct {
			PublicKey struct {
				Challenge string `json:"challenge"`
			} `json:"publicKey"`
		} `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Ceremony, out.Options.PublicKey.Challenge
}

// testAuthenticator is a minimal software passkey (ES256) registered for
// the localhost RP.
type testAuthenticator struct {
	key    *ecdsa.PrivateKey
	id     []byte
	handle []byte
	count  uint32
}

func newTestAuthenticator(t *testing.T, s *Service) *testAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := s.userHandle(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	raw := pub.Bytes() // 0x04 || X || Y
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
		Curve:         int64(webauthncose.P256), XCoord: raw[1:33], YCoord: raw[33:],
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &testAuthenticator{key: key, id: []byte("test-credential-id"), handle: handle}
	data, _ := json.Marshal(webauthn.Credential{ID: a.id, PublicKey: cose})
	if _, err := s.st.DB.Exec(`INSERT INTO webauthn_credentials(id, name, data, rp_id, created_at) VALUES(?,?,?,?,0)`,
		base64.RawURLEncoding.EncodeToString(a.id), "Test", string(data), "localhost"); err != nil {
		t.Fatal(err)
	}
	return a
}

// assert returns an assertion body for challenge.
func (a *testAuthenticator) assert(t *testing.T, challenge string) string {
	t.Helper()
	a.count++
	cdj, _ := json.Marshal(map[string]string{"type": "webauthn.get", "challenge": challenge, "origin": "http://localhost:2207"})
	rp := sha256.Sum256([]byte("localhost"))
	ad := append(rp[:], 0x05, byte(a.count>>24), byte(a.count>>16), byte(a.count>>8), byte(a.count)) // UP|UV
	ch := sha256.Sum256(cdj)
	digest := sha256.Sum256(append(append([]byte{}, ad...), ch[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	body, _ := json.Marshal(map[string]any{
		"id": enc(a.id), "rawId": enc(a.id), "type": "public-key",
		"response": map[string]string{
			"clientDataJSON": enc(cdj), "authenticatorData": enc(ad), "signature": enc(sig), "userHandle": enc(a.handle),
		},
	})
	return string(body)
}

func passkeyFinish(s *Service, ip, token, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	httpx.Handler(s.handlePasskeyLoginFinish)(rec,
		passkeyReq("POST", "/api/auth/passkey/login/finish?ceremony="+url.QueryEscape(token), ip, body))
	return rec
}

func TestPasskeyLoginCeremonySurvivesFlood(t *testing.T) {
	s, _ := newTestService(t)
	token, _ := passkeyBegin(t, s, "192.0.2.1")
	for i := 0; i < 5000; i++ {
		passkeyBegin(t, s, fmt.Sprintf("10.%d.%d.1", i/250, i%250))
	}
	if _, err := s.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.1", ""), token, "localhost"); err != nil {
		t.Fatalf("owner's ceremony lost to the flood: %v", err)
	}
	s.mu.Lock()
	nc, nu := len(s.cerem), len(s.usedLogin)
	s.mu.Unlock()
	if nc != 0 || nu != 0 {
		t.Fatalf("flood left server state: %d ceremonies, %d used logins", nc, nu)
	}
}

func TestPasskeyLoginCeremonySingleUse(t *testing.T) {
	s, _ := newTestService(t)
	a := newTestAuthenticator(t, s)
	token, challenge := passkeyBegin(t, s, "192.0.2.1")
	if strings.ContainsAny(token, "+/=") {
		t.Fatalf("token is not URL-safe: %q", token)
	}
	if rec := passkeyFinish(s, "192.0.2.1", token, a.assert(t, challenge)); rec.Code != http.StatusOK {
		t.Fatalf("first finish: %d %s", rec.Code, rec.Body)
	}
	rec := passkeyFinish(s, "192.0.2.1", token, a.assert(t, challenge))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ceremony_expired") {
		t.Fatalf("replayed ceremony: %d %s", rec.Code, rec.Body)
	}
}

func TestPasskeyLoginCeremonyBoundToIP(t *testing.T) {
	s, _ := newTestService(t)
	token, _ := passkeyBegin(t, s, "192.0.2.1")
	if _, err := s.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.2", ""), token, "localhost"); err == nil {
		t.Fatal("ceremony opened from another IP")
	}
	if _, err := s.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.1", ""), token, "example.com"); err == nil {
		t.Fatal("ceremony opened for another RP")
	}
	// Flip one character in the middle: still valid base64url, so this
	// reaches the AEAD check rather than failing in the decoder.
	b := []byte(token)
	mid := len(b) / 2
	if b[mid] == 'A' {
		b[mid] = 'B'
	} else {
		b[mid] = 'A'
	}
	if _, _, ok := loginTokenKey(string(b)); !ok {
		t.Fatal("tampered token should still decode")
	}
	if _, err := s.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.1", ""), string(b), "localhost"); err == nil {
		t.Fatal("tampered ceremony accepted")
	}
	if _, err := s.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.1", ""), token, "localhost"); err != nil {
		t.Fatalf("owner's ceremony refused: %v", err)
	}
}

func TestPasskeyLoginCeremonyRefusedAfterRestart(t *testing.T) {
	s, st := newTestService(t)
	token, _ := passkeyBegin(t, s, "192.0.2.1")
	// A second Service on the same store is a restarted process: same
	// install key, empty usedLogin.
	proxy := httpx.NewProxyTrust(nil)
	s2 := New(s.cfg, st, audit.New(st, proxy), proxy)
	_, err := s2.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.1", ""), token, "localhost")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != "ceremony_expired" {
		t.Fatalf("token from before the restart: %v", err)
	}
}

func TestPasskeyLoginCeremonyExpires(t *testing.T) {
	s, _ := newTestService(t)
	token, err := s.sealLogin(sealedLogin{
		Purpose: loginPurpose, RPID: "localhost", IP: "192.0.2.1", Boot: s.bootID,
		Expires: time.Now().Add(-time.Second).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.1", ""), token, "localhost")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != "ceremony_expired" {
		t.Fatalf("expired token: %v", err)
	}
	// The same payload, unexpired, opens: the refusal above is the expiry.
	token, _ = s.sealLogin(sealedLogin{
		Purpose: loginPurpose, RPID: "localhost", IP: "192.0.2.1", Boot: s.bootID,
		Expires: time.Now().Add(time.Minute).Unix(),
	})
	if _, err := s.openLoginCeremony(passkeyReq("POST", "/", "192.0.2.1", ""), token, "localhost"); err != nil {
		t.Fatalf("fresh token refused: %v", err)
	}
}

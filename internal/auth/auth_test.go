// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	sess, err := s.createSession(rec, httptest.NewRequest("POST", "/api/auth/login", nil), method)
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
	if err := s.verifyTOTP(ctx, code); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := s.verifyTOTP(ctx, code); err == nil {
		t.Fatal("replayed code must be rejected")
	}
	if err := s.verifyTOTP(ctx, "000000"); err == nil {
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

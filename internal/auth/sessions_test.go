// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// signIn creates a session from a browser holding cookies (nil = new
// browser), over HTTPS.
func signIn(t *testing.T, s *Service, remember bool, cookies ...*http.Cookie) (map[string]*http.Cookie, *Session) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.TLS = &tls.ConnectionState{}
	return signInWith(t, s, r, MethodPasskey, remember, cookies...)
}

func signInWith(t *testing.T, s *Service, r *http.Request, method string, remember bool, cookies ...*http.Cookie) (map[string]*http.Cookie, *Session) {
	t.Helper()
	r.Header.Set("User-Agent", "test-browser")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	sess, err := s.createSession(rec, r, method, remember)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		out[c.Name] = c
	}
	return out, sess
}

func countSessions(t *testing.T, s *Service) int {
	var n int
	if err := s.st.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSignInAgainReplacesBrowserSession(t *testing.T) {
	s, _ := newTestService(t)
	first, _ := signIn(t, s, false)
	dev := first[DeviceCookie]
	if dev == nil || !dev.HttpOnly {
		t.Fatal("device cookie missing or readable by scripts")
	}
	signIn(t, s, false, dev)
	signIn(t, s, true, dev)
	if n := countSessions(t, s); n != 1 {
		t.Fatalf("same browser: %d sessions, want 1", n)
	}
	signIn(t, s, false) // another browser
	if n := countSessions(t, s); n != 2 {
		t.Fatalf("two browsers: %d sessions, want 2", n)
	}
}

func TestRememberedSessionOutlivesIdleLimit(t *testing.T) {
	s, st := newTestService(t)
	c, sess := signIn(t, s, true)
	if c[SessionCookie].Expires.Before(time.Now().Add(300 * 24 * time.Hour)) {
		t.Fatal("remembered session cookie should outlive the browser session")
	}
	// Unused for a day: well past session_idle, within session_remember.
	day := time.Now().Add(-24 * time.Hour).Unix()
	st.DB.Exec(`UPDATE sessions SET last_seen = ?, created_at = ? WHERE id_hash = ?`, day, day, sess.IDHash)
	s.lastSeen = map[string]time.Time{}
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(c[SessionCookie])
	got, err := s.lookup(r)
	if err != nil {
		t.Fatalf("remembered session expired: %v", err)
	}
	if time.Until(got.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("use should extend the session, expires in %v", time.Until(got.ExpiresAt))
	}
	// Unused for longer than session_remember: gone.
	old := time.Now().Add(-31 * 24 * time.Hour).Unix()
	st.DB.Exec(`UPDATE sessions SET last_seen = ? WHERE id_hash = ?`, old, sess.IDHash)
	s.lastSeen = map[string]time.Time{}
	if _, err := s.lookup(r); err == nil {
		t.Fatal("session unused for 31 days should expire")
	}
}

func TestPruneSessions(t *testing.T) {
	s, st := newTestService(t)
	_, idle := signIn(t, s, false)
	_, kept := signIn(t, s, true)
	old := time.Now().Add(-2 * time.Hour).Unix()
	st.DB.Exec(`UPDATE sessions SET last_seen = ?`, old)
	s.pruneSessions(context.Background())
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id_hash = ?`, idle.IDHash).Scan(&n)
	if n != 0 {
		t.Fatal("idle session should be pruned")
	}
	st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id_hash = ?`, kept.IDHash).Scan(&n)
	if n != 1 {
		t.Fatal("remembered session should be kept")
	}
}

func TestRevokeSession(t *testing.T) {
	s, _ := newTestService(t)
	_, cur := signIn(t, s, false)
	_, other := signIn(t, s, false)
	revoke := func(id string) int {
		r := httptest.NewRequest("DELETE", "/api/auth/sessions/"+id, nil)
		r.SetPathValue("id", id)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, cur))
		rec := httptest.NewRecorder()
		if err := s.handleRevoke(rec, r); err != nil {
			return 400
		}
		return rec.Code
	}
	if c := revoke(cur.IDHash[:12]); c == 200 {
		t.Fatal("revoking the current session must use sign out")
	}
	if c := revoke(other.IDHash[:12]); c != 200 {
		t.Fatalf("revoke other: %d", c)
	}
	if n := countSessions(t, s); n != 1 {
		t.Fatalf("%d sessions left, want 1", n)
	}
}

func TestRememberNeedsSecureContext(t *testing.T) {
	s, _ := newTestService(t)
	// Plain HTTP from another machine: the cookie could be read on the way.
	_, sess := signInWith(t, s, httptest.NewRequest("POST", "/api/auth/login", nil), MethodPasskey, true)
	if sess.Remember {
		t.Fatal("remember honoured over plain HTTP from the network")
	}
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	if _, sess := signInWith(t, s, r, MethodPasskey, true); !sess.Remember {
		t.Fatal("remember should work from loopback")
	}
	r = httptest.NewRequest("POST", "http://localhost:2207/api/auth/login", nil)
	if _, sess := signInWith(t, s, r, MethodPasskey, true); !sess.Remember {
		t.Fatal("remember should work on localhost")
	}
}

func TestRememberedSessionHardLimit(t *testing.T) {
	s, st := newTestService(t)
	c, sess := signIn(t, s, true)
	r := httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(c[SessionCookie])
	// Used daily for 80 days: may last 10 more days, not 30.
	created := time.Now().Add(-80 * 24 * time.Hour)
	st.DB.Exec(`UPDATE sessions SET created_at = ?, last_seen = ? WHERE id_hash = ?`,
		created.Unix(), time.Now().Add(-24*time.Hour).Unix(), sess.IDHash)
	s.lastSeen = map[string]time.Time{}
	got, err := s.lookup(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := created.Add(90 * 24 * time.Hour); got.ExpiresAt.After(want.Add(time.Second)) {
		t.Fatalf("expires %v, after the 90-day cap %v", got.ExpiresAt, want)
	}
	// 91 days after sign-in, in use and not yet expired in the database.
	st.DB.Exec(`UPDATE sessions SET created_at = ?, last_seen = ?, expires_at = ? WHERE id_hash = ?`,
		time.Now().Add(-91*24*time.Hour).Unix(), time.Now().Unix(), time.Now().Add(time.Hour).Unix(), sess.IDHash)
	if _, err := s.lookup(r); err == nil {
		t.Fatal("session older than session_remember_max should end")
	}
	if n := countSessions(t, s); n != 0 {
		t.Fatalf("%d sessions left, want 0", n)
	}
}

func TestPasswordOnlySessionPendingTOTP(t *testing.T) {
	s, st := newTestService(t)
	if !s.cfg.Auth.RequireTOTP {
		t.Fatal("test expects require_totp by default")
	}
	https := func() *http.Request {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.TLS = &tls.ConnectionState{}
		return r
	}
	_, cur := signInWith(t, s, https(), MethodPassword, true)
	if cur.Remember {
		t.Fatal("a password-only session must not be remembered")
	}
	_, other := signInWith(t, s, https(), MethodPassword, false)
	_, pk := signIn(t, s, false)
	if !s.limited(cur) || s.limited(pk) {
		t.Fatal("limited: password-only sessions only")
	}

	// Enable TOTP from cur.
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Wargo", AccountName: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := st.Seal(key.Secret())
	if err := st.Set(context.Background(), "totp", totpState{Secret: sealed}); err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(key.Secret(), time.Now())
	r := httptest.NewRequest("POST", "/api/auth/totp/enable", strings.NewReader(`{"code":"`+code+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, cur))
	if err := s.handleTOTPEnable(httptest.NewRecorder(), r); err != nil {
		t.Fatal(err)
	}
	// Even with TOTP on, a password-only session stays limited.
	if !s.limited(other) {
		t.Fatal("password-only session not limited once TOTP is on")
	}
	method := func(h string) string {
		var m string
		st.DB.QueryRow(`SELECT method FROM sessions WHERE id_hash = ?`, h).Scan(&m)
		return m
	}
	if m := method(cur.IDHash); m != MethodPasswordTOTP {
		t.Fatalf("enabling session method %q, want upgraded", m)
	}
	if m := method(other.IDHash); m != "" {
		t.Fatal("other password-only session should be deleted")
	}
	if m := method(pk.IDHash); m != MethodPasskey {
		t.Fatal("passkey session should be kept")
	}
}

func TestRevokeRejectsBadIDs(t *testing.T) {
	for _, id := range []string{"", "abc", "ABCDEF012345", "abcdef01234%", "abcdef01234g", "abcdef0123456"} {
		if shortSessionID(id) {
			t.Errorf("%q accepted", id)
		}
	}
	if !shortSessionID("abcdef012345") {
		t.Error("valid id rejected")
	}
}

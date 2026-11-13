// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/config"
)

func newTestServer(t *testing.T, bypass bool) *httptest.Server {
	t.Helper()
	cfg := config.Default()
	cfg.Auth.User = "tester"
	cfg.DataDir = t.TempDir()
	cfg.Dev.AuthBypass = bypass
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.http.Handler)
	t.Cleanup(func() {
		ts.Close()
		s.deps.Store.Close()
	})
	return ts
}

func TestSecurityHeadersAndAuthGate(t *testing.T) {
	ts := newTestServer(t, false)
	for path, want := range map[string]int{
		"/api/system/info":   http.StatusUnauthorized,
		"/api/capabilities":  http.StatusUnauthorized,
		"/ws/events":         http.StatusUnauthorized,
		"/api/auth/state":    http.StatusOK,
		"/some/client/route": http.StatusOK, // SPA fallback (or "not built" 503)
	} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		got := res.StatusCode
		if path == "/some/client/route" && got == http.StatusServiceUnavailable {
			got = http.StatusOK
		}
		if got != want {
			t.Errorf("%s: got %d want %d", path, got, want)
		}
		csp := res.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: missing CSP: %q", path, csp)
		}
		if res.Header.Get("X-Frame-Options") != "DENY" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing hardening headers", path)
		}
	}
}

func TestLoginRejectsCrossOrigin(t *testing.T) {
	ts := newTestServer(t, false)
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/login", strings.NewReader(`{"username":"x","password":"y"}`))
	req.Header.Set("Origin", "https://evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d", res.StatusCode)
	}
}

func TestDevBypassServesAPI(t *testing.T) {
	ts := newTestServer(t, true)
	res, err := http.Get(ts.URL + "/api/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("got %d", res.StatusCode)
	}
	var caps map[string]any
	json.NewDecoder(res.Body).Decode(&caps)
	if _, ok := caps["elevation"]; !ok {
		t.Fatalf("capabilities missing elevation: %v", caps)
	}
	res2, _ := http.Get(ts.URL + "/api/does-not-exist")
	res2.Body.Close()
	if res2.StatusCode != 404 {
		t.Fatalf("unknown API route: %d", res2.StatusCode)
	}
}

func TestNotificationsWired(t *testing.T) {
	ts := newTestServer(t, true)
	res, err := http.Get(ts.URL + "/api/notifications")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var snap map[string]json.RawMessage
	json.NewDecoder(res.Body).Decode(&snap)
	if res.StatusCode != 200 || string(snap["recent"]) != "[]" {
		t.Fatalf("GET /api/notifications: %d %v", res.StatusCode, snap)
	}
}

// DNS rebinding: a page on http://evil.example:2207 re-pointed at our
// address sends Host: evil.example:2207 (and a matching Origin). Even with
// the dev bypass on, nothing must be served for it.
func TestUnknownHostRejected(t *testing.T) {
	ts := newTestServer(t, true)
	for _, path := range []string{"/api/capabilities", "/api/auth/state", "/"} {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Host = "evil.example:2207"
		req.Header.Set("Origin", "http://evil.example:2207")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("%s: got %d", path, res.StatusCode)
		}
		if res.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: security headers missing on rejection", path)
		}
	}
}

func TestUnknownHostPageIsEscapedHTML(t *testing.T) {
	ts := newTestServer(t, true)
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.Host = "<script>x</script>.example"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusMisdirectedRequest || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("got %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if strings.Contains(string(b), "<script>") || !strings.Contains(string(b), "auth.origins") {
		t.Fatalf("page not escaped or missing guidance: %s", b)
	}
}

func TestPublicBodyLimit(t *testing.T) {
	ts := newTestServer(t, false)
	big := `{"username":"` + strings.Repeat("a", 1<<20) + `","password":"x"}`
	res, err := http.Post(ts.URL+"/api/auth/login", "application/json", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized unauthenticated body: %d", res.StatusCode)
	}
}

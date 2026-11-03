// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/config"
)

func TestSameServer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"http://nas:32400", "http://nas:32400/sub", true},
		{"http://NAS:32400", "http://nas:32400", true},
		{"http://nas", "http://nas:80", true},
		{"https://nas", "https://nas:443", true},
		{"http://nas:32400", "https://nas:32400", false},
		{"http://nas:32400", "http://nas:32401", false},
		{"http://nas:32400", "http://evil:32400", false},
		{"", "http://nas", false},
	} {
		if got := sameServer(c.a, c.b); got != c.want {
			t.Errorf("sameServer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// recorder is a server that remembers the credentials it was sent.
func recorder(t *testing.T) (*httptest.Server, *atomic.Value) {
	var got atomic.Value
	got.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range []string{"X-Plex-Token", "X-Api-Key", "Authorization"} {
			if v := r.Header.Get(h); v != "" {
				got.Store(v)
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// leaked fails if a saved secret reached the recorder.
func leaked(t *testing.T, got *atomic.Value) {
	t.Helper()
	v := got.Load().(string)
	for _, secret := range []string{plexTok, "secretkey0123456789", "Basic " + base64.StdEncoding.EncodeToString([]byte("u:s3cret")), "Basic " + base64.StdEncoding.EncodeToString([]byte(":s3cret"))} {
		if v == secret {
			t.Fatalf("saved secret sent to another server: %q", v)
		}
	}
}

func TestSavedSecretStaysWithItsServer(t *testing.T) {
	fp := newFakePlex(t)
	pw := newFakeProwlarr(t, false)
	cfg := config.Config{}
	cfg.Integrations.Prowlarr = config.Prowlarr{URL: pw.URL, APIKey: "secretkey0123456789"}
	cfg.Integrations.Transmission = config.Transmission{URL: "127.0.0.1:1", Username: "u", Password: "s3cret"}
	cfg.Integrations.Plex = config.Plex{URL: fp.URL, Token: plexTok}
	h := newHarness(t, cfg)
	evil, got := recorder(t)

	// Testing another address needs the secret entered.
	for name, msg := range map[string]string{"plex": "Enter the token to test a different address", "prowlarr": "Enter the API key to test a different address"} {
		w, out := h.do(t, "POST", "/api/integrations/"+name+"/test", map[string]any{"url": evil.URL})
		if w.Code != 400 || out["message"] != msg {
			t.Fatalf("%s test elsewhere: %d %v", name, w.Code, out)
		}
	}
	// Transmission may have no password: the test goes out without one.
	h.do(t, "POST", "/api/integrations/transmission/test", map[string]any{"url": evil.URL})
	leaked(t, got)
	// The saved server still works with the saved secret.
	if _, out := h.do(t, "POST", "/api/integrations/plex/test", map[string]any{"url": fp.URL + "/"}); out["ok"] != true {
		t.Fatalf("same server: %v", out)
	}

	// Saving another address without a secret drops the saved one.
	if _, out := h.do(t, "PUT", "/api/integrations/plex", map[string]any{"url": evil.URL}); out["hasToken"] != false || out["configured"] != false {
		t.Fatalf("plex moved: %v", out)
	}
	if _, out := h.do(t, "PUT", "/api/integrations/prowlarr", map[string]any{"url": evil.URL}); out["hasApiKey"] != false {
		t.Fatalf("prowlarr moved: %v", out)
	}
	if _, out := h.do(t, "PUT", "/api/integrations/transmission", map[string]any{"url": evil.URL, "username": "u"}); out["hasPassword"] != false {
		t.Fatalf("transmission moved: %v", out)
	}
	leaked(t, got)
	// A new secret with the new address is kept.
	if _, out := h.do(t, "PUT", "/api/integrations/plex", map[string]any{"url": fp.URL, "token": plexTok}); out["hasToken"] != true || out["available"] != true {
		t.Fatalf("plex with token: %v", out)
	}
}

func TestPlexActivitySharedAndFailureCached(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status/sessions" {
			calls.Add(1)
		}
		time.Sleep(50 * time.Millisecond)
		if fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"MediaContainer":{}}`))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, config.Config{})
	sealed, _ := h.st.Seal(plexTok)
	h.s.px = plexSettings{URL: srv.URL, TokenSealed: sealed}

	// Concurrent callers share one fetch.
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if w, _ := h.do(t, "GET", "/api/plex/activity", nil); w.Code != 200 {
				t.Errorf("activity: %d %s", w.Code, w.Body)
			}
		})
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d fetches, want 1", n)
	}

	// A failure is cached too, so a down server isn't asked on every call.
	fail.Store(true)
	h.s.plexAct.mu.Lock()
	h.s.plexAct.at = time.Time{}
	h.s.plexAct.mu.Unlock()
	for range 3 {
		if w, _ := h.do(t, "GET", "/api/plex/activity", nil); w.Code != http.StatusBadGateway {
			t.Fatalf("down: %d %s", w.Code, w.Body)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d fetches, want 2", n)
	}
}

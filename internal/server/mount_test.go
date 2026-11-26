// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// agentToken is what the fake mounted feature accepts, standing in for a
// valid agent token.
const agentToken = "wargo_valid-agent-token"

// fakeMounted is a feature with its own endpoint and an interest in the
// private API, like the MCP service.
type fakeMounted struct {
	api      http.Handler
	bodySeen int
}

func (f *fakeMounted) Register(*http.ServeMux) {}

func (f *fakeMounted) Mount() (string, http.Handler) {
	return "/agent", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+agentToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, err := io.ReadAll(r.Body)
		f.bodySeen = len(b)
		if err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
}

func (f *fakeMounted) SetAPI(h http.Handler) { f.api = h }

func withFeature(t *testing.T, f Feature) {
	t.Helper()
	old := features
	features = []Feature{f}
	t.Cleanup(func() { features = old })
}

func TestMountedFeatureRouting(t *testing.T) {
	fake := &fakeMounted{}
	withFeature(t, func(*Deps) (Registrar, error) { return fake, nil })
	ts := newTestServer(t, false)
	if fake.api == nil {
		t.Fatal("SetAPI not called")
	}

	post := func(path, host, body string) int {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		if host != "" {
			req.Host = host
		}
		req.Header.Set("Authorization", "Bearer "+agentToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// No session cookie needed: the feature authenticates on its own.
	if got := post("/agent", "", "{}"); got != http.StatusTeapot {
		t.Fatalf("mounted path: %d", got)
	}
	// Only the exact path is mounted.
	if got := post("/agent/x", "", "{}"); got == http.StatusTeapot {
		t.Fatal("subpath routed to the mounted handler")
	}
	// The Host check still applies.
	if got := post("/agent", "evil.example:2207", "{}"); got != http.StatusMisdirectedRequest {
		t.Fatalf("unknown host: %d", got)
	}
	// Bodies are capped.
	if got := post("/agent", "", strings.Repeat("a", 2<<20)); got != http.StatusRequestEntityTooLarge || fake.bodySeen > 1<<20 {
		t.Fatalf("oversized body: %d, read %d bytes", got, fake.bodySeen)
	}
}

// Review Focus 2: an agent token never authenticates the browser API or
// the event socket. (That a cookie plus a bearer is just the cookie session
// is TestRequireIgnoresBearer in internal/auth.)
func TestTokenRefusedOnAPI(t *testing.T) {
	fake := &fakeMounted{}
	withFeature(t, func(*Deps) (Registrar, error) { return fake, nil })
	ts := newTestServer(t, false)
	for _, path := range []string{"/api/capabilities", "/ws/events"} {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+agentToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s with a bearer token: %d", path, res.StatusCode)
		}
	}
}

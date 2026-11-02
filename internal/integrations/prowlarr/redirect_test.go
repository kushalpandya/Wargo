// SPDX-License-Identifier: AGPL-3.0-only

package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A redirect to another host must not carry the API key there.
func TestAPIKeyNotForwardedOnRedirect(t *testing.T) {
	var leaked atomic.Bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			leaked.Store(true)
		}
		w.Write([]byte(`{"appName":"Prowlarr","version":"1"}`))
	}))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "secret-key").Status(context.Background()); err == nil {
		t.Fatal("cross-host redirect was followed")
	}
	if leaked.Load() {
		t.Fatal("API key was sent to the redirect target")
	}
}

func TestSameHostRedirectFollowed(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/status" {
			http.Redirect(w, r, "/prowlarr/api/v1/system/status", http.StatusMovedPermanently)
			return
		}
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"appName":"Prowlarr","version":"1.2"}`))
	}))
	defer srv.Close()
	st, err := New(srv.URL, "k").Status(context.Background())
	if err != nil || st.Version != "1.2" {
		t.Fatal(st, err)
	}
}

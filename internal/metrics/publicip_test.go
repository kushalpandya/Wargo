// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicIPFallbackAndCache(t *testing.T) {
	calls := 0
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("<html>not an ip</html>"))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("203.0.113.7\n"))
	}))
	defer good.Close()

	p := &publicIP{client: good.Client(), urls: []string{bad.URL, good.URL}}
	got := p.get(context.Background())
	if got.IP != "203.0.113.7" || got.Error != "" {
		t.Fatalf("got %+v", got)
	}
	p.get(context.Background())
	if calls != 2 {
		t.Fatalf("expected the result to be cached, %d calls", calls)
	}
}

func TestPublicIPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	p := &publicIP{client: srv.Client(), urls: []string{srv.URL}}
	if got := p.get(context.Background()); got.IP != "" || got.Error == "" {
		t.Fatalf("got %+v", got)
	}
}

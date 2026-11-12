// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAManifestType(t *testing.T) {
	files := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html>")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Wargo"}`)},
	}
	w := httptest.NewRecorder()
	spaHandler(files).ServeHTTP(w, httptest.NewRequest("GET", "/manifest.webmanifest", nil))
	if ct := w.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("manifest served as %q", ct)
	}
}

func TestSPAServiceWorker(t *testing.T) {
	files := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html>")},
		"sw.js":      {Data: []byte("self.addEventListener('fetch', () => {})")},
	}
	w := httptest.NewRecorder()
	spaHandler(files).ServeHTTP(w, httptest.NewRequest("GET", "/sw.js", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("sw.js served as %q", ct)
	}
	// The browser must check the worker against the server on every launch.
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control %q", cc)
	}

	// Without a worker (dev builds, other bases) it is a 404, never index.html.
	delete(files, "sw.js")
	w = httptest.NewRecorder()
	spaHandler(files).ServeHTTP(w, httptest.NewRequest("GET", "/sw.js", nil))
	if w.Code != 404 {
		t.Errorf("missing sw.js: status %d", w.Code)
	}
}

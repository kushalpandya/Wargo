// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http/httptest"
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

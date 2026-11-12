// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnsupportedCapabilities(t *testing.T) {
	u := Unsupported("events")
	k, v := u.(Capable).Capabilities(context.Background())
	m := v.(map[string]any)
	if k != "events" || m["supported"] != false || m["available"] != false {
		t.Fatal(k, m)
	}
	mux := http.NewServeMux()
	u.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/system/events", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatal(rec.Code)
	}
}

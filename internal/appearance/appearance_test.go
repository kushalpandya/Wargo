// SPDX-License-Identifier: AGPL-3.0-only

package appearance

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestSettings(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mux := http.NewServeMux()
	New(st, audit.New(st, httpx.NewProxyTrust(nil))).Register(mux)
	do := func(method string, body any) (int, Settings) {
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/appearance", bytes.NewReader(b)))
		var v Settings
		json.Unmarshal(w.Body.Bytes(), &v)
		return w.Code, v
	}
	if _, v := do("GET", nil); v.Accent != "periwinkle" {
		t.Fatalf("default: %+v", v)
	}
	if code, _ := do("PUT", map[string]string{"accent": "neon"}); code != 400 {
		t.Fatalf("unknown accent accepted: %d", code)
	}
	if code, _ := do("PUT", map[string]string{"accent": "mono"}); code != 200 {
		t.Fatalf("put: %d", code)
	}
	if _, v := do("GET", nil); v.Accent != "mono" {
		t.Fatalf("not saved: %+v", v)
	}
}

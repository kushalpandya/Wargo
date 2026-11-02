// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfContainerGuard(t *testing.T) {
	selfID := strings.Repeat("e", 64)
	f := &fakeDocker{containers: map[string]*fakeContainer{
		selfID: appContainer(selfID, "wargo", "host"),
	}}
	s := startFake(t, f)
	s.selfID = selfID
	mux := http.NewServeMux()
	s.Register(mux)

	l, err := s.List(context.Background(), true)
	if err != nil || len(l) != 1 || !l[0].Self {
		t.Fatalf("self not marked: %+v %v", l, err)
	}
	for _, path := range []string{"/api/containers/wargo/stop", "/api/containers/" + selfID[:12] + "/restart", "/api/containers/wargo/update"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "self_container") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/stop") || strings.HasSuffix(c, "/restart") {
			t.Fatalf("container was touched: %v", f.calls)
		}
	}
}

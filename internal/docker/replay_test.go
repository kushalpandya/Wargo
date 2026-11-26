// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/store"
)

// An agent's approved update_container request (strategy auto) is first
// replayed without a credential: the approval page learns from the answer
// whether it needs one. An update that needs a credential (an elevated
// compose run) must answer elevation_required before it records, claims or
// starts anything; one that needs none runs, since the owner has already
// approved it.
func TestAdminReplayWithoutCredentialHasNoSideEffects(t *testing.T) {
	composeID := strings.Repeat("3", 64)
	plainID := strings.Repeat("4", 64)
	compose := appContainer(composeID, "app", "p_default")
	compose.inspect["Config"].(map[string]any)["Labels"] = map[string]any{
		labelProject: "p", labelService: "app", labelWorkingDir: "/nonexistent/p",
		// Not readable by Wargo: only an elevated compose run could use it.
		labelConfig: "/nonexistent/p/compose.yaml",
	}
	plain := appContainer(plainID, "plain", "bridge")
	plain.inspect["Config"].(map[string]any)["Labels"] = map[string]any{}
	f := &fakeDocker{newImageID: "sha256:new", containers: map[string]*fakeContainer{composeID: compose, plainID: plain}}
	s := startFake(t, f)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	al := audit.New(st, httpx.NewProxyTrust(nil))
	s.audit = al
	s.elevate = elevate.New(al, httpx.NewProxyTrust(nil), auth.NewThrottle(), "")
	s.composeAvailable = func() bool { return true }
	mux := http.NewServeMux()
	s.Register(mux)
	replay := func(id, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/containers/"+id+"/update", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, r)
		return w
	}

	w := replay(composeID, `{"strategy":"compose"}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "elevation_required") {
		t.Fatalf("elevated compose update: %d %s", w.Code, w.Body)
	}
	if n := len(s.jobs.List()); n != 0 {
		t.Fatalf("%d jobs started before the credential", n)
	}
	s.mu.Lock()
	claimed := len(s.updating)
	s.mu.Unlock()
	if claimed != 0 {
		t.Fatal("update claimed before the credential")
	}
	if entries := auditCount(t, st); entries != 0 {
		t.Fatalf("%d audit entries before the credential", entries)
	}
	f.mu.Lock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, "POST ") || strings.HasPrefix(c, "DELETE ") {
			t.Errorf("daemon changed before the credential: %s", c)
		}
	}
	f.mu.Unlock()

	// What update_container stores: a plain recreate, which needs no
	// credential in the UI either.
	w = replay(plainID, `{"strategy":"auto"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "jobId") {
		t.Fatalf("plain update: %d %s", w.Code, w.Body)
	}
	waitJobs(t, s.jobs)
}

func auditCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitJobs(t *testing.T, jm *jobs.Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		done := true
		for _, j := range jm.List() {
			done = done && j.FinishedAt != nil
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("update job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

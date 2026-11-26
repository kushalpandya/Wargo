// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/audit"
)

func do(mux *http.ServeMux, method, path, body string, agent bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if agent {
		r = r.WithContext(audit.WithAgent(r.Context(), audit.AgentRef{ID: "a1", Name: "A", Tool: "t"}))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestAgentAPIRefusesAgentContext(t *testing.T) {
	_, mux := endpointService(t, nil, nil)
	for _, c := range [][3]string{
		{"GET", "/api/mcp/settings", ""},
		{"PUT", "/api/mcp/settings", `{"enabled":true,"network":"local","approvalMinutes":15}`},
		{"GET", "/api/mcp/agents", ""},
		{"POST", "/api/mcp/agents", `{"name":"x","level":"admin"}`},
		{"PATCH", "/api/mcp/agents/abc", `{"level":"admin"}`},
		{"DELETE", "/api/mcp/agents/abc", ""},
	} {
		if w := do(mux, c[0], c[1], c[2], true); w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", c[0], c[1], w.Code)
		}
	}
	if w := do(mux, "GET", "/api/mcp/agents", "", false); w.Code != 200 {
		t.Fatalf("user list: %d", w.Code)
	}
}

func TestSettingsAPI(t *testing.T) {
	s, mux := endpointService(t, nil, nil)
	w := do(mux, "GET", "/api/mcp/settings", "", false)
	var got Settings
	json.Unmarshal(w.Body.Bytes(), &got)
	if got != (Settings{false, "local", 15}) {
		t.Fatalf("%s", w.Body)
	}
	if w := do(mux, "PUT", "/api/mcp/settings", `{"enabled":true,"network":"lan","approvalMinutes":15}`, false); w.Code != 400 {
		t.Fatalf("bad network: %d", w.Code)
	}
	if w := do(mux, "PUT", "/api/mcp/settings", `{"enabled":true,"network":"anywhere","approvalMinutes":30}`, false); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if k, v := s.Capabilities(t.Context()); k != "mcp" || v.(map[string]any)["enabled"] != true {
		t.Fatalf("capabilities %s %v", k, v)
	}
	var action string
	s.st.DB.QueryRow(`SELECT action FROM audit_log WHERE action = 'mcp.settings'`).Scan(&action)
	if action != "mcp.settings" {
		t.Fatal("settings change not audited")
	}
}

func TestAgentAPILifecycle(t *testing.T) {
	s, mux := endpointService(t, nil, nil)
	w := do(mux, "POST", "/api/mcp/agents", `{"name":"Bot","level":"operate"}`, false)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var out struct {
		Agent Agent  `json:"agent"`
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if !strings.HasPrefix(out.Token, "wargo_") || out.Agent.ID == "" {
		t.Fatalf("%s", w.Body)
	}
	if w := do(mux, "GET", "/api/mcp/agents", "", false); strings.Contains(w.Body.String(), out.Token) || strings.Contains(w.Body.String(), "token") {
		t.Fatalf("list leaks token: %s", w.Body)
	}
	if w := do(mux, "PATCH", "/api/mcp/agents/"+out.Agent.ID, `{"name":"Bot2","level":"admin"}`, false); w.Code != 200 {
		t.Fatalf("patch: %d %s", w.Code, w.Body)
	}
	var target string
	s.st.DB.QueryRow(`SELECT target FROM audit_log WHERE action = 'mcp.agent.update'`).Scan(&target)
	if target != "Bot" {
		t.Fatalf("update entry names %q, not the agent", target)
	}
	if w := do(mux, "PATCH", "/api/mcp/agents/"+out.Agent.ID, `{"level":"god"}`, false); w.Code != 400 {
		t.Fatalf("bad level: %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/mcp/agents/"+out.Agent.ID, `{"name":"X","level":"god"}`, false); w.Code != 400 {
		t.Fatalf("name with bad level: %d", w.Code)
	}
	if w := do(mux, "GET", "/api/mcp/agents", "", false); !strings.Contains(w.Body.String(), `"Bot2"`) || strings.Contains(w.Body.String(), `"X"`) {
		t.Fatalf("a failed update changed the name: %s", w.Body)
	}
	if w := do(mux, "PATCH", "/api/mcp/agents/nope", `{"name":"x"}`, false); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
	if w := do(mux, "POST", "/api/mcp/agents", `{"name":"","level":"read"}`, false); w.Code != 400 {
		t.Fatalf("empty name: %d", w.Code)
	}
	if w := do(mux, "DELETE", "/api/mcp/agents/"+out.Agent.ID, "", false); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if w := do(mux, "DELETE", "/api/mcp/agents/"+out.Agent.ID, "", false); w.Code != 404 {
		t.Fatalf("delete again: %d", w.Code)
	}
}

func TestCreateIsAudited(t *testing.T) {
	s, mux := endpointService(t, nil, nil)
	w := do(mux, "POST", "/api/mcp/agents", `{"name":"Bot","level":"read"}`, false)
	var out struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &out)
	var target, details, source string
	err := s.st.DB.QueryRow(`SELECT target, COALESCE(details,''), source FROM audit_log WHERE action = 'mcp.agent.create'`).Scan(&target, &details, &source)
	if err != nil {
		t.Fatal(err)
	}
	if target != "Bot" || source != audit.SourceUser {
		t.Fatalf("target %q source %q", target, source)
	}
	if out.Token == "" || strings.Contains(details, out.Token) || strings.Contains(details, "wargo_") {
		t.Fatalf("token in details: %s", details)
	}
}

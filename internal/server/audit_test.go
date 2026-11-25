// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestRequestIDHeader(t *testing.T) {
	ts := newTestServer(t, false)
	a, err := http.Get(ts.URL + "/api/auth/state")
	if err != nil {
		t.Fatal(err)
	}
	a.Body.Close()
	b, err := http.Get(ts.URL + "/api/auth/state")
	if err != nil {
		t.Fatal(err)
	}
	b.Body.Close()
	ida, idb := a.Header.Get(httpx.RequestIDHeader), b.Header.Get(httpx.RequestIDHeader)
	if len(ida) != 16 || ida == idb {
		t.Fatalf("request IDs %q %q", ida, idb)
	}
}

func TestPanicIsAudited(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	al := audit.New(st, httpx.NewProxyTrust(nil))
	h := httpx.RequestIDs(recoverer(al, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]int
		m["boom"]++ // nil map write
	})))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/files/rename?token=s3cret", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", w.Code)
	}
	var action, target, code, request, output string
	err = st.DB.QueryRow(`SELECT action, target, code, request, output FROM audit_log`).Scan(&action, &target, &code, &request, &output)
	if err != nil {
		t.Fatal(err)
	}
	if action != "server.panic" || target != "POST /api/files/rename" || code != "panic" || request != w.Header().Get(httpx.RequestIDHeader) {
		t.Fatalf("entry: %s %s %s %s", action, target, code, request)
	}
	// The stack starts at the code that panicked, not in the runtime.
	if !strings.Contains(output, "TestPanicIsAudited") || strings.Contains(output, "runtime/panic.go") || strings.Contains(output, "s3cret") {
		t.Fatalf("output: %s", output)
	}
}

func TestStartAndStopAudited(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.User = "tester"
	cfg.DataDir = t.TempDir()
	cfg.Listen = "127.0.0.1:0"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// Run writes wargo.start before it waits for ctx, so canceling at once
	// still records both.
	go func() { done <- s.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.DB.Query(`SELECT action, source, details FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, source, details string
		rows.Scan(&action, &source, &details)
		got = append(got, action+" "+source)
		if action == "wargo.start" && (!strings.Contains(details, `"mode":"native"`) || !strings.Contains(details, `"features"`)) {
			t.Errorf("start details: %s", details)
		}
	}
	if strings.Join(got, ",") != "wargo.start system,wargo.stop system" {
		t.Fatalf("entries: %v", got)
	}
}

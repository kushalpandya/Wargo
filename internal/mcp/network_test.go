// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalRule(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.9", "192.168.1.5", "fd00::1", "fe80::1", "169.254.1.1", "::ffff:192.168.1.5"} {
		if !Local(net.ParseIP(ip), nil) {
			t.Errorf("%s should be local", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "172.32.0.1", "2001:db8::1", "203.0.113.9"} {
		if Local(net.ParseIP(ip), nil) {
			t.Errorf("%s should not be local", ip)
		}
	}
	if Local(nil, nil) {
		t.Error("nil is local")
	}
}

func TestAllowedNetworksFromConfig(t *testing.T) {
	s, _ := endpointService(t, nil, []string{"8.8.8.0/24"})
	enable(t, s, NetworkLocal)
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.RemoteAddr = "8.8.8.8:4000"
	if !s.admits(r) {
		t.Fatal("8.8.8.8 refused with allowed_networks 8.8.8.0/24")
	}
	r.RemoteAddr = "8.8.4.4:4000"
	if s.admits(r) {
		t.Fatal("8.8.4.4 admitted")
	}
}

// Review Focus 1: cloudflared connects from 127.0.0.1 and names the real
// client in CF-Connecting-IP. That is never local.
func TestForwardedFromUntrustedPeerIsNotLocal(t *testing.T) {
	post := func(s *Service, header string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Header.Set(header, "203.0.113.9")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	untrusted, _ := endpointService(t, nil, nil)
	enable(t, untrusted, NetworkLocal)
	for _, h := range []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		if w := post(untrusted, h); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"mcp_network"`) {
			t.Errorf("untrusted peer with %s under local: %d %s", h, w.Code, w.Body)
		}
	}

	trusted, _ := endpointService(t, []string{"127.0.0.1/32"}, nil)
	enable(t, trusted, NetworkLocal)
	if w := post(trusted, "CF-Connecting-IP"); w.Code != http.StatusForbidden {
		t.Errorf("trusted proxy forwarding a public client under local: %d", w.Code)
	}

	enable(t, untrusted, NetworkAnywhere)
	if w := post(untrusted, "CF-Connecting-IP"); w.Code != http.StatusUnauthorized {
		t.Errorf("anywhere should reach the token check: %d", w.Code)
	}
}

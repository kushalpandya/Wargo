// SPDX-License-Identifier: AGPL-3.0-only

package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrust(t *testing.T) {
	trust := NewProxyTrust([]string{"127.0.0.1/32"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 127.0.0.1")
	if got := trust.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("trusted proxy: %s", got)
	}
	// A client-supplied CF-Connecting-IP passed through a non-Cloudflare
	// proxy must not override the proxy's own X-Forwarded-For.
	r.Header.Set("CF-Connecting-IP", "198.51.100.7")
	if got := trust.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("CF-Connecting-IP overrode X-Forwarded-For: %s", got)
	}
	r.Header.Del("X-Forwarded-For")
	if got := trust.ClientIP(r); got != "198.51.100.7" {
		t.Fatalf("cloudflare header: %s", got)
	}
	r.Header.Set("CF-Connecting-IP", "not-an-ip")
	if got := trust.ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("garbage header accepted: %s", got)
	}
	// Spoofed left-most entries are ignored; the right-most untrusted wins.
	r.Header.Set("X-Forwarded-For", "1.1.1.1, 203.0.113.9")
	if got := trust.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("spoofed XFF: %s", got)
	}
	// Several XFF header lines are one list.
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	r.Header.Add("X-Forwarded-For", "203.0.113.10")
	if got := trust.ClientIP(r); got != "203.0.113.10" {
		t.Fatalf("multi-line XFF: %s", got)
	}
	r.RemoteAddr = "10.0.0.5:5000"
	if got := trust.ClientIP(r); got != "10.0.0.5" {
		t.Fatalf("untrusted peer must not be able to spoof: %s", got)
	}
}

func TestIsSecure(t *testing.T) {
	trust := NewProxyTrust([]string{"127.0.0.1/32"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Forwarded-Proto", "https")
	if !trust.IsSecure(r) {
		t.Fatal("trusted proxy https")
	}
	r.RemoteAddr = "192.168.1.2:1"
	if trust.IsSecure(r) {
		t.Fatal("untrusted X-Forwarded-Proto honoured")
	}
}

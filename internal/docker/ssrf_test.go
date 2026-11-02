// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for _, c := range []struct {
		ip   string
		want bool
	}{
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"100.100.1.1", false},     // CGNAT / Tailscale
		{"0.0.0.0", false},
		{"::", false},
		{"fe80::1", false},
		{"fd00::1", false},
		{"::ffff:127.0.0.1", false},
		{"::ffff:192.168.0.1", false},
		{"64:ff9b::a00:1", false},
		{"2002:c0a8:101::1", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
	} {
		if got := publicAddr(netip.MustParseAddr(c.ip)); got != c.want {
			t.Errorf("publicAddr(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestValidIconURL(t *testing.T) {
	for _, c := range []struct {
		u    string
		want bool
	}{
		{"https://cdn.jsdelivr.net/gh/x/y.png", true},
		{"https://cdn.example.com:443/y.png", true},
		{"http://cdn.example.com/y.png", false},
		{"https://user:pw@cdn.example.com/y.png", false},
		{"https://cdn.example.com:8080/y.png", false},
		{"file:///etc/passwd", false},
		{"https:///nohost", false},
	} {
		if got := validIconURL(c.u); got != c.want {
			t.Errorf("validIconURL(%q) = %v, want %v", c.u, got, c.want)
		}
	}
}

// A CasaOS-style icon label pointing at a loopback/private service must not
// make the server connect to it (SSRF), even via redirects.
func TestLabelIconBlocksPrivateAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes(t))
	}))
	defer srv.Close()
	st := newIconStore(t.TempDir())
	// Trust the test certificate but keep the guarded dialer.
	st.labelClient.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	src := iconSource{URL: srv.URL + "/icon.png"} // https://127.0.0.1:port
	if _, err := st.resolve(context.Background(), []iconSource{src}); err == nil {
		t.Fatal("loopback icon URL was fetched")
	}
	if hits.Load() != 0 {
		t.Fatalf("server was contacted %d times", hits.Load())
	}
	for _, u := range []string{"https://localhost/x.png", "https://169.254.169.254/latest/meta-data", "https://[::1]/x.png"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if resp, err := st.labelClient.Do(req); err == nil {
			resp.Body.Close()
			t.Errorf("%s: request was allowed", u)
		}
	}
}

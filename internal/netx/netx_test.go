// SPDX-License-Identifier: AGPL-3.0-only

package netx

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func req(t *testing.T, raw string) *http.Request {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Request{URL: u}
}

func TestSameHostRedirects(t *testing.T) {
	check := SameHostRedirects(3)
	via := []*http.Request{req(t, "http://prowlarr:9696/api")}
	for raw, ok := range map[string]bool{
		"http://prowlarr:9696/x":     true,
		"https://prowlarr/x":         true, // upgrade on the same host name
		"http://prowlarr:9697/x":     false,
		"http://evil.example/x":      false,
		"http://127.0.0.1:9696/x":    false,
		"file:///etc/passwd":         false,
		"https://prowlarr.evil.com/": false,
	} {
		err := check(req(t, raw), via)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v", raw, err)
		}
	}
	if check(req(t, "http://prowlarr:9696/x"), make([]*http.Request, 3)) == nil {
		t.Error("redirect limit not enforced")
	}
}

func TestReadLimited(t *testing.T) {
	if b, err := ReadLimited(strings.NewReader("abc"), 3); err != nil || string(b) != "abc" {
		t.Fatal(b, err)
	}
	if _, err := ReadLimited(strings.NewReader("abcd"), 3); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestHTTPRedirects(t *testing.T) {
	check := HTTPRedirects(2)
	via := []*http.Request{req(t, "http://a/")}
	if check(req(t, "gopher://x/"), via) == nil {
		t.Fatal("non-http redirect allowed")
	}
	if check(req(t, "https://b/"), via) != nil {
		t.Fatal("http redirect refused")
	}
}

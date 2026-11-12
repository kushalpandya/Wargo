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
		"https://prowlarr:9696/x":    true, // upgrade on the same host and port
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

func TestSameHostRedirectsRequiresPort(t *testing.T) {
	check := SameHostRedirects(3)
	try := func(from, to string) error {
		a, _ := http.NewRequest("GET", from, nil)
		b, _ := http.NewRequest("GET", to, nil)
		return check(b, []*http.Request{a})
	}
	if err := try("http://h:32400/x", "https://h:8443/x"); !errors.Is(err, ErrRedirect) {
		t.Errorf("different port upgrade allowed: %v", err)
	}
	if err := try("http://h/x", "https://h/x"); err != nil {
		t.Errorf("default ports refused: %v", err)
	}
	if err := try("http://h:80/x", "https://h:443/x"); err != nil {
		t.Errorf("80 to 443 refused: %v", err)
	}
	if err := try("http://h:32400/x", "https://h:32400/x"); err != nil {
		t.Errorf("same port refused: %v", err)
	}
}

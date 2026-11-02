// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// privateSuffixes are DNS suffixes that public DNS never answers for, so an
// attacker cannot point them at Wargo for DNS rebinding.
var privateSuffixes = []string{".localhost", ".local", ".lan", ".home", ".home.arpa", ".internal", ".localdomain", ".intranet", ".test"}

// requestHostname returns r.Host without port and brackets, lower-cased.
func requestHostname(r *http.Request) string {
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
}

// HostAllowed reports whether r's Host header names this Wargo instance.
//
// This is the DNS-rebinding defence: a page on http://evil.example:2207
// whose name is re-pointed at 127.0.0.1 is same-origin from the browser's
// point of view and its Origin matches its Host, so origin checks alone
// can't stop it. Such a page always carries the attacker's own public
// hostname in Host, which is never in this list:
//
//   - IP literals (rebinding needs a name), "localhost" and names below
//     suffixes public DNS doesn't serve (.local, .lan, .home.arpa, ...),
//   - single-label names ("nas"), which public DNS can't serve either,
//   - this machine's hostname,
//   - the hosts of auth.origins and dev.origins.
//
// Reverse proxies must therefore forward the public hostname listed in
// auth.origins (nginx: proxy_set_header Host $host).
func (s *Service) HostAllowed(r *http.Request) bool {
	h := requestHostname(r)
	if h == "" {
		return false
	}
	if net.ParseIP(h) != nil || h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, suf := range privateSuffixes {
		if strings.HasSuffix(h, suf) {
			return true
		}
	}
	if hn, err := os.Hostname(); err == nil && strings.EqualFold(strings.TrimSuffix(hn, "."), h) {
		return true
	}
	for _, o := range s.allOrigins() {
		if u, err := url.Parse(o); err == nil && strings.EqualFold(u.Hostname(), h) {
			return true
		}
	}
	return false
}

func (s *Service) allOrigins() []string {
	out := make([]string, 0, len(s.cfg.Auth.Origins)+len(s.cfg.Dev.Origins))
	out = append(out, s.cfg.Auth.Origins...)
	return append(out, s.cfg.Dev.Origins...)
}

// forwardedHeaders reveal a request that came through a proxy or tunnel.
var forwardedHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "Forwarded", "CF-Connecting-IP", "CF-Ray"}

// bypassAllowed limits dev.auth_bypass to direct local requests: a
// loopback listener can still be published by cloudflared or nginx on the
// same host, which would otherwise expose an unauthenticated root shell.
func (s *Service) bypassAllowed(r *http.Request) bool {
	for _, h := range forwardedHeaders {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	h := requestHostname(r)
	if ip := net.ParseIP(h); ip != nil {
		return true
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	for _, o := range s.cfg.Dev.Origins {
		if u, err := url.Parse(o); err == nil && strings.EqualFold(u.Hostname(), h) {
			return true
		}
	}
	return false
}

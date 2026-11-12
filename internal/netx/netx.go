// SPDX-License-Identifier: AGPL-3.0-only

// Package netx holds small HTTP client helpers shared by features that talk
// to user-configured or third-party web services (launcher icons and health
// checks, Prowlarr, Transmission): bounded body reads and redirect policies.
package netx

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrTooLarge is returned by ReadLimited when the body exceeds max bytes.
var ErrTooLarge = errors.New("response is too large")

// ReadLimited reads at most max bytes from r; a longer body yields
// ErrTooLarge (never a silently truncated result).
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Drain discards up to 64 KiB of the body (so the connection can be
// reused) and closes it.
func Drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// NoRedirects is a CheckRedirect that returns the redirect response itself.
func NoRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// ErrRedirect is returned when a redirect is refused by a policy below.
var ErrRedirect = errors.New("redirect not allowed")

// SameHostRedirects returns a CheckRedirect that follows at most max
// redirects, only to the original host and port (an http→https upgrade on
// the same host name is allowed). Use it for clients that send credentials
// in custom headers: net/http only strips Authorization and Cookie on
// cross-host redirects, so e.g. an X-Api-Key header would otherwise be
// forwarded to wherever a server (or a man in the middle on plain http)
// redirects to.
func SameHostRedirects(max int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return ErrRedirect
		}
		orig := via[0].URL
		u := req.URL
		switch {
		case u.Scheme == orig.Scheme && strings.EqualFold(u.Host, orig.Host):
			return nil
		case orig.Scheme == "http" && u.Scheme == "https" && strings.EqualFold(u.Hostname(), orig.Hostname()) && upgradePortOK(orig.Port(), u.Port()):
			return nil
		}
		return ErrRedirect
	}
}

// upgradePortOK reports whether an http to https upgrade keeps the same port,
// or goes from the default http port to the default https port.
func upgradePortOK(from, to string) bool {
	if from == to {
		return true
	}
	return (from == "" || from == "80") && (to == "" || to == "443")
}

// HTTPRedirects returns a CheckRedirect that follows at most max redirects
// and only to http(s) URLs.
func HTTPRedirects(max int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return http.ErrUseLastResponse
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return ErrRedirect
		}
		return nil
	}
}

// NewTransport returns a transport with every phase bounded by timeout and
// without proxy-from-environment (requests go where the user configured).
func NewTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		// Response headers are small for every service we talk to.
		MaxResponseHeaderBytes: 1 << 20,
	}
}

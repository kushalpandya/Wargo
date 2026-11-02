// SPDX-License-Identifier: AGPL-3.0-only

// Package httpx holds small HTTP helpers shared by all Wargo services.
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Error is an API error with a stable machine-readable code.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// With returns a copy of e carrying extra data for the client.
func (e *Error) With(data any) *Error {
	c := *e
	c.Data = data
	return &c
}

func (e *Error) Error() string { return e.Message }

func Errorf(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

var (
	ErrUnauthorized = Errorf(http.StatusUnauthorized, "unauthorized", "Authentication required")
	ErrForbidden    = Errorf(http.StatusForbidden, "forbidden", "Not allowed")
	ErrNotFound     = Errorf(http.StatusNotFound, "not_found", "Not found")
	ErrUnavailable  = Errorf(http.StatusServiceUnavailable, "unavailable", "Service unavailable")
)

// BadRequest returns a 400 error with the given message.
func BadRequest(msg string) *Error { return Errorf(http.StatusBadRequest, "bad_request", msg) }

// JSON writes v with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// OK writes v with status 200.
func OK(w http.ResponseWriter, v any) { JSON(w, http.StatusOK, v) }

// Fail writes err as a JSON error. Unknown errors become 500 and are logged.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		e = Errorf(http.StatusInternalServerError, "internal", err.Error())
	}
	JSON(w, e.Status, e)
}

// Decode reads a JSON body (max 1 MiB) into v.
func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return BadRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

// Handler adapts a func returning an error into an http.HandlerFunc.
func Handler(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			Fail(w, r, err)
		}
	}
}

// QueryInt parses an integer query parameter with a default.
func QueryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

// ProxyTrust decides which forwarded headers to believe.
type ProxyTrust struct{ nets []*net.IPNet }

func NewProxyTrust(cidrs []string) *ProxyTrust {
	t := &ProxyTrust{}
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			t.nets = append(t.nets, n)
		}
	}
	return t
}

func (t *ProxyTrust) trusted(ip net.IP) bool {
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the caller's IP, honouring proxy headers only from trusted peers.
func (t *ProxyTrust) ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !t.trusted(peer) {
		return host
	}
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		parts := strings.Split(v, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip != nil && !t.trusted(ip) {
				return ip.String()
			}
		}
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	return host
}

// IsSecure reports whether the original request used TLS.
func (t *ProxyTrust) IsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip != nil && t.trusted(ip) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

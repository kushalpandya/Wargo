// SPDX-License-Identifier: AGPL-3.0-only

// Package server wires every Wargo service into one HTTP server.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/metrics"
	"github.com/kushalpandya/wargo/internal/store"
	"github.com/kushalpandya/wargo/web"
)

// Version is set at build time.
var Version = "0.1.0-alpha"

// Registrar is implemented by every feature service.
type Registrar interface{ Register(mux *http.ServeMux) }

// Runner is implemented by services with background loops.
type Runner interface{ Run(ctx context.Context) }

// Capable services describe what they can do on this host; the UI uses it
// to show or hide navigation and actions. Key is the capability namespace.
type Capable interface {
	Capabilities(ctx context.Context) (key string, value any)
}

// Deps are shared building blocks handed to feature services.
type Deps struct {
	Cfg     config.Config
	Store   *store.Store
	Hub     *events.Hub
	Jobs    *jobs.Manager
	Audit   *audit.Log
	Elevate *elevate.Service
	Proxy   *httpx.ProxyTrust
	Auth    *auth.Service
}

// Feature constructs a service from shared deps.
type Feature func(d *Deps) (Registrar, error)

var features []Feature

// RegisterFeature adds a feature constructor (called from init in cmd/wargo).
func RegisterFeature(f Feature) { features = append(features, f) }

type Server struct {
	deps    *Deps
	http    *http.Server
	runners []Runner
	capable []Capable
}

func New(cfg config.Config) (*Server, error) {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	proxy := httpx.NewProxyTrust(cfg.Auth.TrustedProxies)
	hub := events.NewHub()
	al := audit.New(st, proxy)
	au := auth.New(cfg, st, al, proxy)
	hub.OriginPatterns = au.OriginPatterns()
	d := &Deps{
		Cfg: cfg, Store: st, Hub: hub, Jobs: jobs.NewManager(hub), Audit: al, Proxy: proxy, Auth: au,
		Elevate: elevate.New(al, proxy, auth.NewThrottle(), "").WithStore(st),
	}
	if host.Container() {
		d.Elevate.WithHostAuth(au)
	}
	s := &Server{deps: d}

	public := http.NewServeMux()
	private := http.NewServeMux()
	au.RegisterPublic(public)
	au.Register(private)
	d.Jobs.Register(private)
	al.Register(private)
	d.Elevate.Register(private)

	mc := metrics.NewCollector(hub, cfg.Metrics.Interval.Duration, cfg.Metrics.History.Duration)
	mc.Register(private)
	s.runners = append(s.runners, mc)

	for _, f := range features {
		svc, err := f(d)
		if err != nil {
			return nil, err
		}
		if svc == nil {
			continue
		}
		svc.Register(private)
		if r, ok := svc.(Runner); ok {
			s.runners = append(s.runners, r)
		}
		if c, ok := svc.(Capable); ok {
			s.capable = append(s.capable, c)
		}
	}

	private.HandleFunc("GET /api/capabilities", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]any{"elevation": d.Elevate.Methods(), "version": Version, "container": host.Container()}
		for _, c := range s.capable {
			k, v := c.Capabilities(r.Context())
			out[k] = v
		}
		httpx.OK(w, out)
	})

	private.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]string{"version": Version})
	})
	private.HandleFunc("GET /ws/events", hub.ServeWS)
	private.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, httpx.ErrNotFound)
	})

	protected := au.Require(private)
	spa := spaHandler(web.Dist())
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case isPublic(r):
			public.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/ws/"):
			protected.ServeHTTP(w, r)
		default:
			spa.ServeHTTP(w, r)
		}
	})

	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           recoverer(securityHeaders(proxy, root)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	return s, nil
}

func isPublic(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/auth/state", "/api/auth/login", "/api/auth/second-factor",
		"/api/auth/passkey/login/begin", "/api/auth/passkey/login/finish", "/api/auth/logout":
		return true
	}
	return false
}

// Run serves until ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	for _, r := range s.runners {
		go r.Run(ctx)
	}
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	slog.Info("wargo listening", "addr", "http://"+ln.Addr().String(), "version", Version)
	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.http.Shutdown(sctx)
		s.deps.Store.Close()
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func securityHeaders(proxy *httpx.ProxyTrust, next http.Handler) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'wasm-unsafe-eval'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"media-src 'self' blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		"worker-src 'self' blob:",
		"frame-src 'self' blob:",
		"frame-ancestors 'none'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	}, "; ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if proxy.IsSecure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "path", r.URL.Path, "err", v)
				httpx.Fail(w, r, httpx.Errorf(http.StatusInternalServerError, "internal", "internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

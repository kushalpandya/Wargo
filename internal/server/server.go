// SPDX-License-Identifier: AGPL-3.0-only

// Package server wires every Wargo service into one HTTP server.
package server

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
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
	"github.com/kushalpandya/wargo/internal/notify"
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

// Mounter services serve an endpoint of their own at exactly path, outside
// the browser session check: h authenticates its requests itself. The Host
// check still applies and request bodies are capped (mountedBody).
type Mounter interface {
	Mount() (path string, h http.Handler)
}

// APIUser services call the private API in process. SetAPI hands them the
// private mux once every feature has registered on it.
type APIUser interface{ SetAPI(h http.Handler) }

// mountedBody caps request bodies to mounted endpoints.
const mountedBody = 1 << 20

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
	// Notify takes occurrences from features for the notifications panel
	// and push. It is ready before any feature constructor runs.
	Notify *notify.Center
}

// Feature constructs a service from shared deps.
type Feature func(d *Deps) (Registrar, error)

var features []Feature

// RegisterFeature adds a feature constructor (called from init in cmd/wargo).
func RegisterFeature(f Feature) { features = append(features, f) }

type Server struct {
	deps     *Deps
	http     *http.Server
	runners  []Runner
	capable  []Capable
	features []string // the services that started, for wargo.start
}

func New(cfg config.Config) (*Server, error) {
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	proxy := httpx.NewProxyTrust(cfg.Auth.TrustedProxies)
	hub := events.NewHub()
	al := audit.New(st, proxy)
	al.Version = Version
	au := auth.New(cfg, st, al, proxy)
	hub.OriginPatterns = au.OriginPatterns()
	d := &Deps{
		Cfg: cfg, Store: st, Hub: hub, Jobs: jobs.NewManager(hub), Audit: al, Proxy: proxy, Auth: au,
		Elevate: elevate.New(al, proxy, auth.NewThrottle(), "").WithStore(st).WithTempDir(filepath.Join(cfg.DataDir, "tmp")),
	}
	if host.Container() {
		d.Elevate.WithHostAuth(au)
	}
	d.Jobs.Audit = al
	if d.Notify, err = notify.New(st, hub, au.IdleCutoff); err != nil {
		st.Close()
		return nil, err
	}
	d.Notify.Audit = al
	s := &Server{deps: d}

	public := http.NewServeMux()
	private := http.NewServeMux()
	au.RegisterPublic(public)
	au.Register(private)
	d.Jobs.Register(private)
	al.Register(private)
	d.Elevate.Register(private)
	d.Notify.Register(private)

	mc := metrics.NewCollector(hub, cfg.Metrics.Interval.Duration, cfg.Metrics.History.Duration)
	mc.ReportDisks = d.Notify.ReportDisks
	mc.Register(private)
	s.runners = append(s.runners, mc)
	s.capable = append(s.capable, mc)

	mounts := map[string]http.Handler{}
	var apiUsers []APIUser
	for _, f := range features {
		svc, err := f(d)
		if err != nil {
			al.Write(nil, audit.Event{Action: "wargo.feature_failed", Target: featureName(f), Err: err})
			return nil, err
		}
		if svc == nil {
			continue
		}
		svc.Register(private)
		s.features = append(s.features, strings.TrimPrefix(fmt.Sprintf("%T", svc), "*"))
		if r, ok := svc.(Runner); ok {
			s.runners = append(s.runners, r)
		}
		if c, ok := svc.(Capable); ok {
			s.capable = append(s.capable, c)
		}
		if m, ok := svc.(Mounter); ok {
			p, h := m.Mount()
			mounts[p] = h
		}
		if u, ok := svc.(APIUser); ok {
			apiUsers = append(apiUsers, u)
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

	for _, u := range apiUsers {
		u.SetAPI(private)
	}

	protected := au.Require(private)
	spa := spaHandler(web.Dist())
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !au.HostAllowed(r) {
			rejectHost(w, r)
			return
		}
		if h, ok := mounts[p]; ok {
			// No read deadline: responses may stream for a long time.
			r.Body = http.MaxBytesReader(w, r.Body, mountedBody)
			h.ServeHTTP(w, r)
			return
		}
		switch {
		case isPublic(r):
			// Unauthenticated: small bodies, and no slow-body tarpits.
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			public.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/ws/"):
			protected.ServeHTTP(w, r)
		default:
			spa.ServeHTTP(w, r)
		}
	})

	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           httpx.RequestIDs(recoverer(al, securityHeaders(proxy, root))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	return s, nil
}

var (
	hostWarnMu sync.Mutex
	hostWarned = map[string]bool{}
)

// rejectHost answers requests whose Host isn't this instance (DNS
// rebinding protection, see auth.HostAllowed).
func rejectHost(w http.ResponseWriter, r *http.Request) {
	hostWarnMu.Lock()
	warn := !hostWarned[r.Host] && len(hostWarned) < 64
	if warn {
		hostWarned[r.Host] = true
	}
	hostWarnMu.Unlock()
	if warn {
		slog.Warn("rejected request for an unknown host; add its origin to auth.origins (WARGO_ORIGINS) if it is yours",
			"host", r.Host, "path", r.URL.Path)
	}
	const msg = "This host name is not configured for Wargo. Add its URL to auth.origins (or WARGO_ORIGINS)."
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
		httpx.Fail(w, r, httpx.Errorf(http.StatusMisdirectedRequest, "unknown_host", msg))
		return
	}
	// Browsers navigating here get a readable page instead of JSON. The
	// host is shown escaped and only as text.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusMisdirectedRequest)
	origin := "https://" + r.Host
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Wargo: address not allowed</title>
<style>body{font:15px/1.5 system-ui,sans-serif;max-width:36rem;margin:12vh auto;padding:0 1.25rem;color:#1b2430;background:#f2f4f6}
@media(prefers-color-scheme:dark){body{color:#e2e7ed;background:#0a0d11}code,pre{background:#1a212a!important}}
h1{font-size:1.2rem}pre,code{background:#e4e8ec;border-radius:6px;padding:.15rem .35rem}pre{padding:.75rem;overflow:auto}</style>
<h1>This address isn't allowed</h1>
<p>Wargo only answers host names it knows, to protect against DNS-rebinding attacks.
To use <code>%s</code>, add it to <code>auth.origins</code> in <code>/etc/wargo/config.toml</code>
(Docker: the <code>WARGO_ORIGINS</code> variable) and restart Wargo:</p>
<pre>[auth]
origins = ["%s"]</pre>
<p>IP addresses, <code>localhost</code>, this machine's hostname and <code>.local</code>/<code>.lan</code> names always work.</p>
`, html.EscapeString(r.Host), html.EscapeString(origin))
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
	started := time.Now()
	al := s.deps.Audit
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		al.Write(nil, audit.Event{Action: "wargo.start", Target: Version, Err: err, Details: s.startDetails()})
		return err
	}
	slog.Info("wargo listening", "addr", "http://"+ln.Addr().String(), "version", Version)
	al.Write(nil, audit.Event{Action: "wargo.start", Target: Version, Details: s.startDetails()})
	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.http.Shutdown(sctx)
		if s.deps.Notify != nil {
			s.deps.Notify.Close()
		}
		al.Write(nil, audit.Event{Action: "wargo.stop", Target: Version, Duration: time.Since(started),
			Details: map[string]any{"reason": stopReason(ctx)}})
		s.deps.Store.Close()
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		al.Write(nil, audit.Event{Action: "wargo.stop", Target: Version, Err: err, Duration: time.Since(started)})
		return err
	}
}

// startDetails describes this instance for the wargo.start entry, so a
// later problem can be matched with what changed (version, host, mode).
func (s *Server) startDetails() map[string]any {
	cfg := s.deps.Cfg
	d := map[string]any{
		"mode":     map[bool]string{true: "container", false: "native"}[host.Container()],
		"os":       runtime.GOOS + "/" + runtime.GOARCH,
		"go":       runtime.Version(),
		"listen":   cfg.Listen,
		"user":     cfg.Auth.User,
		"features": s.features,
	}
	if osr := hostOSName(); osr != "" {
		d["host"] = osr
	}
	if k := kernelRelease(); k != "" {
		d["kernel"] = k
	}
	return d
}

// stopReason is why ctx ended ("signal: terminated" and the like).
func stopReason(ctx context.Context) string {
	if c := context.Cause(ctx); c != nil && !errors.Is(c, context.Canceled) {
		return c.Error()
	}
	return "stopped"
}

// hostOSName is the host's PRETTY_NAME from os-release ("" if unknown).
func hostOSName() string {
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		b, err := host.ReadFile(p)
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
				return strings.Trim(v, `"'`)
			}
		}
	}
	return ""
}

// kernelRelease is the running kernel (shared with the host in a container).
func kernelRelease() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// featureName is where a feature constructor is defined, for example
// "cmd/wargo/feature_pkg.go".
func featureName(f Feature) string {
	fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer())
	if fn == nil {
		return "unknown"
	}
	file, _ := fn.FileLine(fn.Entry())
	if i := strings.LastIndex(file, "/cmd/"); i >= 0 {
		return file[i+1:]
	}
	return filepath.Base(file)
}

func securityHeaders(proxy *httpx.ProxyTrust, next http.Handler) http.Handler {
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
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

// panicFrames is how many stack frames a crash entry keeps (each frame is
// a function line and a file line).
const panicFrames = 30

func recoverer(al *audit.Log, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				stack := debug.Stack()
				slog.Error("panic", "path", r.URL.Path, "request", httpx.RequestID(r.Context()), "err", v, "stack", string(stack))
				if al != nil {
					al.Write(r, audit.Event{Action: "server.panic", Target: r.Method + " " + r.URL.Path,
						Err: fmt.Errorf("%v", v), Code: "panic", Output: &audit.Output{Lines: stackLines(stack)}})
				}
				httpx.Fail(w, r, httpx.Errorf(http.StatusInternalServerError, "internal", "internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// stackLines keeps the frames from the one that panicked down, skipping
// the runtime and the recovery itself.
func stackLines(stack []byte) []string {
	lines := strings.Split(strings.TrimRight(string(stack), "\n"), "\n")
	// Frames after the last runtime/panic.go frame are the panicking code.
	start := 1
	for i, l := range lines {
		if strings.HasPrefix(l, "panic(") || strings.HasPrefix(l, "runtime.gopanic(") {
			start = i + 2
		}
	}
	if start >= len(lines) {
		start = 1
	}
	out := append([]string{lines[0]}, lines[start:]...)
	if len(out) > 1+2*panicFrames {
		out = out[:1+2*panicFrames]
	}
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

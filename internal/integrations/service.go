// SPDX-License-Identifier: AGPL-3.0-only

// Package integrations connects Wargo to Prowlarr (search), Transmission
// (downloads) and Plex (what is playing). Settings live in the store with
// secrets sealed.
package integrations

import (
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/integrations/plex"
	"github.com/kushalpandya/wargo/internal/integrations/prowlarr"
	"github.com/kushalpandya/wargo/internal/integrations/transmission"
	"github.com/kushalpandya/wargo/internal/store"
)

const (
	keyProwlarr     = "integrations.prowlarr"
	keyTransmission = "integrations.transmission"
	keyPlex         = "integrations.plex"
	healthTTL       = 30 * time.Second
	// Topic carries the Transmission torrent list while subscribed.
	Topic = "transmission"
)

type prowlarrSettings struct {
	URL          string `json:"url"`
	APIKeySealed string `json:"apiKeySealed,omitempty"`
}

type transmissionSettings struct {
	URL            string `json:"url"`
	Username       string `json:"username,omitempty"`
	PasswordSealed string `json:"passwordSealed,omitempty"`
}

type health struct {
	mu        sync.Mutex
	at        time.Time
	ok        bool
	version   string
	err       string
	hasTr     bool
	trClient  *prowlarr.TransmissionClient
	configKey string
}

// Service implements both integrations.
type Service struct {
	st     *store.Store
	hub    *events.Hub
	audit  *audit.Log
	socket string

	mu sync.Mutex
	pw prowlarrSettings
	tr transmissionSettings
	px plexSettings
	// trc is reused across calls so the session id survives.
	trc    *transmission.Client
	trcKey string

	pwHealth, trHealth, pxHealth health
	plexAct                      plexActivityCache

	cache       *prowlarr.Cache
	limiter     *limiter
	searchCache *responseCache

	idxMu  sync.Mutex
	idx    []prowlarr.Indexer
	idxAt  time.Time
	idxKey string

	// for tests
	now func() time.Time
}

// New builds the service. Settings are seeded from cfg.Integrations when the
// store has none yet.
func New(cfg config.Config, st *store.Store, hub *events.Hub, al *audit.Log) *Service {
	s := &Service{
		st: st, hub: hub, audit: al, socket: cfg.Docker.Socket,
		// Bounded by count; the sliding 6 h TTL keeps results usable while
		// the browser still shows them (the UI re-searches if one expires).
		cache:   prowlarr.NewCache(4000, 6*time.Hour),
		limiter: newLimiter(20, time.Minute),
		// a few recent responses (results reference entries in cache)
		searchCache: &responseCache{ttl: 5 * time.Minute, max: 16},
		now:         time.Now,
	}
	if hub != nil {
		hub.Sticky(Topic)
	}
	ctx := context.Background()
	if ok, err := st.Get(ctx, keyProwlarr, &s.pw); err != nil {
		slog.Warn("integrations: load prowlarr settings", "err", err)
	} else if !ok && cfg.Integrations.Prowlarr.URL != "" {
		if u, err := normalizeURL(cfg.Integrations.Prowlarr.URL, ""); err == nil {
			s.pw.URL = u
			s.pw.APIKeySealed, _ = st.Seal(cfg.Integrations.Prowlarr.APIKey)
			_ = st.Set(ctx, keyProwlarr, s.pw)
		}
	}
	if ok, err := st.Get(ctx, keyTransmission, &s.tr); err != nil {
		slog.Warn("integrations: load transmission settings", "err", err)
	} else if !ok && cfg.Integrations.Transmission.URL != "" {
		if u, err := normalizeURL(cfg.Integrations.Transmission.URL, "/transmission/rpc"); err == nil {
			s.tr.URL = u
			s.tr.Username = cfg.Integrations.Transmission.Username
			s.tr.PasswordSealed, _ = st.Seal(cfg.Integrations.Transmission.Password)
			_ = st.Set(ctx, keyTransmission, s.tr)
		}
	}
	if ok, err := st.Get(ctx, keyPlex, &s.px); err != nil {
		slog.Warn("integrations: load plex settings", "err", err)
	} else if !ok && cfg.Integrations.Plex.URL != "" {
		if u, err := normalizeURL(cfg.Integrations.Plex.URL, ""); err == nil {
			s.px.URL = u
			if tok := strings.TrimSpace(cfg.Integrations.Plex.Token); plexTokenRe.MatchString(tok) {
				s.px.TokenSealed, _ = st.Seal(tok)
			} else if tok != "" {
				slog.Warn("integrations: integrations.plex.token does not look like a Plex token; ignored")
			}
			_ = st.Set(ctx, keyPlex, s.px)
		}
	}
	return s
}

// normalizeURL validates an http(s) base URL. For Transmission (rpcPath set)
// a bare host or a /transmission/ base is completed to the RPC endpoint.
func normalizeURL(raw, rpcPath string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", httpx.BadRequest("URL must be an http(s) address like http://127.0.0.1:9696")
	}
	u.RawQuery, u.Fragment = "", ""
	if rpcPath != "" {
		p := strings.TrimRight(u.Path, "/")
		switch {
		case p == "":
			u.Path = rpcPath
		case strings.HasSuffix(p, "/rpc"):
			u.Path = p
		default:
			u.Path = p + "/rpc"
		}
	} else {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String(), nil
}

// sameServer reports whether two normalized URLs name the same scheme,
// host and port. A saved secret is only sent back to its own server.
func sameServer(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil || a == "" || b == "" {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return ua.Scheme == ub.Scheme && strings.EqualFold(ua.Hostname(), ub.Hostname()) && port(ua) == port(ub)
}

// keepSecret returns the sealed secret to keep when the URL changes from
// old to u without a new secret: the saved one for the same server, none
// for another.
func keepSecret(old, u, sealed string) string {
	if sameServer(old, u) {
		return sealed
	}
	return ""
}

// savedSecret unseals the saved secret for use with u: only when u is the
// saved server. ok is false when it exists but belongs to another server.
func (s *Service) savedSecret(saved, u, sealed string) (secret string, ok bool) {
	if sealed == "" {
		return "", true
	}
	if !sameServer(saved, u) {
		return "", false
	}
	secret, _ = s.st.Unseal(sealed)
	return secret, true
}

func (s *Service) prowlarrSettings() prowlarrSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pw
}

func (s *Service) transmissionSettings() transmissionSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tr
}

// prowlarrClient returns a client for the saved settings, or nil.
func (s *Service) prowlarrClient() *prowlarr.Client {
	p := s.prowlarrSettings()
	if p.URL == "" || p.APIKeySealed == "" {
		return nil
	}
	key, err := s.st.Unseal(p.APIKeySealed)
	if err != nil || key == "" {
		return nil
	}
	return prowlarr.New(p.URL, key)
}

func (s *Service) transmissionClient() *transmission.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tr
	if t.URL == "" {
		return nil
	}
	k := t.URL + "\x00" + t.Username + "\x00" + t.PasswordSealed
	if s.trc != nil && s.trcKey == k {
		return s.trc
	}
	pass, err := s.st.Unseal(t.PasswordSealed)
	if err != nil {
		return nil
	}
	s.trc, s.trcKey = transmission.New(t.URL, t.Username, pass), k
	return s.trc
}

func errString(err error) string { return prowlarr.Redact(plex.Redact(err.Error())) }

// checkProwlarr probes a client: status + download clients.
func checkProwlarr(ctx context.Context, c *prowlarr.Client) (version string, tc *prowlarr.TransmissionClient, err error) {
	st, err := c.Status(ctx)
	if err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(st.AppName, "prowlarr") && st.AppName != "" {
		return "", nil, errors.New("this is " + st.AppName + ", not Prowlarr")
	}
	if dcs, err := c.DownloadClients(ctx); err == nil {
		tc = prowlarr.FindTransmission(dcs)
	}
	return st.Version, tc, nil
}

// prowlarrHealth returns cached health (refreshing when stale).
func (s *Service) prowlarrHealth(ctx context.Context, force bool) *health {
	h := &s.pwHealth
	h.mu.Lock()
	defer h.mu.Unlock()
	p := s.prowlarrSettings()
	key := p.URL + "\x00" + p.APIKeySealed
	if !force && h.configKey == key && s.now().Sub(h.at) < healthTTL {
		return h
	}
	h.configKey, h.at = key, s.now()
	h.ok, h.version, h.err, h.hasTr, h.trClient = false, "", "", false, nil
	c := s.prowlarrClient()
	if c == nil {
		return h
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, tc, err := checkProwlarr(cctx, c)
	if err != nil {
		h.err = errString(err)
		return h
	}
	h.ok, h.version, h.trClient, h.hasTr = true, v, tc, tc != nil
	return h
}

func (s *Service) transmissionHealth(ctx context.Context, force bool) *health {
	h := &s.trHealth
	h.mu.Lock()
	defer h.mu.Unlock()
	t := s.transmissionSettings()
	key := t.URL + "\x00" + t.Username + "\x00" + t.PasswordSealed
	if !force && h.configKey == key && s.now().Sub(h.at) < healthTTL {
		return h
	}
	h.configKey, h.at = key, s.now()
	h.ok, h.version, h.err = false, "", ""
	c := s.transmissionClient()
	if c == nil {
		return h
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sess, err := c.Session(cctx)
	if err != nil {
		h.err = errString(err)
		return h
	}
	h.ok, h.version = true, sess.Version
	return h
}

// ProwlarrStatus is the browser-safe view of the Prowlarr integration.
type ProwlarrStatus struct {
	URL                   string                       `json:"url"`
	HasAPIKey             bool                         `json:"hasApiKey"`
	Configured            bool                         `json:"configured"`
	Available             bool                         `json:"available"`
	HasTransmissionClient bool                         `json:"hasTransmissionClient"`
	TransmissionClient    *prowlarr.TransmissionClient `json:"transmissionClient,omitempty"`
	Version               string                       `json:"version,omitempty"`
	Error                 string                       `json:"error,omitempty"`
}

// TransmissionStatus is the browser-safe view of the Transmission integration.
type TransmissionStatus struct {
	URL         string `json:"url"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
	Configured  bool   `json:"configured"`
	Available   bool   `json:"available"`
	Version     string `json:"version,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *Service) prowlarrStatus(ctx context.Context, force bool) ProwlarrStatus {
	p := s.prowlarrSettings()
	out := ProwlarrStatus{URL: p.URL, HasAPIKey: p.APIKeySealed != "", Configured: p.URL != "" && p.APIKeySealed != ""}
	if !out.Configured {
		return out
	}
	h := s.prowlarrHealth(ctx, force)
	h.mu.Lock()
	defer h.mu.Unlock()
	out.Available, out.Version, out.Error, out.HasTransmissionClient, out.TransmissionClient = h.ok, h.version, h.err, h.hasTr, h.trClient
	return out
}

func (s *Service) transmissionStatus(ctx context.Context, force bool) TransmissionStatus {
	t := s.transmissionSettings()
	out := TransmissionStatus{URL: t.URL, Username: t.Username, HasPassword: t.PasswordSealed != "", Configured: t.URL != ""}
	if !out.Configured {
		return out
	}
	h := s.transmissionHealth(ctx, force)
	h.mu.Lock()
	defer h.mu.Unlock()
	out.Available, out.Version, out.Error = h.ok, h.version, h.err
	return out
}

// --- registrars ---

// ProwlarrFeature exposes /api/integrations/* and /api/prowlarr/*.
type ProwlarrFeature struct{ *Service }

// TransmissionFeature exposes /api/transmission/* and the live topic.
type TransmissionFeature struct{ *Service }

func (s *Service) Prowlarr() *ProwlarrFeature         { return &ProwlarrFeature{s} }
func (s *Service) Transmission() *TransmissionFeature { return &TransmissionFeature{s} }

func (f *ProwlarrFeature) Capabilities(ctx context.Context) (string, any) {
	st := f.prowlarrStatus(ctx, false)
	return "prowlarr", map[string]any{
		"available": st.Available, "configured": st.Configured, "hasTransmissionClient": st.Available && st.HasTransmissionClient,
		"version": st.Version, "error": st.Error,
	}
}

func (f *TransmissionFeature) Capabilities(ctx context.Context) (string, any) {
	st := f.transmissionStatus(ctx, false)
	return "transmission", map[string]any{"available": st.Available, "configured": st.Configured, "version": st.Version, "error": st.Error}
}

func (f *ProwlarrFeature) Register(mux *http.ServeMux) {
	s := f.Service
	mux.HandleFunc("GET /api/integrations", httpx.Handler(s.handleStatus))
	mux.HandleFunc("PUT /api/integrations/prowlarr", httpx.Handler(s.handlePutProwlarr))
	mux.HandleFunc("PUT /api/integrations/transmission", httpx.Handler(s.handlePutTransmission))
	mux.HandleFunc("POST /api/integrations/{name}/test", httpx.Handler(s.handleTest))
	mux.HandleFunc("GET /api/integrations/discover", httpx.Handler(s.handleDiscover))
	mux.HandleFunc("POST /api/integrations/prowlarr/import-key", httpx.Handler(s.handleImportKey))

	mux.HandleFunc("GET /api/prowlarr/indexers", httpx.Handler(s.handleIndexers))
	mux.HandleFunc("GET /api/prowlarr/search", httpx.Handler(s.handleSearch))
	mux.HandleFunc("GET /api/prowlarr/results/{id}/magnet", httpx.Handler(s.handleMagnet))
	mux.HandleFunc("GET /api/prowlarr/results/{id}/torrent", httpx.Handler(s.handleTorrent))
	mux.HandleFunc("POST /api/prowlarr/results/{id}/queue", httpx.Handler(s.handleQueue))
}

func (f *TransmissionFeature) Register(mux *http.ServeMux) {
	s := f.Service
	mux.HandleFunc("GET /api/transmission/torrents", httpx.Handler(s.handleTorrents))
	mux.HandleFunc("POST /api/transmission/torrents", httpx.Handler(s.handleAdd))
	mux.HandleFunc("POST /api/transmission/torrents/{id}/start", httpx.Handler(s.handleStartStop(true)))
	mux.HandleFunc("POST /api/transmission/torrents/{id}/stop", httpx.Handler(s.handleStartStop(false)))
	mux.HandleFunc("DELETE /api/transmission/torrents/{id}", httpx.Handler(s.handleRemove))
}

// --- settings handlers ---

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) error {
	force := r.URL.Query().Get("refresh") == "1"
	httpx.OK(w, map[string]any{
		"prowlarr":     s.prowlarrStatus(r.Context(), force),
		"transmission": s.transmissionStatus(r.Context(), force),
		"plex":         s.plexStatus(r.Context(), force),
	})
	return nil
}

type prowlarrInput struct {
	URL    string  `json:"url"`
	APIKey *string `json:"apiKey"`
}

type transmissionInput struct {
	URL      string  `json:"url"`
	Username string  `json:"username"`
	Password *string `json:"password"`
}

func (s *Service) handlePutProwlarr(w http.ResponseWriter, r *http.Request) error {
	var in prowlarrInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := normalizeURL(in.URL, "")
	if err != nil {
		return err
	}
	key := ""
	if in.APIKey != nil {
		key = strings.TrimSpace(*in.APIKey)
	}
	s.mu.Lock()
	next := prowlarrSettings{URL: u, APIKeySealed: keepSecret(s.pw.URL, u, s.pw.APIKeySealed)}
	if key != "" {
		if next.APIKeySealed, err = s.st.Seal(key); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	err = s.save(r.Context(), keyProwlarr, u, next)
	if err == nil {
		s.pw = next
	}
	s.mu.Unlock()
	s.audit.Record(r, "integrations.prowlarr.update", u, err, map[string]any{"apiKeyChanged": key != ""})
	if err != nil {
		return err
	}
	httpx.OK(w, s.prowlarrStatus(r.Context(), true))
	return nil
}

func (s *Service) handlePutTransmission(w http.ResponseWriter, r *http.Request) error {
	var in transmissionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := normalizeURL(in.URL, "/transmission/rpc")
	if err != nil {
		return err
	}
	s.mu.Lock()
	next := transmissionSettings{URL: u, Username: strings.TrimSpace(in.Username), PasswordSealed: keepSecret(s.tr.URL, u, s.tr.PasswordSealed)}
	if in.Password != nil && *in.Password != "" {
		if next.PasswordSealed, err = s.st.Seal(*in.Password); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	err = s.save(r.Context(), keyTransmission, u, next)
	if err == nil {
		s.tr = next
	}
	s.mu.Unlock()
	s.audit.Record(r, "integrations.transmission.update", u, err, map[string]any{"username": next.Username, "passwordChanged": in.Password != nil && *in.Password != ""})
	if err != nil {
		return err
	}
	httpx.OK(w, s.transmissionStatus(r.Context(), true))
	return nil
}

// save stores an integration's settings; no URL removes them.
func (s *Service) save(ctx context.Context, key, u string, v any) error {
	if u == "" {
		return s.st.Delete(ctx, key)
	}
	return s.st.Set(ctx, key, v)
}

// TestResult is returned by POST /api/integrations/{name}/test.
type TestResult struct {
	OK                    bool                         `json:"ok"`
	Version               string                       `json:"version,omitempty"`
	Error                 string                       `json:"error,omitempty"`
	HasTransmissionClient bool                         `json:"hasTransmissionClient,omitempty"`
	TransmissionClient    *prowlarr.TransmissionClient `json:"transmissionClient,omitempty"`
	URL                   string                       `json:"url,omitempty"`
}

func (s *Service) handleTest(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch r.PathValue("name") {
	case "prowlarr":
		var in prowlarrInput
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &in); err != nil {
				return err
			}
		}
		saved := s.prowlarrSettings()
		u := saved.URL
		if in.URL != "" {
			var err error
			if u, err = normalizeURL(in.URL, ""); err != nil {
				return err
			}
		}
		key := ""
		if in.APIKey != nil {
			key = strings.TrimSpace(*in.APIKey)
		}
		if key == "" {
			var ok bool
			if key, ok = s.savedSecret(saved.URL, u, saved.APIKeySealed); !ok {
				return httpx.BadRequest("Enter the API key to test a different address")
			}
		}
		if u == "" || key == "" {
			return httpx.BadRequest("URL and API key are required")
		}
		v, tc, err := checkProwlarr(ctx, prowlarr.New(u, key))
		res := TestResult{OK: err == nil, Version: v, HasTransmissionClient: tc != nil, TransmissionClient: tc, URL: u}
		if err != nil {
			res.Error = errString(err)
		}
		httpx.OK(w, res)
	case "transmission":
		var in transmissionInput
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &in); err != nil {
				return err
			}
		}
		saved := s.transmissionSettings()
		u, user := saved.URL, saved.Username
		if in.URL != "" {
			var err error
			if u, err = normalizeURL(in.URL, "/transmission/rpc"); err != nil {
				return err
			}
			user = strings.TrimSpace(in.Username)
		}
		// The saved password only goes to its own server; another
		// address is tested without one unless it is entered.
		pass := ""
		if in.Password != nil && *in.Password != "" {
			pass = *in.Password
		} else {
			pass, _ = s.savedSecret(saved.URL, u, saved.PasswordSealed)
		}
		if u == "" {
			return httpx.BadRequest("URL is required")
		}
		sess, err := transmission.New(u, user, pass).Session(ctx)
		res := TestResult{OK: err == nil, URL: u}
		if err != nil {
			res.Error = errString(err)
		} else {
			res.Version = sess.Version
		}
		httpx.OK(w, res)
	case "plex":
		return s.testPlex(ctx, w, r)
	default:
		return httpx.ErrNotFound
	}
	return nil
}

func (s *Service) discover(ctx context.Context) *Discovery {
	d := &Discovery{Prowlarr: []Candidate{}, Transmission: []Candidate{}, Plex: []Candidate{}}
	cs, err := listContainers(ctx, s.socket)
	if err != nil {
		d.Docker.Error = err.Error()
	} else {
		d.Docker.Available = true
		d.Prowlarr, d.Transmission, d.Plex = discover(cs)
	}
	return d
}

func (s *Service) handleDiscover(w http.ResponseWriter, r *http.Request) error {
	d := s.discover(r.Context())
	if st := s.prowlarrStatus(r.Context(), false); st.Available && st.TransmissionClient != nil {
		tc := st.TransmissionClient
		d.TransmissionFromProwlarr = &TransmissionHint{Name: tc.Name, URL: tc.URL(), Host: tc.Host, Port: tc.Port, Username: tc.Username}
	}
	httpx.OK(w, d)
	return nil
}

var apiKeyRe = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)

// parseAPIKey extracts <ApiKey> from a Prowlarr config.xml.
func parseAPIKey(r io.Reader) (string, error) {
	var cfg struct {
		APIKey string `xml:"ApiKey"`
	}
	if err := xml.NewDecoder(r).Decode(&cfg); err != nil {
		return "", errors.New("config.xml could not be parsed")
	}
	k := strings.TrimSpace(cfg.APIKey)
	if !apiKeyRe.MatchString(k) {
		return "", errors.New("config.xml has no valid <ApiKey>")
	}
	return k, nil
}

// secretImport describes reading an integration's secret from the config
// file of a detected container.
type secretImport struct {
	action      string // audit action
	name        string // "Prowlarr"
	what        string // "the API key"
	notDetected string
	candidates  func(*Discovery) []Candidate
	parse       func(io.Reader) (string, error)
	savedURL    func() string
	// apply seals and saves secret with URL u.
	apply func(ctx context.Context, u, secret string) error
}

// importSecret handles POST {configPath, url}: configPath must be a
// detected candidate's config file. The URL is the one given, else the
// saved one, else the detected one.
func (s *Service) importSecret(r *http.Request, im secretImport) (err error) {
	var in struct {
		ConfigPath string `json:"configPath"`
		URL        string `json:"url"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	want := filepath.Clean(in.ConfigPath)
	var details map[string]any
	defer func() { s.audit.Record(r, im.action, want, err, details) }()
	var match *Candidate
	cands := im.candidates(s.discover(r.Context()))
	for i := range cands {
		if cands[i].ConfigPath != "" && cands[i].ConfigPath == want {
			match = &cands[i]
		}
	}
	if match == nil {
		return httpx.Errorf(http.StatusForbidden, "not_detected", im.notDetected)
	}
	details = map[string]any{"container": match.Container}
	secret, err := readConfigSecret(match.configRoot, match.configRel, im.parse)
	if err != nil {
		return httpx.Errorf(http.StatusUnprocessableEntity, "unreadable", "Could not read "+im.what+": "+err.Error())
	}
	u := cmp.Or(strings.TrimSpace(in.URL), im.savedURL(), match.URL)
	if u, err = normalizeURL(u, ""); err != nil {
		return err
	}
	if u == "" {
		return httpx.BadRequest("Enter the " + im.name + " URL first")
	}
	return im.apply(r.Context(), u, secret)
}

func (s *Service) handleImportKey(w http.ResponseWriter, r *http.Request) error {
	err := s.importSecret(r, secretImport{
		action: "integrations.prowlarr.import_key", name: "Prowlarr", what: "the API key",
		notDetected: "That path is not the /config mount of a detected Prowlarr container",
		candidates:  func(d *Discovery) []Candidate { return d.Prowlarr },
		parse:       parseAPIKey,
		savedURL:    func() string { return s.prowlarrSettings().URL },
		apply: func(ctx context.Context, u, key string) error {
			sealed, err := s.st.Seal(key)
			if err != nil {
				return err
			}
			next := prowlarrSettings{URL: u, APIKeySealed: sealed}
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := s.save(ctx, keyProwlarr, u, next); err != nil {
				return err
			}
			s.pw = next
			return nil
		},
	})
	if err != nil {
		return err
	}
	httpx.OK(w, s.prowlarrStatus(r.Context(), true))
	return nil
}

// --- rate limiting ---

// limiter allows one search at a time (others wait for the slot) and at most
// max searches per window.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   []time.Time
	slot   chan struct{}
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, slot: make(chan struct{}, 1)}
}

// acquire waits (bounded by ctx) for the single search slot, then applies
// the rate limit. release must be called after a nil return.
func (l *limiter) acquire(ctx context.Context, now func() time.Time) error {
	wait, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	select {
	case l.slot <- struct{}{}:
	case <-wait.Done():
		return httpx.Errorf(http.StatusTooManyRequests, "search_busy", "Another search is still running")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now().Add(-l.window)
	i := 0
	for i < len(l.hits) && l.hits[i].Before(cut) {
		i++
	}
	l.hits = l.hits[i:]
	if len(l.hits) >= l.max {
		<-l.slot
		return httpx.Errorf(http.StatusTooManyRequests, "rate_limited", "Too many searches, wait a minute")
	}
	l.hits = append(l.hits, now())
	return nil
}

func (l *limiter) release() { <-l.slot }

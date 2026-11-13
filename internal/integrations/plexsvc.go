// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/integrations/plex"
)

const (
	plexActivityTTL = 10 * time.Second
	plexAccountsTTL = 10 * time.Minute
	plexHistoryN    = 8
	plexRecentN     = 8
)

var (
	errPlexNotConfigured = httpx.Errorf(http.StatusConflict, "not_configured", "Plex is not configured")
	errPlexToken         = httpx.Errorf(http.StatusConflict, "not_configured", "Couldn't decrypt the saved Plex token; enter it again")
)

type plexSettings struct {
	URL         string `json:"url"`
	TokenSealed string `json:"tokenSealed,omitempty"`
}

func (s *Service) plexSettings() plexSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.px
}

func (s *Service) plexClient() (*plex.Client, error) {
	p := s.plexSettings()
	if p.URL == "" || p.TokenSealed == "" {
		return nil, errPlexNotConfigured
	}
	tok, err := s.st.Unseal(p.TokenSealed)
	if err != nil || tok == "" {
		slog.Warn("plex: can't decrypt the saved token", "err", err)
		return nil, errPlexToken
	}
	return plex.New(p.URL, tok), nil
}

func (s *Service) plexHealth(ctx context.Context, force bool) *health {
	h := &s.pxHealth
	h.mu.Lock()
	defer h.mu.Unlock()
	p := s.plexSettings()
	key := p.URL + "\x00" + p.TokenSealed
	if !force && h.configKey == key && s.now().Sub(h.at) < healthTTL {
		return h
	}
	h.configKey, h.at = key, s.now()
	h.ok, h.version, h.err = false, "", ""
	c, err := s.plexClient()
	if err != nil {
		h.err = err.Error()
		return h
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := c.Check(cctx)
	h.version = v
	if err != nil {
		h.err = plexErrString(err)
		return h
	}
	h.ok = true
	return h
}

func plexErrString(err error) string {
	if errors.Is(err, plex.ErrUnauthorized) {
		return "Plex rejected the token"
	}
	return errString(err)
}

// PlexStatus is the browser-safe view of the Plex integration.
type PlexStatus struct {
	URL        string `json:"url"`
	HasToken   bool   `json:"hasToken"`
	Configured bool   `json:"configured"`
	Available  bool   `json:"available"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s *Service) plexStatus(ctx context.Context, force bool) PlexStatus {
	p := s.plexSettings()
	out := PlexStatus{URL: p.URL, HasToken: p.TokenSealed != "", Configured: p.URL != "" && p.TokenSealed != ""}
	if !out.Configured {
		return out
	}
	h := s.plexHealth(ctx, force)
	h.mu.Lock()
	defer h.mu.Unlock()
	out.Available, out.Version, out.Error = h.ok, h.version, h.err
	return out
}

// PlexFeature exposes /api/integrations/plex/* and /api/plex/*.
type PlexFeature struct{ *Service }

func (s *Service) Plex() *PlexFeature { return &PlexFeature{s} }

func (f *PlexFeature) Capabilities(ctx context.Context) (string, any) {
	st := f.plexStatus(ctx, false)
	return "plex", map[string]any{"available": st.Available, "configured": st.Configured, "version": st.Version, "error": st.Error}
}

func (f *PlexFeature) Register(mux *http.ServeMux) {
	s := f.Service
	mux.HandleFunc("PUT /api/integrations/plex", httpx.Handler(s.handlePutPlex))
	mux.HandleFunc("POST /api/integrations/plex/import-token", httpx.Handler(s.handleImportPlexToken))
	mux.HandleFunc("GET /api/plex/activity", httpx.Handler(s.handlePlexActivity))
	mux.HandleFunc("GET /api/plex/thumb", httpx.Handler(s.handlePlexThumb))
}

type plexInput struct {
	URL   string  `json:"url"`
	Token *string `json:"token"`
}

func (s *Service) handlePutPlex(w http.ResponseWriter, r *http.Request) error {
	var in plexInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	u, err := normalizeURL(in.URL, "")
	if err != nil {
		return err
	}
	tok := ""
	if in.Token != nil {
		tok = strings.TrimSpace(*in.Token)
	}
	if tok != "" && !plexTokenRe.MatchString(tok) {
		return httpx.BadRequest("That does not look like a Plex token")
	}
	s.mu.Lock()
	next := plexSettings{URL: u, TokenSealed: keepSecret(s.px.URL, u, s.px.TokenSealed)}
	if tok != "" {
		if next.TokenSealed, err = s.st.Seal(tok); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	err = s.save(r.Context(), keyPlex, u, next)
	if err == nil {
		s.px = next
	}
	s.mu.Unlock()
	s.audit.Record(r, "integrations.plex.update", u, err, map[string]any{"tokenChanged": tok != ""})
	if err != nil {
		return err
	}
	httpx.OK(w, s.plexStatus(r.Context(), true))
	return nil
}

func (s *Service) testPlex(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var in plexInput
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &in); err != nil {
			return err
		}
	}
	saved := s.plexSettings()
	u := saved.URL
	if in.URL != "" {
		var err error
		if u, err = normalizeURL(in.URL, ""); err != nil {
			return err
		}
	}
	tok := ""
	if in.Token != nil {
		tok = strings.TrimSpace(*in.Token)
	}
	if tok == "" {
		var ok bool
		if tok, ok = s.savedSecret(saved.URL, u, saved.TokenSealed); !ok {
			return httpx.BadRequest("Enter the token to test a different address")
		}
	}
	if u == "" || tok == "" {
		return httpx.BadRequest("URL and token are required")
	}
	v, err := plex.New(u, tok).Check(ctx)
	res := TestResult{OK: err == nil, Version: v, URL: u}
	if err != nil {
		res.Error = plexErrString(err)
	}
	httpx.OK(w, res)
	return nil
}

// --- token import from Preferences.xml ---

var plexTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// parsePlexToken extracts PlexOnlineToken from a Preferences.xml.
func parsePlexToken(r io.Reader) (string, error) {
	var prefs struct {
		XMLName xml.Name `xml:"Preferences"`
		Token   string   `xml:"PlexOnlineToken,attr"`
	}
	if err := xml.NewDecoder(r).Decode(&prefs); err != nil {
		return "", errors.New("Preferences.xml could not be parsed")
	}
	t := strings.TrimSpace(prefs.Token)
	if t == "" {
		return "", errors.New("Preferences.xml has no token (sign the server in to Plex first)")
	}
	if !plexTokenRe.MatchString(t) {
		return "", errors.New("Preferences.xml has no valid token")
	}
	return t, nil
}

func (s *Service) handleImportPlexToken(w http.ResponseWriter, r *http.Request) error {
	err := s.importSecret(r, secretImport{
		action: "integrations.plex.import_token", name: "Plex", what: "the token",
		notDetected: "That path is not the Preferences.xml of a detected Plex container",
		candidates:  func(d *Discovery) []Candidate { return d.Plex },
		parse:       parsePlexToken,
		savedURL:    func() string { return s.plexSettings().URL },
		apply: func(ctx context.Context, u, tok string) error {
			sealed, err := s.st.Seal(tok)
			if err != nil {
				return err
			}
			next := plexSettings{URL: u, TokenSealed: sealed}
			s.mu.Lock()
			defer s.mu.Unlock()
			if err := s.save(ctx, keyPlex, u, next); err != nil {
				return err
			}
			s.px = next
			return nil
		},
	})
	if err != nil {
		return err
	}
	httpx.OK(w, s.plexStatus(r.Context(), true))
	return nil
}

// --- activity ---

// PlexSession is an active stream.
type PlexSession struct {
	SessionKey string `json:"sessionKey,omitempty"`
	RatingKey  string `json:"ratingKey,omitempty"`
	// ShowKey is the show's rating key for an episode.
	ShowKey          string `json:"showKey,omitempty"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	ParentTitle      string `json:"parentTitle,omitempty"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
	ParentIndex      int64  `json:"parentIndex"`
	Index            int64  `json:"index"`
	Year             int64  `json:"year,omitempty"`
	ViewOffset       int64  `json:"viewOffset"`
	Duration         int64  `json:"duration"`
	User             string `json:"user,omitempty"`
	Player           string `json:"player,omitempty"`
	Product          string `json:"product,omitempty"`
	// State is playing, paused or buffering.
	State string `json:"state"`
	Local bool   `json:"local"`
	// Decision is transcode, directstream or directplay.
	Decision string `json:"decision"`
	Thumb    string `json:"thumb,omitempty"`
}

// PlexItem is a history entry or a recently added item.
type PlexItem struct {
	Key              string `json:"key"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	ParentTitle      string `json:"parentTitle,omitempty"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
	ParentIndex      int64  `json:"parentIndex"`
	Index            int64  `json:"index"`
	Year             int64  `json:"year,omitempty"`
	LeafCount        int64  `json:"leafCount,omitempty"`
	// At is viewedAt (history) or addedAt (recent), unix seconds.
	At    int64  `json:"at"`
	User  string `json:"user,omitempty"`
	Thumb string `json:"thumb,omitempty"`
}

// PlexActivity is returned by GET /api/plex/activity. A failing section is
// empty with its message in Errors.
type PlexActivity struct {
	Sessions  []PlexSession     `json:"sessions"`
	History   []PlexItem        `json:"history"`
	Recent    []PlexItem        `json:"recent"`
	Errors    map[string]string `json:"errors,omitempty"`
	WebURL    string            `json:"webUrl,omitempty"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// plexActivityCache keeps the last result, failures included, for
// plexActivityTTL, and runs one fetch at a time.
type plexActivityCache struct {
	mu     sync.Mutex
	key    string
	at     time.Time
	data   *PlexActivity
	err    error
	flight *activityFetch
	acc    map[int64]string
	accAt  time.Time
	accKey string
}

// activityFetch is a fetch in progress; done closes when data/err are set.
type activityFetch struct {
	key  string
	done chan struct{}
	data *PlexActivity
	err  error
}

var plexThumbRe = regexp.MustCompile(`^/library/metadata/\d{1,12}/thumb(/\d{1,20})?$`)

func safeThumb(p string) string {
	if plexThumbRe.MatchString(p) {
		return p
	}
	return ""
}

// webURL links to the Plex web app, except for loopback addresses, which
// would point at the visitor's own machine.
func webURL(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return ""
	}
	return strings.TrimRight(base, "/") + "/web/index.html"
}

// plexActivity returns the cached activity or waits for the one fetch in
// progress, starting it if needed. The fetch is detached from the caller,
// so a closed tab neither cancels it nor caches its cancellation.
func (s *Service) plexActivity(ctx context.Context) (*PlexActivity, error) {
	p := s.plexSettings()
	c, err := s.plexClient()
	if err != nil {
		return nil, err
	}
	key := p.URL + "\x00" + p.TokenSealed
	a := &s.plexAct
	a.mu.Lock()
	if a.key == key && s.now().Sub(a.at) < plexActivityTTL {
		defer a.mu.Unlock()
		return a.data, a.err
	}
	f := a.flight
	if f == nil || f.key != key {
		f = &activityFetch{key: key, done: make(chan struct{})}
		a.flight = f
		acc := a.acc
		if a.accKey != key {
			acc = nil // another server: its account names don't apply
		}
		needAcc := a.accKey != key || s.now().Sub(a.accAt) > plexAccountsTTL
		go s.fetchPlexActivity(context.WithoutCancel(ctx), c, p.URL, f, acc, needAcc)
	}
	a.mu.Unlock()
	select {
	case <-f.done:
		return f.data, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) fetchPlexActivity(ctx context.Context, c *plex.Client, base string, f *activityFetch, acc map[int64]string, needAcc bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, acc, err := s.loadPlexActivity(ctx, c, base, acc, needAcc)
	f.data, f.err = out, err
	a := &s.plexAct
	a.mu.Lock()
	if needAcc && acc != nil {
		a.acc, a.accAt, a.accKey = acc, s.now(), f.key
	}
	a.key, a.at, a.data, a.err = f.key, s.now(), out, err
	if a.flight == f {
		a.flight = nil
	}
	a.mu.Unlock()
	close(f.done)
}

// loadPlexActivity fetches activity; acc maps account ids to names and is
// refreshed when needAcc.
func (s *Service) loadPlexActivity(ctx context.Context, c *plex.Client, base string, acc map[int64]string, needAcc bool) (*PlexActivity, map[int64]string, error) {
	var (
		wg                 sync.WaitGroup
		ss, hs, rs         []plex.Metadata
		ssErr, hsErr, rErr error
	)
	wg.Add(3)
	go func() { defer wg.Done(); ss, ssErr = c.Sessions(ctx) }()
	go func() { defer wg.Done(); hs, hsErr = c.History(ctx, plexHistoryN) }()
	go func() { defer wg.Done(); rs, rErr = c.RecentlyAdded(ctx, plexRecentN) }()
	if needAcc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if m, err := c.Accounts(ctx); err == nil {
				acc = m
			}
		}()
	}
	wg.Wait()
	if ssErr != nil && hsErr != nil && rErr != nil {
		return nil, acc, ssErr
	}

	out := &PlexActivity{Sessions: []PlexSession{}, History: []PlexItem{}, Recent: []PlexItem{}, WebURL: webURL(base), UpdatedAt: s.now()}
	errs := map[string]string{}
	if ssErr != nil {
		errs["sessions"] = plexErrString(ssErr)
	}
	if hsErr != nil {
		errs["history"] = plexErrString(hsErr)
	}
	if rErr != nil {
		errs["recent"] = plexErrString(rErr)
	}
	if len(errs) > 0 {
		out.Errors = errs
	}
	for _, m := range ss {
		v := PlexSession{
			SessionKey: m.SessionKey, RatingKey: m.RatingKey, ShowKey: m.GrandparentRatingKey,
			Type: m.Type, Title: m.Title, ParentTitle: m.ParentTitle, GrandparentTitle: m.GrandparentTitle,
			ParentIndex: int64(m.ParentIndex), Index: int64(m.Index), Year: int64(m.Year),
			ViewOffset: int64(m.ViewOffset), Duration: int64(m.Duration),
			Decision: m.Decision(), Thumb: safeThumb(m.Poster()), State: "playing",
		}
		if m.User != nil {
			v.User = m.User.Title
		}
		if m.Player != nil {
			v.Player, v.Product, v.Local = m.Player.Title, m.Player.Product, bool(m.Player.Local)
			if m.Player.State != "" {
				v.State = m.Player.State
			}
		}
		out.Sessions = append(out.Sessions, v)
	}
	// playing before paused or buffering
	sort.SliceStable(out.Sessions, func(i, j int) bool {
		return out.Sessions[i].State == "playing" && out.Sessions[j].State != "playing"
	})
	item := func(m plex.Metadata, at int64, key string) PlexItem {
		return PlexItem{
			Key: key, Type: m.Type, Title: m.Title, ParentTitle: m.ParentTitle, GrandparentTitle: m.GrandparentTitle,
			ParentIndex: int64(m.ParentIndex), Index: int64(m.Index), Year: int64(m.Year), LeafCount: int64(m.LeafCount),
			At: at, Thumb: safeThumb(m.Poster()),
		}
	}
	for i, m := range hs {
		k := m.HistoryKey
		if k == "" {
			k = "h" + strconv.Itoa(i)
		}
		v := item(m, int64(m.ViewedAt), k)
		v.User = acc[int64(m.AccountID)]
		out.History = append(out.History, v)
	}
	for i, m := range rs {
		k := m.RatingKey
		if k == "" {
			k = "r" + strconv.Itoa(i)
		}
		out.Recent = append(out.Recent, item(m, int64(m.AddedAt), k))
	}
	return out, acc, nil
}

func (s *Service) handlePlexActivity(w http.ResponseWriter, r *http.Request) error {
	act, err := s.plexActivity(r.Context())
	if err != nil {
		return upstream(err)
	}
	httpx.OK(w, act)
	return nil
}

// thumbWidths are the poster widths served (the next one up is used).
var thumbWidths = []int{80, 120, 160, 240}

// handlePlexThumb proxies a small poster so the browser never needs the
// token. Only library metadata images are allowed.
func (s *Service) handlePlexThumb(w http.ResponseWriter, r *http.Request) error {
	c, err := s.plexClient()
	if err != nil {
		return err
	}
	q := r.URL.Query()
	p := q.Get("path")
	if !plexThumbRe.MatchString(p) {
		return httpx.BadRequest("invalid image path")
	}
	width := 120
	if v := q.Get("w"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return httpx.BadRequest("invalid width")
		}
		width = n
	}
	// A few sizes only, so Plex and the browser cache can reuse them.
	width = thumbWidths[min(sort.SearchInts(thumbWidths, width), len(thumbWidths)-1)]
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	b, ct, err := c.Photo(ctx, p, width, width*3/2)
	if err != nil {
		return upstream(err)
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=86400")
	_, err = w.Write(b)
	return err
}

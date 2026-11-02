// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/launcher"
)

// Container logos: GET /api/containers/{id}/icon resolves a logo from
// dashboard-icons (by image name, compose service, container name) or a
// CasaOS "icon" label, downloads it once into <dataDir>/icons/containers/
// and serves it from there. Misses are remembered for missTTL.

const missTTL = 24 * time.Hour

// iconStore fetches and caches container logos.
type iconStore struct {
	dir    string // <dataDir>/icons/containers
	base   string // dashboard-icons CDN prefix (tests override)
	client *http.Client
	// labelClient fetches URLs taken from container labels. Labels are
	// attacker-controlled (an image's labels are inherited by its
	// containers), so it only connects to public addresses.
	labelClient *http.Client

	mu       sync.Mutex
	inflight map[string]*iconFetch
	failed   map[string]time.Time // transient (network) failures, retried after 5 minutes
}

type iconFetch struct {
	done chan struct{}
	path string
	err  error
}

var errNoIcon = errors.New("no icon")

func httpsOnlyRedirects(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" || len(via) >= 3 {
		return errors.New("redirect not allowed")
	}
	return nil
}

func newIconStore(dataDir string) *iconStore {
	return &iconStore{
		dir:  filepath.Join(dataDir, "icons", "containers"),
		base: launcher.DashboardIconsBase,
		client: &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: httpsOnlyRedirects,
		},
		labelClient: publicOnlyClient(15 * time.Second),
		inflight:    map[string]*iconFetch{},
		failed:      map[string]time.Time{},
	}
}

var errBlockedAddr = errors.New("destination address not allowed")

// cgnat and other special-purpose ranges that netip has no predicate for.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT (Tailscale etc.)
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 can map to private v4
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2002::/16"),      // 6to4 embeds arbitrary v4
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

// publicAddr reports whether ip is a globally routable unicast address.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// publicOnlyClient is an HTTPS client that refuses to connect to
// loopback, private, link-local and other non-public addresses. The check
// runs on the resolved address at connect time (also for redirects), so
// DNS rebinding can't bypass it. It ignores proxy settings: a proxy
// would hide the real destination from the check.
func publicOnlyClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errBlockedAddr
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicAddr(ip) {
				return errBlockedAddr
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: httpsOnlyRedirects}
}

// iconSource is one place a container logo may come from.
type iconSource struct {
	Slug string // dashboard-icons slug, or
	URL  string // an https image URL from a label
}

func (src iconSource) key() string {
	if src.Slug != "" {
		return "slug-" + src.Slug
	}
	sum := sha256.Sum256([]byte(src.URL))
	return "url-" + hex.EncodeToString(sum[:16])
}

// iconLabelKeys are labels that may hold a logo URL (CasaOS sets "icon").
var iconLabelKeys = []string{"icon", "io.casaos.icon", "io.casaos.app.icon", "casaos.icon"}

// iconCandidates lists logo sources in order of preference: dashboard-icons
// slugs for the image, compose service and container name, then a
// CasaOS-style icon label (https only).
func iconCandidates(a *apiContainer) []iconSource {
	var out []iconSource
	seen := map[string]bool{}
	addSlug := func(s string) {
		if s = launcher.GuessSlug(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, iconSource{Slug: s})
		}
	}
	if !strings.HasPrefix(a.Image, "sha256:") {
		addSlug(a.Image)
	}
	addSlug(a.Labels[labelService])
	if len(a.Names) > 0 {
		addSlug(strings.TrimPrefix(a.Names[0], "/"))
	}
	for _, k := range iconLabelKeys {
		v := strings.TrimSpace(a.Labels[k])
		if validIconURL(v) {
			out = append(out, iconSource{URL: v})
			break
		}
	}
	return out
}

// validIconURL accepts plain https URLs on the default port without
// credentials (see publicOnlyClient for the address check).
func validIconURL(v string) bool {
	if len(v) > 1024 {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && (u.Port() == "" || u.Port() == "443")
}

func (st *iconStore) missPath(src iconSource) string { return filepath.Join(st.dir, src.key()+".miss") }

// cached returns the cached file for src ("" if none) and whether a
// fresh negative entry exists.
func (st *iconStore) cached(src iconSource) (path string, miss bool) {
	for _, ext := range []string{"png", "svg", "webp", "jpg"} {
		p := filepath.Join(st.dir, src.key()+"."+ext)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return p, false
		}
	}
	if fi, err := os.Stat(st.missPath(src)); err == nil && time.Since(fi.ModTime()) < missTTL {
		return "", true
	}
	return "", false
}

// allMissed reports whether every source is a fresh negative cache entry,
// so the list can omit the icon URL instead of sending the browser to a 404.
func (st *iconStore) allMissed(srcs []iconSource) bool {
	for _, src := range srcs {
		if _, miss := st.cached(src); !miss {
			return false
		}
	}
	return true
}

// resolve returns the cached logo file for the first source that has one,
// downloading as needed.
func (st *iconStore) resolve(ctx context.Context, srcs []iconSource) (string, error) {
	for _, src := range srcs {
		p, miss := st.cached(src)
		if p != "" {
			return p, nil
		}
		if miss {
			continue
		}
		p, err := st.fetchOnce(ctx, src)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, errNoIcon) {
			// Network trouble: don't try further sources now, so a CDN
			// outage doesn't fall through to a worse logo.
			return "", err
		}
	}
	return "", errNoIcon
}

// fetchOnce deduplicates concurrent downloads of the same source.
func (st *iconStore) fetchOnce(ctx context.Context, src iconSource) (string, error) {
	k := src.key()
	st.mu.Lock()
	if t, ok := st.failed[k]; ok && time.Since(t) < 5*time.Minute {
		st.mu.Unlock()
		return "", errors.New("icon source unreachable")
	}
	if f, ok := st.inflight[k]; ok {
		st.mu.Unlock()
		select {
		case <-f.done:
			return f.path, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &iconFetch{done: make(chan struct{})}
	st.inflight[k] = f
	st.mu.Unlock()

	// Detach from the request so a closed tab doesn't waste the download.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	f.path, f.err = st.fetch(fctx, src)
	cancel()

	st.mu.Lock()
	delete(st.inflight, k)
	if f.err != nil && !errors.Is(f.err, errNoIcon) {
		st.failed[k] = time.Now()
	} else {
		delete(st.failed, k)
	}
	st.mu.Unlock()
	close(f.done)
	return f.path, f.err
}

func (st *iconStore) fetch(ctx context.Context, src iconSource) (string, error) {
	u := src.URL
	if src.Slug != "" {
		if !launcher.ValidSlug(src.Slug) {
			return "", errNoIcon
		}
		u = st.base + src.Slug + ".png"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", errNoIcon
	}
	req.Header.Set("User-Agent", "Wargo")
	client := st.client
	if src.Slug == "" {
		client = st.labelClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if src.URL != "" {
			// A broken label URL is not worth retrying every few minutes.
			return "", st.markMiss(src)
		}
		return "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusForbidden:
		return "", st.markMiss(src)
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return "", errors.New("icon CDN answered " + resp.Status)
	default:
		return "", st.markMiss(src)
	}
	if ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); ct != "" && !strings.HasPrefix(ct, "image/") && ct != "application/octet-stream" {
		return "", st.markMiss(src)
	}
	if resp.ContentLength > launcher.MaxIconSize {
		return "", st.markMiss(src)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, launcher.MaxIconSize+1))
	if err != nil {
		return "", err
	}
	ext := launcher.SniffIcon(b)
	if len(b) > launcher.MaxIconSize || ext == "" || (src.Slug != "" && ext != "png") {
		return "", st.markMiss(src)
	}
	return st.save(src, ext, b)
}

func (st *iconStore) markMiss(src iconSource) error {
	if os.MkdirAll(st.dir, 0o700) == nil {
		p := st.missPath(src)
		if f, err := os.Create(p); err == nil {
			f.Close()
			now := time.Now()
			_ = os.Chtimes(p, now, now)
		}
	}
	return errNoIcon
}

func (st *iconStore) save(src iconSource, ext string, b []byte) (string, error) {
	if err := os.MkdirAll(st.dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(st.dir, ".dl-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	dst := filepath.Join(st.dir, src.key()+"."+ext)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	os.Remove(st.missPath(src))
	return dst, nil
}

func (s *Service) handleIcon(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if s.icons == nil {
		return httpx.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	a, err := s.rawContainer(ctx, id)
	if err != nil {
		return dockerErr(err)
	}
	srcs := iconCandidates(a)
	notFound := func() error {
		w.Header().Set("Cache-Control", "private, max-age=300")
		return httpx.ErrNotFound
	}
	if len(srcs) == 0 {
		return notFound()
	}
	p, err := s.icons.resolve(ctx, srcs)
	if err != nil {
		return notFound()
	}
	f, err := os.Open(p)
	if err != nil {
		return notFound()
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return notFound()
	}
	ext := strings.TrimPrefix(filepath.Ext(p), ".")
	h := w.Header()
	h.Set("Content-Type", launcher.IconContentType(ext))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=604800")
	if ext == "svg" {
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
	return nil
}

// rawContainer finds the list entry for id (short/full id or name).
func (s *Service) rawContainer(ctx context.Context, id string) (*apiContainer, error) {
	for attempt := 0; attempt < 2; attempt++ {
		s.mu.Lock()
		raw := s.raw
		s.mu.Unlock()
		if raw == nil || attempt > 0 {
			if _, err := s.List(ctx, attempt > 0); err != nil {
				return nil, err
			}
			s.mu.Lock()
			raw = s.raw
			s.mu.Unlock()
		}
		for i := range raw {
			a := raw[i]
			name := ""
			if len(a.Names) > 0 {
				name = strings.TrimPrefix(a.Names[0], "/")
			}
			if a.ID == id || name == id || (len(id) >= 12 && strings.HasPrefix(a.ID, id)) {
				return &a, nil
			}
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Message: "no such container: " + id}
}

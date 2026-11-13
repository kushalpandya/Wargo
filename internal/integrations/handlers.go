// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/kushalpandya/wargo/internal/integrations/prowlarr"
	"github.com/kushalpandya/wargo/internal/integrations/torrent"
	"github.com/kushalpandya/wargo/internal/integrations/transmission"
)

var (
	errProwlarrNotConfigured     = httpx.Errorf(http.StatusConflict, "not_configured", "Prowlarr is not configured")
	errTransmissionNotConfigured = httpx.Errorf(http.StatusConflict, "not_configured", "Transmission is not configured")
	errResultExpired             = httpx.Errorf(http.StatusNotFound, "result_expired", "This search result has expired, search again")
	errMagnetOnly                = httpx.Errorf(http.StatusNotFound, "magnet_only", "This result only provides a magnet link")
)

// upstream converts a client error into a 502 (never 401, which the UI would
// treat as a logged-out session) with secrets redacted.
func upstream(err error) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return httpx.Errorf(http.StatusGatewayTimeout, "upstream_timeout", "The service did not respond in time")
	}
	code := "upstream"
	if errors.Is(err, prowlarr.ErrUnauthorized) || errors.Is(err, transmission.ErrUnauthorized) || errors.Is(err, plex.ErrUnauthorized) {
		code = "upstream_unauthorized"
	}
	return httpx.Errorf(http.StatusBadGateway, code, errString(err))
}

// IndexerRef is a browser-safe indexer.
type IndexerRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (s *Service) indexers(ctx context.Context, c *prowlarr.Client) ([]IndexerRef, error) {
	s.idxMu.Lock()
	defer s.idxMu.Unlock()
	key := c.BaseURL
	if s.idxKey != key || s.now().Sub(s.idxAt) > 5*time.Minute || s.idx == nil {
		list, err := c.Indexers(ctx)
		if err != nil {
			return nil, err
		}
		s.idx, s.idxAt, s.idxKey = list, s.now(), key
	}
	out := []IndexerRef{}
	for _, i := range s.idx {
		if i.Enable && (i.Protocol == "" || i.Protocol == "torrent") {
			out = append(out, IndexerRef{ID: i.ID, Name: i.Name})
		}
	}
	sort.Slice(out, func(a, b int) bool { return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name) })
	return out, nil
}

func (s *Service) handleIndexers(w http.ResponseWriter, r *http.Request) error {
	c := s.prowlarrClient()
	if c == nil {
		return errProwlarrNotConfigured
	}
	list, err := s.indexers(r.Context(), c)
	if err != nil {
		return upstream(err)
	}
	httpx.OK(w, map[string]any{"indexers": list})
	return nil
}

func parseInts(raw string, max int) ([]int, error) {
	var out []int
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 1_000_000 {
			return nil, httpx.BadRequest("invalid id list")
		}
		out = append(out, n)
		if len(out) > max {
			return nil, httpx.BadRequest("too many ids")
		}
	}
	return out, nil
}

// IndexerError describes a failing indexer.
type IndexerError struct {
	IndexerID int    `json:"indexerId"`
	Indexer   string `json:"indexer"`
	Message   string `json:"message"`
}

func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) error {
	c := s.prowlarrClient()
	if c == nil {
		return errProwlarrNotConfigured
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		return httpx.BadRequest("query is required")
	}
	if len(q) > 200 {
		return httpx.BadRequest("query is too long")
	}
	cats, err := parseInts(r.URL.Query().Get("categories"), 20)
	if err != nil {
		return err
	}
	ids, err := parseInts(r.URL.Query().Get("indexers"), 200)
	if err != nil {
		return err
	}
	ckey := c.BaseURL + "\x00" + strings.ToLower(q) + "\x00" + r.URL.Query().Get("categories") + "\x00" + r.URL.Query().Get("indexers")
	if v, ok := s.searchCache.get(ckey, s.now()); ok {
		httpx.OK(w, v)
		return nil
	}
	if err := s.limiter.acquire(r.Context(), s.now); err != nil {
		return err
	}
	defer s.limiter.release()
	// an identical search may have finished while we waited for the slot
	if v, ok := s.searchCache.get(ckey, s.now()); ok {
		httpx.OK(w, v)
		return nil
	}

	start := time.Now()
	idx, idxErr := s.indexers(r.Context(), c)
	list, err := c.Search(r.Context(), prowlarr.SearchParams{Query: q, Categories: cats, IndexerIDs: ids})
	if err != nil {
		return upstream(err)
	}
	host := ""
	if u, err := url.Parse(c.BaseURL); err == nil {
		host = u.Host
	}
	results := s.cache.Map(list, host)
	errs := []IndexerError{}
	if idxErr == nil {
		names := map[int]string{}
		for _, i := range idx {
			names[i.ID] = i.Name
		}
		wanted := map[int]bool{}
		for _, id := range ids {
			wanted[id] = true
		}
		if sts, err := c.IndexerStatus(r.Context()); err == nil {
			for _, st := range sts {
				name, ok := names[st.IndexerID]
				if !ok || (len(wanted) > 0 && !wanted[st.IndexerID]) || !st.DisabledTill.After(s.now()) {
					continue
				}
				errs = append(errs, IndexerError{IndexerID: st.IndexerID, Indexer: name,
					Message: "Temporarily disabled after failures until " + st.DisabledTill.Local().Format("15:04")})
			}
		}
	} else {
		idx = []IndexerRef{}
	}
	resp := map[string]any{"results": results, "indexers": idx, "tookMs": time.Since(start).Milliseconds(), "errors": errs}
	s.searchCache.put(ckey, resp, s.now())
	httpx.OK(w, resp)
	return nil
}

func (s *Service) entry(r *http.Request) (*prowlarr.Entry, *prowlarr.Client, error) {
	c := s.prowlarrClient()
	if c == nil {
		return nil, nil, errProwlarrNotConfigured
	}
	e, ok := s.cache.Get(r.PathValue("id"))
	if !ok {
		return nil, nil, errResultExpired
	}
	return e, c, nil
}

// magnet resolves (and caches) a magnet link for e.
func (s *Service) magnet(ctx context.Context, c *prowlarr.Client, e *prowlarr.Entry) (string, error) {
	if m := e.ResolvedMagnet(); m != "" {
		return m, nil
	}
	var lastErr error
	for _, link := range []string{e.MagnetURL, e.DownloadURL} {
		if link == "" {
			continue
		}
		f, err := c.Fetch(ctx, link)
		if err != nil {
			lastErr = err
			continue
		}
		if f.Magnet != "" {
			e.SetMagnet(f.Magnet)
			return f.Magnet, nil
		}
		m, err := torrent.Parse(f.Torrent)
		if err != nil {
			lastErr = fmt.Errorf("the indexer returned an invalid .torrent: %w", err)
			continue
		}
		mag := m.Magnet()
		e.SetMagnet(mag)
		return mag, nil
	}
	if e.InfoHash != "" {
		mag := torrent.BuildMagnet(e.InfoHash, "", e.Title, e.Size, nil)
		e.SetMagnet(mag)
		return mag, nil
	}
	if lastErr == nil {
		lastErr = errors.New("result has no magnet or download link")
	}
	return "", lastErr
}

// torrentFile fetches the .torrent for e.
func (s *Service) torrentFile(ctx context.Context, c *prowlarr.Client, e *prowlarr.Entry) ([]byte, error) {
	if e.DownloadURL == "" {
		return nil, errMagnetOnly
	}
	f, err := c.Fetch(ctx, e.DownloadURL)
	if err != nil {
		return nil, err
	}
	if f.Magnet != "" {
		e.SetMagnet(f.Magnet)
		return nil, errMagnetOnly
	}
	m, err := torrent.Parse(f.Torrent)
	if err != nil {
		return nil, httpx.Errorf(http.StatusBadGateway, "invalid_torrent", "The indexer returned an invalid .torrent file")
	}
	if e.ResolvedMagnet() == "" {
		e.SetMagnet(m.Magnet())
	}
	return f.Torrent, nil
}

func (s *Service) handleMagnet(w http.ResponseWriter, r *http.Request) error {
	e, c, err := s.entry(r)
	if err != nil {
		return err
	}
	m, err := s.magnet(r.Context(), c, e)
	if err != nil {
		return upstream(err)
	}
	httpx.OK(w, map[string]string{"magnet": m})
	return nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9 ._()\[\]+-]+`)

// SafeFilename turns a title into a conservative filename stem.
func SafeFilename(title string) string {
	s := unsafeName.ReplaceAllString(title, "_")
	s = strings.Trim(strings.Join(strings.Fields(s), " "), " ._")
	if len(s) > 150 {
		s = s[:150]
	}
	if s == "" {
		s = "download"
	}
	return s
}

func (s *Service) handleTorrent(w http.ResponseWriter, r *http.Request) error {
	e, c, err := s.entry(r)
	if err != nil {
		return err
	}
	b, err := s.torrentFile(r.Context(), c, e)
	if err != nil {
		return upstream(err)
	}
	name := SafeFilename(e.Title) + ".torrent"
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, name, url.PathEscape(name)))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(b)
	return err
}

func (s *Service) handleQueue(w http.ResponseWriter, r *http.Request) error {
	e, c, err := s.entry(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	via := ""
	details := map[string]any{"title": e.Title, "indexerId": e.IndexerID}
	if st := s.prowlarrStatus(ctx, false); st.Available && st.HasTransmissionClient {
		via = "prowlarr"
		err = c.Grab(ctx, e.GUID, e.IndexerID)
	} else if tst := s.transmissionStatus(ctx, false); tst.Available {
		via = "transmission"
		err = s.addFromEntry(ctx, c, e, details)
	} else {
		err = httpx.Errorf(http.StatusConflict, "no_download_client", "Neither Prowlarr nor Wargo has a working Transmission connection")
	}
	details["via"] = via
	s.audit.Record(r, "prowlarr.queue", e.Title, err, details)
	if err != nil {
		return upstream(err)
	}
	httpx.OK(w, map[string]string{"via": via})
	return nil
}

func (s *Service) addFromEntry(ctx context.Context, c *prowlarr.Client, e *prowlarr.Entry, details map[string]any) error {
	tc := s.transmissionClient()
	if tc == nil {
		return errTransmissionNotConfigured
	}
	var magnet, meta string
	if e.DownloadURL != "" && e.ResolvedMagnet() == "" {
		if b, err := s.torrentFile(ctx, c, e); err == nil {
			meta = base64.StdEncoding.EncodeToString(b)
		} else if !errors.Is(err, errMagnetOnly) {
			return err
		}
	}
	if meta == "" {
		m, err := s.magnet(ctx, c, e)
		if err != nil {
			return err
		}
		magnet = m
	}
	added, err := tc.Add(ctx, magnet, meta)
	if err == nil {
		details["torrentId"], details["duplicate"] = added.ID, added.Duplicate
	}
	s.trHealth.mu.Lock()
	s.trHealth.at = time.Time{}
	s.trHealth.mu.Unlock()
	return err
}

// --- Transmission ---

// TorrentView is the browser view of a torrent.
type TorrentView struct {
	ID             int64   `json:"id"`
	Hash           string  `json:"hash"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	PercentDone    float64 `json:"percentDone"`
	RateDownload   int64   `json:"rateDownload"`
	RateUpload     int64   `json:"rateUpload"`
	ETA            int64   `json:"eta"`
	SizeWhenDone   int64   `json:"sizeWhenDone"`
	LeftUntilDone  int64   `json:"leftUntilDone"`
	UploadRatio    float64 `json:"uploadRatio"`
	Error          int     `json:"error"`
	ErrorString    string  `json:"errorString"`
	AddedDate      int64   `json:"addedDate"`
	PeersConnected int     `json:"peersConnected"`
	DownloadDir    string  `json:"downloadDir"`
}

// StatsView summarises the session.
type StatsView struct {
	DownloadSpeed      int64  `json:"downloadSpeed"`
	UploadSpeed        int64  `json:"uploadSpeed"`
	ActiveTorrentCount int    `json:"activeTorrentCount"`
	PausedTorrentCount int    `json:"pausedTorrentCount"`
	FreeSpace          *int64 `json:"freeSpace,omitempty"`
}

// Payload is returned by GET /api/transmission/torrents and published on Topic.
type Payload struct {
	Torrents []TorrentView `json:"torrents"`
	Stats    StatsView     `json:"stats"`
	Error    string        `json:"error,omitempty"`
	At       time.Time     `json:"at"`
}

func (s *Service) payload(ctx context.Context) (*Payload, error) {
	c := s.transmissionClient()
	if c == nil {
		return nil, errTransmissionNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := c.Torrents(ctx)
	if err != nil {
		return nil, err
	}
	st, err := c.Stats(ctx)
	if err != nil {
		return nil, err
	}
	p := &Payload{Torrents: make([]TorrentView, 0, len(list)), At: s.now()}
	p.Stats = StatsView{DownloadSpeed: st.DownloadSpeed, UploadSpeed: st.UploadSpeed, ActiveTorrentCount: st.ActiveTorrentCount, PausedTorrentCount: st.PausedTorrentCount}
	dir := ""
	for _, t := range list {
		p.Torrents = append(p.Torrents, TorrentView{
			ID: t.ID, Hash: t.HashString, Name: t.Name, Status: transmission.StatusName(t.Status),
			PercentDone: t.PercentDone, RateDownload: t.RateDownload, RateUpload: t.RateUpload, ETA: t.ETA,
			SizeWhenDone: t.SizeWhenDone, LeftUntilDone: t.LeftUntilDone, UploadRatio: t.UploadRatio,
			Error: t.Error, ErrorString: t.ErrorString, AddedDate: t.AddedDate, PeersConnected: t.PeersConnected,
			DownloadDir: t.DownloadDir,
		})
	}
	if sess, err := c.Session(ctx); err == nil && sess.DownloadDir != "" {
		dir = sess.DownloadDir
	}
	if dir != "" {
		if n, err := c.FreeSpace(ctx, dir); err == nil && n >= 0 {
			p.Stats.FreeSpace = &n
		}
	}
	return p, nil
}

func (s *Service) handleTorrents(w http.ResponseWriter, r *http.Request) error {
	p, err := s.payload(r.Context())
	if err != nil {
		return upstream(err)
	}
	httpx.OK(w, p)
	return nil
}

// Run polls Transmission while the live topic has subscribers, and watches
// Transmission and Plex for notifications.
func (f *TransmissionFeature) Run(ctx context.Context) {
	s := f.Service
	go s.watch(ctx)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.hub == nil || s.hub.Subscribers(Topic) == 0 || s.transmissionSettings().URL == "" {
			continue
		}
		s.publish(ctx)
	}
}

func (s *Service) publish(ctx context.Context) {
	p, err := s.payload(ctx)
	if err != nil {
		p = &Payload{Torrents: []TorrentView{}, Error: errString(err), At: s.now()}
	}
	s.hub.Publish(Topic, p)
}

var hashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func parseID(raw string) (transmission.ID, error) {
	if hashRe.MatchString(raw) {
		return transmission.ID{Hash: strings.ToLower(raw)}, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return transmission.ID{}, httpx.BadRequest("invalid torrent id")
	}
	return transmission.ID{Num: n}, nil
}

func (s *Service) afterMutation(ctx context.Context) {
	if s.hub != nil && s.hub.Subscribers(Topic) > 0 {
		go s.publish(context.WithoutCancel(ctx))
	}
}

func (s *Service) handleAdd(w http.ResponseWriter, r *http.Request) error {
	c := s.transmissionClient()
	if c == nil {
		return errTransmissionNotConfigured
	}
	var in struct {
		Magnet        string `json:"magnet"`
		TorrentBase64 string `json:"torrentBase64"`
	}
	if err := decodeLarge(r, &in); err != nil {
		return err
	}
	target := ""
	switch {
	case in.Magnet != "" && in.TorrentBase64 == "":
		if !strings.HasPrefix(in.Magnet, "magnet:?") || len(in.Magnet) > 16<<10 {
			return httpx.BadRequest("magnet must start with magnet:?")
		}
		// Never audit the raw link: tracker URLs often embed passkeys.
		target = "magnet link"
		if h := torrent.MagnetInfoHash(in.Magnet); h != "" {
			target = h
		}
	case in.TorrentBase64 != "" && in.Magnet == "":
		b, err := base64.StdEncoding.DecodeString(in.TorrentBase64)
		if err != nil {
			return httpx.BadRequest("torrentBase64 is not valid base64")
		}
		m, err := torrent.Parse(b)
		if err != nil {
			return httpx.BadRequest("not a valid .torrent file: " + err.Error())
		}
		target = m.Name
	default:
		return httpx.BadRequest("provide exactly one of magnet or torrentBase64")
	}
	added, err := c.Add(r.Context(), in.Magnet, in.TorrentBase64)
	var details any
	if added != nil {
		details = map[string]any{"id": added.ID, "name": added.Name, "duplicate": added.Duplicate}
	}
	s.audit.Record(r, "transmission.add", target, err, details)
	if err != nil {
		return upstream(err)
	}
	s.afterMutation(r.Context())
	httpx.OK(w, added)
	return nil
}

func decodeLarge(r *http.Request, v any) error {
	// .torrent files are base64 encoded (4/3 of torrent.MaxSize)
	if err := json.NewDecoder(io.LimitReader(r.Body, torrent.MaxSize*4/3+4096)).Decode(v); err != nil {
		return httpx.BadRequest("invalid JSON body (a .torrent must be at most 10 MB)")
	}
	return nil
}

func (s *Service) handleStartStop(start bool) func(w http.ResponseWriter, r *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		c := s.transmissionClient()
		if c == nil {
			return errTransmissionNotConfigured
		}
		id, err := parseID(r.PathValue("id"))
		if err != nil {
			return err
		}
		action := "transmission.stop"
		if start {
			action = "transmission.start"
			err = c.Start(r.Context(), id)
		} else {
			err = c.Stop(r.Context(), id)
		}
		s.audit.Record(r, action, r.PathValue("id"), err, nil)
		if err != nil {
			return upstream(err)
		}
		s.afterMutation(r.Context())
		httpx.OK(w, map[string]bool{"ok": true})
		return nil
	}
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) error {
	c := s.transmissionClient()
	if c == nil {
		return errTransmissionNotConfigured
	}
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var del bool
	switch r.URL.Query().Get("deleteData") {
	case "", "false", "0":
	case "true", "1":
		del = true
	default:
		return httpx.BadRequest("deleteData must be true or false")
	}
	err = c.Remove(r.Context(), id, del)
	s.audit.Record(r, "transmission.remove", r.PathValue("id"), err, map[string]bool{"deleteData": del})
	if err != nil {
		return upstream(err)
	}
	s.afterMutation(r.Context())
	httpx.OK(w, map[string]bool{"ok": true})
	return nil
}

// responseCache keeps a few recent search responses so reloads and repeated
// queries do not hammer the indexers.
type responseCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]cachedResponse
}

type cachedResponse struct {
	v  any
	at time.Time
}

func (c *responseCache) get(k string, now time.Time) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || now.Sub(e.at) > c.ttl {
		return nil, false
	}
	return e.v, true
}

func (c *responseCache) put(k string, v any, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedResponse{}
	}
	for key, e := range c.entries {
		if now.Sub(e.at) > c.ttl {
			delete(c.entries, key)
		}
	}
	if len(c.entries) >= c.max {
		var oldest string
		for key, e := range c.entries {
			if oldest == "" || e.at.Before(c.entries[oldest].at) {
				oldest = key
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[k] = cachedResponse{v: v, at: now}
}

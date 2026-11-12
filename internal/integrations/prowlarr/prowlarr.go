// SPDX-License-Identifier: AGPL-3.0-only

// Package prowlarr is a minimal read-mostly Prowlarr v1 API client.
package prowlarr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/integrations/torrent"
	"github.com/kushalpandya/wargo/internal/netx"
)

const (
	maxAPIResponse = 32 << 20
	// maxMagnet bounds a magnet link taken from a redirect (they are kept
	// in the result cache).
	maxMagnet = 64 << 10
)

var (
	ErrUnauthorized = errors.New("prowlarr: API key rejected")
	// ErrMagnetOnly means a download URL resolved to a magnet link, not a .torrent.
	ErrMagnetOnly = errors.New("prowlarr: result only provides a magnet link")
)

// Client calls one Prowlarr instance.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// Download is used for /download links; it never follows redirects.
	Download *http.Client
	// SearchTimeout bounds search requests (indexers can be slow).
	SearchTimeout time.Duration
}

// transport has no proxy from the environment: requests go where configured.
// The response header timeout is lifted because searches and grabs are slow
// by nature; each client Timeout bounds the whole request instead.
var transport = func() *http.Transport {
	t := netx.NewTransport(15 * time.Second)
	t.ResponseHeaderTimeout = 0
	return t
}()

func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		// The API key travels in X-Api-Key, which net/http would forward on
		// a cross-host redirect: only follow redirects within the host.
		HTTP:          &http.Client{Timeout: 15 * time.Second, Transport: transport, CheckRedirect: netx.SameHostRedirects(5)},
		Download:      &http.Client{Timeout: 45 * time.Second, Transport: transport, CheckRedirect: netx.NoRedirects},
		SearchTimeout: 90 * time.Second,
	}
}

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, q url.Values, body, out any) error {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("prowlarr: %w", stripURL(err))
	}
	defer netx.Drain(res)
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := readMessage(res.Body)
		if msg != "" {
			return fmt.Errorf("prowlarr: HTTP %d: %s", res.StatusCode, msg)
		}
		return fmt.Errorf("prowlarr: unexpected HTTP %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxAPIResponse)).Decode(out); err != nil {
		return fmt.Errorf("prowlarr: invalid response: %w", err)
	}
	return nil
}

// readMessage extracts a short error message from a Prowlarr error body.
func readMessage(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 8<<10))
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		return truncate(m.Message, 200)
	}
	var arr []struct {
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(b, &arr) == nil && len(arr) > 0 {
		return truncate(arr[0].ErrorMessage, 200)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// stripURL removes the request URL (which may carry query secrets) from net/http errors.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Status is /api/v1/system/status.
type Status struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var s Status
	return &s, c.do(ctx, c.HTTP, http.MethodGet, "/api/v1/system/status", nil, nil, &s)
}

// HealthItem is one /api/v1/health entry.
type HealthItem struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (c *Client) Health(ctx context.Context) ([]HealthItem, error) {
	var h []HealthItem
	return h, c.do(ctx, c.HTTP, http.MethodGet, "/api/v1/health", nil, nil, &h)
}

// Indexer is a configured indexer.
type Indexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Enable   bool   `json:"enable"`
	Protocol string `json:"protocol"`
	Privacy  string `json:"privacy"`
}

func (c *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	var l []Indexer
	return l, c.do(ctx, c.HTTP, http.MethodGet, "/api/v1/indexer", nil, nil, &l)
}

// IndexerStatus reports indexers temporarily disabled after failures.
type IndexerStatus struct {
	IndexerID         int       `json:"indexerId"`
	DisabledTill      time.Time `json:"disabledTill"`
	MostRecentFailure time.Time `json:"mostRecentFailure"`
}

func (c *Client) IndexerStatus(ctx context.Context) ([]IndexerStatus, error) {
	var l []IndexerStatus
	return l, c.do(ctx, c.HTTP, http.MethodGet, "/api/v1/indexerstatus", nil, nil, &l)
}

// Field is a provider settings field.
type Field struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// DownloadClient is a configured download client.
type DownloadClient struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Enable         bool    `json:"enable"`
	Implementation string  `json:"implementation"`
	Protocol       string  `json:"protocol"`
	Fields         []Field `json:"fields"`
}

// Field returns the value of a settings field.
func (d DownloadClient) Field(name string) any {
	for _, f := range d.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return nil
}

func (c *Client) DownloadClients(ctx context.Context) ([]DownloadClient, error) {
	var l []DownloadClient
	return l, c.do(ctx, c.HTTP, http.MethodGet, "/api/v1/downloadclient", nil, nil, &l)
}

// TransmissionClient is the Transmission connection configured in Prowlarr.
type TransmissionClient struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	UseSSL   bool   `json:"useSsl"`
	URLBase  string `json:"urlBase"`
	Username string `json:"username"`
}

// URL returns the RPC URL implied by the settings.
func (t TransmissionClient) URL() string {
	scheme := "http"
	if t.UseSSL {
		scheme = "https"
	}
	base := t.URLBase
	if base == "" {
		base = "/transmission/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return fmt.Sprintf("%s://%s%s", scheme, hostPort(t.Host, t.Port), base+"rpc")
}

func hostPort(h string, p int) string {
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	if p == 0 {
		return h
	}
	return h + ":" + strconv.Itoa(p)
}

// FindTransmission returns the first enabled Transmission download client.
func FindTransmission(list []DownloadClient) *TransmissionClient {
	for _, d := range list {
		if !d.Enable || !strings.EqualFold(d.Implementation, "Transmission") {
			continue
		}
		t := &TransmissionClient{Name: d.Name}
		t.Host, _ = d.Field("host").(string)
		if p, ok := d.Field("port").(float64); ok {
			t.Port = int(p)
		}
		t.UseSSL, _ = d.Field("useSsl").(bool)
		t.URLBase, _ = d.Field("urlBase").(string)
		t.Username, _ = d.Field("username").(string)
		return t
	}
	return nil
}

// Category of a release.
type Category struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// Release is a raw search result (contains secrets in DownloadURL/MagnetURL).
type Release struct {
	GUID        string     `json:"guid"`
	Title       string     `json:"title"`
	Size        int64      `json:"size"`
	Seeders     *int       `json:"seeders"`
	Leechers    *int       `json:"leechers"`
	Grabs       *int       `json:"grabs"`
	Files       *int       `json:"files"`
	Indexer     string     `json:"indexer"`
	IndexerID   int        `json:"indexerId"`
	PublishDate time.Time  `json:"publishDate"`
	InfoURL     string     `json:"infoUrl"`
	DownloadURL string     `json:"downloadUrl"`
	MagnetURL   string     `json:"magnetUrl"`
	InfoHash    string     `json:"infoHash"`
	Protocol    string     `json:"protocol"`
	Categories  []Category `json:"categories"`
}

// SearchParams for Search.
type SearchParams struct {
	Query      string
	Categories []int
	IndexerIDs []int
	Limit      int
}

func (c *Client) Search(ctx context.Context, p SearchParams) ([]Release, error) {
	q := url.Values{}
	q.Set("query", p.Query)
	q.Set("type", "search")
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", "0")
	for _, c := range p.Categories {
		q.Add("categories", strconv.Itoa(c))
	}
	for _, id := range p.IndexerIDs {
		q.Add("indexerIds", strconv.Itoa(id))
	}
	hc := *c.HTTP
	hc.Timeout = c.SearchTimeout
	var out []Release
	return out, c.do(ctx, &hc, http.MethodGet, "/api/v1/search", q, nil, &out)
}

// Grab asks Prowlarr to send a release to its download client.
func (c *Client) Grab(ctx context.Context, guid string, indexerID int) error {
	hc := *c.HTTP
	hc.Timeout = 60 * time.Second
	return c.do(ctx, &hc, http.MethodPost, "/api/v1/search", nil, map[string]any{"guid": guid, "indexerId": indexerID}, nil)
}

// Fetched is what a Prowlarr download link resolved to.
type Fetched struct {
	Magnet  string
	Torrent []byte
}

// Fetch resolves a Prowlarr download/magnet proxy link without following
// redirects: a redirect to magnet: yields Magnet, a 200 body yields Torrent
// (capped at torrent.MaxSize). Only URLs on the configured Prowlarr host are
// fetched.
func (c *Client) Fetch(ctx context.Context, link string) (*Fetched, error) {
	if strings.HasPrefix(link, "magnet:") {
		return &Fetched{Magnet: link}, nil
	}
	u, err := url.Parse(link)
	if err != nil {
		return nil, errors.New("prowlarr: invalid download link")
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
		return nil, errors.New("prowlarr: download link does not point at the configured Prowlarr")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.APIKey)
	res, err := c.Download.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: download failed: %w", stripURL(err))
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode >= 300 && res.StatusCode < 400:
		loc := res.Header.Get("Location")
		if len(loc) > maxMagnet {
			return nil, errors.New("prowlarr: magnet link is too long")
		}
		if strings.HasPrefix(strings.ToLower(loc), "magnet:") {
			return &Fetched{Magnet: loc}, nil
		}
		return nil, errors.New("prowlarr: download redirected to an unsupported location")
	case res.StatusCode == http.StatusOK:
		b, err := netx.ReadLimited(res.Body, torrent.MaxSize)
		if errors.Is(err, netx.ErrTooLarge) {
			return nil, torrent.ErrTooLarge
		}
		if err != nil {
			return nil, fmt.Errorf("prowlarr: download failed: %w", stripURL(err))
		}
		return &Fetched{Torrent: b}, nil
	default:
		msg := readMessage(res.Body)
		if msg == "" {
			msg = http.StatusText(res.StatusCode)
		}
		return nil, fmt.Errorf("prowlarr: indexer download failed (HTTP %d: %s)", res.StatusCode, msg)
	}
}

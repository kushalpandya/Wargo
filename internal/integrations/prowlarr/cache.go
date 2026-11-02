// SPDX-License-Identifier: AGPL-3.0-only

package prowlarr

import (
	"container/list"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/integrations/torrent"
)

// Entry is the server-side record behind an opaque result id. It holds the
// links that embed the Prowlarr API key and must never reach the browser.
type Entry struct {
	ID          string
	GUID        string
	IndexerID   int
	Title       string
	Size        int64
	InfoHash    string
	DownloadURL string
	MagnetURL   string

	mu      sync.Mutex
	magnet  string // resolved magnet (cached)
	torrent bool   // DownloadURL known to serve a .torrent
}

// ResolvedMagnet returns a cached resolved magnet.
func (e *Entry) ResolvedMagnet() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.magnet
}

// SetMagnet caches the resolved magnet.
func (e *Entry) SetMagnet(m string) {
	e.mu.Lock()
	e.magnet = m
	e.mu.Unlock()
}

// Cache is a bounded LRU of result entries with a TTL.
type Cache struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	ll    *list.List
	items map[string]*list.Element
	salt  []byte
	now   func() time.Time
}

type cacheItem struct {
	e       *Entry
	expires time.Time
}

func NewCache(max int, ttl time.Duration) *Cache {
	salt := make([]byte, 32)
	rand.Read(salt)
	return &Cache{max: max, ttl: ttl, ll: list.New(), items: map[string]*list.Element{}, salt: salt, now: time.Now}
}

// ID derives the opaque, stable-per-process id for a release.
func (c *Cache) ID(indexerID int, guid string) string {
	m := hmac.New(sha256.New, c.salt)
	m.Write([]byte(strconv.Itoa(indexerID)))
	m.Write([]byte{0})
	m.Write([]byte(guid))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:15])
}

// Put stores e (replacing an older entry with the same id, keeping any
// resolved magnet).
func (c *Cache) Put(e *Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[e.ID]; ok {
		old := el.Value.(*cacheItem)
		if m := old.e.ResolvedMagnet(); m != "" {
			e.SetMagnet(m)
		}
		old.e, old.expires = e, c.now().Add(c.ttl)
		c.ll.MoveToFront(el)
		return
	}
	c.items[e.ID] = c.ll.PushFront(&cacheItem{e: e, expires: c.now().Add(c.ttl)})
	for c.ll.Len() > c.max {
		el := c.ll.Back()
		c.ll.Remove(el)
		delete(c.items, el.Value.(*cacheItem).e.ID)
	}
}

// Get returns a live entry.
func (c *Cache) Get(id string) (*Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[id]
	if !ok {
		return nil, false
	}
	it := el.Value.(*cacheItem)
	if c.now().After(it.expires) {
		c.ll.Remove(el)
		delete(c.items, id)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return it.e, true
}

// Len returns the number of cached entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Result is the browser-safe view of a release.
type Result struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Size       int64      `json:"size"`
	Seeders    *int       `json:"seeders"`
	Leechers   *int       `json:"leechers"`
	Grabs      *int       `json:"grabs,omitempty"`
	Indexer    string     `json:"indexer"`
	IndexerID  int        `json:"indexerId"`
	Age        *time.Time `json:"age,omitempty"`
	Categories []Category `json:"categories"`
	InfoHash   string     `json:"infoHash,omitempty"`
	HasMagnet  bool       `json:"hasMagnet"`
	HasTorrent bool       `json:"hasTorrent"`
	InfoURL    string     `json:"infoUrl,omitempty"`
}

// Map converts raw releases to safe results, storing secrets in the cache.
// prowlarrHost is the host of the configured instance; links pointing at it
// are never exposed.
func (c *Cache) Map(list []Release, prowlarrHost string) []Result {
	out := make([]Result, 0, len(list))
	for _, r := range list {
		if r.Protocol != "" && r.Protocol != "torrent" {
			continue
		}
		e := &Entry{
			ID: c.ID(r.IndexerID, r.GUID), GUID: r.GUID, IndexerID: r.IndexerID, Title: r.Title, Size: r.Size,
			DownloadURL: r.DownloadURL, MagnetURL: r.MagnetURL,
		}
		e.InfoHash = torrent.NormalizeHash(r.InfoHash)
		if e.InfoHash == "" {
			e.InfoHash = torrent.MagnetInfoHash(r.GUID)
		}
		if e.InfoHash == "" {
			e.InfoHash = torrent.MagnetInfoHash(r.MagnetURL)
		}
		if strings.HasPrefix(r.GUID, "magnet:") {
			e.SetMagnet(r.GUID)
		} else if strings.HasPrefix(r.MagnetURL, "magnet:") {
			e.SetMagnet(r.MagnetURL)
		}
		c.Put(e)
		res := Result{
			ID: e.ID, Title: r.Title, Size: r.Size, Seeders: r.Seeders, Leechers: r.Leechers, Grabs: r.Grabs,
			Indexer: r.Indexer, IndexerID: r.IndexerID, InfoHash: e.InfoHash,
			HasMagnet:  e.ResolvedMagnet() != "" || r.MagnetURL != "" || e.InfoHash != "",
			HasTorrent: r.DownloadURL != "",
			InfoURL:    SafeInfoURL(r.InfoURL, prowlarrHost),
			Categories: []Category{},
		}
		if !res.HasMagnet && res.HasTorrent {
			// a .torrent can always be converted into a magnet server-side
			res.HasMagnet = true
		}
		if !r.PublishDate.IsZero() && r.PublishDate.Year() > 1971 {
			t := r.PublishDate
			res.Age = &t
		}
		for _, cat := range r.Categories {
			if cat.Name != "" && cat.ID < 100000 {
				res.Categories = append(res.Categories, Category{ID: cat.ID, Name: cat.Name})
			}
		}
		out = append(out, res)
	}
	return out
}

var secretParam = regexp.MustCompile(`(?i)(api_?key|apikey|passkey|token|auth|key|sig)=`)

// SafeInfoURL returns the indexer page URL if it is safe to expose.
func SafeInfoURL(raw, prowlarrHost string) string {
	if raw == "" || len(raw) > 2048 {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	if prowlarrHost != "" && strings.EqualFold(u.Host, prowlarrHost) {
		return ""
	}
	if secretParam.MatchString(u.RawQuery) || u.User != nil {
		return ""
	}
	return raw
}

var apikeyRe = regexp.MustCompile(`(?i)(https?://\S*?)?[?&]?api_?key=[^&\s"']*`)

// Redact removes API-key-bearing URLs/params from a message.
func Redact(s string) string {
	return apikeyRe.ReplaceAllString(s, "[redacted]")
}

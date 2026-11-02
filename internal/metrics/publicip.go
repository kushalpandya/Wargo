// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// publicIPServices answer a plain-text IPv4 address. They are tried in
// order; this is the only lookup Wargo makes to a third party for system
// metrics, and it runs only while someone views the network card.
var publicIPServices = []string{
	"https://api.ipify.org",
	"https://ipv4.icanhazip.com",
}

const (
	publicIPTTL      = 30 * time.Minute
	publicIPRetryTTL = 5 * time.Minute
)

// PublicIP is the host's address as seen from the internet.
type PublicIP struct {
	IP        string `json:"ip,omitempty"`
	Source    string `json:"source,omitempty"`
	CheckedAt int64  `json:"checkedAt,omitempty"` // unix ms
	Error     string `json:"error,omitempty"`
}

type publicIP struct {
	mu     sync.Mutex
	cur    PublicIP
	until  time.Time
	client *http.Client
	urls   []string
}

// get returns the cached address, looking it up when stale. Concurrent
// callers wait for one lookup.
func (p *publicIP) get(ctx context.Context) PublicIP {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().Before(p.until) {
		return p.cur
	}
	client, urls := p.client, p.urls
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	if urls == nil {
		urls = publicIPServices
	}
	res := PublicIP{CheckedAt: time.Now().UnixMilli(), Error: "public IP lookup failed"}
	for _, u := range urls {
		if ip, err := lookupIP(ctx, client, u); err == nil {
			res = PublicIP{IP: ip, Source: strings.TrimPrefix(u, "https://"), CheckedAt: res.CheckedAt}
			break
		}
	}
	if res.IP == "" && ctx.Err() != nil {
		return res // the caller went away; don't cache a failure for others
	}
	ttl := publicIPTTL
	if res.IP == "" {
		ttl = publicIPRetryTTL
	}
	p.cur, p.until = res, time.Now().Add(ttl)
	return res
}

func lookupIP(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "wargo")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", io.ErrUnexpectedEOF
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil {
		return "", err
	}
	return addr.String(), nil
}

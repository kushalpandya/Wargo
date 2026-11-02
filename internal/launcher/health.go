// SPDX-License-Identifier: AGPL-3.0-only

package launcher

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	healthInterval = 60 * time.Second
	healthTimeout  = 5 * time.Second
)

type healthResult struct {
	url     string
	up      bool
	checked time.Time
}

type healthChecker struct {
	mu      sync.Mutex
	results map[string]healthResult
	kickc   chan struct{}
	client  *http.Client
}

func newHealthChecker() *healthChecker {
	c := newHTTPClient(healthTimeout)
	// Health checks only probe reachability (no response data is trusted or
	// shown), so self-signed certificates common in homelabs are accepted.
	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	return &healthChecker{results: map[string]healthResult{}, kickc: make(chan struct{}, 1), client: c}
}

// newHTTPClient returns a client that never follows redirects to non-HTTP
// schemes and ignores proxy environment variables.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("redirect to unsupported scheme")
			}
			return nil
		},
	}
}

func (h *healthChecker) kick() {
	select {
	case h.kickc <- struct{}{}:
	default:
	}
}

func (h *healthChecker) forget(id string) {
	h.mu.Lock()
	delete(h.results, id)
	h.mu.Unlock()
}

// status returns "up"/"down" for tile id if a check for url has completed.
func (h *healthChecker) status(id, url string) *string {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.results[id]
	if !ok || r.url != url {
		return nil
	}
	s := "down"
	if r.up {
		s = "up"
	}
	return &s
}

// probe reports whether url answers with a status below 500.
func (h *healthChecker) probe(ctx context.Context, url string) bool {
	if _, err := validURL(url, true); err != nil {
		return false
	}
	try := func(method string) (int, error) {
		ctx, cancel := context.WithTimeout(ctx, healthTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("User-Agent", "Wargo-HealthCheck/1")
		resp, err := h.client.Do(req)
		if err != nil {
			return 0, err
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	code, err := try(http.MethodHead)
	if err != nil || code == http.StatusMethodNotAllowed || code == http.StatusNotImplemented {
		code, err = try(http.MethodGet)
	}
	return err == nil && code < 500
}

// Run checks tile health every minute while the launcher is enabled.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(healthInterval)
	defer t.Stop()
	s.checkAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.health.kickc:
		}
		s.checkAll(ctx)
	}
}

func (s *Service) checkAll(ctx context.Context) {
	if !s.settings(ctx).Enabled {
		return
	}
	tiles, err := s.list(ctx)
	if err != nil {
		return
	}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for _, t := range tiles {
		if t.HealthURL == "" || !t.Enabled {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t Tile) {
			defer func() { <-sem; wg.Done() }()
			up := s.health.probe(ctx, t.HealthURL)
			s.health.mu.Lock()
			s.health.results[t.ID] = healthResult{url: t.HealthURL, up: up, checked: time.Now()}
			s.health.mu.Unlock()
		}(t)
	}
	wg.Wait()
}

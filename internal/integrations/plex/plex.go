// SPDX-License-Identifier: AGPL-3.0-only

// Package plex is a small read-only client for a Plex Media Server: server
// identity, active sessions, watch history, recently added items and poster
// images. The token travels only in the X-Plex-Token header.
package plex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/netx"
)

const (
	maxResponse = 4 << 20
	// MaxPhoto bounds a proxied poster.
	MaxPhoto = 1 << 20
)

var (
	ErrUnauthorized = errors.New("plex: token rejected")
	ErrNotImage     = errors.New("plex: not an image")
)

// Client calls one Plex Media Server.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// transport is shared so clients built per request reuse connections.
var transport = netx.NewTransport(8 * time.Second)

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		// The token is a custom header, which net/http would forward on a
		// cross-host redirect: only follow redirects within the host.
		HTTP: &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: netx.SameHostRedirects(3)},
	}
}

var tokenRe = regexp.MustCompile(`(?i)(X-Plex-Token[=:]\s*)[^&\s"']+`)

// Redact removes a token from a message (the client never puts it in a
// URL, but a server error might echo it).
func Redact(s string) string { return tokenRe.ReplaceAllString(s, "${1}[redacted]") }

// stripURL removes the request URL from net/http errors.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (c *Client) request(ctx context.Context, path string, q url.Values, withToken bool, page *[2]int) (*http.Response, error) {
	u := c.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if withToken {
		req.Header.Set("X-Plex-Token", c.Token)
	}
	if page != nil {
		req.Header.Set("X-Plex-Container-Start", strconv.Itoa(page[0]))
		req.Header.Set("X-Plex-Container-Size", strconv.Itoa(page[1]))
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plex: %w", stripURL(err))
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		netx.Drain(res)
		return nil, ErrUnauthorized
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		netx.Drain(res)
		return nil, fmt.Errorf("plex: unexpected HTTP %d", res.StatusCode)
	}
	return res, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, withToken bool, page *[2]int, out any) error {
	res, err := c.request(ctx, path, q, withToken, page)
	if err != nil {
		return err
	}
	defer netx.Drain(res)
	b, err := netx.ReadLimited(res.Body, maxResponse)
	if err != nil {
		return fmt.Errorf("plex: %w", stripURL(err))
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("plex: invalid response (is this a Plex Media Server?)")
	}
	return nil
}

// Int accepts a JSON number or a numeric string (Plex mixes both).
type Int int64

func (n *Int) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return nil
	}
	*n = Int(v)
	return nil
}

// Bool accepts true/false, 1/0 or their string forms.
type Bool bool

func (v *Bool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "true", "1":
		*v = true
	default:
		*v = false
	}
	return nil
}

// Identity returns the server version (GET /identity, no token needed).
func (c *Client) Identity(ctx context.Context) (string, error) {
	var out struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
			Version           string `json:"version"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/identity", nil, false, nil, &out); err != nil {
		return "", err
	}
	if out.MediaContainer.MachineIdentifier == "" && out.MediaContainer.Version == "" {
		return "", errors.New("plex: this does not look like a Plex Media Server")
	}
	return out.MediaContainer.Version, nil
}

// Check returns the version and verifies the token.
func (c *Client) Check(ctx context.Context) (string, error) {
	v, err := c.Identity(ctx)
	if err != nil {
		return "", err
	}
	if _, err := c.Sessions(ctx); err != nil {
		return v, err
	}
	return v, nil
}

// Metadata is the subset of a Plex metadata item we use.
type Metadata struct {
	RatingKey        string `json:"ratingKey"`
	HistoryKey       string `json:"historyKey"`
	Type             string `json:"type"`
	Title            string `json:"title"`
	ParentTitle      string `json:"parentTitle"`
	GrandparentTitle string `json:"grandparentTitle"`
	ParentIndex      Int    `json:"parentIndex"`
	Index            Int    `json:"index"`
	Year             Int    `json:"year"`
	LeafCount        Int    `json:"leafCount"`
	ViewOffset       Int    `json:"viewOffset"`
	Duration         Int    `json:"duration"`
	ViewedAt         Int    `json:"viewedAt"`
	AddedAt          Int    `json:"addedAt"`
	AccountID        Int    `json:"accountID"`
	Thumb            string `json:"thumb"`
	ParentThumb      string `json:"parentThumb"`
	GrandparentThumb string `json:"grandparentThumb"`

	// GrandparentRatingKey is the show of an episode.
	GrandparentRatingKey string `json:"grandparentRatingKey"`

	// Sessions only.
	User *struct {
		Title string `json:"title"`
	} `json:"User"`
	Player *struct {
		Title    string `json:"title"`
		Product  string `json:"product"`
		Platform string `json:"platform"`
		State    string `json:"state"`
		Local    Bool   `json:"local"`
	} `json:"Player"`
	TranscodeSession *struct {
		VideoDecision string `json:"videoDecision"`
		AudioDecision string `json:"audioDecision"`
	} `json:"TranscodeSession"`
	Media []struct {
		Part []struct {
			Decision string `json:"decision"`
		} `json:"Part"`
	} `json:"Media"`
	SessionKey string `json:"sessionKey"`
}

// Poster picks the best poster path for an item: the show poster for an
// episode, the album cover for a track.
func (m Metadata) Poster() string {
	pick := func(xs ...string) string {
		for _, x := range xs {
			if x != "" {
				return x
			}
		}
		return ""
	}
	switch m.Type {
	case "episode":
		return pick(m.GrandparentThumb, m.ParentThumb, m.Thumb)
	case "season":
		return pick(m.Thumb, m.ParentThumb)
	case "track":
		return pick(m.ParentThumb, m.GrandparentThumb, m.Thumb)
	}
	return m.Thumb
}

// Decision is how a session is delivered: "transcode", "directstream" or
// "directplay".
func (m Metadata) Decision() string {
	part := ""
	if len(m.Media) > 0 && len(m.Media[0].Part) > 0 {
		part = m.Media[0].Part[0].Decision
	}
	if t := m.TranscodeSession; t != nil {
		switch {
		case t.VideoDecision == "transcode":
			return "transcode"
		case m.Type == "track" && t.AudioDecision == "transcode":
			return "transcode"
		case t.VideoDecision == "copy" || t.AudioDecision == "copy" || t.AudioDecision == "transcode":
			return "directstream"
		}
	}
	switch part {
	case "transcode":
		return "transcode"
	case "copy":
		return "directstream"
	}
	return "directplay"
}

type container struct {
	MediaContainer struct {
		Metadata []Metadata `json:"Metadata"`
		Account  []struct {
			ID   Int    `json:"id"`
			Name string `json:"name"`
		} `json:"Account"`
	} `json:"MediaContainer"`
}

// Sessions lists active streams (GET /status/sessions).
func (c *Client) Sessions(ctx context.Context) ([]Metadata, error) {
	var out container
	if err := c.get(ctx, "/status/sessions", nil, true, nil, &out); err != nil {
		return nil, err
	}
	return out.MediaContainer.Metadata, nil
}

// History lists the n most recent plays.
func (c *Client) History(ctx context.Context, n int) ([]Metadata, error) {
	var out container
	q := url.Values{"sort": {"viewedAt:desc"}}
	if err := c.get(ctx, "/status/sessions/history/all", q, true, &[2]int{0, n}, &out); err != nil {
		return nil, err
	}
	return trim(out.MediaContainer.Metadata, n), nil
}

// RecentlyAdded lists the n newest library items.
func (c *Client) RecentlyAdded(ctx context.Context, n int) ([]Metadata, error) {
	var out container
	if err := c.get(ctx, "/library/recentlyAdded", nil, true, &[2]int{0, n}, &out); err != nil {
		return nil, err
	}
	return trim(out.MediaContainer.Metadata, n), nil
}

func trim(l []Metadata, n int) []Metadata {
	if len(l) > n {
		return l[:n]
	}
	return l
}

// Accounts maps server account ids to names (GET /accounts).
func (c *Client) Accounts(ctx context.Context) (map[int64]string, error) {
	var out container
	if err := c.get(ctx, "/accounts", nil, true, nil, &out); err != nil {
		return nil, err
	}
	m := make(map[int64]string, len(out.MediaContainer.Account))
	for _, a := range out.MediaContainer.Account {
		if a.Name != "" {
			m[int64(a.ID)] = a.Name
		}
	}
	return m, nil
}

// photoTypes are the image types passed on to the browser.
var photoTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true}

// Photo fetches a resized image of a library path (validated by the
// caller) through Plex's photo transcoder.
func (c *Client) Photo(ctx context.Context, path string, width, height int) ([]byte, string, error) {
	q := url.Values{
		"width": {strconv.Itoa(width)}, "height": {strconv.Itoa(height)},
		"minSize": {"1"}, "upscale": {"1"}, "url": {path},
	}
	res, err := c.request(ctx, "/photo/:/transcode", q, true, nil)
	if err != nil {
		return nil, "", err
	}
	defer netx.Drain(res)
	ct := strings.ToLower(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]))
	if !photoTypes[ct] {
		return nil, "", ErrNotImage
	}
	b, err := netx.ReadLimited(res.Body, MaxPhoto)
	if err != nil {
		return nil, "", fmt.Errorf("plex: %w", stripURL(err))
	}
	return b, ct, nil
}

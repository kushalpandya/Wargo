// SPDX-License-Identifier: AGPL-3.0-only

// Package transmission is a small Transmission RPC client (legacy method
// names, supported by Transmission 2.x – 4.x).
package transmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/netx"
)

const (
	sessionHeader = "X-Transmission-Session-Id"
	maxResponse   = 32 << 20
)

var (
	// ErrUnauthorized means the RPC credentials were rejected.
	ErrUnauthorized = errors.New("transmission: authentication failed (check username and password)")
	// ErrForbidden is returned when Transmission's rpc-whitelist rejects us.
	ErrForbidden = errors.New("transmission: access denied by RPC whitelist")
)

// RPCError is a non-"success" result string.
type RPCError struct{ Result string }

func (e *RPCError) Error() string { return "transmission: " + e.Result }

// Client talks to one Transmission RPC endpoint. It is safe for concurrent use.
type Client struct {
	URL      string
	Username string
	Password string
	HTTP     *http.Client

	mu  sync.Mutex
	sid string
}

// New returns a client for the given RPC URL (e.g. http://host:9091/transmission/rpc).
func New(url, username, password string) *Client {
	return &Client{URL: url, Username: username, Password: password, HTTP: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: netx.NoRedirects,
	}}
}

type request struct {
	Method    string `json:"method"`
	Arguments any    `json:"arguments,omitempty"`
}

type response struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c *Client) sessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sid
}

func (c *Client) setSessionID(s string) {
	c.mu.Lock()
	c.sid = s
	c.mu.Unlock()
}

// Call invokes method with args and decodes the response arguments into out
// (if non-nil). It transparently performs the session-id handshake.
func (c *Client) Call(ctx context.Context, method string, args any, out any) error {
	body, err := json.Marshal(request{Method: method, Arguments: args})
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if sid := c.sessionID(); sid != "" {
			req.Header.Set(sessionHeader, sid)
		}
		if c.Username != "" || c.Password != "" {
			req.SetBasicAuth(c.Username, c.Password)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			return fmt.Errorf("transmission: %w", err)
		}
		switch res.StatusCode {
		case http.StatusConflict:
			sid := res.Header.Get(sessionHeader)
			drain(res)
			if sid == "" || attempt >= 2 {
				return errors.New("transmission: session handshake failed")
			}
			c.setSessionID(sid)
			continue
		case http.StatusUnauthorized:
			drain(res)
			return ErrUnauthorized
		case http.StatusForbidden:
			drain(res)
			return ErrForbidden
		case http.StatusOK:
		default:
			drain(res)
			return fmt.Errorf("transmission: unexpected HTTP %d (is this the RPC URL?)", res.StatusCode)
		}
		var r response
		err = json.NewDecoder(io.LimitReader(res.Body, maxResponse)).Decode(&r)
		drain(res)
		if err != nil {
			return fmt.Errorf("transmission: invalid response: %w", err)
		}
		if r.Result != "success" {
			return &RPCError{Result: r.Result}
		}
		if out != nil && len(r.Arguments) > 0 {
			return json.Unmarshal(r.Arguments, out)
		}
		return nil
	}
}

func drain(res *http.Response) { netx.Drain(res) }

// Session is the subset of session-get Wargo uses.
type Session struct {
	Version     string `json:"version"`
	RPCVersion  int    `json:"rpc-version"`
	DownloadDir string `json:"download-dir"`
}

func (c *Client) Session(ctx context.Context) (*Session, error) {
	var s Session
	err := c.Call(ctx, "session-get", map[string]any{"fields": []string{"version", "rpc-version", "download-dir"}}, &s)
	return &s, err
}

// Stats is the subset of session-stats Wargo uses.
type Stats struct {
	DownloadSpeed      int64 `json:"downloadSpeed"`
	UploadSpeed        int64 `json:"uploadSpeed"`
	ActiveTorrentCount int   `json:"activeTorrentCount"`
	PausedTorrentCount int   `json:"pausedTorrentCount"`
	TorrentCount       int   `json:"torrentCount"`
}

func (c *Client) Stats(ctx context.Context) (*Stats, error) {
	var s Stats
	err := c.Call(ctx, "session-stats", nil, &s)
	return &s, err
}

// FreeSpace returns free bytes at path.
func (c *Client) FreeSpace(ctx context.Context, path string) (int64, error) {
	var out struct {
		SizeBytes int64 `json:"size-bytes"`
	}
	err := c.Call(ctx, "free-space", map[string]any{"path": path}, &out)
	return out.SizeBytes, err
}

// Torrent as returned by torrent-get with TorrentFields.
type Torrent struct {
	ID             int64   `json:"id"`
	HashString     string  `json:"hashString"`
	Name           string  `json:"name"`
	Status         int     `json:"status"`
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

// TorrentFields are requested by Torrents.
var TorrentFields = []string{
	"id", "hashString", "name", "status", "percentDone", "rateDownload", "rateUpload", "eta",
	"sizeWhenDone", "leftUntilDone", "uploadRatio", "error", "errorString", "addedDate",
	"peersConnected", "downloadDir",
}

func (c *Client) Torrents(ctx context.Context) ([]Torrent, error) {
	var out struct {
		Torrents []Torrent `json:"torrents"`
	}
	err := c.Call(ctx, "torrent-get", map[string]any{"fields": TorrentFields}, &out)
	return out.Torrents, err
}

// ID identifies a torrent by numeric id or hash string.
type ID struct {
	Num  int64
	Hash string
}

func (id ID) value() any {
	if id.Hash != "" {
		return strings.ToLower(id.Hash)
	}
	return id.Num
}

func (c *Client) Start(ctx context.Context, id ID) error {
	return c.Call(ctx, "torrent-start", map[string]any{"ids": []any{id.value()}}, nil)
}

func (c *Client) Stop(ctx context.Context, id ID) error {
	return c.Call(ctx, "torrent-stop", map[string]any{"ids": []any{id.value()}}, nil)
}

func (c *Client) Remove(ctx context.Context, id ID, deleteData bool) error {
	return c.Call(ctx, "torrent-remove", map[string]any{"ids": []any{id.value()}, "delete-local-data": deleteData}, nil)
}

// Added describes a torrent created (or found duplicate) by torrent-add.
type Added struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HashString string `json:"hashString"`
	Duplicate  bool   `json:"duplicate"`
}

// Add queues a magnet/URL (filename) or base64 metainfo. Exactly one must be set.
func (c *Client) Add(ctx context.Context, filename, metainfoB64 string) (*Added, error) {
	args := map[string]any{}
	switch {
	case filename != "" && metainfoB64 == "":
		args["filename"] = filename
	case metainfoB64 != "" && filename == "":
		args["metainfo"] = metainfoB64
	default:
		return nil, errors.New("transmission: need exactly one of magnet or metainfo")
	}
	var out struct {
		Added     *Added `json:"torrent-added"`
		Duplicate *Added `json:"torrent-duplicate"`
	}
	if err := c.Call(ctx, "torrent-add", args, &out); err != nil {
		return nil, err
	}
	if out.Duplicate != nil {
		out.Duplicate.Duplicate = true
		return out.Duplicate, nil
	}
	if out.Added == nil {
		return &Added{}, nil
	}
	return out.Added, nil
}

// StatusName maps Transmission's numeric status.
func StatusName(s int) string {
	switch s {
	case 0:
		return "stopped"
	case 1:
		return "check-wait"
	case 2:
		return "checking"
	case 3:
		return "download-wait"
	case 4:
		return "downloading"
	case 5:
		return "seed-wait"
	case 6:
		return "seeding"
	}
	return "stopped"
}

// SPDX-License-Identifier: AGPL-3.0-only

// Package docker is a small Docker Engine API client (unix socket, no SDK)
// plus the Wargo containers feature built on it.
//
// Other packages (e.g. the terminal for `docker exec`) can reuse the client:
//
//	c := docker.Shared(cfg.Docker.Socket)        // or svc.Client()
//	resp, err := c.Do(ctx, "POST", "/containers/"+id+"/exec", body)
//
// Do prefixes the negotiated API version (e.g. /v1.47) and returns the raw
// response for any status code; the caller must close resp.Body. For
// connection hijacking (exec attach) use Dial to obtain a raw socket.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultSocket is used when no socket path is configured.
const DefaultSocket = "/var/run/docker.sock"

// maxAPIVersion is the newest API version this client speaks. The server
// may be newer; we then pin requests to this version.
const maxAPIVersion = "1.47"

// APIError is a non-2xx answer from the daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

// IsNotFound reports whether err is a Docker 404.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// Client talks to the Docker Engine API over a unix socket.
type Client struct {
	socket string
	hc     *http.Client

	mu         sync.Mutex
	prefix     string // "/v1.47" once negotiated
	apiVersion string // server's ApiVersion
	version    string // engine version
}

// NewClient creates a client for the unix socket at path.
func NewClient(path string) *Client {
	if path == "" {
		path = DefaultSocket
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
	}
	return &Client{socket: path, hc: &http.Client{Transport: tr}}
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*Client{}
)

// Shared returns a process-wide client for the socket path so features
// reuse one connection pool.
func Shared(path string) *Client {
	if path == "" {
		path = DefaultSocket
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if c, ok := shared[path]; ok {
		return c
	}
	c := NewClient(path)
	shared[path] = c
	return c
}

// Socket returns the unix socket path.
func (c *Client) Socket() string { return c.socket }

// Dial opens a raw connection to the daemon socket (for hijacked streams).
func (c *Client) Dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", c.socket)
}

// versionLess compares "1.41" style versions.
func versionLess(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	for i := 0; i < 2; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// VersionInfo is the subset of GET /version we use.
type VersionInfo struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	MinAPI     string `json:"MinAPIVersion"`
	Os         string `json:"Os"`
	Arch       string `json:"Arch"`
}

// Ping fetches the (unversioned) /version endpoint and (re)negotiates the
// API version.
func (c *Client) Ping(ctx context.Context) (VersionInfo, error) {
	var v VersionInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return v, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return v, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return v, readError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err != nil {
		return v, err
	}
	use := maxAPIVersion
	if v.APIVersion != "" && versionLess(v.APIVersion, use) {
		use = v.APIVersion
	}
	c.mu.Lock()
	c.prefix, c.apiVersion, c.version = "/v"+use, v.APIVersion, v.Version
	c.mu.Unlock()
	return v, nil
}

// APIVersion returns the version used for requests ("" before negotiation).
func (c *Client) APIVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.TrimPrefix(c.prefix, "/v")
}

func (c *Client) ensure(ctx context.Context) (string, error) {
	c.mu.Lock()
	p := c.prefix
	c.mu.Unlock()
	if p != "" {
		return p, nil
	}
	if _, err := c.Ping(ctx); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prefix, nil
}

// Do sends a request to path (e.g. "/containers/json?all=1") under the
// negotiated API version. A non-nil body is sent as JSON. The response is
// returned for every status code; callers must close resp.Body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	p, err := c.ensure(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+p+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.hc.Do(req)
}

func readError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		msg = m.Message
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

// call sends in (JSON-encoded when non-nil) and decodes the answer into
// out (when non-nil). 304 Not Modified counts as success.
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil
	}
	if resp.StatusCode >= 300 {
		return readError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// stream performs a request and returns the body for status < 300.
func (c *Client) stream(ctx context.Context, method, path string, in any) (io.ReadCloser, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.Do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, readError(resp)
	}
	return resp.Body, nil
}

// ContainerName resolves a container id (or prefix) to its name.
func (c *Client) ContainerName(ctx context.Context, id string) (string, error) {
	if !validRef(id) {
		return "", errors.New("invalid id")
	}
	var v struct {
		Name string `json:"Name"`
	}
	if err := c.call(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &v); err != nil {
		return "", err
	}
	return strings.TrimPrefix(v.Name, "/"), nil
}

// ContainerEvent is one entry from the daemon's event history.
type ContainerEvent struct {
	Action string // e.g. "die", "oom", "health_status: unhealthy"
	ID     string
	Name   string
	Image  string
	Exit   string // exit code for "die"
	Time   time.Time
}

// RecentEvents returns container events between since and until from the
// daemon's in-memory event history (it keeps only the most recent ones).
// actions limits the event types, e.g. "die", "oom", "health_status".
func (c *Client) RecentEvents(ctx context.Context, since, until time.Time, actions ...string) ([]ContainerEvent, error) {
	f := map[string][]string{"type": {"container"}}
	if len(actions) > 0 {
		f["event"] = actions
	}
	fj, _ := json.Marshal(f)
	q := url.Values{}
	q.Set("since", strconv.FormatInt(since.Unix(), 10))
	q.Set("until", strconv.FormatInt(until.Unix(), 10))
	q.Set("filters", string(fj))
	body, err := c.stream(ctx, http.MethodGet, "/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var out []ContainerEvent
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() && len(out) < 5000 {
		var ev struct {
			Action   string `json:"Action"`
			TimeNano int64  `json:"timeNano"`
			Actor    struct {
				ID         string            `json:"ID"`
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		out = append(out, ContainerEvent{
			Action: ev.Action, ID: ev.Actor.ID, Name: ev.Actor.Attributes["name"], Image: ev.Actor.Attributes["image"],
			Exit: ev.Actor.Attributes["exitCode"], Time: time.Unix(0, ev.TimeNano),
		})
	}
	return out, sc.Err()
}

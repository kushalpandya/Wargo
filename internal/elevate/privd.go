// SPDX-License-Identifier: AGPL-3.0-only

package elevate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/privproto"
)

var errPasskeyRejected = httpx.Errorf(http.StatusUnauthorized, "elevation_failed", "Passkey approval was rejected")

// PrivdRequest is a privileged run authorised by a passkey assertion.
type PrivdRequest struct {
	Action    string
	Target    string
	Argv      []string
	Challenge string
	Assertion json.RawMessage
	Origin    string
}

// PrivdClient talks to wargo-privd over its unix socket.
type PrivdClient struct{ socket string }

func NewPrivdClient(socket string) *PrivdClient {
	if socket == "" {
		socket = privproto.DefaultSocket
	}
	return &PrivdClient{socket: socket}
}

func (c *PrivdClient) call(ctx context.Context, req privproto.Request, timeout time.Duration) (*privproto.Response, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, httpx.Errorf(http.StatusServiceUnavailable, "privd_unavailable", "wargo-privd is not running")
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp privproto.Response
	if err := json.NewDecoder(io.LimitReader(conn, 64<<20)).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Available reports whether the privd socket exists and answers.
func (c *PrivdClient) Available() bool {
	if _, err := os.Stat(c.socket); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := c.call(ctx, privproto.Request{Op: "status"}, time.Second)
	return err == nil && resp.OK && resp.Credentials > 0
}

// Challenge asks privd for WebAuthn request options bound to action+target.
func (c *PrivdClient) Challenge(ctx context.Context, action, target, origin string) (map[string]any, error) {
	resp, err := c.call(ctx, privproto.Request{Op: "challenge", Action: action, Target: target, Origin: origin}, 5*time.Second)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, httpx.Errorf(http.StatusBadGateway, "privd_error", resp.Error)
	}
	return map[string]any{"challenge": resp.ChallengeID, "options": resp.Options}, nil
}

// Run asks privd to verify the assertion and execute argv.
func (c *PrivdClient) Run(ctx context.Context, req PrivdRequest, stdin io.Reader) ([]byte, error) {
	var in []byte
	if stdin != nil {
		b, err := io.ReadAll(io.LimitReader(stdin, 32<<20))
		if err != nil {
			return nil, err
		}
		in = b
	}
	resp, err := c.call(ctx, privproto.Request{
		Op: "run", Action: req.Action, Target: req.Target, Origin: req.Origin,
		ChallengeID: req.Challenge, Assertion: req.Assertion, Argv: req.Argv, Stdin: in,
	}, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	if resp.Rejected {
		return nil, errPasskeyRejected
	}
	if !resp.OK {
		return resp.Output, httpx.Errorf(http.StatusBadGateway, "command_failed", resp.Error)
	}
	return resp.Output, nil
}

var _ = errors.New

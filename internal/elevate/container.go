// SPDX-License-Identifier: AGPL-3.0-only

package elevate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/helper"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Container mode: Wargo is root inside a privileged container, so sudo and
// wargo-privd have no role. Each privileged request is still approved
// individually — with the admin account's own password (checked against the
// host's shadow file, throttled) or a passkey step-up — and the action then
// runs in-process (`wargo helper` operations) or on the host via nsenter.
// This keeps the approval model and audit trail; note that a compromised
// Wargo process is root here regardless (see docs/SECURITY.md).

// HostAuth is implemented by *auth.Service.
type HostAuth interface {
	VerifyAdminPassword(password string) error
	HasPasskeys(ctx context.Context) bool
	BeginStepUp(r *http.Request) (any, string, error)
	FinishStepUp(r *http.Request, ceremonyID string, body []byte) (string, error)
}

var errBadAccountPassword = httpx.Errorf(http.StatusUnauthorized, "elevation_failed", "Incorrect password")

type stepUp struct {
	action, target string
	expires        time.Time
}

type containerState struct {
	auth    HostAuth
	mu      sync.Mutex
	pending map[string]stepUp
}

// WithHostAuth enables container-mode elevation. Returns s for chaining.
func (s *Service) WithHostAuth(a HostAuth) *Service {
	s.ctr = &containerState{auth: a, pending: map[string]stepUp{}}
	return s
}

func (s *Service) containerMode() bool { return s.ctr != nil }

func (s *Service) containerMethods() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return map[string]any{
		"sudo":     false,
		"password": true,
		"passkey":  s.ctr.auth.HasPasskeys(ctx),
		"mode":     "container",
	}
}

// containerChallenge starts a passkey step-up bound to action and target.
func (s *Service) containerChallenge(r *http.Request, action, target string) (map[string]any, error) {
	opts, id, err := s.ctr.auth.BeginStepUp(r)
	if err != nil {
		return nil, err
	}
	c := s.ctr
	c.mu.Lock()
	now := time.Now()
	for k, v := range c.pending {
		if now.After(v.expires) {
			delete(c.pending, k)
		}
	}
	c.pending[id] = stepUp{action: action, target: target, expires: now.Add(5 * time.Minute)}
	c.mu.Unlock()
	return map[string]any{"challenge": id, "options": opts}, nil
}

func (s *Service) takeStepUp(id, action, target string) bool {
	c := s.ctr
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[id]
	delete(c.pending, id)
	return ok && time.Now().Before(p.expires) && p.action == action && p.target == target
}

// containerApprove verifies the credential for action+target.
func (s *Service) containerApprove(r *http.Request, cred *Credential, action, target string) error {
	switch cred.Method {
	case "password", "sudo":
		pw := cred.Password
		cred.Password = ""
		if !validPassword(pw) {
			return errBadAccountPassword
		}
		start := time.Now()
		err := s.ctr.auth.VerifyAdminPassword(pw)
		if d := 300*time.Millisecond - time.Since(start); d > 0 {
			time.Sleep(d)
		}
		if err != nil {
			slog.Info("elevation password check failed", "err", err)
			return errBadAccountPassword
		}
		return nil
	case "passkey":
		if cred.Challenge == "" || !s.takeStepUp(cred.Challenge, action, target) {
			return errPasskeyRejected
		}
		if _, err := s.ctr.auth.FinishStepUp(r, cred.Challenge, cred.Assertion); err != nil {
			slog.Info("elevation passkey rejected", "err", err)
			return errPasskeyRejected
		}
		return nil
	}
	return httpx.BadRequest("unknown elevation method")
}

// runApproved executes argv as root after approval: `wargo helper` argv
// runs in-process, anything else on the host via nsenter.
func (s *Service) runApproved(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	var buf bytes.Buffer
	var err error
	code := 0
	if len(argv) >= 2 && argv[0] == s.exe && argv[1] == "helper" {
		code, err = helper.Exec(argv[2:], stdin, &buf, &buf)
	} else {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		cmd := host.Command(ctx, argv...)
		cmd.Stdin = orEmpty(stdin)
		cmd.Stdout, cmd.Stderr = &buf, &buf
		err = cmd.Run()
	}
	out := buf.Bytes()
	if err == nil && code != 0 {
		err = fmt.Errorf("exit status %d", code)
	}
	if err != nil {
		slog.Warn("elevated command failed", "argv", argv, "err", err)
		if errors.Is(err, fsops.ErrSymlinkInPath) {
			return out, errSymlinkInPath()
		}
		msg := err.Error()
		if l := strings.TrimSpace(lastLine(out)); l != "" {
			msg += ": " + l
		}
		return out, httpx.Errorf(http.StatusBadGateway, "command_failed", msg)
	}
	return out, nil
}

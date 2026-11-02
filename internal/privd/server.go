// SPDX-License-Identifier: AGPL-3.0-only

package privd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/kushalpandya/wargo/internal/privproto"
)

const (
	challengeTTL  = 2 * time.Minute
	maxPending    = 32
	maxRequest    = 48 << 20
	maxOutput     = 32 << 20
	commandTTL    = 10 * time.Minute
	readDeadline  = 30 * time.Second
	writeDeadline = commandTTL + time.Minute
)

// CredentialStore is the JSON file of passkeys trusted for approvals.
type CredentialStore struct {
	UserHandle  string                `json:"userHandle"` // base64url
	RPIDs       []string              `json:"rpIds"`
	Credentials []webauthn.Credential `json:"credentials"`
}

type waUser struct {
	id    []byte
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.id }
func (u *waUser) WebAuthnName() string                       { return "wargo" }
func (u *waUser) WebAuthnDisplayName() string                { return "Wargo admin" }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

type pending struct {
	session webauthn.SessionData
	binding [32]byte
	origin  string
	rpID    string
	expires time.Time
	created time.Time
}

// Server answers privproto requests on a unix socket.
type Server struct {
	cfg Config

	mu      sync.Mutex
	pending map[string]*pending
	credMu  sync.Mutex

	now func() time.Time
	// peerUID returns the uid of the connected process.
	peerUID func(net.Conn) (int, error)
	// run executes a validated command (replaced in tests).
	run func(ctx context.Context, argv []string, stdin []byte) ([]byte, error)
}

// New creates a server for cfg.
func New(cfg Config) *Server {
	return &Server{cfg: cfg, pending: map[string]*pending{}, now: time.Now, peerUID: peerUID, run: runCommand}
}

// binding ties a challenge to exactly one action, target and origin.
func binding(action, target, origin string) [32]byte {
	h := sha256.New()
	for _, s := range []string{action, target, origin} {
		fmt.Fprintf(h, "%d:%s\x00", len(s), s)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func peerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return -1, err
	}
	if serr != nil {
		return -1, serr
	}
	return int(cred.Uid), nil
}

// Serve accepts connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		go s.handleConn(c)
	}
}

func (s *Server) handleConn(c net.Conn) {
	defer c.Close()
	uid, err := s.peerUID(c)
	if err != nil || uid != s.cfg.AllowedUID {
		slog.Warn("rejected peer", "uid", uid, "err", err)
		json.NewEncoder(c).Encode(privproto.Response{Error: "permission denied"})
		return
	}
	c.SetReadDeadline(time.Now().Add(readDeadline))
	var req privproto.Request
	if err := json.NewDecoder(io.LimitReader(c, maxRequest)).Decode(&req); err != nil {
		json.NewEncoder(c).Encode(privproto.Response{Error: "malformed request"})
		return
	}
	c.SetWriteDeadline(time.Now().Add(writeDeadline))
	resp := s.Handle(context.Background(), req)
	if err := json.NewEncoder(c).Encode(resp); err != nil {
		slog.Warn("write response", "err", err)
	}
}

// Handle processes one request.
func (s *Server) Handle(ctx context.Context, req privproto.Request) privproto.Response {
	switch req.Op {
	case "status":
		cs, err := s.loadCreds()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return privproto.Response{Error: "credentials unreadable: " + err.Error()}
		}
		return privproto.Response{OK: true, Credentials: len(cs.Credentials)}
	case "challenge":
		return s.challenge(req)
	case "run":
		return s.runOp(ctx, req)
	}
	return privproto.Response{Error: "unknown op"}
}

func (s *Server) loadCreds() (CredentialStore, error) {
	var cs CredentialStore
	f, err := os.Open(s.cfg.Credentials)
	if err != nil {
		return cs, err
	}
	defer f.Close()
	// Whoever can write this file can approve root actions with their own
	// passkey: refuse it unless only root can (when running as root).
	if err := checkTrustedPath(s.cfg.Credentials, f); err != nil {
		return cs, err
	}
	b, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil {
		return cs, err
	}
	if err := json.Unmarshal(b, &cs); err != nil {
		return cs, err
	}
	return cs, nil
}

func (s *Server) saveCreds(cs CredentialStore) error {
	b, err := json.MarshalIndent(cs, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.cfg.Credentials)
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.cfg.Credentials)
}

func (s *Server) relyingParty(origin string) (*webauthn.WebAuthn, string, string, error) {
	canonical, rpID, ok := s.cfg.matchOrigin(origin)
	if !ok {
		return nil, "", "", fmt.Errorf("origin %q is not configured in privd.toml", origin)
	}
	wa, err := webauthn.New(&webauthn.Config{RPID: rpID, RPDisplayName: "Wargo", RPOrigins: []string{canonical}})
	return wa, rpID, canonical, err
}

func (s *Server) loadUser(rpID string) (*waUser, CredentialStore, error) {
	cs, err := s.loadCreds()
	if err != nil {
		return nil, cs, errors.New("no trusted passkeys (run the trust step in Wargo settings)")
	}
	if len(cs.Credentials) == 0 {
		return nil, cs, errors.New("no trusted passkeys")
	}
	if len(cs.RPIDs) > 0 && !slices.Contains(cs.RPIDs, rpID) {
		return nil, cs, fmt.Errorf("no passkeys trusted for %s", rpID)
	}
	id, err := base64.RawURLEncoding.DecodeString(cs.UserHandle)
	if err != nil || len(id) == 0 {
		return nil, cs, errors.New("credential store has an invalid user handle")
	}
	return &waUser{id: id, creds: cs.Credentials}, cs, nil
}

func randomID() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) challenge(req privproto.Request) privproto.Response {
	if req.Action == "" {
		return privproto.Response{Error: "missing action"}
	}
	wa, rpID, origin, err := s.relyingParty(req.Origin)
	if err != nil {
		return privproto.Response{Error: err.Error()}
	}
	u, _, err := s.loadUser(rpID)
	if err != nil {
		return privproto.Response{Error: err.Error()}
	}
	opts, sd, err := wa.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return privproto.Response{Error: err.Error()}
	}
	now := s.now()
	sd.Expires = now.Add(challengeTTL)
	optJSON, err := json.Marshal(opts)
	if err != nil {
		return privproto.Response{Error: err.Error()}
	}
	id := randomID()
	s.mu.Lock()
	s.gcLocked(now)
	if len(s.pending) >= maxPending {
		var oldest string
		for k, p := range s.pending {
			if oldest == "" || p.created.Before(s.pending[oldest].created) {
				oldest = k
			}
		}
		delete(s.pending, oldest)
	}
	s.pending[id] = &pending{
		session: *sd, binding: binding(req.Action, req.Target, origin), origin: origin, rpID: rpID,
		expires: now.Add(challengeTTL), created: now,
	}
	s.mu.Unlock()
	return privproto.Response{OK: true, ChallengeID: id, Options: optJSON}
}

func (s *Server) gcLocked(now time.Time) {
	for k, p := range s.pending {
		if now.After(p.expires) {
			delete(s.pending, k)
		}
	}
}

// take removes and returns a pending challenge (single use).
func (s *Server) take(id string) *pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	delete(s.pending, id)
	if p == nil || s.now().After(p.expires) {
		return nil
	}
	return p
}

func rejected(msg string) privproto.Response {
	return privproto.Response{Rejected: true, Error: msg}
}

func (s *Server) runOp(ctx context.Context, req privproto.Request) privproto.Response {
	p := s.take(req.ChallengeID)
	if p == nil {
		return rejected("approval expired or unknown; try again")
	}
	_, _, origin, err := s.relyingParty(req.Origin)
	if err != nil {
		return rejected(err.Error())
	}
	want := binding(req.Action, req.Target, origin)
	if subtle.ConstantTimeCompare(want[:], p.binding[:]) != 1 {
		slog.Warn("challenge binding mismatch", "action", req.Action, "target", req.Target)
		return rejected("approval does not match this action")
	}
	if err := s.verify(p, req.Assertion); err != nil {
		slog.Warn("assertion rejected", "action", req.Action, "err", err)
		return rejected("passkey verification failed")
	}
	argv, err := s.cfg.checkArgv(req.Argv)
	if err != nil {
		slog.Warn("command rejected", "argv", req.Argv, "err", err)
		return privproto.Response{Error: err.Error()}
	}
	if err := checkTrustedFile(argv[0]); err != nil {
		return privproto.Response{Error: "untrusted executable: " + err.Error()}
	}
	if argv[0] == s.cfg.SystemdRun {
		var logs []*os.File
		if argv, logs, err = s.cfg.openLogTargets(argv); err != nil {
			slog.Warn("log target rejected", "argv", req.Argv, "err", err)
			return privproto.Response{Error: err.Error()}
		}
		defer func() {
			for _, f := range logs {
				f.Close()
			}
		}()
	}
	slog.Info("approved", "action", req.Action, "target", req.Target, "argv", argv)
	out, err := s.run(ctx, argv, req.Stdin)
	if err != nil {
		msg := err.Error()
		if l := lastLine(out); l != "" {
			msg += ": " + l
		}
		return privproto.Response{Output: out, Error: msg}
	}
	return privproto.Response{OK: true, Output: out}
}

// verify checks the assertion for p and persists the updated sign count.
func (s *Server) verify(p *pending, assertion json.RawMessage) error {
	if len(assertion) == 0 {
		return errors.New("missing assertion")
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(assertion)
	if err != nil {
		return err
	}
	wa, _, _, err := s.relyingParty(p.origin)
	if err != nil {
		return err
	}
	s.credMu.Lock()
	defer s.credMu.Unlock()
	u, cs, err := s.loadUser(p.rpID)
	if err != nil {
		return err
	}
	if p.session.UserVerification != protocol.VerificationRequired {
		return errors.New("user verification not required by session")
	}
	cred, err := wa.ValidateLogin(u, p.session, parsed)
	if err != nil {
		return err
	}
	if cred.Authenticator.CloneWarning {
		return errors.New("signature counter did not increase (possible cloned authenticator)")
	}
	for i := range cs.Credentials {
		if bytes.Equal(cs.Credentials[i].ID, cred.ID) {
			cs.Credentials[i].Authenticator.SignCount = cred.Authenticator.SignCount
			cs.Credentials[i].Flags = cred.Flags
		}
	}
	if err := s.saveCreds(cs); err != nil {
		slog.Warn("could not persist sign count", "err", err)
	}
	return nil
}

type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else {
		c.truncated = true
	}
	return len(p), nil
}

func runCommand(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTTL)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8", "HOME=/root"}
	cmd.Dir = "/"
	cmd.Stdin = bytes.NewReader(stdin)
	var out cappedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if out.truncated {
		out.buf.WriteString("\n[output truncated]\n")
	}
	return out.buf.Bytes(), err
}

// Listen returns the systemd-activated socket or creates one at path,
// accessible to root and the allowed user's primary group only.
func Listen(cfg Config) (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == fmt.Sprint(os.Getpid()) && os.Getenv("LISTEN_FDS") == "1" {
		f := os.NewFile(3, "systemd-socket")
		ln, err := net.FileListener(f)
		f.Close()
		return ln, err
	}
	if st, err := os.Lstat(cfg.Socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", cfg.Socket)
		}
		os.Remove(cfg.Socket)
	}
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", cfg.Socket)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	gid := -1
	if os.Geteuid() == 0 {
		gid = primaryGID(cfg.AllowedUID)
	}
	if gid >= 0 {
		if err := os.Chown(cfg.Socket, 0, gid); err != nil {
			ln.Close()
			return nil, err
		}
		err = os.Chmod(cfg.Socket, 0o660)
	} else {
		err = os.Chmod(cfg.Socket, 0o600)
	}
	if err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300]
	}
	return l
}

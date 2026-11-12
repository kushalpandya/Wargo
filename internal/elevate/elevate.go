// SPDX-License-Identifier: AGPL-3.0-only

// Package elevate runs privileged commands on demand. Wargo never holds
// root: each privileged request carries a sudo password (piped to
// `sudo -k -S`, never stored) or a passkey assertion verified by the
// optional wargo-privd helper.
package elevate

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// Header carries base64(JSON Credential) on privileged requests.
const Header = "X-Wargo-Elevation"

// Credential is what the browser supplies to elevate one request.
type Credential struct {
	Method    string          `json:"method"` // "sudo" | "passkey"
	Password  string          `json:"password,omitempty"`
	Challenge string          `json:"challenge,omitempty"`
	Assertion json.RawMessage `json:"assertion,omitempty"`
}

// ErrRequired asks the client to prompt and retry with a credential.
func (s *Service) ErrRequired(action, target string) *httpx.Error {
	return httpx.Errorf(http.StatusForbidden, "elevation_required", "Administrator approval required").
		With(map[string]any{"action": action, "target": target, "methods": s.Methods()})
}

var errBadPassword = httpx.Errorf(http.StatusUnauthorized, "elevation_failed", "Incorrect sudo password")

// errSymlinkInPath matches the error the file manager returns when it meets
// a symbolic link itself (files.fsErr), so elevated refusals look the same.
func errSymlinkInPath() error {
	return httpx.Errorf(http.StatusBadRequest, "symlink_in_path", "Path contains a symbolic link")
}

// helperSymlinkRefusal reports whether a failed helper's output says it
// refused a path because of a symbolic link (fsops.ErrSymlinkInPath).
func helperSymlinkRefusal(out string) bool {
	return strings.Contains(out, fsops.ErrSymlinkInPath.Error())
}

func spoolFailed() error {
	return httpx.Errorf(http.StatusInternalServerError, "elevation_spool_failed", "Could not prepare the data for the approved command")
}

// Throttler is satisfied by auth.Throttle.
type Throttler interface {
	Allow(ip string) (bool, time.Duration)
	Fail(ip string)
	Success(ip string)
	// Release ends an attempt that neither failed nor succeeded
	// authentication (e.g. the approved command itself failed).
	Release(ip string)
}

type Service struct {
	audit    *audit.Log
	proxy    *httpx.ProxyTrust
	throttle Throttler
	privd    *PrivdClient
	exe      string
	store    *store.Store
	ctr      *containerState // container mode (nil natively)
	tempDir  string          // private spool dir for sudo payloads
}

// WithTempDir sets the private directory where runSudo spools a command's
// stdin payload so it never shares sudo's stdin with the password. It is
// created (or tightened to 0700) now; spool re-checks it on every use.
func (s *Service) WithTempDir(dir string) *Service {
	s.tempDir = dir
	if err := s.prepareTempDir(); err != nil {
		slog.Warn("elevation temp dir is not usable", "dir", dir, "err", err)
	}
	return s
}

// prepareTempDir makes sure tempDir is a real directory (not a symlink)
// owned by this user, creating it and chmodding it to 0700 as needed.
func (s *Service) prepareTempDir() error {
	if s.tempDir == "" {
		return errors.New("no temp dir configured")
	}
	if err := os.MkdirAll(s.tempDir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(s.tempDir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("temp dir is not a directory")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return errors.New("temp dir is owned by someone else")
	}
	if fi.Mode().Perm() != 0o700 {
		return os.Chmod(s.tempDir, 0o700)
	}
	return nil
}

func New(al *audit.Log, proxy *httpx.ProxyTrust, t Throttler, privdSocket string) *Service {
	exe, _ := os.Executable()
	return &Service{audit: al, proxy: proxy, throttle: t, privd: NewPrivdClient(privdSocket), exe: exe}
}

// Methods reports which elevation methods can work on this host.
func (s *Service) Methods() map[string]any {
	if s.containerMode() {
		return s.containerMethods()
	}
	_, sudoErr := exec.LookPath("sudo")
	nnp := noNewPrivs()
	return map[string]any{
		"sudo":           sudoErr == nil && !nnp,
		"passkey":        s.privd.Available(),
		"noNewPrivsHint": nnp,
		// wargo-privd (passkey-approved root actions) is Linux-only.
		"privd": runtime.GOOS == "linux",
	}
}

func noNewPrivs() bool {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "NoNewPrivs:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "NoNewPrivs:")) == "1"
		}
	}
	return false
}

// Helper returns argv for `wargo helper <args...>`.
func (s *Service) Helper(args ...string) []string {
	return append([]string{s.exe, "helper"}, args...)
}

func credentialFrom(r *http.Request) (*Credential, error) {
	h := r.Header.Get(Header)
	if h == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(h)
	if err != nil {
		return nil, httpx.BadRequest("malformed elevation header")
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, httpx.BadRequest("malformed elevation header")
	}
	return &c, nil
}

// Has reports whether r carries an elevation credential.
func Has(r *http.Request) bool { return r.Header.Get(Header) != "" }

// Run executes argv as root for action on target (shown to the user and
// bound into passkey challenges), authorised by r's credential.
// Output is combined stdout+stderr.
func (s *Service) Run(r *http.Request, action, target string, argv []string, stdin io.Reader) ([]byte, error) {
	cred, err := credentialFrom(r)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, s.ErrRequired(action, target)
	}
	ip := s.proxy.ClientIP(r)
	if ok, wait := s.throttle.Allow(ip); !ok {
		return nil, httpx.Errorf(http.StatusTooManyRequests, "throttled", "Too many attempts. Try again in "+wait.Round(time.Second).String())
	}
	if s.containerMode() {
		return s.runContainer(r, cred, ip, action, target, argv, stdin)
	}
	var out []byte
	switch cred.Method {
	case "sudo":
		if !validPassword(cred.Password) {
			err = errBadPassword
			break
		}
		out, err = s.runSudo(r.Context(), cred.Password, argv, stdin)
		cred.Password = ""
	case "passkey":
		out, err = s.privd.Run(r.Context(), PrivdRequest{
			Action: action, Target: target, Argv: argv, Challenge: cred.Challenge, Assertion: cred.Assertion,
			Origin: r.Header.Get("Origin"),
		}, stdin)
	default:
		err = httpx.BadRequest("unknown elevation method")
	}
	switch {
	case errors.Is(err, errBadPassword) || errors.Is(err, errPasskeyRejected):
		s.throttle.Fail(ip)
	case err == nil:
		s.throttle.Success(ip)
	default:
		s.throttle.Release(ip)
	}
	ctx := audit.WithElevation(r.Context(), cred.Method)
	s.audit.Record(r.WithContext(ctx), action, target, err, map[string]any{"argv": argv})
	return out, err
}

// runContainer approves and runs a request in container mode. Everything
// happens with full root privileges even when the caller is inside
// host.AsUser (file manager handlers).
func (s *Service) runContainer(r *http.Request, cred *Credential, ip, action, target string, argv []string, stdin io.Reader) ([]byte, error) {
	var out []byte
	method := cred.Method
	if method == "sudo" {
		method = "password"
	}
	err := host.AsRoot(func() error {
		var err error
		if err = s.containerApprove(r, cred, action, target); err != nil {
			if errors.Is(err, errBadAccountPassword) || errors.Is(err, errPasskeyRejected) {
				s.throttle.Fail(ip)
			} else {
				s.throttle.Release(ip)
			}
		} else {
			s.throttle.Success(ip)
			out, err = s.runApproved(r.Context(), argv, stdin)
		}
		ctx := audit.WithElevation(r.Context(), method)
		s.audit.Record(r.WithContext(ctx), action, target, err, map[string]any{"argv": argv})
		return err
	})
	return out, err
}

func (s *Service) runSudo(ctx context.Context, password string, argv []string, stdin io.Reader) ([]byte, error) {
	if password == "" {
		return nil, errBadPassword
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if stdin != nil {
		if len(argv) < 2 || argv[1] != "helper" {
			return nil, httpx.Errorf(http.StatusBadRequest, "elevation_bad_command", "Only Wargo's own helper can take data with approval")
		}
		path, err := s.spool(stdin)
		if err != nil {
			return nil, err
		}
		defer os.Remove(path)
		argv = append(append(append([]string{}, argv[:2]...), "--stdin-file", path), argv[2:]...)
	}
	args := append([]string{"-k", "-S", "-p", "", "--"}, argv...)
	cmd := exec.CommandContext(ctx, "sudo", args...)
	// Only the password goes to sudo's stdin: the payload travels in a file.
	cmd.Stdin = strings.NewReader(password + "\n")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.Bytes()
	if err != nil {
		low := strings.ToLower(string(out))
		if strings.Contains(low, "incorrect password") || strings.Contains(low, "sorry, try again") ||
			strings.Contains(low, "no password was provided") || strings.Contains(low, "authentication failure") {
			return nil, errBadPassword
		}
		if strings.Contains(low, "is not in the sudoers") || strings.Contains(low, "not allowed to execute") {
			return out, httpx.Errorf(http.StatusForbidden, "sudo_denied", "sudo denied this command")
		}
		if strings.Contains(low, "no new privileges") {
			return out, httpx.Errorf(http.StatusConflict, "no_new_privs",
				"sudo is blocked by NoNewPrivileges; see docs/SECURITY.md")
		}
		slog.Warn("elevated command failed", "argv", argv, "err", err)
		if helperSymlinkRefusal(string(out)) {
			return out, errSymlinkInPath()
		}
		return out, httpx.Errorf(http.StatusBadGateway, "command_failed", fmt.Sprintf("%s: %s", err, lastLine(out)))
	}
	return out, nil
}

// spool copies r into a new 0600 file in the private temp dir and returns
// its path. The caller removes it.
func (s *Service) spool(r io.Reader) (string, error) {
	if err := s.prepareTempDir(); err != nil {
		slog.Warn("elevation temp dir is not usable", "dir", s.tempDir, "err", err)
		return "", spoolFailed()
	}
	f, err := os.CreateTemp(s.tempDir, "elev-*")
	if err != nil {
		slog.Warn("could not create elevation spool file", "err", err)
		return "", spoolFailed()
	}
	const limit = 32 << 20
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		slog.Warn("could not write elevation spool file", "err", err)
		err = spoolFailed()
	} else if n > limit {
		err = httpx.Errorf(http.StatusRequestEntityTooLarge, "too_large", "That is too large to send with administrator approval")
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// validPassword rejects empty passwords and ones containing line breaks or
// NUL, which sudo would read as separate password attempts.
func validPassword(pw string) bool {
	return pw != "" && len(pw) <= 1024 && !strings.ContainsAny(pw, "\r\n\x00")
}

func orEmpty(r io.Reader) io.Reader {
	if r == nil {
		return strings.NewReader("")
	}
	return r
}

func lastLine(b []byte) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// Register mounts the elevation info endpoints.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/elevate/methods", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, s.Methods())
	})
	mux.HandleFunc("POST /api/elevate/challenge", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		var req struct {
			Action string `json:"action"`
			Target string `json:"target"`
		}
		if err := httpx.Decode(r, &req); err != nil {
			return err
		}
		var ch map[string]any
		var err error
		if s.containerMode() {
			ch, err = s.containerChallenge(r, req.Action, req.Target)
		} else {
			ch, err = s.privd.Challenge(r.Context(), req.Action, req.Target, r.Header.Get("Origin"))
		}
		if err != nil {
			return err
		}
		httpx.OK(w, ch)
		return nil
	}))
	s.registerPrivd(mux)
}

// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// loginPathScript prints the login shell's PATH after a marker line, so
// noise printed by dotfiles can be skipped.
const loginPathScript = "printf '\\n__WARGO_PATH__=%s\\n' \"$PATH\""

const (
	pathMarker     = "__WARGO_PATH__="
	maxLoginPath   = 8 << 10
	loginPathLimit = 3 * time.Second
)

// parseLoginPath extracts the PATH from the output of loginPathScript. It
// uses the last marker line and accepts only absolute entries without
// control characters.
func parseLoginPath(out []byte) (string, bool) {
	var val string
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), pathMarker); ok {
			val, found = v, true
		}
	}
	if !found || val == "" || len(val) > maxLoginPath {
		return "", false
	}
	for _, r := range val {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	for _, dir := range strings.Split(val, ":") {
		if !filepath.IsAbs(dir) {
			return "", false
		}
	}
	return val, true
}

// resolveLoginPath asks the account's login shell for its PATH, once. It
// returns "" on any failure.
func resolveLoginPath(ctx context.Context, a account, run func(ctx context.Context, argv, env []string) ([]byte, error)) string {
	ctx, cancel := context.WithTimeout(ctx, loginPathLimit)
	defer cancel()
	out, err := run(ctx, []string{a.Shell, "-l", "-c", loginPathScript}, buildEnv(nil, a))
	if err != nil {
		return ""
	}
	p, _ := parseLoginPath(out)
	return p
}

// runLogin runs argv non-interactively: stdin is /dev/null, it gets its own
// session (no controlling terminal) and the whole group is killed on timeout.
func runLogin(ctx context.Context, argv, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 500 * time.Millisecond
	var buf bytes.Buffer
	cmd.Stdout = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// lookIn returns the first executable dir/name for dir in path.
func lookIn(path, name string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, name); isExecutable(p) {
			return p, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// withPath returns env with PATH set to path.
func withPath(env []string, path string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+path)
}

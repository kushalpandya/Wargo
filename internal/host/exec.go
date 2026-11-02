// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultPath is the PATH used for commands run on the host.
const DefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Nsenter is the nsenter binary inside the container image.
var Nsenter = "nsenter"

// nsFlags enter the host's mount, UTS, IPC, network and cgroup namespaces
// of PID 1. The PID namespace is already shared (pid: host, verified at
// startup) so nsenter execs directly instead of forking.
var nsFlags = []string{"-t", "1", "-m", "-u", "-i", "-n", "-C"}

const cdScript = `cd -- "$1" || exit 126; shift; exec "$@"`

// Opts configure a host command.
type Opts struct {
	// AsUser runs the command as the admin account (with its supplementary
	// groups) instead of root. Natively Wargo already is that user.
	AsUser bool
	// Dir is the working directory as a host path.
	Dir string
	// Env replaces the environment. Container mode defaults to a minimal
	// one (PATH, LANG, HOME, USER...).
	Env []string
}

// Command runs argv on the host as root (container mode) or as Wargo's own
// user (native, where callers elevate through sudo/privd instead).
func Command(ctx context.Context, argv ...string) *exec.Cmd {
	return CommandOpts(ctx, Opts{}, argv...)
}

// UserCommand runs argv on the host as the admin user.
func UserCommand(ctx context.Context, argv ...string) *exec.Cmd {
	return CommandOpts(ctx, Opts{AsUser: true}, argv...)
}

// CommandOpts builds a host command. ctx may be nil.
func CommandOpts(ctx context.Context, o Opts, argv ...string) *exec.Cmd {
	if !Container() {
		cmd := newCmd(ctx, argv[0], argv[1:]...)
		cmd.Dir = o.Dir
		if o.Env != nil {
			cmd.Env = o.Env
		}
		return cmd
	}
	var acct *Account
	if o.AsUser {
		acct = Admin()
	}
	full := NsenterArgv(o.Dir, acct, userSwitch(), argv)
	cmd := newCmd(ctx, full[0], full[1:]...)
	if o.Env != nil {
		cmd.Env = o.Env
	} else {
		cmd.Env = BaseEnv(acct)
	}
	return cmd
}

func newCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	if ctx == nil {
		return exec.Command(name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

// BaseEnv is the minimal environment for host commands; acct nil = root.
func BaseEnv(acct *Account) []string {
	home, user, shell := "/root", "root", "/bin/sh"
	if acct != nil {
		home, user = acct.Home, acct.Name
		if acct.Shell != "" {
			shell = acct.Shell
		}
	}
	return []string{"PATH=" + DefaultPath, "HOME=" + home, "USER=" + user, "LOGNAME=" + user, "SHELL=" + shell, "LANG=C.UTF-8"}
}

// Switch selects how a host command drops to the admin user.
type Switch int

const (
	// SwitchSetpriv uses util-linux setpriv(1) on the host:
	// exact uid/gid and initgroups(3), no PAM session.
	SwitchSetpriv Switch = iota
	// SwitchSu falls back to su(1) (root needs no password).
	SwitchSu
)

// NsenterArgv builds the full argv for running argv in the host
// namespaces. acct nil runs as root. dir is a host path ("" = /).
func NsenterArgv(dir string, acct *Account, sw Switch, argv []string) []string {
	out := []string{Nsenter}
	out = append(out, nsFlags...)
	out = append(out, "--")
	if dir != "" {
		// nsenter --wd opens the directory before entering the mount
		// namespace, i.e. in the container's view; change directory on the
		// host side instead. The script is constant; dir and argv are
		// positional parameters.
		argv = append([]string{"/bin/sh", "-c", cdScript, "wargo-cd", dir}, argv...)
	}
	if acct == nil {
		return append(out, argv...)
	}
	switch sw {
	case SwitchSu:
		// `exec "$0" "$@"` runs argv verbatim: nothing is interpolated into
		// the script, the arguments are passed as positional parameters.
		out = append(out, "su", "-s", "/bin/sh", acct.Name, "-c", `exec "$0" "$@"`)
		return append(out, argv...)
	default:
		out = append(out, "setpriv", "--reuid="+itoa(acct.UID), "--regid="+itoa(acct.GID), "--init-groups", "--")
		return append(out, argv...)
	}
}

// UserArgv returns the full container-mode argv running argv on the host
// as the admin account (for callers that need argv rather than *exec.Cmd,
// such as PTY sessions). Native: argv unchanged.
func UserArgv(argv ...string) []string {
	if !Container() {
		return argv
	}
	return NsenterArgv("", Admin(), userSwitch(), argv)
}

func userSwitch() Switch {
	// LookPath caches, so this is cheap after the first call.
	if _, err := LookPath("setpriv"); err == nil {
		return SwitchSetpriv
	}
	return SwitchSu
}

// LoginShellArgv returns argv for an interactive login shell of the admin
// on the host, optionally running cmd (a constant or safely quoted shell
// command) instead. Container mode only: `su -l` as root gives the user's
// real login environment, groups and home without a password.
func LoginShellArgv(acct *Account, cmd string) []string {
	out := []string{Nsenter}
	out = append(out, nsFlags...)
	out = append(out, "--", "su", "-l", acct.Name)
	if cmd != "" {
		out = append(out, "-c", cmd)
	}
	return out
}

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellJoin quotes and joins argv.
func ShellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = ShellQuote(a)
	}
	return strings.Join(q, " ")
}

var (
	lookMu    sync.Mutex
	lookCache = map[string]string{}
)

// LookPath finds an executable on the host and returns its host path.
// Native: exec.LookPath with Wargo's PATH. Container: the default system
// PATH below the host root (symlinks resolved inside the root).
func LookPath(name string) (string, error) {
	if !Container() {
		return exec.LookPath(name)
	}
	if strings.Contains(name, "/") {
		if executable(name) {
			return name, nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	lookMu.Lock()
	p, ok := lookCache[name]
	lookMu.Unlock()
	if ok {
		if p == "" {
			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return p, nil
	}
	for _, d := range filepath.SplitList(DefaultPath) {
		c := filepath.Join(d, name)
		if executable(c) {
			p = c
			break
		}
	}
	lookMu.Lock()
	lookCache[name] = p
	lookMu.Unlock()
	if p == "" {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	return p, nil
}

func executable(hostPath string) bool {
	rp, err := Resolve(hostPath)
	if err != nil {
		return false
	}
	st, err := os.Stat(rp)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// ErrNotContainer is returned by container-only helpers in native mode.
var ErrNotContainer = errors.New("not in container mode")

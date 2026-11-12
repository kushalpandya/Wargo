// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/kushalpandya/wargo/internal/host"
)

const (
	KindHost      = "host"
	KindRoot      = "root"
	KindContainer = "container"
)

var containerRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ValidContainer reports whether s is an acceptable container id/name.
func ValidContainer(s string) bool { return containerRe.MatchString(s) }

// account describes the user Wargo runs as.
type account struct {
	User, Home, Shell string
}

// currentAccount resolves user, home and login shell. The shell comes from
// $SHELL, then the login shell (passwd entry or Directory Services), then
// /bin/sh.
func currentAccount(getenv func(string) string) account {
	if host.Container() {
		return containerAccount()
	}
	a := account{User: getenv("USER"), Home: getenv("HOME")}
	if u, err := user.Current(); err == nil {
		a.User, a.Home = u.Username, u.HomeDir
		uid, _ := strconv.Atoi(u.Uid)
		a.Shell = host.LoginShell(u.Username, uid)
	}
	if s := getenv("SHELL"); isExecutable(s) {
		a.Shell = s
	}
	if !isExecutable(a.Shell) {
		a.Shell = "/bin/sh"
	}
	if a.Home == "" {
		a.Home = "/"
	}
	return a
}

// containerAccount describes the host admin account (container mode).
func containerAccount() account {
	a := account{Shell: "/bin/sh", Home: "/"}
	if acct := host.Admin(); acct != nil {
		a.User, a.Home = acct.Name, acct.Home
		if acct.Shell != "" {
			if rp, err := host.Resolve(acct.Shell); err == nil && isExecutable(rp) {
				a.Shell = acct.Shell
			}
		}
	}
	return a
}

func isExecutable(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// buildEnv constructs a minimal, clean environment for a terminal. Nothing
// from Wargo's own environment leaks except an allow-list of locale and
// session variables.
func buildEnv(environ []string, a account, extra ...string) []string {
	src := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			src[k] = v
		}
	}
	path := src["PATH"]
	if path == "" {
		path = defaultPath
	}
	env := []string{
		"HOME=" + a.Home,
		"USER=" + a.User,
		"LOGNAME=" + a.User,
		"SHELL=" + a.Shell,
		"PATH=" + path,
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	}
	lang := src["LANG"]
	if lang == "" {
		lang = defaultLang
	}
	env = append(env, "LANG="+lang)
	var lc []string
	for k, v := range src {
		if strings.HasPrefix(k, "LC_") && validEnvName(k) {
			lc = append(lc, k+"="+v)
		}
	}
	sort.Strings(lc)
	env = append(env, lc...)
	if v := src["XDG_RUNTIME_DIR"]; v != "" {
		env = append(env, "XDG_RUNTIME_DIR="+v)
	}
	return append(env, extra...)
}

func validEnvName(k string) bool {
	for _, c := range k {
		if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return k != ""
}

// tools are resolved executable paths ("" when missing).
type tools struct {
	Tmux, Sudo, Docker string
}

func lookTools(look func(string) (string, error)) tools {
	p := func(n string) string {
		s, err := look(n)
		if err != nil {
			return ""
		}
		return s
	}
	t := tools{Tmux: p("tmux"), Sudo: p("sudo"), Docker: p("docker")}
	if runtime.GOOS == "darwin" {
		// Never resolve sudo through the login PATH (user-writable dirs).
		t.Sudo = ""
		if _, err := os.Stat("/usr/bin/sudo"); err == nil {
			t.Sudo = "/usr/bin/sudo"
		}
	}
	return t
}

// spec is a fully resolved command to run in a PTY.
type spec struct {
	Argv  []string
	Dir   string
	Title string
}

var errBadKind = errors.New("unknown terminal kind")

// containerShell picks bash when present, else sh.
const containerShell = "command -v bash >/dev/null && exec bash || exec sh"

// buildSpec turns a request into argv. tmuxN is the index for the tmux
// session name (wargo-<n>).
func buildSpec(kind, target string, useTmux bool, tmuxN int, a account, t tools) (spec, error) {
	if host.Container() {
		return buildContainerSpec(kind, target, useTmux, tmuxN, a, t)
	}
	shellName := filepath.Base(a.Shell)
	switch kind {
	case KindHost:
		if useTmux {
			if t.Tmux == "" {
				return spec{}, errors.New("tmux is not installed")
			}
			name := "wargo-" + strconv.Itoa(tmuxN)
			return spec{Argv: []string{t.Tmux, "new-session", "-A", "-s", name}, Dir: a.Home, Title: "tmux " + name}, nil
		}
		return spec{Argv: []string{a.Shell, "-l"}, Dir: a.Home, Title: shellName}, nil
	case KindRoot:
		if t.Sudo == "" {
			return spec{}, errors.New("sudo is not installed")
		}
		return spec{Argv: []string{t.Sudo, "-i"}, Dir: a.Home, Title: "root"}, nil
	case KindContainer:
		if !ValidContainer(target) {
			return spec{}, fmt.Errorf("invalid container name %q", target)
		}
		if t.Docker == "" {
			return spec{}, errors.New("docker CLI is not installed")
		}
		return spec{
			Argv:  []string{t.Docker, "exec", "-it", "-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor", target, "sh", "-c", containerShell},
			Dir:   a.Home,
			Title: target,
		}, nil
	}
	return spec{}, errBadKind
}

// buildContainerSpec is buildSpec for container mode: every session runs
// in the host's namespaces via nsenter. Host and root shells use `su -l`
// (root needs no password and it yields the user's real login session); the
// root shell then runs the host's `sudo -i`, which asks for the password in
// the terminal exactly as natively. Container shells use the host's docker
// CLI as the admin user. Paths in t are host paths.
func buildContainerSpec(kind, target string, useTmux bool, tmuxN int, a account, t tools) (spec, error) {
	acct := host.Admin()
	if acct == nil {
		return spec{}, errors.New("admin account is not configured")
	}
	shellName := filepath.Base(a.Shell)
	switch kind {
	case KindHost:
		if useTmux {
			if t.Tmux == "" {
				return spec{}, errors.New("tmux is not installed on the host")
			}
			name := "wargo-" + strconv.Itoa(tmuxN)
			cmd := "exec " + host.ShellJoin([]string{t.Tmux, "new-session", "-A", "-s", name})
			return spec{Argv: host.LoginShellArgv(acct, cmd), Title: "tmux " + name}, nil
		}
		return spec{Argv: host.LoginShellArgv(acct, ""), Title: shellName}, nil
	case KindRoot:
		if t.Sudo == "" {
			return spec{}, errors.New("sudo is not installed on the host")
		}
		return spec{Argv: host.RootShellArgv(acct, t.Sudo), Title: "root"}, nil
	case KindContainer:
		if !ValidContainer(target) {
			return spec{}, fmt.Errorf("invalid container name %q", target)
		}
		if t.Docker == "" {
			return spec{}, errors.New("the docker CLI is not installed on the host")
		}
		return spec{
			Argv:  host.UserArgv(t.Docker, "exec", "-it", "-e", "TERM=xterm-256color", "-e", "COLORTERM=truecolor", target, "sh", "-c", containerShell),
			Title: target,
		}, nil
	}
	return spec{}, errBadKind
}

func (s spec) command(env []string) *exec.Cmd {
	cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
	cmd.Env = env
	cmd.Dir = s.Dir
	return cmd
}

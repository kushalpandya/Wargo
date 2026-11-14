// SPDX-License-Identifier: AGPL-3.0-only

package brew

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/host"
)

// fakeHost builds a host root with an admin account "me" (this process's
// uid) and a Linuxbrew install whose bin/brew is a relative symlink, as the
// installer makes it.
func fakeHost(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	w := func(p, body string, mode os.FileMode) {
		t.Helper()
		p = filepath.Join(r, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	uid := os.Getuid()
	w("etc/passwd", fmt.Sprintf("root:x:0:0::/root:/bin/sh\nme:x:%d:%d::/home/me:/bin/zsh\n", uid, uid), 0o644)
	w("etc/group", fmt.Sprintf("me:x:%d:\n", uid), 0o644)
	w("home/linuxbrew/.linuxbrew/Homebrew/bin/brew", "#!/bin/sh\n", 0o755)
	for _, d := range []string{"Cellar", "bin"} {
		os.MkdirAll(filepath.Join(r, "home/linuxbrew/.linuxbrew", d), 0o755)
	}
	if err := os.Symlink("../Homebrew/bin/brew", filepath.Join(r, "home/linuxbrew/.linuxbrew/bin/brew")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.SetRoot(r))
	if _, err := host.SetAdmin("me"); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestContainerDetectsHostHomebrew(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("container mode is Linux only")
	}
	fakeHost(t)
	s := &Service{supported: true, onHost: true}
	s.detect(candidates(host.Admin().Home))
	if s.bin != "/home/linuxbrew/.linuxbrew/bin/brew" || s.prefix != "/home/linuxbrew/.linuxbrew" {
		t.Fatalf("want host paths, got bin %q prefix %q", s.bin, s.prefix)
	}
	if s.problem != "" {
		t.Errorf("admin owns Homebrew, got problem %q", s.problem)
	}
}

func TestContainerHomebrewOwnedByAnother(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getuid() == 0 {
		t.Skip("needs Linux and a non-root test user")
	}
	r := fakeHost(t)
	other := os.Getuid() + 1
	os.WriteFile(filepath.Join(r, "etc/passwd"),
		fmt.Appendf(nil, "me:x:%d:%d::/home/me:/bin/sh\n", other, other), 0o644)
	host.SetRoot(r) // drop the cached user database and admin
	host.SetAdmin("me")
	s := &Service{supported: true, onHost: true}
	s.detect(candidates(host.Admin().Home))
	if !strings.Contains(s.problem, "belongs to") || !strings.Contains(s.problem, "runs as me") {
		t.Errorf("problem %q", s.problem)
	}
}

func TestContainerRunsBrewOnHostAsAdmin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("container mode is Linux only")
	}
	fakeHost(t)
	s := &Service{supported: true, onHost: true, bin: "/home/linuxbrew/.linuxbrew/bin/brew", prefix: "/home/linuxbrew/.linuxbrew"}
	cmd := s.command(nil, s.env(), s.bin, "upgrade")
	if cmd.Args[0] != host.Nsenter {
		t.Fatalf("brew must run through nsenter: %q", cmd.Args)
	}
	n := len(cmd.Args)
	if !slices.Equal(cmd.Args[n-2:], []string{s.bin, "upgrade"}) {
		t.Errorf("command tail %q", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "me") && !slices.Contains(cmd.Args, fmt.Sprintf("--reuid=%d", os.Getuid())) {
		t.Errorf("brew must run as the admin: %q", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "/tmp") {
		t.Errorf("working directory must be the host's /tmp: %q", cmd.Args)
	}
	for _, want := range []string{"HOME=/home/me", "USER=me", "PATH=/home/linuxbrew/.linuxbrew/bin:", "HOMEBREW_NO_AUTO_UPDATE=1"} {
		if !slices.ContainsFunc(cmd.Env, func(e string) bool { return strings.HasPrefix(e, want) }) {
			t.Errorf("env lacks %q: %q", want, cmd.Env)
		}
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "WARGO_") || strings.HasPrefix(e, "SUDO_ASKPASS=") {
			t.Errorf("container environment leaked: %q", e)
		}
	}
}

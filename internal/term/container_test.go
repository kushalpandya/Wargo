// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/host"
)

func fakeHostRoot(t *testing.T) {
	t.Helper()
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "etc"), 0o755)
	os.MkdirAll(filepath.Join(r, "usr/bin"), 0o755)
	os.WriteFile(filepath.Join(r, "etc/passwd"), []byte("kushal:x:1000:1000::/home/kushal:/usr/bin/zsh\n"), 0o644)
	os.WriteFile(filepath.Join(r, "etc/group"), []byte("wheel:x:998:kushal\nkushal:x:1000:\n"), 0o644)
	for _, b := range []string{"zsh", "sudo", "docker", "tmux", "setpriv"} {
		os.WriteFile(filepath.Join(r, "usr/bin", b), []byte("#!/bin/sh\n"), 0o755)
	}
	t.Cleanup(host.SetRoot(r))
	if _, err := host.SetAdmin("kushal"); err != nil {
		t.Fatal(err)
	}
}

func TestContainerSpecs(t *testing.T) {
	fakeHostRoot(t)
	a := currentAccount(os.Getenv)
	if a.User != "kushal" || a.Home != "/home/kushal" || a.Shell != "/usr/bin/zsh" {
		t.Fatalf("account %+v", a)
	}
	tl := lookTools(host.LookPath)
	if tl.Sudo != "/usr/bin/sudo" || tl.Docker != "/usr/bin/docker" || tl.Tmux != "/usr/bin/tmux" {
		t.Fatalf("tools %+v", tl)
	}
	ns := "nsenter -t 1 -m -u -i -n -C -- "

	sp, err := buildSpec(KindHost, "", false, 0, a, tl)
	if err != nil || strings.Join(sp.Argv, " ") != ns+"su -l kushal" || sp.Dir != "" {
		t.Fatalf("host: %q %q %v", sp.Argv, sp.Dir, err)
	}
	sp, _ = buildSpec(KindHost, "", true, 3, a, tl)
	if !slices.Equal(sp.Argv[len(sp.Argv)-2:], []string{"-c", "exec /usr/bin/tmux new-session -A -s wargo-3"}) {
		t.Fatalf("tmux: %q", sp.Argv)
	}
	sp, _ = buildSpec(KindRoot, "", false, 0, a, tl)
	if strings.Join(sp.Argv, " ") != ns+"su -l kushal -c exec /usr/bin/sudo -i" {
		t.Fatalf("root: %q", sp.Argv)
	}
	sp, err = buildSpec(KindContainer, "prowlarr", false, 0, a, tl)
	if err != nil {
		t.Fatal(err)
	}
	want := ns + "setpriv --reuid=1000 --regid=1000 --init-groups -- /usr/bin/docker exec -it"
	if !strings.HasPrefix(strings.Join(sp.Argv, " "), want) || !slices.Contains(sp.Argv, "prowlarr") {
		t.Fatalf("container: %q", sp.Argv)
	}
	if _, err := buildSpec(KindContainer, "bad;name", false, 0, a, tl); err == nil {
		t.Fatal("invalid container name accepted")
	}
	if termEnviron() != nil {
		t.Fatal("container env must not leak into terminals")
	}
}

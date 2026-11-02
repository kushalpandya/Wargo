// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestWrapperScriptReportsExit(t *testing.T) {
	out, err := exec.Command("/bin/sh", "-c", wrapperScript, "wargo-upgrade", "/bin/sh", "-c", `echo "hello; $(id)"; exit 3`).CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	s := string(out)
	if !strings.Contains(s, "hello; $(id)") && !strings.Contains(s, "hello; uid=") {
		t.Errorf("output %q", s)
	}
	if !strings.HasSuffix(s, "\n"+exitMarker+"3\n") {
		t.Errorf("missing exit marker: %q", s)
	}
}

func TestSystemdRunArgv(t *testing.T) {
	s := &Service{be: &aptBackend{}}
	rec := &RunRecord{ID: "x", Unit: "wargo-upgrade-x", Log: "/var/lib/wargo/pkg/x.log", Backend: "apt"}
	argv := s.systemdRunArgv(rec)
	for _, want := range []string{
		"systemd-run", "--unit=wargo-upgrade-x", "--collect", "--property=Type=exec",
		"--property=StandardOutput=append:/var/lib/wargo/pkg/x.log",
		"--property=StandardError=append:/var/lib/wargo/pkg/x.log",
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=LC_ALL=C",
		"-o", "APT::Status-Fd=1", "Dpkg::Options::=--force-confdef", "Dpkg::Options::=--force-confold", "full-upgrade",
	} {
		if !slices.Contains(argv, want) {
			t.Errorf("missing %q in %v", want, argv)
		}
	}
	sep := slices.Index(argv, "--")
	if sep < 0 || argv[sep+1] != "wargo" || argv[sep+2] != "helper" || argv[sep+3] != "pkg-run" || argv[sep+4] != "--" || argv[sep+5] != "apt-get" {
		t.Errorf("command part: %v", argv[sep:])
	}
	s.be = &pamacBackend{}
	argv = s.systemdRunArgv(rec)
	if !slices.Equal(argv[len(argv)-6:], []string{"pacman", "-Syu", "--noconfirm", "--noprogressbar", "--color", "never"}) {
		t.Errorf("pamac upgrades with pacman: %v", argv)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
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
	rec := &RunRecord{ID: "x", Unit: "wargo-upgrade-x", Log: "/var/lib/wargo/pkg-x.log", Backend: "apt"}
	argv, err := s.systemdRunArgv(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"systemd-run", "--unit=wargo-upgrade-x", "--collect", "--property=Type=exec",
		"--property=StandardOutput=append:/var/lib/wargo/pkg-x.log",
		"--property=StandardError=append:/var/lib/wargo/pkg-x.log",
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
	argv, _ = s.systemdRunArgv(rec)
	if !slices.Equal(argv[len(argv)-6:], []string{"pacman", "-Syu", "--noconfirm", "--noprogressbar", "--color", "never"}) {
		t.Errorf("pamac upgrades with pacman: %v", argv)
	}
}

func TestUpgradeLogAcceptedByPrivd(t *testing.T) {
	// wargo-privd only accepts logs directly inside /var/lib/wargo/,
	// /var/log/wargo/ or /run/wargo/ (see privd.TestUpgradeLogAcceptedByPrivd).
	for _, dir := range []string{"/var/lib/wargo", "/var/log/wargo"} {
		if got := filepath.Dir(upgradeLogPath(dir, "20260101-120000")); got != dir {
			t.Errorf("log dir %q, want %q", got, dir)
		}
	}
}

func TestPruneLogsBothLocations(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"pkg-20260101-000001.log", "pkg-20260101-000002.log", "pkg-20260101-000003.log", "20250101-000001.log", "other.log", "state.json"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	pruneLogs(dir, 2)
	var left []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	want := []string{"other.log", "pkg-20260101-000002.log", "pkg-20260101-000003.log", "state.json"}
	if !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

func TestContainerKeepsBareLogName(t *testing.T) {
	s := &Service{dataDir: containerLogDir, bareLogName: true}
	if got := s.logPath("20260101-120000"); got != containerLogDir+"/20260101-120000.log" {
		t.Errorf("container log %q", got)
	}
	s = &Service{dataDir: "/var/lib/wargo"}
	if got := s.logPath("20260101-120000"); got != "/var/lib/wargo/pkg-20260101-120000.log" {
		t.Errorf("native log %q", got)
	}
}

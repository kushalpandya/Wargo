// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

func TestContainerSystemdRunArgv(t *testing.T) {
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "usr/bin"), 0o755)
	os.WriteFile(filepath.Join(r, "usr/bin/pacman"), []byte("#!/bin/sh\n"), 0o755)
	defer host.SetRoot(r)()
	s := &Service{be: &pacmanBackend{}}
	rec := &RunRecord{ID: "x", Unit: "wargo-upgrade-x", Log: containerLogDir + "/x.log", Backend: "pacman"}
	argv := s.systemdRunArgv(rec)
	if !slices.Contains(argv, "--property=StandardOutput=append:/var/log/wargo/x.log") {
		t.Errorf("log must be a host path: %v", argv)
	}
	sep := slices.Index(argv, "--")
	want := []string{"--", "/bin/sh", "-c", wrapperScript, "wargo-upgrade", "/usr/bin/pacman", "-Syu"}
	if sep < 0 || !slices.Equal(argv[sep:sep+len(want)], want) {
		t.Errorf("command part: %q", argv[sep:])
	}
	if slices.Contains(argv, "helper") {
		t.Error("container mode must not reference wargo helper on the host")
	}
}

func TestContainerHostPaths(t *testing.T) {
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "etc/pacman.d"), 0o755)
	os.WriteFile(filepath.Join(r, "etc/pacman.d/mirrorlist.pacnew"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(r, "var/lib/pacman"), 0o755)
	os.WriteFile(filepath.Join(r, "var/lib/pacman/db.lck"), nil, 0o644)
	os.WriteFile(filepath.Join(r, "etc/os-release"), []byte("ID=manjaro\nPRETTY_NAME=\"Manjaro Linux\"\n"), 0o644)
	defer host.SetRoot(r)()
	cf := findConfigFiles("/etc", []string{".pacnew"}, time.Time{})
	if len(cf) != 1 || cf[0].Path != "/etc/pacman.d/mirrorlist.pacnew" {
		t.Fatalf("config files %+v", cf)
	}
	if l := checkLocks([]string{"/var/lib/pacman/db.lck"}); !l.Locked || l.Path != "/var/lib/pacman/db.lck" {
		t.Fatalf("lock %+v", l)
	}
	if o := readOSRelease(); o.ID != "manjaro" {
		t.Fatalf("os-release %+v", o)
	}
}

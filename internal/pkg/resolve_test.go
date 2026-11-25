// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"errors"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Recorded from pacman 7.1 with --noconfirm --ask=4, in a container with
// a local repository where workspace 2-1 conflicts with oldsession.
func TestRemovedPackagesPacman(t *testing.T) {
	b, err := os.ReadFile("testdata/pacman-resolve-conflict.txt")
	if err != nil {
		t.Fatal(err)
	}
	var st ProgressState
	for _, l := range split(string(b)) {
		parsePacman(l, &st)
		if name := removedName("pamac", l); name != "" {
			st.noteRemoved(name)
		}
	}
	if got := st.Removed(); !slices.Equal(got, []string{"oldsession"}) {
		t.Fatalf("removed: %v", got)
	}
	if st.Upgraded != 1 {
		t.Fatalf("upgraded %d, want 1 (the removal doesn't count)", st.Upgraded)
	}
	// The question is still in the log, though answered: it reads as the
	// conflict that was resolved.
	if d := diagnosePacman(split(string(b))); d == nil || d.Code != CodeConflict {
		t.Fatalf("diagnosis: %+v", d)
	}
}

func TestRemovedName(t *testing.T) {
	for _, c := range []struct{ backend, line, want string }{
		{"pacman", "(1/2) removing plasma-x11-session                [######] 100%", "plasma-x11-session"},
		{"pacman", "removing oldsession...", "oldsession"},
		{"apt", "Removing libfoo1:amd64 (1.0-1) ...", "libfoo1"},
		{"dnf", "  Erasing          : bar-1.0-1.fc41.x86_64                1/2", "bar-1.0-1.fc41.x86_64"},
		{"dnf", "[2/3] Removing bar-0:1.0-1.fc41.x86_64       100% |  12.0 KiB/s", "bar-0:1.0-1.fc41.x86_64"},
		{"zypper", `<progress id="remove-resolvable" name="(1/2) Removing: bar-1.0-1.1.x86_64" value="0"/>`, "bar-1.0-1.1.x86_64"},
		{"apk", "(1/2) Purging bar (1.0-r0)", "bar"},
		// Not removals.
		{"pacman", "(2/2) Remove Firefox Distribution Settings", ""},
		{"pacman", "removing old kernels...", ""},
		{"apt", "Setting up libfoo2:amd64 (2.0-1) ...", ""},
	} {
		if got := removedName(c.backend, c.line); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.backend, c.line, got, c.want)
		}
	}
}

func TestUnexpectedRemovals(t *testing.T) {
	// Approving python doesn't cover python-requests.
	if got := unexpectedRemovals("pacman", []string{"python", "python-requests"}, []string{"python"}); !slices.Equal(got, []string{"python-requests"}) {
		t.Fatalf("pacman: %v", got)
	}
	// dnf names are compared without version, epoch and architecture.
	if got := unexpectedRemovals("dnf", []string{"bar-0:1.0-1.fc41.x86_64", "baz-2-1.fc41.noarch"}, []string{"bar-1.0-1.fc41.x86_64"}); !slices.Equal(got, []string{"baz-2-1.fc41.noarch"}) {
		t.Fatalf("dnf: %v", got)
	}
	// Nothing was named: the owner approved whatever conflicts.
	if got := unexpectedRemovals("zypper", []string{"bar-1.0-1.1.x86_64"}, nil); got != nil {
		t.Fatalf("zypper: %v", got)
	}
}

func TestResolveConflictsArgv(t *testing.T) {
	for _, c := range []struct {
		be   Backend
		flag string
	}{{&pamacBackend{}, "--ask=4"}, {&pacmanBackend{}, "--ask=4"}, {&dnfBackend{}, "--allowerasing"}, {&zypperBackend{}, "--force-resolution"}} {
		s := &Service{be: c.be}
		rec := &RunRecord{ID: "x", Unit: "wargo-upgrade-x", Log: "/var/lib/wargo/pkg-x.log", Backend: c.be.Name(), ResolveConflicts: true}
		argv, err := s.systemdRunArgv(rec)
		if err != nil {
			t.Fatal(err)
		}
		if argv[len(argv)-1] != c.flag {
			t.Errorf("%s: %v", c.be.Name(), argv)
		}
		rec.ResolveConflicts = false
		if argv, _ := s.systemdRunArgv(rec); slices.Contains(argv, c.flag) {
			t.Errorf("%s: flag without permission: %v", c.be.Name(), argv)
		}
	}
	for _, be := range []Backend{&aptBackend{}, &apkBackend{}} {
		if be.ResolveConflictsArgs() != nil {
			t.Errorf("%s should resolve conflicts on its own or not at all", be.Name())
		}
	}
}

func TestApprovableRemovals(t *testing.T) {
	s := &Service{be: &pamacBackend{}}
	failed := &RunRecord{Backend: "pamac", Diagnosis: &Diagnosis{Code: CodeConflict, Conflicts: []Conflict{
		{Package: "plasma-workspace", Remove: "plasma-x11-session"}, {Package: "other", Remove: "plasma-x11-session"}}}}
	got, err := s.approvableRemovals(failed)
	if err != nil || !slices.Equal(got, []string{"plasma-x11-session"}) {
		t.Fatalf("got %v %v", got, err)
	}
	for name, last := range map[string]*RunRecord{
		"no run":       nil,
		"succeeded":    {Backend: "pamac", OK: true},
		"other reason": {Backend: "pamac", Diagnosis: &Diagnosis{Code: CodeFileConflict}},
		"other tool":   {Backend: "dnf", Diagnosis: &Diagnosis{Code: CodeConflict}},
	} {
		if _, err := s.approvableRemovals(last); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
	if _, err := (&Service{be: &aptBackend{}}).approvableRemovals(failed); err == nil {
		t.Error("apt: allowed")
	}
}

// shPamac is pamac with a binary that exists everywhere, so handlers get
// past the "not installed" check in tests.
type shPamac struct{ pamacBackend }

func (*shPamac) Binary() string { return "sh" }

func TestUpgradeResolveConflicts(t *testing.T) {
	s, _, al := auditEnv(t)
	s.be = &shPamac{}
	s.d.Elevate = elevate.New(al, httpx.NewProxyTrust(nil), auth.NewThrottle(), "")
	post := func() error {
		r := httptest.NewRequest("POST", "/api/pkg/upgrade", strings.NewReader(`{"resolveConflicts":true}`))
		return s.handleUpgrade(httptest.NewRecorder(), r)
	}
	s.lastRun = &RunRecord{Backend: "pamac", OK: true}
	var he *httpx.Error
	if err := post(); !errors.As(err, &he) || he.Code != "no_conflict" {
		t.Fatalf("after a good run: %v", err)
	}
	s.lastRun = &RunRecord{Backend: "pamac", Diagnosis: &Diagnosis{Code: CodeConflict,
		Conflicts: []Conflict{{Package: "plasma-workspace", Remove: "plasma-x11-session"}}}}
	err := post()
	if !errors.As(err, &he) || he.Code != "elevation_required" {
		t.Fatalf("after a conflict: %v", err)
	}
	target, _ := he.Data.(map[string]any)["target"].(string)
	if !strings.Contains(target, "removing plasma-x11-session and anything else that conflicts") {
		t.Fatalf("approval target: %q", target)
	}
}

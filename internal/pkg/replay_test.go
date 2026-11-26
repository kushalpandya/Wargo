// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kushalpandya/wargo/internal/auth"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

// replayBackend is shPamac without lock files to look at (the host's
// pacman lock must not decide the test), whose check needs root when
// needsRoot.
type replayBackend struct {
	shPamac
	needsRoot bool
}

func (*replayBackend) LockFiles() []string    { return nil }
func (b *replayBackend) CheckNeedsRoot() bool { return b.needsRoot }

// replayEnv is a Service on replayBackend with plenty of free space (not
// the host's disk), elevation and no event hub: anything published would
// panic.
func replayEnv(t *testing.T, needsRoot bool) (*Service, *store.Store) {
	t.Helper()
	s, st, al := auditEnv(t)
	s.be = &replayBackend{needsRoot: needsRoot}
	s.d.Elevate = elevate.New(al, httpx.NewProxyTrust(nil), auth.NewThrottle(), "")
	old := diskSpaceFn
	diskSpaceFn = func() []DiskInfo { return []DiskInfo{{Path: "/", Free: 1 << 40}} }
	t.Cleanup(func() { diskSpaceFn = old })
	return s, st
}

func auditCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// check_updates on a host whose check needs root (apt, zypper, apk, or
// pacman without checkupdates): the approved POST /api/pkg/check is first
// replayed without a credential. It must answer elevation_required with
// nothing audited, claimed or published.
func TestCheckReplayWithoutCredentialHasNoSideEffects(t *testing.T) {
	s, st := replayEnv(t, true)
	err := s.handleCheck(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/pkg/check", nil))
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != "elevation_required" {
		t.Fatalf("replay without a credential: %v", err)
	}
	if n := auditCount(t, st); n != 0 {
		t.Fatalf("%d audit entries before the credential", n)
	}
	if s.checking {
		t.Fatal("check left claimed")
	}
}

// An agent's approved apply_os_updates request (POST /api/pkg/upgrade,
// no body) is first replayed without a credential: the approval page
// learns from the answer that it needs one. The handler must answer
// elevation_required before it audits, persists, prunes or creates anything.
func TestAdminReplayWithoutCredentialHasNoSideEffects(t *testing.T) {
	s, st := replayEnv(t, false)
	s.last = &CheckResult{Updates: []Update{{Name: "linux"}}}
	// Eleven earlier logs: starting an upgrade prunes them to ten.
	var logs []string
	for i := range 11 {
		p := upgradeLogPath(s.dataDir, fmt.Sprintf("20260101-0000%02d", i))
		if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		logs = append(logs, filepath.Base(p))
	}

	err := s.handleUpgrade(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/pkg/upgrade", nil))
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != "elevation_required" {
		t.Fatalf("replay without a credential: %v", err)
	}
	if n := auditCount(t, st); n != 0 {
		t.Fatalf("%d audit entries before the credential", n)
	}
	var rec RunRecord
	if ok, _ := st.Get(context.Background(), keyRunning, &rec); ok {
		t.Fatalf("running upgrade recorded before the credential: %+v", rec)
	}
	if s.job != nil {
		t.Fatal("job attached before the credential")
	}
	var left []string
	entries, _ := os.ReadDir(s.dataDir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if !slices.Equal(left, logs) {
		t.Fatalf("files changed before the credential: %v", left)
	}
}

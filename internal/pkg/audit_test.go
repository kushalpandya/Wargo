// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/internal/store"
)

func auditEnv(t *testing.T) (*Service, *store.Store, *audit.Log) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	al := audit.New(st, httpx.NewProxyTrust(nil))
	dir := t.TempDir()
	return &Service{d: &server.Deps{Store: st, Audit: al}, dataDir: dir, legacyLogDir: filepath.Join(dir, "pkg")}, st, al
}

func TestOpenAuditLogOnlyServesUpgradeLogs(t *testing.T) {
	s, _, _ := auditEnv(t)
	good := upgradeLogPath(s.dataDir, "20261124-183339")
	os.WriteFile(good, []byte("x\n"), 0o600)
	os.WriteFile(filepath.Join(s.dataDir, "wargo.db.key"), []byte("k"), 0o600)
	link := upgradeLogPath(s.dataDir, "20261124-000000")
	os.Symlink(filepath.Join(s.dataDir, "wargo.db.key"), link)

	if f, err := s.openAuditLog(good); err != nil {
		t.Fatalf("upgrade log: %v", err)
	} else {
		f.Close()
	}
	for _, p := range []string{
		filepath.Join(s.dataDir, "wargo.db.key"),              // not a log name
		filepath.Join(t.TempDir(), "pkg-20261124-183339.log"), // another directory
		s.dataDir + "/sub/../pkg-20261124-183339.log",         // not clean
		"pkg-20261124-183339.log",                             // relative
	} {
		if _, err := s.openAuditLog(p); !errors.Is(err, audit.ErrNotMine) {
			t.Errorf("%s: %v", p, err)
		}
	}
	// A symlink in place of a log is refused when opening.
	if f, err := s.openAuditLog(link); err == nil {
		f.Close()
		t.Error("symlinked log was opened")
	}
}

func TestFinishedUpgradeKeepsTheLogTail(t *testing.T) {
	s, st, al := auditEnv(t)
	r := httptest.NewRequest("POST", "/api/pkg/upgrade", nil)
	r = r.WithContext(httpx.WithRequestID(r.Context(), "req1"))
	span := al.Begin(r, "pkg.upgrade", "Install 3 updates with pacman")
	al.Write(r, audit.Event{Action: "pkg.upgrade", Target: "Install 3 updates with pacman"})

	id := "20261124-183339"
	rec := &RunRecord{ID: id, Unit: "wargo-upgrade-" + id, Log: upgradeLogPath(s.dataDir, id), Backend: "pacman",
		Packages: 3, StartedAt: time.Now().Add(-time.Minute), Audit: &span}
	os.WriteFile(rec.Log, []byte(":: Starting full system upgrade...\n"+
		":: plasma-workspace-6.8.2-1 and plasma-x11-session-6.7.5-1 are in conflict. Remove plasma-x11-session? [y/N] error: unresolvable package conflicts detected\n"+
		"error: failed to prepare transaction (conflicting dependencies)\n\n"+exitMarker+"1\n"), 0o600)
	now := time.Now()
	one := 1
	rec.FinishedAt, rec.ExitStatus, rec.Stage = &now, &one, StagePreparing
	s.auditFinished(rec, errors.New("the package manager exited with status 1"))

	var ref, dur int64
	var source, details, output string
	err := st.DB.QueryRow(`SELECT ref, source, duration_ms, details, output FROM audit_log WHERE action = 'pkg.upgrade.finished'`).
		Scan(&ref, &source, &dur, &details, &output)
	if err != nil {
		t.Fatal(err)
	}
	if ref != 1 || source != audit.SourceJob || dur < 59_000 {
		t.Fatalf("ref %d source %s duration %d", ref, source, dur)
	}
	for _, want := range []string{`"exitStatus":1`, `"stage":"Preparing"`, `"unit":"wargo-upgrade-` + id} {
		if !strings.Contains(details, want) {
			t.Errorf("details lack %s: %s", want, details)
		}
	}
	if !strings.Contains(output, "are in conflict") || strings.Contains(output, exitMarker) || !strings.Contains(output, rec.Log) {
		t.Errorf("output: %s", output)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package brew

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/elevate"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/pkg"
	"github.com/kushalpandya/wargo/internal/server"
	"github.com/kushalpandya/wargo/internal/store"
)

// The test binary doubles as `wargo brew-askpass` for the askpass script.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "brew-askpass" {
		os.Exit(AskpassMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "fake", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseOutdated(t *testing.T) {
	ups, pinned, err := parseOutdated(read(t, "outdated.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 6 || len(pinned) != 1 || pinned[0].Name != "python@3.13" || pinned[0].Source != "pinned" {
		t.Fatalf("updates %+v pinned %+v", ups, pinned)
	}
	applyInfo(ups, read(t, "info.json"))
	by := map[string]pkg.Update{}
	for _, u := range ups {
		by[u.Name] = u
	}
	if g := by["git"]; g.Current != "2.51.0" || g.New != "2.52.1" || g.Repo != "homebrew/core" || g.Source != "formula" {
		t.Errorf("git = %+v", g)
	}
	if c := by["nordvpn"]; c.Source != "cask" || !c.NeedsPassword || c.Repo != "homebrew/cask" {
		t.Errorf("nordvpn = %+v", c)
	}
	if c := by["iterm2"]; c.NeedsPassword {
		t.Errorf("iterm2 should not need a password: %+v", c)
	}
}

func TestOlderCaskVersionShape(t *testing.T) {
	ups, _, err := parseOutdated([]byte(`{"formulae":[],"casks":[{"name":"x","installed_versions":"1.0","current_version":"2.0"}]}`))
	if err != nil || len(ups) != 1 || ups[0].Current != "1.0" {
		t.Fatalf("%+v %v", ups, err)
	}
}

func TestNeedsPassword(t *testing.T) {
	cases := map[string]bool{
		`[{"app":["A.app"]}]`: false,
		`[{"pkg":["A.pkg"]}]`: true,
		`[{"uninstall":[{"quit":"x","delete":"~/Library/x"}]}]`:       false,
		`[{"uninstall":[{"delete":["/Library/LaunchDaemons/x"]}]}]`:   true,
		`[{"uninstall":[{"delete":"/opt/homebrew/share/x"}]}]`:        false,
		`[{"uninstall":[{"launchctl":"com.x"}]}]`:                     true,
		`[{"installer":[{"script":{"executable":"i","sudo":true}}]}]`: true,
		`[{"installer":[{"script":{"executable":"i"}}]}]`:             false,
	}
	for in, want := range cases {
		var a []map[string]json.RawMessage
		if err := json.Unmarshal([]byte(in), &a); err != nil {
			t.Fatal(err)
		}
		if got := needsPassword(a); got != want {
			t.Errorf("%s: got %v", in, got)
		}
	}
}

func TestProgressFromSimulatedRun(t *testing.T) {
	cmd := exec.Command("/bin/sh", filepath.Join("testdata", "fake", "upgrade.sh"))
	cmd.Env = append(os.Environ(), "WARGO_BREW_FAKE_DELAY=0", "SUDO_ASKPASS=/bin/echo")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	p := progress{planned: 6}
	stages := map[string]bool{}
	prev := 0.0
	for _, l := range strings.Split(string(out), "\n") {
		parseLine(l, &p)
		stages[p.Stage] = true
		if p.Upgraded < p.total && p.Frac >= 0.9 {
			t.Fatalf("%.2f after %d of %d packages at %q", p.Frac, p.Upgraded, p.total, l)
		}
		if p.Frac < prev {
			t.Fatalf("progress went back at %q", l)
		}
		prev = p.Frac
	}
	if p.total != 6 || p.Upgraded != 6 {
		t.Errorf("total %d upgraded %d", p.total, p.Upgraded)
	}
	for _, s := range []string{StagePreparing, StageDownloading, StageInstalling, StageCleanup} {
		if !stages[s] {
			t.Errorf("stage %q never reached", s)
		}
	}
	if p.sudoFail {
		t.Error("sudo failure reported for a good run")
	}
	var q progress
	parseLine("sudo: a terminal is required to read the password; either use the -S option", &q)
	if !q.sudoFail {
		t.Error("sudo failure not detected")
	}
}

func TestAskpassNeedsBinary(t *testing.T) {
	if _, err := startAskpass("", "pw"); err == nil {
		t.Fatal("started without a binary path")
	}
}

func TestShellQuote(t *testing.T) {
	out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(`it's a "path" $HOME`)).Output()
	if err != nil || string(out) != `it's a "path" $HOME` {
		t.Fatalf("%q %v", out, err)
	}
}

func TestAskpassServesPassword(t *testing.T) {
	exe, _ := os.Executable()
	a, err := startAskpass(exe, "s3cret pw")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(a.dir); st.Mode().Perm() != 0o700 {
		t.Errorf("askpass dir mode %v", st.Mode().Perm())
	}
	// Nothing is served before the update's process is known.
	if out, err := exec.Command(a.script, "Password:").Output(); err == nil || len(out) != 0 {
		t.Fatalf("served before setRoot: %q %v", out, err)
	}
	// Nor to a process outside the update.
	other := exec.Command("/bin/sleep", "5")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	a.setRoot(other.Process.Pid)
	if out, err := exec.Command(a.script, "Password:").Output(); err == nil || len(out) != 0 {
		t.Fatalf("served to an unrelated process: %q %v", out, err)
	}
	// The test process stands in for brew; the script runs as its child,
	// as sudo's askpass does under brew. sudo passes its prompt as argv[1].
	a.setRoot(os.Getpid())
	out, err := exec.Command(a.script, "Password:").Output()
	if err != nil || string(out) != "s3cret pw\n" {
		t.Fatalf("askpass = %q, %v", out, err)
	}
	a.Close()
	if _, err := os.Stat(a.dir); !os.IsNotExist(err) {
		t.Error("askpass dir left behind")
	}

	empty, err := startAskpass(exe, "")
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	empty.setRoot(os.Getpid())
	if out, err := exec.Command(empty.script, "Password:").Output(); err == nil || len(out) != 0 {
		t.Fatalf("askpass without a password should fail: %q %v", out, err)
	}
	c, err := net.Dial("unix", filepath.Join(empty.dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(c); len(b) != 0 {
		t.Errorf("served %q", b)
	}
	c.Close()
}

func testService(t *testing.T) (*Service, *events.Hub) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	t.Setenv("WARGO_BREW_FAKE", filepath.Join(must(os.Getwd()), "testdata", "fake"))
	t.Setenv("WARGO_BREW_FAKE_DELAY", "0")
	hub := events.NewHub()
	al := audit.New(st, httpx.NewProxyTrust(nil))
	d := &server.Deps{Cfg: config.Config{DataDir: t.TempDir()}, Store: st, Hub: hub, Jobs: jobs.NewManager(hub), Audit: al,
		Elevate: elevate.New(al, httpx.NewProxyTrust(nil), nil, "")}
	return New(d), hub
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func call(s *Service, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	s.Register(mux)
	r := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestSimulatedCheckAndUpgrade(t *testing.T) {
	s, _ := testService(t)
	if w := call(s, "POST", "/api/brew/upgrade", nil); w.Code != http.StatusConflict {
		t.Fatalf("upgrade before a check: %d %s", w.Code, w.Body)
	}
	if w := call(s, "POST", "/api/brew/check", nil); w.Code != 200 {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	if n := len(s.status().LastCheck.Updates); n != 6 {
		t.Fatalf("pending %d", n)
	}
	// nordvpn installs a .pkg: approval with a password comes first.
	w := call(s, "POST", "/api/brew/upgrade", nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "elevation_required") || !strings.Contains(w.Body.String(), `"passkey":false`) {
		t.Fatalf("expected a password prompt: %d %s", w.Code, w.Body)
	}
	cred := base64.StdEncoding.EncodeToString([]byte(`{"method":"sudo","password":"x"}`))
	if w := call(s, "POST", "/api/brew/upgrade", map[string]string{elevate.Header: cred}); w.Code != 200 {
		t.Fatalf("upgrade: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := s.status()
		if st.Running == nil && st.LastRun != nil {
			if !st.LastRun.OK || st.LastRun.Upgraded != 6 || st.LastRun.Stage != StageCleanup {
				t.Fatalf("run %+v", st.LastRun)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("update did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The list is refreshed after the update (simulated: nothing pending,
	// the pinned formula still listed).
	for {
		c := s.status().LastCheck
		if !c.Stale && len(c.Updates) == 0 && len(c.Extras) == 1 && c.AfterRun == s.status().LastRun.ID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("list not refreshed after the update: %+v", c)
		}
		time.Sleep(50 * time.Millisecond)
	}
	w = call(s, "GET", "/api/brew/log", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "nordvpn was successfully upgraded") {
		t.Fatalf("log: %d %s", w.Code, w.Body)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	s.d.Store.Set(ctx, keyRunning, &pkg.RunRecord{ID: "20260101-000000", Backend: "brew"})
	s2 := New(s.d)
	st := s2.status()
	if st.LastRun == nil || st.LastRun.OK || st.LastRun.FinishedAt == nil || st.Running != nil {
		t.Fatalf("%+v", st.LastRun)
	}
	if ok, _ := s.d.Store.Get(ctx, keyRunning, &pkg.RunRecord{}); ok {
		t.Error("running record kept")
	}
}

func TestOwnerProblem(t *testing.T) {
	dir := t.TempDir()
	if p := ownerProblem(dir, os.Getuid()); p != "" {
		t.Errorf("own prefix: %q", p)
	}
	if p := ownerProblem(dir, os.Getuid()+1); !strings.Contains(p, "belongs to") {
		t.Errorf("foreign prefix: %q", p)
	}
	if p := ownerProblem(dir, 0); p == "" {
		t.Error("root accepted")
	}
}

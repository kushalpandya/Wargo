// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseOSRelease(t *testing.T) {
	o := ParseOSRelease([]byte("NAME=\"Manjaro Linux\"\nPRETTY_NAME=\"Manjaro Linux\"\nID=manjaro\nID_LIKE=arch\n# comment\n"))
	if o.ID != "manjaro" || !reflect.DeepEqual(o.IDLike, []string{"arch"}) || o.PrettyName != "Manjaro Linux" {
		t.Fatalf("%+v", o)
	}
}

func TestDetect(t *testing.T) {
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	only := func(bins ...string) lookFn {
		return func(b string) bool {
			for _, x := range bins {
				if x == b {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		o    OSRelease
		look lookFn
		want string
	}{
		{OSRelease{ID: "manjaro", IDLike: []string{"arch"}}, all, "pamac"},
		{OSRelease{ID: "manjaro-arm"}, only("pacman"), "pacman"},
		{OSRelease{ID: "arch"}, all, "pacman"},
		{OSRelease{ID: "endeavouros", IDLike: []string{"arch"}}, none, "pacman"},
		{OSRelease{ID: "ubuntu", IDLike: []string{"debian"}}, none, "apt"},
		{OSRelease{ID: "raspbian", IDLike: []string{"debian"}}, none, "apt"},
		{OSRelease{ID: "linuxmint", IDLike: []string{"ubuntu", "debian"}}, none, "apt"},
		{OSRelease{ID: "fedora"}, none, "dnf"},
		{OSRelease{ID: "rocky", IDLike: []string{"rhel", "centos", "fedora"}}, none, "dnf"},
		{OSRelease{ID: "opensuse-tumbleweed", IDLike: []string{"opensuse", "suse"}}, none, "zypper"},
		{OSRelease{ID: "alpine"}, none, "apk"},
		{OSRelease{ID: "weird"}, only("apt-get"), "apt"},
		{OSRelease{ID: "weird"}, none, ""},
	}
	for _, c := range cases {
		b := Detect(c.o, c.look)
		got := ""
		if b != nil {
			got = b.Name()
		}
		if got != c.want {
			t.Errorf("%+v: got %q want %q", c.o, got, c.want)
		}
	}
	if z := Detect(OSRelease{ID: "opensuse-tumbleweed"}, none).(*zypperBackend); !z.tumbleweed || z.UpgradeArgv()[3] != "dist-upgrade" {
		t.Error("tumbleweed should dist-upgrade")
	}
	p := Detect(OSRelease{ID: "arch"}, only("pacman")).(*pacmanBackend)
	if !p.CheckNeedsRoot() || p.RefreshArgv() == nil {
		t.Error("arch without checkupdates needs an elevated refresh")
	}
	p = Detect(OSRelease{ID: "arch"}, only("pacman", "checkupdates", "yay")).(*pacmanBackend)
	if p.CheckNeedsRoot() || p.aurHelper != "yay" {
		t.Error("arch with checkupdates checks unprivileged")
	}
}

func TestParsePamacUpToDate(t *testing.T) {
	// Captured from the dev host (pamac 11.7.4).
	ups, extras := ParsePamacCheck(fixture(t, "pamac-uptodate.txt"))
	if len(ups) != 0 || len(extras) != 0 {
		t.Fatalf("got %v %v", ups, extras)
	}
}

func TestParsePamacUpdates(t *testing.T) {
	ups, extras := ParsePamacCheck(fixture(t, "pamac-updates.txt"))
	if len(ups) != 12 || len(extras) != 2 {
		t.Fatalf("got %d updates, %d extras", len(ups), len(extras))
	}
	if ups[0] != (Update{Name: "linux618", Current: "6.18.49-1", New: "6.18.55-1", Repo: "core", Source: "repo"}) {
		t.Errorf("%+v", ups[0])
	}
	if ups[4].Name != "glibc" || ups[4].New != "2.44+r58+g9a1c2d4e0f11-1" {
		t.Errorf("%+v", ups[4])
	}
	if ups[11].Repo != "multilib" || ups[5].Current != "1:25.2.4-1" {
		t.Errorf("%+v %+v", ups[11], ups[5])
	}
	if extras[0] != (Update{Name: "yay", Current: "12.5.0-1", New: "12.5.2-1", Repo: "AUR", Source: "aur"}) {
		t.Errorf("%+v", extras[0])
	}
	ups, extras = ParsePamacCheck(fixture(t, "pamac-quiet-noise.txt"))
	if len(ups) != 1 || len(extras) != 1 || ups[0].New != "5.3.3-2" {
		t.Errorf("%v %v", ups, extras)
	}
}

func TestParseCheckupdates(t *testing.T) {
	ups := ParseArrowList(fixture(t, "checkupdates.txt"), "repo")
	if len(ups) != 4 {
		t.Fatalf("got %d: %v", len(ups), ups)
	}
	if ups[2] != (Update{Name: "mesa", Current: "1:25.2.4-1", New: "1:25.2.5-1", Source: "repo"}) {
		t.Errorf("%+v", ups[2])
	}
	// pacman -Qu prints the same format.
	ups = ParseArrowList([]byte("bash 5.3.3-1 -> 5.3.3-2\nzstd 1.5.7-1 -> 1.5.7-2\n"), "repo")
	if len(ups) != 2 || ups[1].Name != "zstd" {
		t.Errorf("%v", ups)
	}
}

func TestEnrichPacman(t *testing.T) {
	ups := ParseArrowList(fixture(t, "checkupdates.txt"), "repo")
	run := func(ctx context.Context, argv ...string) ([]byte, []byte, int, error) {
		if argv[0] != "pacman" || argv[1] != "-Sp" {
			t.Fatalf("unexpected %v", argv)
		}
		return fixture(t, "pacman-print.txt"), nil, 0, nil
	}
	enrichPacman(context.Background(), run, ups, "")
	if ups[0].Repo != "core" || ups[0].Size != 148213760 {
		t.Errorf("%+v", ups[0])
	}
	// Version mismatch (stale sync db): repo filled, size left unknown.
	if ups[2].Repo != "extra" || ups[2].Size != 0 {
		t.Errorf("%+v", ups[2])
	}
}

func TestPamacCheckExitCodes(t *testing.T) {
	b := &pamacBackend{}
	mk := func(out string, code int) Runner {
		return func(ctx context.Context, argv ...string) ([]byte, []byte, int, error) {
			if argv[0] == "pamac" {
				return []byte(out), []byte("Error creating textual authentication agent: ...\nfailed to sync\n"), code, nil
			}
			return nil, nil, 1, nil
		}
	}
	if ups, _, err := b.Check(context.Background(), mk(string(fixture(t, "pamac-updates.txt")), 100)); err != nil || len(ups) != 12 {
		t.Errorf("exit 100: %v %d", err, len(ups))
	}
	if ups, _, err := b.Check(context.Background(), mk("Your system is up to date.\n", 0)); err != nil || len(ups) != 0 {
		t.Errorf("exit 0: %v", err)
	}
	if _, _, err := b.Check(context.Background(), mk("", 1)); err == nil || !strings.Contains(err.Error(), "failed to sync") {
		t.Errorf("exit 1: %v", err)
	}
}

func TestParseAptSimulate(t *testing.T) {
	ups := ParseAptSimulate(fixture(t, "apt-simulate.txt"))
	if len(ups) != 6 {
		t.Fatalf("got %d: %v", len(ups), ups)
	}
	if ups[0] != (Update{Name: "base-files", Current: "12.4+deb12u11", New: "12.4+deb12u12", Repo: "stable", Source: "repo"}) {
		t.Errorf("%+v", ups[0])
	}
	if ups[1].Name != "libc6" || ups[1].Repo != "stable" || ups[1].New != "2.36-9+deb12u13" {
		t.Errorf("%+v", ups[1])
	}
	if ups[3].Current != "" || ups[3].Repo != "stable-security" {
		t.Errorf("new dependency: %+v", ups[3])
	}
	if ups[5].New != "1:9.2p1-2+deb12u7" {
		t.Errorf("%+v", ups[5])
	}
	ups = ParseAptSimulate(fixture(t, "apt-ubuntu-simulate.txt"))
	if len(ups) != 3 || ups[0].Repo != "noble-updates" || ups[2].Repo != "noble-updates" {
		t.Errorf("%v", ups)
	}
}

func TestParseAptList(t *testing.T) {
	ups := ParseAptList(fixture(t, "apt-list.txt"))
	want := []Update{
		{Name: "base-files", Current: "12.4+deb12u11", New: "12.4+deb12u12", Repo: "stable", Source: "repo"},
		{Name: "libc6", Current: "2.36-9+deb12u10", New: "2.36-9+deb12u13", Repo: "stable", Source: "repo"},
		{Name: "openssh-client", Current: "1:9.2p1-2+deb12u6", New: "1:9.2p1-2+deb12u7", Repo: "stable-security", Source: "repo"},
	}
	if !reflect.DeepEqual(ups, want) {
		t.Errorf("got %+v", ups)
	}
}

func TestParseDnf(t *testing.T) {
	ups := ParseDnfCheckUpdate(fixture(t, "dnf-check.txt"))
	if len(ups) != 5 {
		t.Fatalf("got %d: %+v", len(ups), ups)
	}
	if ups[3].Name != "python3-very-long-package-name-for-wrapping.noarch" || ups[3].New != "2.4.1-3.fc42" {
		t.Errorf("wrapped: %+v", ups[3])
	}
	if ups[4].Repo != "updates-testing" {
		t.Errorf("%+v", ups[4])
	}
	cur := ParseRpmQuery(fixture(t, "rpm-qa.txt"))
	if cur["kernel.x86_64"] != "6.16.9-200.fc42" {
		t.Errorf("%v", cur)
	}
	b := &dnfBackend{}
	run := func(ctx context.Context, argv ...string) ([]byte, []byte, int, error) {
		if argv[0] == "dnf" {
			return fixture(t, "dnf-check.txt"), nil, 100, nil
		}
		return fixture(t, "rpm-qa.txt"), nil, 0, nil
	}
	ups, _, err := b.Check(context.Background(), run)
	if err != nil || ups[0].Current != "6.16.9-200.fc42" {
		t.Errorf("%v %+v", err, ups[0])
	}
}

func TestParseZypper(t *testing.T) {
	ups, err := ParseZypperXML(fixture(t, "zypper-lu.xml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Update{
		{Name: "vim", Current: "9.1.1406-1.1", New: "9.1.1629-1.1", Repo: "repo-oss", Source: "repo"},
		{Name: "kernel-default", Current: "6.17.3-1.1", New: "6.17.5-1.1", Repo: "repo-update", Source: "repo"},
	}
	if !reflect.DeepEqual(ups, want) {
		t.Errorf("got %+v", ups)
	}
}

func TestParseApk(t *testing.T) {
	ups := ParseApkVersion(fixture(t, "apk-version.txt"))
	want := []Update{
		{Name: "busybox", Current: "1.36.1-r29", New: "1.36.1-r31", Source: "repo"},
		{Name: "py3-cryptography", Current: "42.0.8-r0", New: "42.0.8-r1", Source: "repo"},
		{Name: "libcrypto3", Current: "3.3.2-r1", New: "3.3.3-r0", Source: "repo"},
	}
	if !reflect.DeepEqual(ups, want) {
		t.Errorf("got %+v", ups)
	}
}

func TestParseFlatpak(t *testing.T) {
	ups := ParseFlatpakUpdates(fixture(t, "flatpak-ls.txt"), ParseFlatpakList(fixture(t, "flatpak-list.txt")))
	if len(ups) != 3 {
		t.Fatalf("%+v", ups)
	}
	if ups[0] != (Update{Name: "org.mozilla.firefox", Current: "144.0", New: "144.0.2", Repo: "flathub", Source: "flatpak"}) {
		t.Errorf("%+v", ups[0])
	}
	if ups[2].New != "" || ups[2].Current != "49" {
		t.Errorf("%+v", ups[2])
	}
}

// replay feeds a fixture through a parser and records each state.
func replay(t *testing.T, name string, parse func(string, *ProgressState)) (ProgressState, []ProgressState) {
	t.Helper()
	var st ProgressState
	var hist []ProgressState
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, name))))
	for sc.Scan() {
		prev := st.Frac
		parse(sc.Text(), &st)
		if st.Frac < prev {
			t.Fatalf("progress went backwards on %q: %v -> %v", sc.Text(), prev, st.Frac)
		}
		hist = append(hist, st)
	}
	return st, hist
}

func stageSeq(h []ProgressState) []string {
	var out []string
	for _, s := range h {
		if s.Stage != "" && (len(out) == 0 || out[len(out)-1] != s.Stage) {
			out = append(out, s.Stage)
		}
	}
	return out
}

func TestPacmanProgress(t *testing.T) {
	st, hist := replay(t, "pacman-upgrade.txt", parsePacman)
	want := []string{StagePreparing, StageDownloading, StageInstalling, StageHooks}
	if got := stageSeq(hist); !reflect.DeepEqual(got, want) {
		t.Errorf("stages %v", got)
	}
	if st.total != 5 || st.Upgraded != 5 {
		t.Errorf("total=%d upgraded=%d", st.total, st.Upgraded)
	}
	if st.Frac < 0.98 || st.Current != "Updating the desktop file MIME type cache" {
		t.Errorf("final %+v", st)
	}
	// Spot-check intermediate states.
	find := func(cur string) ProgressState {
		for _, h := range hist {
			if h.Current == cur {
				return h
			}
		}
		t.Fatalf("no state with current %q", cur)
		return ProgressState{}
	}
	var dl, inst bool
	for _, h := range hist {
		if h.Current == "linux618" && h.Stage == StageDownloading && h.Frac > 0.1 && h.Frac < 0.45 {
			dl = true
		}
		if h.Current == "linux618" && h.Stage == StageInstalling && h.Frac > 0.6 && h.Frac < 0.9 {
			inst = true
		}
	}
	if !dl || !inst {
		t.Errorf("linux618 download %v install %v", dl, inst)
	}
	if s := find("Synchronizing core"); s.Stage != StagePreparing {
		t.Errorf("db sync %+v", s)
	}
	if s := find("Arming ConditionNeedsUpdate"); s.Stage != StageHooks {
		t.Errorf("hook %+v", s)
	}
}

func TestPacmanProgressTTY(t *testing.T) {
	st, hist := replay(t, "pacman-upgrade-tty.txt", parsePacman)
	if st.Upgraded != 12 || st.Stage != StageHooks || st.Frac < 0.98 {
		t.Errorf("%+v", st)
	}
	var sawFoo, sawBar bool
	for _, h := range hist {
		if h.Current == "foo" && h.Stage == StageDownloading {
			sawFoo = true
		}
		if h.Current == "bar" && h.Stage == StageInstalling {
			sawBar = true
		}
	}
	if !sawFoo || !sawBar {
		t.Errorf("foo download %v, bar install %v", sawFoo, sawBar)
	}
	st, _ = replay(t, "pacman-nothing.txt", parsePacman)
	if st.Stage != StageDone || st.Frac != 1 {
		t.Errorf("nothing to do: %+v", st)
	}
}

func TestPacmanPkgName(t *testing.T) {
	for in, want := range map[string]string{
		"linux-6.18.2-1-x86_64.pkg.tar.zst":        "linux",
		"python-requests-2.32.3-1-any.pkg.tar.zst": "python-requests",
		"mesa-1:25.2.5-1-x86_64":                   "mesa",
		"core.db":                                  "core.db",
	} {
		if got := pacmanPkgName(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestAptProgress(t *testing.T) {
	st, hist := replay(t, "apt-status.txt", parseApt)
	want := []string{StagePreparing, StageDownloading, StageInstalling, StageHooks, StageInstalling}
	if got := stageSeq(hist); !reflect.DeepEqual(got, want) {
		t.Errorf("stages %v", got)
	}
	if st.Frac < 0.94 {
		t.Errorf("final %+v", st)
	}
	for _, h := range hist {
		if h.Current == "libc6" && h.Stage == StageInstalling {
			return
		}
	}
	t.Error("libc6 (pkg:arch form) not parsed")
}

func TestAptStatusSplit(t *testing.T) {
	cases := []struct {
		in       string
		pkg, msg string
		pct      float64
	}{
		{"1:9.0498:Retrieving file 1 of 3", "1", "Retrieving file 1 of 3", 9.0498},
		{"libc6:amd64:22.2:Unpacking libc6:amd64", "libc6:amd64", "Unpacking libc6:amd64", 22.2},
		{"dpkg-exec:0:Running dpkg", "dpkg-exec", "Running dpkg", 0},
	}
	for _, c := range cases {
		p, pct, msg, ok := aptStatus(c.in)
		if !ok || p != c.pkg || pct != c.pct || msg != c.msg {
			t.Errorf("%q: %q %v %q %v", c.in, p, pct, msg, ok)
		}
	}
}

func TestDnfProgress(t *testing.T) {
	st, hist := replay(t, "dnf-upgrade.txt", parseDnf)
	want := []string{StagePreparing, StageDownloading, StageInstalling, StageHooks, StageDone}
	if got := stageSeq(hist); !reflect.DeepEqual(got, want) {
		t.Errorf("stages %v", got)
	}
	if st.Upgraded != 3 || st.Frac != 1 {
		t.Errorf("%+v", st)
	}
}

func TestZypperProgress(t *testing.T) {
	st, hist := replay(t, "zypper-progress.xml", parseZypper)
	want := []string{StagePreparing, StageDownloading, StageInstalling, StageHooks}
	if got := stageSeq(hist); !reflect.DeepEqual(got, want) {
		t.Errorf("stages %v", got)
	}
	if st.Upgraded != 2 || st.Frac < 0.9 {
		t.Errorf("%+v", st)
	}
	var sawKernel bool
	for _, h := range hist {
		if h.Current == "kernel-default-6.17.5-1.1.x86_64" && h.Stage == StageInstalling {
			sawKernel = true
		}
	}
	if !sawKernel {
		t.Error("kernel install not parsed")
	}
}

func TestApkProgress(t *testing.T) {
	st, hist := replay(t, "apk-upgrade.txt", parseApk)
	want := []string{StagePreparing, StageInstalling, StageHooks, StageDone}
	if got := stageSeq(hist); !reflect.DeepEqual(got, want) {
		t.Errorf("stages %v", got)
	}
	if st.Upgraded != 3 || st.Frac != 1 {
		t.Errorf("%+v", st)
	}
}

func TestCleanLine(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[1;34m::\x1b[0m\x1b[1m Synchronizing\x1b[0m": ":: Synchronizing",
		"downloading 10%\rdownloading 50%\r":              "downloading 50%",
		"plain\ttext\n":                                   "plain\ttext",
	} {
		if got := CleanLine(in); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}

func TestModulesMissing(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "6.18.55-1-MANJARO"), 0o755)
	if req, _ := modulesMissing("6.18.55-1-MANJARO", []string{dir}); req {
		t.Error("installed kernel reported missing")
	}
	if req, reason := modulesMissing("6.18.49-1-MANJARO", []string{dir}); !req || !strings.Contains(reason, "6.18.49") {
		t.Error("missing kernel not reported")
	}
	if req, _ := modulesMissing("x", []string{filepath.Join(dir, "nope")}); req {
		t.Error("no modules dir at all must not report")
	}
}

func TestFindConfigFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a/b"), 0o755)
	old := filepath.Join(dir, "a/old.conf.pacnew")
	os.WriteFile(old, nil, 0o644)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)
	os.WriteFile(filepath.Join(dir, "a/b/new.conf.pacnew"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "a/b/x.conf"), nil, 0o644)
	os.MkdirAll(filepath.Join(dir, "locked"), 0o000)
	defer os.Chmod(filepath.Join(dir, "locked"), 0o755)
	got := findConfigFiles(dir, []string{".pacnew", ".pacsave"}, time.Now().Add(-time.Minute))
	if len(got) != 2 || !got[0].New || got[1].New || !strings.HasSuffix(got[0].Path, "new.conf.pacnew") {
		t.Errorf("%+v", got)
	}
}

func TestCheckLocks(t *testing.T) {
	dir := t.TempDir()
	if st := checkLocks([]string{filepath.Join(dir, "db.lck")}); st.Locked {
		t.Error("absent lock reported")
	}
	os.WriteFile(filepath.Join(dir, "db.lck"), nil, 0o644)
	if st := checkLocks([]string{filepath.Join(dir, "db.lck")}); !st.Locked {
		t.Error("pacman lock not detected")
	}
	pid := filepath.Join(dir, "dnf.pid")
	os.WriteFile(pid, []byte("999999999\n"), 0o644)
	if st := checkLocks([]string{pid}); st.Locked {
		t.Error("stale pid file reported as lock")
	}
	os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0o644)
	if st := checkLocks([]string{pid}); !st.Locked {
		t.Error("live pid not detected")
	}
	// An always-present lock file without an active lock is free.
	front := filepath.Join(dir, "lock-frontend")
	os.WriteFile(front, nil, 0o640)
	if st := checkLocks([]string{front}); st.Locked {
		t.Error("unlocked dpkg lock reported")
	}
	f, _ := os.Open(front)
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if st := checkLocks([]string{front}); !st.Locked {
		t.Error("held flock not detected via /proc/locks")
	}
}

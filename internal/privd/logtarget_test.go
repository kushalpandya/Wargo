// SPDX-License-Identifier: AGPL-3.0-only

package privd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func logFixture(t *testing.T) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	old := logDirs
	logDirs = []string{dir + "/"}
	t.Cleanup(func() { logDirs = old })
	return &Config{AllowedUID: os.Getuid(), SystemdRun: "/usr/bin/systemd-run"}, dir
}

func runArgv(log string) []string {
	return []string{"/usr/bin/systemd-run", "--unit=wargo-upgrade-1",
		"--property=StandardOutput=append:" + log, "--property=StandardError=append:" + log, "--", "pacman", "-Syu"}
}
func TestLogTargetCreatedWhenMissing(t *testing.T) {
	c, dir := logFixture(t)
	log := filepath.Join(dir, "new.log")
	_, files, err := c.openLogTargets(runArgv(log))
	if err != nil {
		t.Fatal(err)
	}
	files[0].Close()
	st, err := os.Lstat(log)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o640 {
		t.Fatalf("log not created as a private regular file: %v %v", st, err)
	}
}

func TestLogTargetSymlinkRejected(t *testing.T) {
	c, dir := logFixture(t)
	victim := filepath.Join(t.TempDir(), "sudoers")
	os.WriteFile(victim, []byte("root ALL=(ALL) ALL\n"), 0o600)
	log := filepath.Join(dir, "1.log")
	if err := os.Symlink(victim, log); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.openLogTargets(runArgv(log)); err == nil {
		t.Fatal("symlinked log target accepted")
	}
	if b, _ := os.ReadFile(victim); string(b) != "root ALL=(ALL) ALL\n" {
		t.Fatal("victim modified")
	}
}

func TestLogTargetHardlinkRejected(t *testing.T) {
	c, dir := logFixture(t)
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, nil, 0o600)
	log := filepath.Join(dir, "1.log")
	if err := os.Link(victim, log); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.openLogTargets(runArgv(log)); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("hard-linked log target accepted: %v", err)
	}
}

func TestLogTargetSubdirectoryRejected(t *testing.T) {
	c, dir := logFixture(t)
	// A subdirectory owned by the Wargo user could be swapped for a
	// symlink to /etc; only files directly in the log dir are accepted.
	sub := filepath.Join(dir, "sub")
	os.Symlink(t.TempDir(), sub)
	if _, _, err := c.openLogTargets(runArgv(filepath.Join(sub, "x.log"))); err == nil {
		t.Fatal("log in a (symlinked) subdirectory accepted")
	}
}

func TestLogDirSymlinkRejected(t *testing.T) {
	c, _ := logFixture(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "logs")
	os.Symlink(real, link)
	logDirs = []string{link + "/"}
	if _, _, err := c.openLogTargets(runArgv(filepath.Join(link, "1.log"))); err == nil {
		t.Fatal("symlinked log directory accepted")
	}
}

// The log path pkg builds for the native data dir must pass privd's check.
func TestUpgradeLogAcceptedByPrivd(t *testing.T) {
	c, dir := logFixture(t)
	log := filepath.Join(dir, "pkg-20260101-120000.log") // dir plays the role of /var/lib/wargo
	argv, files, err := c.openLogTargets(runArgv(log))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		f.Close()
	}
	if len(argv) == 0 {
		t.Fatal("no argv")
	}
	// The old location, a subdirectory, is still refused.
	if _, _, err := c.openLogTargets(runArgv(filepath.Join(dir, "pkg", "x.log"))); err == nil {
		t.Fatal("subdirectory log accepted")
	}
}

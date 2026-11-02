// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestResolvePackageManager(t *testing.T) {
	for _, bad := range []string{"sh", "/bin/sh", "/tmp/pacman", "../pacman", "bash"} {
		if _, err := ResolvePackageManager(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// pacman or apt-get exists on most dev hosts; skip when neither does.
	for _, pm := range []string{"pacman", "apt-get", "dnf"} {
		if p, err := ResolvePackageManager(pm); err == nil {
			if p[0] != '/' {
				t.Errorf("not absolute: %s", p)
			}
			return
		}
	}
	t.Skip("no package manager on this host")
}

func TestSignalNames(t *testing.T) {
	if _, ok := Signal("TERM"); !ok {
		t.Fatal("TERM")
	}
	if _, ok := Signal("SEGV"); ok {
		t.Fatal("unexpected signal allowed")
	}
}

func TestParsePID(t *testing.T) {
	for _, bad := range []string{"", "0", "1", "-1", "-1234", "+5", " 5", "05", "4294967295", "4294967294", "4194305", "1e3", "0x10", "99999999"} {
		if _, err := ParsePID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"2", "1234", "4194304"} {
		if _, err := ParsePID(good); err != nil {
			t.Errorf("%q rejected: %v", good, err)
		}
	}
}

func TestKillVerifiesStartTime(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer cmd.Process.Kill()
	pid := cmd.Process.Pid
	start, err := procStartTime(pid)
	if err != nil {
		t.Fatal(err)
	}
	// Wrong start time: the pid now belongs to "another" process.
	err = kill([]string{"CONT", strconv.Itoa(pid), strconv.FormatUint(start+1, 10)})
	if !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("signal delivered despite start-time mismatch: %v", err)
	}
	if err := kill([]string{"TERM", strconv.Itoa(pid), strconv.FormatUint(start, 10)}); err != nil {
		t.Fatalf("matching start time: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("process should have been terminated")
	}
	if err := kill([]string{"TERM", "123", "abc"}); err == nil {
		t.Fatal("bad start time accepted")
	}
}

func TestComposeProjectValidated(t *testing.T) {
	for _, bad := range []string{"-H=tcp://evil", "--file=/etc/x", "", "a b", "x;y"} {
		if err := composeUpdate([]string{"/srv", bad, "/srv/compose.yaml"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "project") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestPrivdTrustStoreShape(t *testing.T) {
	good := `{"userHandle":"AAAA","rpIds":["wargo.example.com"],"credentials":[{"id":"AQ==","publicKey":"AQ=="}]}`
	if err := validTrustStore([]byte(good)); err != nil {
		t.Fatalf("valid store rejected: %v", err)
	}
	for _, bad := range []string{
		`{}`, `[]`, `{"userHandle":"AAAA","rpIds":["x"],"credentials":[]}`,
		`{"userHandle":"!!","rpIds":["x"],"credentials":[{"id":"AQ==","publicKey":"AQ=="}]}`,
		`{"userHandle":"AAAA","rpIds":["a/b"],"credentials":[{"id":"AQ==","publicKey":"AQ=="}]}`,
		`{"userHandle":"AAAA","rpIds":["x"],"credentials":[{"id":"AQ=="}]}`,
		`{"userHandle":"AAAA","rpIds":["x"],"credentials":[{"id":"AQ==","publicKey":"AQ=="}],"extra":1}`,
		good + `{}`,
	} {
		if err := validTrustStore([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestFsWriteRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "passwd")
	os.WriteFile(victim, []byte("root:x:0:0\n"), 0o644)
	os.Symlink(victim, filepath.Join(dir, "x"))
	if err := fsOp([]string{"write", dir, "x"}, strings.NewReader("pwned"), io.Discard); err == nil {
		t.Fatal("write through a symlink accepted")
	}
	if b, _ := os.ReadFile(victim); string(b) != "root:x:0:0\n" {
		t.Fatal("symlink target modified")
	}
}

func TestFsWriteDoesNotWriteThroughHardlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("original"), 0o600)
	os.Link(victim, filepath.Join(dir, "x"))
	if err := fsOp([]string{"write", dir, "x"}, strings.NewReader("new"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Fatal("hard-linked inode was overwritten")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "x"))
	st, _ := os.Stat(filepath.Join(dir, "x"))
	if string(b) != "new" || st.Mode().Perm() != 0o600 {
		t.Fatalf("content %q mode %v (mode must be preserved)", b, st.Mode().Perm())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("temp file left behind: %v", ents)
	}
}

func TestFsReadRefusesFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { done <- fsOp([]string{"read", dir, "fifo"}, nil, io.Discard) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO read accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read of a FIFO blocked")
	}
	os.WriteFile(filepath.Join(dir, "f"), []byte("ok"), 0o644)
	var buf strings.Builder
	if err := fsOp([]string{"read", dir, "f"}, nil, &buf); err != nil || buf.String() != "ok" {
		t.Fatalf("regular read: %q %v", buf.String(), err)
	}
}

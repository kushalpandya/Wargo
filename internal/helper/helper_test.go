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

	"github.com/kushalpandya/wargo/internal/host"
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

func TestComposeFilesValidated(t *testing.T) {
	for _, files := range [][]string{
		{"/srv/compose.yaml", "override.yaml"},
		{"/srv/compose.yaml", "/srv/../etc/x.yaml"},
		{"-f"},
	} {
		args := append([]string{"/srv", "media"}, files...)
		if err := composeUpdate(args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Errorf("%v: %v", files, err)
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

// symlinkTree builds root/target (0600), root/d/link -> ../target and
// root/sd -> d2 where d2/f is 0600: every link stays inside the root, so
// os.Root alone would follow them.
func symlinkTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "target"), []byte("secret"), 0o600)
	os.Mkdir(filepath.Join(root, "d"), 0o755)
	os.Symlink("../target", filepath.Join(root, "d", "link"))
	os.Symlink("target", filepath.Join(root, "link"))
	os.Mkdir(filepath.Join(root, "d2"), 0o755)
	os.WriteFile(filepath.Join(root, "d2", "f"), []byte("secret"), 0o600)
	os.Symlink("d2", filepath.Join(root, "sd"))
	return root
}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestHelperFsChmodRefusesSymlink(t *testing.T) {
	root := symlinkTree(t)
	for _, p := range []string{"d/link", "link", "sd/f"} {
		if _, err := Exec([]string{"fs", "chmod", root, p, "644"}, nil, io.Discard, io.Discard); err == nil {
			t.Errorf("chmod %s through a symlink accepted", p)
		}
	}
	if m := modeOf(t, filepath.Join(root, "target")); m != 0o600 {
		t.Fatalf("target mode changed to %v", m)
	}
	if m := modeOf(t, filepath.Join(root, "d2", "f")); m != 0o600 {
		t.Fatalf("d2/f mode changed to %v", m)
	}
	if _, err := Exec([]string{"fs", "chmod", root, "d2/f", "640"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if m := modeOf(t, filepath.Join(root, "d2", "f")); m != 0o640 {
		t.Fatalf("plain chmod gave %v", m)
	}
}

func TestHelperFsReadRefusesSymlink(t *testing.T) {
	root := symlinkTree(t)
	for _, p := range []string{"link", "d/link", "sd/f"} {
		var out strings.Builder
		if _, err := Exec([]string{"fs", "read", root, p}, nil, &out, io.Discard); err == nil {
			t.Errorf("read %s through a symlink accepted", p)
		}
		if out.Len() != 0 {
			t.Errorf("read %s wrote %q", p, out.String())
		}
	}
	var out strings.Builder
	if _, err := Exec([]string{"fs", "read", root, "d2/f"}, nil, &out, io.Discard); err != nil || out.String() != "secret" {
		t.Fatalf("plain read: %q %v", out.String(), err)
	}
}

func TestHelperFsOpsRefuseSymlinkedParent(t *testing.T) {
	root := symlinkTree(t)
	run := func(args ...string) error {
		_, err := Exec(append([]string{"fs"}, args...), strings.NewReader("pwned"), io.Discard, io.Discard)
		return err
	}
	for _, c := range [][]string{
		{"mkdir", root, "sd/new"},
		{"remove", root, "sd/f"},
		{"move", root, "sd/f", "moved"},
		{"move", root, "target", "sd/moved"},
		{"copy", root, "sd/f", "copied"},
		{"copy", root, "target", "sd/copied"},
		{"chown", root, "sd/f", strconv.Itoa(os.Getuid())},
		{"write", root, "sd/f"},
	} {
		if err := run(c...); err == nil {
			t.Errorf("%v through a symlinked folder accepted", c)
		}
	}
	for _, p := range []string{"d2/new", "d2/moved", "copied", "d2/copied", "moved"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s was created", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "d2", "f")); string(b) != "secret" {
		t.Fatal("d2/f changed")
	}
	// The same operations on plain paths still work.
	for _, c := range [][]string{
		{"mkdir", root, "d2/new"},
		{"copy", root, "d2/f", "d2/new/c"},
		{"move", root, "d2/new/c", "d/m"},
		{"chown", root, "d/m", strconv.Itoa(os.Getuid())},
		{"write", root, "d/m"},
		{"remove", root, "d2/new"},
	} {
		if err := run(c...); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "d", "m")); string(b) != "pwned" {
		t.Fatalf("d/m = %q", b)
	}
}

func TestHelperStdinFileRejectsSymlinkAndForeignOwner(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()

	// No option: args pass through untouched.
	in, rest, err := parseStdinFile([]string{"fs", "read"}, uid)
	if err != nil || in != nil || len(rest) != 2 {
		t.Fatalf("passthrough: %v %v %v", in, rest, err)
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseStdinFile([]string{"--stdin-file", link, "fs"}, uid); err == nil {
		t.Error("symlink accepted")
	}
	if _, _, err := parseStdinFile([]string{"--stdin-file", dir, "fs"}, uid); err == nil {
		t.Error("directory accepted")
	}
	if _, _, err := parseStdinFile([]string{"--stdin-file", good, "fs"}, uid+1); err == nil {
		t.Error("foreign owner accepted")
	}
	if _, _, err := parseStdinFile([]string{"--stdin-file", good}, uid); err == nil {
		t.Error("missing operation accepted")
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("rejected file was removed: %v", err)
	}

	in, rest, err = parseStdinFile([]string{"--stdin-file", good, "fs", "write"}, uid)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(in)
	if string(b) != "payload" || len(rest) != 2 || rest[0] != "fs" {
		t.Fatalf("got %q %v", b, rest)
	}
	in.Close()
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Error("file not removed after close")
	}
}

func TestStdinFileOwnerRequiresSudoUID(t *testing.T) {
	opt := []string{"--stdin-file", "/x", "fs"}
	if _, err := stdinFileOwner(opt, "", 0); err == nil {
		t.Error("--stdin-file accepted without SUDO_UID")
	}
	if uid, err := stdinFileOwner([]string{"fs"}, "", 7); err != nil || uid != 7 {
		t.Errorf("no option, no SUDO_UID: %d %v", uid, err)
	}
	if uid, err := stdinFileOwner(opt, "1000", 0); err != nil || uid != 1000 {
		t.Errorf("SUDO_UID: %d %v", uid, err)
	}
	if _, err := stdinFileOwner(opt, "abc", 0); err == nil {
		t.Error("bad SUDO_UID accepted")
	}
}

func TestResolvePackageManagerMustBeRootOwned(t *testing.T) {
	dir := t.TempDir()
	pm := filepath.Join(dir, "pacman")
	if err := os.WriteFile(pm, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldDirs, oldReq, oldUID := systemBinDirs, requireRootOwner, rootUID
	systemBinDirs = []string{dir}
	t.Cleanup(func() { systemBinDirs, requireRootOwner, rootUID = oldDirs, oldReq, oldUID })
	requireRootOwner = false
	if _, err := ResolvePackageManager(pm); err != nil {
		t.Fatalf("unprivileged: %v", err)
	}
	requireRootOwner = true
	if _, err := ResolvePackageManager(pm); err == nil || !strings.Contains(err.Error(), "not owned by root") {
		t.Fatalf("user-owned binary accepted: %v", err)
	}
	// Pretend this user is root, so only the mode check can reject.
	rootUID = uint32(os.Getuid())
	os.Chmod(pm, 0o777)
	if _, err := ResolvePackageManager(pm); err == nil || !strings.Contains(err.Error(), pm+" is writable") {
		t.Fatalf("writable binary: %v", err)
	}
}

func TestResolvePackageManagerContainerUsesHostRoot(t *testing.T) {
	r := t.TempDir()
	if err := os.MkdirAll(filepath.Join(r, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Only the "host" has pacman; the container's own /usr/bin must not matter.
	if err := os.WriteFile(filepath.Join(r, "usr/bin/pacman"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The mount point itself is not trusted material (0777 here).
	os.Chmod(r, 0o777)
	defer host.SetRoot(r)()
	oldReq, oldUID := requireRootOwner, rootUID
	requireRootOwner, rootUID = true, uint32(os.Getuid())
	t.Cleanup(func() { requireRootOwner, rootUID = oldReq, oldUID })
	p, err := ResolvePackageManager("pacman")
	if err != nil || p != "/usr/bin/pacman" {
		t.Fatalf("got %q, %v", p, err)
	}
	// A writable directory below the host root is refused.
	os.Chmod(filepath.Join(r, "usr/bin"), 0o777)
	if _, err := ResolvePackageManager("pacman"); err == nil {
		t.Fatal("writable host directory accepted")
	}
}

func TestResolvePackageManagerSymlinkInWritableDir(t *testing.T) {
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "usr/lib/pm"), 0o755)
	os.MkdirAll(filepath.Join(r, "usr/local/bin"), 0o755)
	os.WriteFile(filepath.Join(r, "usr/lib/pm/pacman"), []byte("x"), 0o755)
	if err := os.Symlink("/usr/lib/pm/pacman", filepath.Join(r, "usr/local/bin/pacman")); err != nil {
		t.Fatal(err)
	}
	defer host.SetRoot(r)()
	oldReq, oldUID := requireRootOwner, rootUID
	requireRootOwner, rootUID = true, uint32(os.Getuid())
	t.Cleanup(func() { requireRootOwner, rootUID = oldReq, oldUID })
	// Trusted link directory: accepted, and the resolved path is what runs.
	lit, res, err := resolvePackageManager("/usr/local/bin/pacman")
	if err != nil || lit != "/usr/local/bin/pacman" || res != "/usr/lib/pm/pacman" {
		t.Fatalf("got %q %q %v", lit, res, err)
	}
	// The directory holding the link is writable: the link could be swapped.
	os.Chmod(filepath.Join(r, "usr/local/bin"), 0o777)
	if _, err := ResolvePackageManager("/usr/local/bin/pacman"); err == nil || !strings.Contains(err.Error(), "/usr/local/bin is writable") {
		t.Fatalf("symlink in writable dir accepted: %v", err)
	}
}

func TestResolvePackageManagerSymlinkedHostRoot(t *testing.T) {
	r := t.TempDir()
	os.MkdirAll(filepath.Join(r, "usr/bin"), 0o755)
	os.WriteFile(filepath.Join(r, "usr/bin/pacman"), []byte("x"), 0o755)
	link := filepath.Join(t.TempDir(), "hostlink")
	if err := os.Symlink(r, link); err != nil {
		t.Fatal(err)
	}
	defer host.SetRoot(link)()
	oldReq, oldUID := requireRootOwner, rootUID
	requireRootOwner, rootUID = true, uint32(os.Getuid())
	t.Cleanup(func() { requireRootOwner, rootUID = oldReq, oldUID })
	if _, err := ResolvePackageManager("pacman"); err != nil {
		t.Fatalf("symlinked host root: %v", err)
	}
}

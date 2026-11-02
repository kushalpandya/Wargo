// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fakeRoot builds a minimal host filesystem and switches to container mode.
func fakeRoot(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	w := func(p, s string, mode os.FileMode) {
		full := filepath.Join(r, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	w("etc/passwd", `root:x:0:0:root:/root:/bin/bash
# comment
+nis
bin:x:1:1::/:/usr/bin/nologin
kushal:x:1000:1000:Kushal Pandya:/home/kushal:/bin/zsh
guest:x:1001:1001::/home/guest:/bin/sh
broken:x:abc:1::/:/bin/sh
nobody:x:65534:65534::/:/usr/bin/nologin
`, 0o644)
	w("etc/group", `root:x:0:root
wheel:x:998:kushal,guest
docker:x:968:kushal
kushal:x:1000:
guest:x:1001:
`, 0o644)
	w("etc/shadow", "root:!:19000::::::\nkushal:$6$salt$hash:19000:0:99999:7:::\nguest:*:19000:::::19001:\n", 0o600)
	w("usr/lib/os-release", "ID=arch\n", 0o644)
	if err := os.Symlink("../usr/lib/os-release", filepath.Join(r, "etc/os-release")); err != nil {
		t.Fatal(err)
	}
	w("usr/bin/tmux", "#!/bin/sh\n", 0o755)
	w("usr/bin/notexec", "", 0o644)
	os.MkdirAll(filepath.Join(r, "var/home/kushal"), 0o755)
	os.Symlink("/var/home", filepath.Join(r, "home2"))
	os.Symlink("/../../../etc", filepath.Join(r, "escape"))
	t.Cleanup(SetRoot(r))
	return r
}

func TestNativeIdentity(t *testing.T) {
	defer SetRoot("")()
	if Container() || Root() != "" {
		t.Fatal("expected native mode")
	}
	for _, p := range []string{"/etc/passwd", "relative", "/"} {
		if Path(p) != p {
			t.Fatalf("Path(%q) = %q", p, Path(p))
		}
		if q, ok := Unpath(p); !ok || q != p {
			t.Fatalf("Unpath(%q)", p)
		}
		if q, _ := Resolve(p); q != p {
			t.Fatalf("Resolve(%q) = %q", p, q)
		}
	}
	called := false
	if err := AsUser(func() error { called = true; return nil }); err != nil || !called {
		t.Fatal("AsUser must just call fn natively")
	}
	if err := AsRoot(func() error { return errors.New("x") }); err == nil || err.Error() != "x" {
		t.Fatal("AsRoot must return fn's error")
	}
	if AdminUID() != os.Getuid() {
		t.Fatal("native admin uid must be the process uid")
	}
	cmd := Command(context.Background(), "echo", "hi")
	if !slices.Equal(cmd.Args, []string{"echo", "hi"}) {
		t.Fatalf("native command must not be wrapped: %q", cmd.Args)
	}
}

func TestRootCleaning(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "relative": "", "/host/": "/host", " /host ": "/host"} {
		if got := cleanRoot(in); got != want {
			t.Errorf("cleanRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathMapping(t *testing.T) {
	r := fakeRoot(t)
	if !Container() {
		t.Fatal("expected container mode")
	}
	cases := map[string]string{
		"/etc/os-release":        r + "/etc/os-release",
		"/proc/1/mountinfo":      r + "/proc/1/mountinfo",
		"/":                      r,
		"/var/../etc/shadow":     r + "/etc/shadow",
		"/../../etc/passwd":      r + "/etc/passwd",
		"relative/stays":         "relative/stays",
		"/dev/disk/by-label/foo": r + "/dev/disk/by-label/foo",
	}
	for in, want := range cases {
		if got := Path(in); got != want {
			t.Errorf("Path(%q) = %q, want %q", in, got, want)
		}
	}
	if p, ok := Unpath(r + "/home/kushal"); !ok || p != "/home/kushal" {
		t.Fatalf("Unpath = %q %v", p, ok)
	}
	if p, ok := Unpath(r); !ok || p != "/" {
		t.Fatalf("Unpath(root) = %q", p)
	}
	if _, ok := Unpath("/data/x"); ok {
		t.Fatal("paths outside the host root must not unpath")
	}
	if _, ok := Unpath(r + "x/y"); ok {
		t.Fatal("sibling prefix must not match")
	}
}

func TestResolveInsideRoot(t *testing.T) {
	r := fakeRoot(t)
	// Relative link.
	if p, err := Resolve("/etc/os-release"); err != nil || p != r+"/usr/lib/os-release" {
		t.Fatalf("relative link: %q %v", p, err)
	}
	b, err := ReadFile("/etc/os-release")
	if err != nil || string(b) != "ID=arch\n" {
		t.Fatalf("ReadFile: %q %v", b, err)
	}
	// Absolute link resolves against the host root, not the container's /.
	if p, err := Resolve("/home2/kushal"); err != nil || p != r+"/var/home/kushal" {
		t.Fatalf("absolute link: %q %v", p, err)
	}
	// ".." in a link target can't climb above the host root.
	if p, err := Resolve("/escape/passwd"); err != nil || p != r+"/etc/passwd" {
		t.Fatalf("escape: %q %v", p, err)
	}
	// Missing tail is kept literally.
	if p, err := Resolve("/home2/new/dir"); err != nil || p != r+"/var/home/new/dir" {
		t.Fatalf("missing: %q %v", p, err)
	}
	if hp, err := EvalSymlinks("/home2/kushal"); err != nil || hp != "/var/home/kushal" {
		t.Fatalf("EvalSymlinks: %q %v", hp, err)
	}
	// Loops are detected.
	os.Symlink("/loop", filepath.Join(r, "loop"))
	if _, err := Resolve("/loop/x"); err == nil {
		t.Fatal("expected loop error")
	}
}

func TestUserDB(t *testing.T) {
	fakeRoot(t)
	u, err := LookupUser("kushal")
	if err != nil {
		t.Fatal(err)
	}
	want := &User{Name: "kushal", UID: 1000, GID: 1000, Gecos: "Kushal Pandya", Home: "/home/kushal", Shell: "/bin/zsh"}
	if !reflect.DeepEqual(u, want) {
		t.Fatalf("got %+v", u)
	}
	if u2, err := LookupUID(1001); err != nil || u2.Name != "guest" {
		t.Fatalf("uid lookup %v %v", u2, err)
	}
	if _, err := LookupUser("broken"); err == nil {
		t.Fatal("malformed entry must be skipped")
	}
	if _, err := LookupUser("nis"); err == nil {
		t.Fatal("NIS entry must be skipped")
	}
	g, err := LookupGroup("wheel")
	if err != nil || g.GID != 998 || !slices.Equal(g.Members, []string{"kushal", "guest"}) {
		t.Fatalf("group %+v %v", g, err)
	}
	if g, err := LookupGID(968); err != nil || g.Name != "docker" {
		t.Fatalf("gid lookup %+v %v", g, err)
	}
	gs, err := UserGroups(u)
	if err != nil || !slices.Equal(gs, []int{1000, 998, 968}) {
		t.Fatalf("groups %v %v", gs, err)
	}
	a, grp, err := DefaultAdmin()
	if err != nil || a.Name != "kushal" || grp != "wheel" {
		t.Fatalf("default admin %v %q %v", a, grp, err)
	}
	acct, err := SetAdmin("kushal")
	if err != nil || acct.UID != 1000 || !slices.Equal(acct.Groups, []int{1000, 998, 968}) {
		t.Fatalf("SetAdmin %+v %v", acct, err)
	}
	if AdminUID() != 1000 || AdminName() != "kushal" {
		t.Fatal("admin accessors")
	}
	uid, gid, groups := Identity()
	if uid != 1000 || gid != 1000 || len(groups) != 3 {
		t.Fatalf("identity %d %d %v", uid, gid, groups)
	}
	sh, err := LookupShadow("kushal")
	if err != nil || sh.Hash != "$6$salt$hash" || sh.Expire != -1 {
		t.Fatalf("shadow %+v %v", sh, err)
	}
	if sh, _ := LookupShadow("guest"); sh == nil || sh.Expire != 19001 {
		t.Fatalf("shadow expiry %+v", sh)
	}
}

func TestFirstAdminPrimaryGroup(t *testing.T) {
	d := &DB{
		users:  ParsePasswd(strings.NewReader("svc:x:999:27::/:/bin/sh\nbob:x:1002:27::/home/bob:/bin/sh\n")),
		groups: ParseGroup(strings.NewReader("sudo:x:27:\n")),
	}
	u, g := d.FirstAdmin("wheel", "sudo")
	if u == nil || u.Name != "bob" || g != "sudo" {
		t.Fatalf("got %v %q", u, g)
	}
}

func TestNsenterArgv(t *testing.T) {
	acct := &Account{User: User{Name: "kushal", UID: 1000, GID: 1000}}
	ns := []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-C", "--"}
	got := NsenterArgv("", nil, SwitchSetpriv, []string{"systemctl", "reboot"})
	if !slices.Equal(got, append(slices.Clone(ns), "systemctl", "reboot")) {
		t.Fatalf("root: %q", got)
	}
	got = NsenterArgv("", acct, SwitchSetpriv, []string{"pamac", "checkupdates"})
	want := append(slices.Clone(ns), "setpriv", "--reuid=1000", "--regid=1000", "--init-groups", "--", "pamac", "checkupdates")
	if !slices.Equal(got, want) {
		t.Fatalf("setpriv: %q", got)
	}
	got = NsenterArgv("", acct, SwitchSu, []string{"docker", "compose", "version"})
	want = append(slices.Clone(ns), "su", "-s", "/bin/sh", "kushal", "-c", `exec "$0" "$@"`, "docker", "compose", "version")
	if !slices.Equal(got, want) {
		t.Fatalf("su: %q", got)
	}
	got = NsenterArgv("/srv/app dir", acct, SwitchSetpriv, []string{"docker", "compose", "pull"})
	want = append(slices.Clone(ns), "setpriv", "--reuid=1000", "--regid=1000", "--init-groups", "--",
		"/bin/sh", "-c", cdScript, "wargo-cd", "/srv/app dir", "docker", "compose", "pull")
	if !slices.Equal(got, want) {
		t.Fatalf("dir: %q", got)
	}
	// The cd wrapper really passes arguments verbatim (run it locally).
	dir := t.TempDir()
	out, err := exec.Command("/bin/sh", "-c", cdScript, "wargo-cd", dir, "/bin/sh", "-c", `pwd; printf '%s|' "$@"`, "x", "a b", "$(evil)").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != dir+"\na b|$(evil)|" {
		t.Fatalf("cd script output %q", out)
	}
	// So does the su script (run with sh instead of su).
	out, err = exec.Command("/bin/sh", "-c", `exec "$0" "$@"`, "printf", "%s|", "a b", "'q'").Output()
	if err != nil || string(out) != "a b|'q'|" {
		t.Fatalf("su script %q %v", out, err)
	}
	if got := LoginShellArgv(acct, "exec sudo -i"); !slices.Equal(got, append(slices.Clone(ns), "su", "-l", "kushal", "-c", "exec sudo -i")) {
		t.Fatalf("login: %q", got)
	}
}

func TestContainerCommandEnv(t *testing.T) {
	fakeRoot(t)
	if _, err := SetAdmin("kushal"); err != nil {
		t.Fatal(err)
	}
	cmd := UserCommand(context.Background(), "id")
	if cmd.Args[0] != "nsenter" || cmd.Args[len(cmd.Args)-1] != "id" {
		t.Fatalf("args %q", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "HOME=/home/kushal") || !slices.Contains(cmd.Env, "USER=kushal") {
		t.Fatalf("env %q", cmd.Env)
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "WARGO_") {
			t.Fatalf("container env leaked: %s", e)
		}
	}
	root := Command(context.Background(), "true")
	if !slices.Contains(root.Env, "HOME=/root") {
		t.Fatalf("root env %q", root.Env)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       "plain",
		"/usr/bin/x":  "/usr/bin/x",
		"":            "''",
		"a b":         "'a b'",
		"it's":        `'it'\''s'`,
		"$(rm -rf /)": "'$(rm -rf /)'",
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ShellJoin([]string{"tmux", "new-session", "-s", "a b"}); got != "tmux new-session -s 'a b'" {
		t.Fatal(got)
	}
}

func TestLookPathContainer(t *testing.T) {
	fakeRoot(t)
	if p, err := LookPath("tmux"); err != nil || p != "/usr/bin/tmux" {
		t.Fatalf("tmux: %q %v", p, err)
	}
	if _, err := LookPath("notexec"); err == nil {
		t.Fatal("non-executable must not be found")
	}
	if _, err := LookPath("missing"); err == nil {
		t.Fatal("missing must not be found")
	}
}

func TestAsUserContainerUnprivileged(t *testing.T) {
	fakeRoot(t)
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	// No admin configured: refuse.
	if err := AsUser(func() error { t.Fatal("fn must not run"); return nil }); err == nil {
		t.Fatal("expected error without an admin")
	}
	if _, err := SetAdmin("kushal"); err != nil {
		t.Fatal(err)
	}
	ran := false
	err := AsUser(func() error { ran = true; return nil })
	if !errors.Is(err, ErrNotRoot) || ran {
		t.Fatalf("non-root container mode must refuse: err=%v ran=%v", err, ran)
	}
	// AsRoot still runs fn (it never switches anything itself).
	if err := AsRoot(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// The thread identity is untouched.
	if getfsuid() != os.Getuid() {
		t.Fatal("fsuid changed")
	}
}

func TestAsUserRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	r := fakeRoot(t)
	if _, err := SetAdmin("kushal"); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(r, "secret")
	os.WriteFile(secret, []byte("x"), 0o600)
	err := AsUser(func() error {
		if getfsuid() != 1000 {
			t.Error("fsuid not switched")
		}
		_, err := os.ReadFile(secret)
		return err
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected permission error as user, got %v", err)
	}
	if getfsuid() != 0 {
		t.Fatal("identity not restored")
	}
}

func TestContainerIDFromMountinfo(t *testing.T) {
	id := strings.Repeat("ab", 32)
	mi := "123 99 0:50 / / rw - overlay overlay rw\n" +
		"456 123 259:2 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw - ext4 /dev/nvme0n1p2 rw\n"
	if got := containerIDFromMountinfo(strings.NewReader(mi)); got != id {
		t.Fatalf("got %q", got)
	}
	if got := containerIDFromMountinfo(strings.NewReader("1 2 3:4 / / rw - ext4 /dev/sda1 rw\n")); got != "" {
		t.Fatal("expected no id")
	}
}

func TestSetupEnv(t *testing.T) {
	r := fakeRoot(t)
	for _, k := range []string{"HOST_PROC", "HOST_SYS", "HOST_ETC", "HOST_VAR", "HOST_RUN", "HOST_DEV", "HOST_ROOT", "HOST_PROC_MOUNTINFO"} {
		t.Setenv(k, "")
	}
	SetupEnv()
	if os.Getenv("HOST_PROC") != r+"/proc" || os.Getenv("HOST_PROC_MOUNTINFO") != r+"/proc/1/mountinfo" || os.Getenv("HOST_ROOT") != r {
		t.Fatalf("env not set: %s %s", os.Getenv("HOST_PROC"), os.Getenv("HOST_PROC_MOUNTINFO"))
	}
}

func TestCheckFailsCleanly(t *testing.T) {
	defer SetRoot(filepath.Join(t.TempDir(), "missing"))()
	err := Check(context.Background())
	var ce *CheckError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("got %v", err)
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package fsops

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestPinParentRefusesSymlinkedComponent(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "root"), 0o755)
	os.MkdirAll(filepath.Join(tmp, "outside"), 0o755)
	os.WriteFile(filepath.Join(tmp, "outside", "f"), []byte("x"), 0o600)
	// A relative link that stays inside the root's parent: os.Root alone
	// would refuse ../outside, so put the link one level down as well.
	os.Symlink("../outside", filepath.Join(tmp, "root", "a"))
	os.MkdirAll(filepath.Join(tmp, "root", "in"), 0o755)
	os.WriteFile(filepath.Join(tmp, "root", "in", "f"), []byte("y"), 0o600)
	os.MkdirAll(filepath.Join(tmp, "root", "sub"), 0o755)
	os.Symlink("../in", filepath.Join(tmp, "root", "sub", "b"))
	root := openTestRoot(t, filepath.Join(tmp, "root"))
	for _, p := range []string{"a/f", "sub/b/f"} {
		parent, _, err := PinParent(root, p)
		if err == nil {
			parent.Close()
			t.Fatalf("PinParent(%q) followed a symlink", p)
		}
		if !errors.Is(err, ErrSymlinkInPath) {
			t.Fatalf("PinParent(%q): %v, want ErrSymlinkInPath", p, err)
		}
	}
	if st, _ := os.Stat(filepath.Join(tmp, "outside", "f")); st.Mode().Perm() != 0o600 {
		t.Fatal("outside/f changed")
	}
	if _, _, err := PinParent(root, "."); err == nil {
		t.Fatal(`PinParent(".") accepted`)
	}
}

func TestPinParentAcceptsPlainDirs(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "root", "x", "y"), 0o755)
	os.WriteFile(filepath.Join(tmp, "root", "x", "y", "f"), []byte("x"), 0o640)
	os.WriteFile(filepath.Join(tmp, "root", "top"), []byte("t"), 0o600)
	root := openTestRoot(t, filepath.Join(tmp, "root"))
	parent, base, err := PinParent(root, "x/y/f")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if base != "f" {
		t.Fatalf("base %q", base)
	}
	got, err := parent.Stat("f")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.Stat(filepath.Join(tmp, "root", "x", "y", "f"))
	if !os.SameFile(got, want) {
		t.Fatal("pinned parent is not x/y")
	}
	top, base, err := PinParent(root, "top")
	if err != nil {
		t.Fatal(err)
	}
	defer top.Close()
	if st, err := top.Stat(base); err != nil || st.Size() != 1 {
		t.Fatalf("top-level entry: %v %v", st, err)
	}
	if _, _, err := PinParent(root, "x/y/f/g"); err == nil {
		t.Fatal("a file used as a folder was accepted")
	}
}

func TestChmodNoFollowRefusesFinalSymlink(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "root"), 0o755)
	target := filepath.Join(tmp, "root", "target")
	os.WriteFile(target, []byte("x"), 0o600)
	os.Symlink("target", filepath.Join(tmp, "root", "link"))
	root := openTestRoot(t, filepath.Join(tmp, "root"))
	err := ChmodNoFollow(root, "link", 0o644)
	if !errors.Is(err, ErrSymlinkInPath) {
		t.Fatalf("ChmodNoFollow on a symlink: %v, want ErrSymlinkInPath", err)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o600 {
		t.Fatalf("target mode changed to %v", st.Mode().Perm())
	}
	if err := ChmodNoFollow(root, "target", 0o640|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o640 || st.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("plain chmod gave %v", st.Mode())
	}
}

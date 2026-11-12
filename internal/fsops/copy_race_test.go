// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package fsops

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// raceTree builds root/src/{f,sub/g} plus root/target (0600, "secret"),
// root/outside/ (empty) and root/secretdir/s ("secret"). Every symlink a
// test plants stays inside root, so os.Root alone would follow it.
func raceTree(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"src/sub", "outside", "secretdir"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.Chmod(filepath.Join(dir, "src"), 0o750)
	os.WriteFile(filepath.Join(dir, "src", "f"), []byte("data"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "sub", "g"), []byte("data"), 0o644)
	os.WriteFile(filepath.Join(dir, "target"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(dir, "secretdir", "s"), []byte("secret"), 0o600)
	return dir, openTestRoot(t, dir)
}

func setHook(t *testing.T, step, name string, fn func()) {
	t.Helper()
	copyHook = func(s, n string) {
		if s == step && n == name {
			fn()
		}
	}
	t.Cleanup(func() { copyHook = nil })
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func checkTarget(t *testing.T, dir string) {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, "target"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("target mode changed to %v", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "target")); string(b) != "secret" {
		t.Errorf("target contents changed to %q", b)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "outside")); len(ents) != 0 {
		t.Errorf("files created outside the destination: %v", ents)
	}
}

// After the top destination folder is created, it is swapped for a link
// to target: the final chmod must not land on target. The source folder
// is empty, so nothing else fails first.
func TestCopySwappedDestinationFolder(t *testing.T) {
	dir, root := raceTree(t)
	must(t, os.Mkdir(filepath.Join(dir, "empty"), 0o750))
	setHook(t, "mkdir", "empty", func() {
		must(t, os.Rename(filepath.Join(dir, "dst"), filepath.Join(dir, "dst.real")))
		must(t, os.Symlink("target", filepath.Join(dir, "dst")))
	})
	if err := Copy(root, "empty", "dst", nil); err == nil {
		t.Error("copy through a swapped destination folder succeeded")
	}
	checkTarget(t, dir)
}

// A destination subfolder swapped for a link to outside/ must not make the
// copy create files there.
func TestCopySwappedDestinationSubfolder(t *testing.T) {
	dir, root := raceTree(t)
	setHook(t, "mkdir", "src/sub", func() {
		must(t, os.Rename(filepath.Join(dir, "dst", "sub"), filepath.Join(dir, "sub.real")))
		must(t, os.Symlink("../outside", filepath.Join(dir, "dst", "sub")))
	})
	if err := Copy(root, "src", "dst", nil); err == nil {
		t.Error("copy through a swapped destination subfolder succeeded")
	}
	checkTarget(t, dir)
}

// A link planted where the destination file is about to be created must
// not be written through, and its target's times or mode must not change.
func TestCopyPlantedDestinationFile(t *testing.T) {
	dir, root := raceTree(t)
	before, _ := os.Stat(filepath.Join(dir, "target"))
	setHook(t, "lstat", "src/f", func() {
		must(t, os.Symlink("../target", filepath.Join(dir, "dst", "f")))
	})
	if err := Copy(root, "src", "dst", nil); err == nil {
		t.Error("copy over a planted destination link succeeded")
	}
	checkTarget(t, dir)
	if after, _ := os.Stat(filepath.Join(dir, "target")); !after.ModTime().Equal(before.ModTime()) {
		t.Error("target times changed")
	}
}

// A source file or folder swapped for a link after it was examined must
// not be read through.
func TestCopySwappedSource(t *testing.T) {
	for _, c := range []struct{ entry, link string }{
		{"src/f", "../target"},
		{"src/sub", "../secretdir"},
	} {
		t.Run(c.entry, func(t *testing.T) {
			dir, root := raceTree(t)
			setHook(t, "lstat", c.entry, func() {
				must(t, os.RemoveAll(filepath.Join(dir, c.entry)))
				must(t, os.Symlink(c.link, filepath.Join(dir, c.entry)))
			})
			if err := Copy(root, "src", "dst", nil); err == nil {
				t.Error("copy through a swapped source succeeded")
			}
			for _, p := range []string{"dst/f", "dst/sub/s"} {
				if b, err := os.ReadFile(filepath.Join(dir, p)); err == nil && string(b) == "secret" {
					t.Errorf("%s holds the secret", p)
				}
			}
			checkTarget(t, dir)
		})
	}
}

// Without interference the descriptor walk copies the whole tree with
// modes, times and symlinks intact.
func TestCopyTreeIntact(t *testing.T) {
	dir, root := raceTree(t)
	os.Symlink("f", filepath.Join(dir, "src", "l"))
	var last int64
	if err := Copy(root, "src", "dst", func(done int64, _ string) { last = done }); err != nil {
		t.Fatal(err)
	}
	if last != 8 {
		t.Errorf("progress reported %d bytes, want 8", last)
	}
	if st, _ := os.Stat(filepath.Join(dir, "dst")); st.Mode().Perm() != 0o750 {
		t.Errorf("dst mode %v", st.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "dst", "sub", "g")); string(b) != "data" {
		t.Errorf("dst/sub/g = %q", b)
	}
	if l, _ := os.Readlink(filepath.Join(dir, "dst", "l")); l != "f" {
		t.Errorf("dst/l -> %q", l)
	}
	s, _ := os.Stat(filepath.Join(dir, "src", "f"))
	d, _ := os.Stat(filepath.Join(dir, "dst", "f"))
	if !s.ModTime().Equal(d.ModTime()) {
		t.Errorf("mtime %v, want %v", d.ModTime(), s.ModTime())
	}
}

// A move removes only what it copied. Entries created in the source after
// their folder was read, replaced by a new file, or written to after they
// were copied all stay in the source and are reported; the rest goes.
func TestMoveKeepsWhatChangedDuringCopy(t *testing.T) {
	dir, root := raceTree(t)
	must(t, os.WriteFile(filepath.Join(dir, "src", "h"), []byte("data"), 0o644))
	// src/sub was read before its copy was made: this file is not copied.
	setHook(t, "mkdir", "src/sub", func() {
		must(t, os.WriteFile(filepath.Join(dir, "src", "sub", "new"), []byte("new"), 0o644))
	})
	c, err := CopyForMove(context.Background(), root, "src", root, "dst", nil)
	must(t, err)
	if _, err := os.Lstat(filepath.Join(dir, "dst", "sub", "new")); err == nil {
		t.Fatal("dst/sub/new was copied; the test does not exercise a late file")
	}
	// After it was copied, f is replaced by a new file (renamed over it, so
	// the old inode is still in use and cannot be reused) and h grows.
	must(t, os.WriteFile(filepath.Join(dir, "f.new"), []byte("DATA"), 0o644))
	must(t, os.Rename(filepath.Join(dir, "f.new"), filepath.Join(dir, "src", "f")))
	h, err := os.OpenFile(filepath.Join(dir, "src", "h"), os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	_, err = h.Write([]byte("more"))
	must(t, err)
	must(t, h.Close())

	changed, err := RemoveCopied(root, "src", c)
	must(t, err)
	sort.Strings(changed)
	if strings.Join(changed, ",") != "src/f,src/h,src/sub/new" {
		t.Errorf("changed = %v", changed)
	}
	for p, want := range map[string]string{"src/f": "DATA", "src/h": "datamore", "src/sub/new": "new"} {
		if b, err := os.ReadFile(filepath.Join(dir, p)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", p, b, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "src", "sub", "g")); err == nil {
		t.Error("unchanged src/sub/g was not removed")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "dst", "sub", "g")); string(b) != "data" {
		t.Errorf("dst/sub/g = %q", b)
	}
}

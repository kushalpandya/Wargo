// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"":            ".",
		"/":           ".",
		"a/b/../c":    "a/c",
		"../../etc":   "etc",
		"/abs/path":   "abs/path",
		`win\style\p`: "win/style/p",
		"./x/./y/":    "x/y",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfinement(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "f.txt"), []byte("hello"), 0o644)
	os.Symlink("/etc", filepath.Join(dir, "escape"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := root.Open("escape/passwd"); err == nil {
		t.Fatal("symlink escape must fail")
	}
	if err := Copy(root, "a", "a/inner", nil); err == nil {
		t.Fatal("copy into itself must fail")
	}
	if err := Copy(root, "a", "b", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := root.ReadFile("b/f.txt"); string(b) != "hello" {
		t.Fatalf("copied content %q", b)
	}
	if err := Move(root, "b", "c"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, "."); err == nil {
		t.Fatal("removing root must fail")
	}
	if err := Remove(root, "c"); err != nil {
		t.Fatal(err)
	}
	if n, c, _ := Size(root, "a"); n != 5 || c != 1 {
		t.Fatalf("size %d count %d", n, c)
	}
}

func TestParseMode(t *testing.T) {
	if m, err := ParseMode("755"); err != nil || m != 0o755 {
		t.Fatal(m, err)
	}
	for _, bad := range []string{"999", "abc", "77777"} {
		if _, err := ParseMode(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A move across devices copies what it can and removes only that from the
// source: special files (which are never copied) stay where they were,
// together with the folders holding them.
func TestMoveCrossDeviceKeepsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"src/sub", "src/empty"} {
		must(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	must(t, os.WriteFile(filepath.Join(dir, "src", "f"), []byte("data"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "src", "sub", "g"), []byte("data"), 0o644))
	if err := syscall.Mkfifo(filepath.Join(dir, "src", "sub", "p"), 0o644); err != nil {
		t.Skip(err)
	}
	root := openTestRoot(t, dir)
	old := renameBetween
	renameBetween = func(*os.Root, string, *os.Root, string) error {
		return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
	}
	t.Cleanup(func() { renameBetween = old })

	if err := Move(root, "src", "dst"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"dst/f", "dst/sub/g"} {
		if b, err := os.ReadFile(filepath.Join(dir, p)); err != nil || string(b) != "data" {
			t.Errorf("%s = %q, %v", p, b, err)
		}
	}
	if st, err := os.Lstat(filepath.Join(dir, "src", "sub", "p")); err != nil || st.Mode()&fs.ModeNamedPipe == 0 {
		t.Fatalf("FIFO not kept in the source: %v", err)
	}
	for _, p := range []string{"src/f", "src/sub/g", "src/empty", "dst/sub/p"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
			t.Errorf("%s still exists", p)
		}
	}

	// The skipped paths are reported.
	must(t, syscall.Mkfifo(filepath.Join(dir, "src", "q"), 0o644))
	skipped, err := CopyBetween(context.Background(), root, "src", root, "dst2", nil)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(skipped)
	if strings.Join(skipped, ",") != "src/q,src/sub/p" {
		t.Errorf("skipped = %v", skipped)
	}
}

// On a case-insensitive drive "A" and "a" name the same entry, so renaming
// one to the other must not fail as "already exists". Two hard links
// stand in for that on a case-sensitive drive; on a case-insensitive one
// (macOS by default) the link fails and the real case is tested.
func TestMoveCaseOnlyRenameOnSameInode(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "A"), []byte("data"), 0o644))
	caseInsensitive := false
	if err := os.Link(filepath.Join(dir, "A"), filepath.Join(dir, "a")); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			t.Fatal(err)
		}
		caseInsensitive = true
	}
	must(t, os.WriteFile(filepath.Join(dir, "B"), []byte("other"), 0o644))
	root := openTestRoot(t, dir)
	if err := Move(root, "A", "a"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a")); string(b) != "data" {
		t.Errorf("a = %q", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Errorf("entries left: %v", ents)
	}
	if caseInsensitive {
		if len(ents) == 2 && (ents[0].Name() != "B" || ents[1].Name() != "a") {
			t.Errorf("entries %q, %q; want B, a", ents[0].Name(), ents[1].Name())
		}
	} else if _, err := os.Lstat(filepath.Join(dir, "A")); err == nil {
		t.Error("A still exists")
	}
	// A different file at the destination is still refused.
	if err := Move(root, "B", "a"); !errors.Is(err, fs.ErrExist) {
		t.Errorf("move onto another file: %v", err)
	}
}

// Two names of one inode that are not case variants of each other are two
// hard links: moving one onto the other is a conflict, and both stay.
func TestMoveOntoHardLinkIsConflict(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a"), []byte("data"), 0o644))
	must(t, os.Link(filepath.Join(dir, "a"), filepath.Join(dir, "b")))
	root := openTestRoot(t, dir)
	if err := Move(root, "a", "b"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("move onto a hard link: %v, want ErrExist", err)
	}
	for _, n := range []string{"a", "b"} {
		if got, err := os.ReadFile(filepath.Join(dir, n)); err != nil || string(got) != "data" {
			t.Errorf("%s = %q, %v", n, got, err)
		}
	}
}

// The same name reached through two roots (two folders holding hard links
// under one name look exactly like this) is a conflict too: only case
// variants take the same-entry path.
func TestMoveSameNameThroughTwoRootsIsConflict(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "x"), []byte("data"), 0o644))
	a, b := openTestRoot(t, dir), openTestRoot(t, dir)
	if err := MoveBetween(a, "x", b, "x"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("got %v, want ErrExist", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "x")); string(got) != "data" {
		t.Errorf("x = %q", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("entries left: %v", ents)
	}
}

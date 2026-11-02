// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"os"
	"path/filepath"
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

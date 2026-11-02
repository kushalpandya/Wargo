// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameInOut(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "root")
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.Symlink("/etc", filepath.Join(dir, "esc"))
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	src := filepath.Join(base, "upload.part")
	os.WriteFile(src, []byte("x"), 0o600)
	if err := RenameIn(root, src, "sub/f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub/f.txt")); err != nil {
		t.Fatal("not moved in")
	}
	// The parent is resolved inside the root: an escaping link is refused.
	os.WriteFile(src, []byte("y"), 0o600)
	if err := RenameIn(root, src, "esc/wargo-test"); err == nil {
		t.Fatal("renamed through an escaping symlink")
	}
	// Moving a symlink out moves the link itself, not its target.
	trash := filepath.Join(base, "trash")
	os.Mkdir(trash, 0o700)
	if err := RenameOut(root, "esc", filepath.Join(trash, "esc")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(trash, "esc")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link not moved as a link")
	}
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Fatal("target affected")
	}
	if err := RenameOut(root, ".", filepath.Join(trash, "x")); err == nil {
		t.Fatal("moved the root")
	}
}

func TestParseModeSpecialBits(t *testing.T) {
	m, err := ParseMode("4755")
	if err != nil || m&os.ModeSetuid == 0 || m.Perm() != 0o755 {
		t.Fatalf("4755 → %v, %v", m, err)
	}
	m, _ = ParseMode("1777")
	if m&os.ModeSticky == 0 {
		t.Fatalf("1777 → %v", m)
	}
}

func TestCopyContextCancel(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), make([]byte, 1<<20), 0o644)
	root, _ := os.OpenRoot(dir)
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CopyContext(ctx, root, "a", "b", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := Copy(root, "a", "a/b", nil); !errors.Is(err, ErrIntoSelf) {
		t.Fatalf("into self: %v", err)
	}
}

func TestBetweenRoots(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	os.MkdirAll(filepath.Join(a, "d/e"), 0o755)
	os.WriteFile(filepath.Join(a, "d/e/f"), []byte("data"), 0o640)
	os.Symlink("/etc/passwd", filepath.Join(a, "d/link"))
	os.MkdirAll(b, 0o755)
	os.Symlink("/etc", filepath.Join(b, "esc"))
	ra, _ := os.OpenRoot(a)
	rb, _ := os.OpenRoot(b)
	defer ra.Close()
	defer rb.Close()

	if err := CopyBetween(context.Background(), ra, "d", rb, "d2", nil); err != nil {
		t.Fatal(err)
	}
	if s, _ := os.ReadFile(filepath.Join(b, "d2/e/f")); string(s) != "data" {
		t.Fatal("copy content")
	}
	if l, _ := os.Readlink(filepath.Join(b, "d2/link")); l != "/etc/passwd" {
		t.Fatal("symlink not copied as a link")
	}
	if err := CopyBetween(context.Background(), ra, "d", rb, "d2", nil); !errors.Is(err, os.ErrExist) {
		t.Fatal("expected exists", err)
	}
	// The destination side stays confined.
	if err := CopyBetween(context.Background(), ra, "d/e/f", rb, "esc/x", nil); err == nil {
		t.Fatal("escaped through a symlinked destination")
	}
	if err := CopyBetween(context.Background(), ra, "d/e/f", rb, "../x", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b, "x")); err != nil {
		t.Fatal(`".." was not kept inside the root`)
	}
	if err := RenameBetween(ra, "d/e/f", rb, "moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b, "moved")); err != nil {
		t.Fatal("rename between roots")
	}
	if err := RenameBetween(ra, "d/e", rb, "esc/e"); err == nil {
		t.Fatal("renamed through a symlink")
	}
	p, err := RealPath(rb, "moved")
	if err != nil || p != filepath.Join(b, "moved") {
		t.Fatal(p, err)
	}
	if p, _ := RealPath(rb, "."); p != b {
		t.Fatal(p)
	}
	// Copying a folder into itself through overlapping roots stops instead
	// of recursing forever.
	inner, _ := os.OpenRoot(filepath.Join(a, "d"))
	defer inner.Close()
	if err := CopyBetween(context.Background(), ra, "d", inner, "e/copy", nil); !errors.Is(err, ErrIntoSelf) {
		t.Fatal("expected into-self", err)
	}
}

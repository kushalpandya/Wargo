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

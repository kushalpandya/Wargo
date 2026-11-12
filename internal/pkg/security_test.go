// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPrepareCheckDB(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db")
	if err := prepareCheckDB(db); err != nil {
		t.Fatalf("fresh dir: %v", err)
	}
	if err := prepareCheckDB(db); err != nil {
		t.Fatalf("existing private dir: %v", err)
	}
	// A pre-created symlink (another user's trap) is refused.
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareCheckDB(link); err == nil {
		t.Fatal("symlink accepted")
	}
	// So is a group/world-writable directory.
	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	os.Chmod(open, 0o777)
	if err := prepareCheckDB(open); err == nil {
		t.Fatal("world-writable dir accepted")
	}
	// And a plain file.
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	if err := prepareCheckDB(file); err == nil {
		t.Fatal("file accepted")
	}
}

func TestEnrichPacmanSkipsOptionLikeNames(t *testing.T) {
	var got []string
	run := func(ctx context.Context, argv ...string) ([]byte, []byte, int, error) {
		got = argv
		return nil, nil, 0, nil
	}
	enrichPacman(context.Background(), run, []Update{{Name: "linux"}, {Name: "--dbpath=/tmp/evil"}, {Name: "-Syu"}}, "")
	i := slices.Index(got, "--")
	if i < 0 || !slices.Equal(got[i+1:], []string{"linux"}) {
		t.Fatalf("argv = %q", got)
	}
	got = nil
	enrichPacman(context.Background(), run, []Update{{Name: "-x"}}, "")
	if got != nil {
		t.Fatalf("ran with no valid names: %q", got)
	}
}

func TestTailLinesRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	os.WriteFile(secret, []byte("root:hash\n"), 0o600)
	log := filepath.Join(dir, "x.log")
	if err := os.Symlink(secret, log); err != nil {
		t.Fatal(err)
	}
	if lines, err := TailLines(log, 1<<20); err == nil {
		t.Fatalf("followed symlink: %q", lines)
	}
}

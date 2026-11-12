// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux || darwin

package fsops_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/helper"
)

// An elevated `fs move` across devices goes through the root helper's
// copy-then-delete path: a source file changed while the copy runs stays
// in the source, and the unchanged files move.
func TestHelperCrossDeviceMoveKeepsChangedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, data := range map[string]string{"src/f": "data", "src/sub/g": "data", "src/sub/k": "data"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(fsops.ForceCrossDevice())
	hooked := false
	t.Cleanup(fsops.SetCopyHook(func(step, name string) {
		if step == "lstat" && name == "src/sub/g" && !hooked {
			hooked = true
			f, err := os.OpenFile(filepath.Join(dir, "src", "sub", "g"), os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Error(err)
				return
			}
			f.Write([]byte("more"))
			f.Close()
		}
	}))

	code, err := helper.Exec([]string{"fs", "move", dir, "src", "dst"}, nil, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("helper fs move: code %d, %v", code, err)
	}
	if !hooked {
		t.Fatal("the copy never examined src/sub/g; the test does not change a file mid-copy")
	}
	if b, err := os.ReadFile(filepath.Join(dir, "src", "sub", "g")); err != nil || string(b) != "datamore" {
		t.Errorf("changed src/sub/g = %q, %v; want it kept in the source", b, err)
	}
	for _, p := range []string{"dst/f", "dst/sub/k"} {
		if b, err := os.ReadFile(filepath.Join(dir, p)); err != nil || string(b) != "data" {
			t.Errorf("%s = %q, %v", p, b, err)
		}
	}
	for _, p := range []string{"src/f", "src/sub/k"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
			t.Errorf("unchanged %s was not moved", p)
		}
	}
}

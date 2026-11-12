// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package files

import (
	"path/filepath"
	"testing"
)

// withMounts fakes the mount table with dirs as ext4 drives. Tests can't
// mount anything, and getfsstat only offers / and /Volumes/*.
func withMounts(t *testing.T, dirs ...string) {
	t.Helper()
	old := detectDrives
	detectDrives = func() []Drive {
		out := []Drive{}
		for i, d := range dirs {
			out = append(out, Drive{ID: mountID(d), Label: filepath.Base(d), Path: d, Device: "/dev/null" + string(rune('a'+i)), FSType: "ext4"})
		}
		return out
	}
	t.Cleanup(func() { detectDrives = old })
}

func TestSealedRootNotReadOnly(t *testing.T) {
	m := map[string]any{}
	statInto(m, "/")
	if m["readOnly"] == true {
		t.Fatalf("/ reported read-only on macOS: %v", m)
	}
}

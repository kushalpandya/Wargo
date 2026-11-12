// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withMounts points detection at a fake mountinfo listing dirs as ext4 mounts.
func withMounts(t *testing.T, dirs ...string) {
	t.Helper()
	var b strings.Builder
	for i, d := range dirs {
		b.WriteString("1 0 8:" + string(rune('1'+i)) + " / " + strings.ReplaceAll(d, " ", `\040`) + " rw - ext4 /dev/null" + string(rune('a'+i)) + " rw\n")
	}
	f := filepath.Join(t.TempDir(), "mountinfo")
	must(t, os.WriteFile(f, []byte(b.String()), 0o644))
	old, oldL, oldS := mountinfoPath, byLabelDir, sysClassBlock
	mountinfoPath, byLabelDir, sysClassBlock = f, t.TempDir(), t.TempDir()
	t.Cleanup(func() { mountinfoPath, byLabelDir, sysClassBlock = old, oldL, oldS })
}

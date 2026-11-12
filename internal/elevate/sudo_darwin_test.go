// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package elevate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSudoIsFixedPathDarwin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if got := sudoBin(); got != "/usr/bin/sudo" {
		t.Fatalf("sudoBin = %q", got)
	}
	if got, _ := lookSudo(); got != "/usr/bin/sudo" {
		t.Fatalf("lookSudo = %q", got)
	}
}

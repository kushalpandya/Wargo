// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package term

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kushalpandya/wargo/internal/host"
)

func TestToolsSudoFixedPathDarwin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if tl := lookTools(host.LookPath); tl.Sudo != "/usr/bin/sudo" {
		t.Fatalf("Sudo = %q", tl.Sudo)
	}
}

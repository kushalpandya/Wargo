// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestConfigFileHostile(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.xml")
	os.WriteFile(secret, []byte("<Config><ApiKey>leakedkey0123456789</ApiKey></Config>"), 0o600)

	// A symlinked config.xml (the container controls the folder) is refused.
	os.Symlink(secret, filepath.Join(dir, "config.xml"))
	if k, err := readAPIKeyFile(filepath.Join(dir, "config.xml")); err == nil {
		t.Fatalf("followed symlink: %q", k)
	}
	if readable(filepath.Join(dir, "config.xml")) {
		t.Fatal("symlink reported readable")
	}

	// A FIFO neither blocks nor is accepted.
	fifo := filepath.Join(t.TempDir(), "config.xml")
	if err := syscall.Mkfifo(fifo, 0o644); err == nil {
		done := make(chan error, 1)
		go func() { _, err := readAPIKeyFile(fifo); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("FIFO accepted")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("blocked on FIFO")
		}
	}

	// A regular file works.
	ok := filepath.Join(t.TempDir(), "config.xml")
	os.WriteFile(ok, []byte("<Config><ApiKey>goodkey0123456789</ApiKey></Config>"), 0o600)
	if k, err := readAPIKeyFile(ok); err != nil || k != "goodkey0123456789" {
		t.Fatal(k, err)
	}
}

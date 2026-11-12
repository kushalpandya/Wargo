// SPDX-License-Identifier: AGPL-3.0-only

package privd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdRunSymlinkInWritableDirRefused(t *testing.T) {
	root := t.TempDir()
	real, links := filepath.Join(root, "real"), filepath.Join(root, "links")
	for _, d := range []string{real, links} {
		os.Mkdir(d, 0o755)
	}
	os.WriteFile(real+"/pacman", []byte("x"), 0o755)
	if err := os.Symlink("../real/pacman", links+"/pacman"); err != nil {
		t.Fatal(err)
	}
	os.Chmod(links, 0o777) // the Wargo user could swap the link by rename
	oldDirs, oldReq, oldUID, oldStop := systemBinDirs, requireRootOwner, rootUID, trustStop
	systemBinDirs, requireRootOwner, rootUID, trustStop = []string{real, links}, true, uint32(os.Getuid()), root
	t.Cleanup(func() { systemBinDirs, requireRootOwner, rootUID, trustStop = oldDirs, oldReq, oldUID, oldStop })
	c := Config{WargoBinary: "/usr/local/bin/wargo", SystemdRun: "/usr/bin/systemd-run", Systemctl: "/usr/bin/systemctl"}
	run := func(cmd string) error {
		_, err := c.checkArgv([]string{"systemd-run", "--unit=wargo-upgrade-1", "--", cmd, "-Syu"})
		return err
	}
	if err := run(real + "/pacman"); err != nil {
		t.Fatalf("trusted binary rejected: %v", err)
	}
	if err := run(links + "/pacman"); err == nil || !strings.Contains(err.Error(), links+" is writable") {
		t.Fatalf("symlink in writable dir accepted: %v", err)
	}
}

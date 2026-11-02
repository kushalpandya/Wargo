// SPDX-License-Identifier: AGPL-3.0-only

package helper

import "testing"

func TestResolvePackageManager(t *testing.T) {
	for _, bad := range []string{"sh", "/bin/sh", "/tmp/pacman", "../pacman", "bash"} {
		if _, err := ResolvePackageManager(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// pacman or apt-get exists on most dev hosts; skip when neither does.
	for _, pm := range []string{"pacman", "apt-get", "dnf"} {
		if p, err := ResolvePackageManager(pm); err == nil {
			if p[0] != '/' {
				t.Errorf("not absolute: %s", p)
			}
			return
		}
	}
	t.Skip("no package manager on this host")
}

func TestSignalNames(t *testing.T) {
	if _, ok := Signal("TERM"); !ok {
		t.Fatal("TERM")
	}
	if _, ok := Signal("SEGV"); ok {
		t.Fatal("unexpected signal allowed")
	}
}

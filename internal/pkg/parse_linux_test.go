// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestCheckLocks(t *testing.T) {
	dir := t.TempDir()
	if st := checkLocks([]string{filepath.Join(dir, "db.lck")}); st.Locked {
		t.Error("absent lock reported")
	}
	os.WriteFile(filepath.Join(dir, "db.lck"), nil, 0o644)
	if st := checkLocks([]string{filepath.Join(dir, "db.lck")}); !st.Locked {
		t.Error("pacman lock not detected")
	}
	pid := filepath.Join(dir, "dnf.pid")
	os.WriteFile(pid, []byte("999999999\n"), 0o644)
	if st := checkLocks([]string{pid}); st.Locked {
		t.Error("stale pid file reported as lock")
	}
	os.WriteFile(pid, []byte(strconv.Itoa(os.Getpid())), 0o644)
	if st := checkLocks([]string{pid}); !st.Locked {
		t.Error("live pid not detected")
	}
	// An always-present lock file without an active lock is free.
	front := filepath.Join(dir, "lock-frontend")
	os.WriteFile(front, nil, 0o640)
	if st := checkLocks([]string{front}); st.Locked {
		t.Error("unlocked dpkg lock reported")
	}
	f, _ := os.Open(front)
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if st := checkLocks([]string{front}); !st.Locked {
		t.Error("held flock not detected via /proc/locks")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package libproc

import (
	"os"
	"syscall"
	"testing"
)

// Runs with and without cgo: the sysctl paths every build has.
func TestGetAndStartTimeAnyBuild(t *testing.T) {
	me, err := Get(os.Getpid())
	if err != nil || me.PID != os.Getpid() || me.UID != os.Getuid() || me.PPID != os.Getppid() {
		t.Fatalf("%+v %v", me, err)
	}
	if st, err := StartTime(os.Getpid()); err != nil || st != me.StartUs || st == 0 {
		t.Fatalf("StartTime %d (%v) != %d", st, err, me.StartUs)
	}
	if _, err := StartTime(-1); err != syscall.ESRCH {
		t.Fatalf("pid -1: %v", err)
	}
	if pids, err := PIDs(); err != nil || len(pids) < 2 {
		t.Fatalf("%d pids %v", len(pids), err)
	}
	if ArgMax() < 4096 {
		t.Fatal("argmax")
	}
}

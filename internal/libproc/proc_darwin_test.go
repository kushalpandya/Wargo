// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin && cgo

package libproc

import (
	"os"
	"strings"
	"testing"
)

func TestGetSelfAndLaunchd(t *testing.T) {
	me, err := Get(os.Getpid())
	if err != nil || !me.Task || me.RSS == 0 || me.UID != os.Getuid() || me.Threads < 1 {
		t.Fatalf("%+v %v", me, err)
	}
	st, _ := StartTime(os.Getpid())
	if st != me.StartUs || st == 0 {
		t.Fatalf("StartTime %d != %d", st, me.StartUs)
	}
	l, err := Get(1)
	if err != nil || l.Name != "launchd" || l.UID != 0 {
		t.Fatalf("pid 1: %+v %v", l, err)
	}
	args, env, err := Args(os.Getpid(), make([]byte, ArgMax()))
	if err != nil || len(args) == 0 || env == 0 {
		t.Fatalf("args %q env %d %v", args, env, err)
	}
	if p, _ := Path(os.Getpid()); !strings.HasSuffix(p, ".test") {
		t.Fatal(p)
	}
	if wd, _ := os.Getwd(); func() string { c, _ := Cwd(os.Getpid()); return c }() != wd {
		t.Fatal("cwd")
	}
}

func TestPIDsAndCounters(t *testing.T) {
	pids, err := PIDs()
	if err != nil || len(pids) < 2 {
		t.Fatalf("%d pids %v", len(pids), err)
	}
	found := false
	for _, p := range pids {
		if p == os.Getpid() {
			found = true
		}
	}
	if !found {
		t.Fatal("own pid missing from PIDs")
	}
	if n, err := FDCount(os.Getpid()); err != nil || n < 3 {
		t.Fatalf("fds %d %v", n, err)
	}
	if _, _, err := DiskIO(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if me, _ := Get(os.Getpid()); me.CPUNs == 0 || me.PPID != os.Getppid() || me.State == "?" {
		t.Fatalf("%+v", me)
	}
	if _, err := Get(-5); err == nil {
		t.Fatal("bad pid accepted")
	}
}

func TestFDCountErrors(t *testing.T) {
	if os.Getuid() != 0 {
		if n, err := FDCount(1); err == nil {
			t.Fatalf("FDCount(1) as non-root: %d, nil", n)
		}
	}
	if n, err := FDCount(999999); err == nil {
		t.Fatalf("FDCount(999999): %d, nil", n)
	}
}

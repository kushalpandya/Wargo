// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package procs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeProc(t *testing.T, root, pid, stat, cmdline string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
	os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644)
}

func TestScanCPU(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "meminfo"), []byte("MemTotal:       1000 kB\nMemFree: 1 kB\n"), 0o644)
	os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 1 2 3\nbtime 1700000000\n"), 0o644)
	writeProc(t, root, "10", "10 (worker) R 1 0 0 0 0 0 0 0 0 0 100 0 0 0 20 0 3 0 200 4096 25 0", "worker\x00-v\x00")
	writeProc(t, root, "11", "11 (kworker/0:1) I 2 0 0 0 0 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0", "")

	s := New(nil, nil, nil, nil)
	s.root = root
	s.pageSize = 4096
	first := s.scan()
	if len(first.Procs) != 2 {
		t.Fatalf("procs = %d", len(first.Procs))
	}
	if first.TotalMem != 1000*1024 {
		t.Fatalf("totalMem = %d", first.TotalMem)
	}
	// Pretend one second passed and the worker used 50 ticks (0.5 s).
	s.prevAt = s.prevAt.Add(-time.Second)
	writeProc(t, root, "10", "10 (worker) R 1 0 0 0 0 0 0 0 0 0 130 20 0 0 20 0 3 0 200 4096 25 0", "worker\x00-v\x00")
	second := s.scan()
	var w, k Proc
	for _, p := range second.Procs {
		switch p.PID {
		case 10:
			w = p
		case 11:
			k = p
		}
	}
	if w.CPU < 45 || w.CPU > 55 {
		t.Fatalf("cpu = %.1f, want ~50", w.CPU)
	}
	if w.Command != "worker -v" || w.Threads != 3 || w.MemRSS != 25*4096 {
		t.Fatalf("worker = %+v", w)
	}
	if w.StartTime != 1700000000*1000+2000 {
		t.Fatalf("startTime = %d", w.StartTime)
	}
	if !k.Kernel {
		t.Fatalf("kworker should be a kernel thread: %+v", k)
	}
}

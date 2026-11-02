// SPDX-License-Identifier: AGPL-3.0-only

package procs

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func startSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip("no sleep binary:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

func TestSignalVerifiedRefusesWhenVerifyFails(t *testing.T) {
	cmd := startSleep(t)
	err := signalVerified(cmd.Process.Pid, syscall.SIGTERM, func() error { return syscall.EPERM })
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("err = %v", err)
	}
	// Still alive: no signal was sent.
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("process was signalled: %v", err)
	}
}

func TestSignalVerifiedSignals(t *testing.T) {
	cmd := startSleep(t)
	if err := signalVerified(cmd.Process.Pid, syscall.SIGTERM, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
}

func TestSignalVerifiedExitedProcess(t *testing.T) {
	if err := signalVerified(1<<22+12345, syscall.SIGTERM, func() error { return nil }); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("err = %v, want ESRCH", err)
	}
}

// verifyTarget rejects a pid whose process changed since the sample (pid
// reuse) so the signal can't hit an unrelated process.
func TestVerifyTargetDetectsPidReuse(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, "10", "10 (worker) R 1 0 0 0 0 0 0 0 0 0 100 0 0 0 20 0 3 0 200 4096 25 0", "worker\x00")
	s := New(nil, nil, nil, nil)
	s.root = root
	if err := s.verifyTarget(Proc{PID: 10, startTicks: 200}); err != nil {
		t.Fatalf("same process rejected: %v", err)
	}
	if err := s.verifyTarget(Proc{PID: 10, startTicks: 199}); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("reused pid accepted: %v", err)
	}
	if err := s.verifyTarget(Proc{PID: 11, startTicks: 200}); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("missing pid accepted: %v", err)
	}
}

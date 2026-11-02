// SPDX-License-Identifier: AGPL-3.0-only

package host

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
	err := SignalVerified(cmd.Process.Pid, syscall.SIGTERM, func() error { return syscall.EPERM })
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
	if err := SignalVerified(cmd.Process.Pid, syscall.SIGTERM, func() error { return nil }); err != nil {
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
	if err := SignalVerified(1<<22+12345, syscall.SIGTERM, func() error { return nil }); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("err = %v, want ESRCH", err)
	}
}

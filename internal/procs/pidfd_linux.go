// SPDX-License-Identifier: AGPL-3.0-only

package procs

import (
	"errors"
	"syscall"
)

// Syscall numbers are the same on every Linux architecture (they were
// added after the syscall tables were unified in 5.1).
const (
	sysPidfdSendSignal = 424
	sysPidfdOpen       = 434
)

func pidfdOpen(pid int) (int, error) {
	fd, _, e := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	if e != 0 {
		return -1, e
	}
	return int(fd), nil
}

func pidfdSendSignal(fd int, sig syscall.Signal) error {
	_, _, e := syscall.Syscall6(sysPidfdSendSignal, uintptr(fd), uintptr(sig), 0, 0, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

// signalVerified sends sig to pid after verify approves the process
// currently using that pid. With a pidfd the check and the signal refer to
// the same process even if it exits and the pid is reused in between: the
// pidfd is pinned to the process that held the pid when it was opened, and
// signalling an exited process fails with ESRCH instead of hitting the new
// owner of the pid. Kernels without pidfds (< 5.3) fall back to kill(2).
func signalVerified(pid int, sig syscall.Signal, verify func() error) error {
	fd, err := pidfdOpen(pid)
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EPERM) {
		// ENOSYS: old kernel; EPERM: seccomp profiles that deny it.
		if err := verify(); err != nil {
			return err
		}
		return syscall.Kill(pid, sig)
	}
	if err != nil {
		return err // ESRCH: gone
	}
	defer syscall.Close(fd)
	// /proc/<pid> now describes the pidfd's process, or a newer one if it
	// already exited — in which case the signal below fails with ESRCH.
	if err := verify(); err != nil {
		return err
	}
	return pidfdSendSignal(fd, sig)
}

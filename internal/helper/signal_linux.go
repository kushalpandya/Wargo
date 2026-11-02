// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Same numbers on every Linux architecture (post-5.1 unified table).
const (
	sysPidfdSendSignal = 424
	sysPidfdOpen       = 434
)

// procStartTime reads field 22 (starttime, clock ticks since boot) of
// /proc/<pid>/stat.
func procStartTime(pid int) (uint64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// comm (field 2) may contain spaces and parentheses: split after the
	// last ')'. Fields from there start at 3 (state).
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return 0, errors.New("malformed stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return 0, errors.New("malformed stat")
	}
	return strconv.ParseUint(f[19], 10, 64)
}

// signalPID sends sig to pid. With wantStart > 0 it first verifies that
// the process holding pid started at wantStart (pid-reuse protection),
// using a pidfd so that the check and the signal refer to the same
// process; kernels without pidfds fall back to check + kill(2).
func signalPID(pid int, sig syscall.Signal, wantStart uint64) error {
	verify := func() error {
		if wantStart == 0 {
			return nil
		}
		got, err := procStartTime(pid)
		if err != nil || got != wantStart {
			return fmt.Errorf("process %d is no longer the one you selected: %w", pid, syscall.ESRCH)
		}
		return nil
	}
	r, _, e := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	if e == syscall.ENOSYS || e == syscall.EPERM {
		if err := verify(); err != nil {
			return err
		}
		return syscall.Kill(pid, sig)
	}
	if e != 0 {
		return e
	}
	fd := int(r)
	defer syscall.Close(fd)
	// /proc/<pid> now shows the pidfd's process or, if it already exited,
	// a newer one — then the signal below fails with ESRCH.
	if err := verify(); err != nil {
		return err
	}
	if _, _, e := syscall.Syscall6(sysPidfdSendSignal, uintptr(fd), uintptr(sig), 0, 0, 0, 0); e != 0 {
		return e
	}
	return nil
}

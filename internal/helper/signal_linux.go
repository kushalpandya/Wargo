// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/kushalpandya/wargo/internal/host"
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
	return host.SignalVerified(pid, sig, verify)
}

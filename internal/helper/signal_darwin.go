// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"fmt"
	"syscall"

	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/libproc"
)

// procStartTime returns the process start time in microseconds since the
// epoch (sysctl kern.proc.pid p_starttime).
func procStartTime(pid int) (uint64, error) { return libproc.StartTime(pid) }

// signalPID sends sig to pid. With wantStart > 0 it first verifies that
// the process holding pid started at wantStart (pid-reuse protection).
// macOS has no pidfd, so the check and kill(2) are separate calls and can
// race by microseconds if the process exits and its pid is reused between
// them.
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

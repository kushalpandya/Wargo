// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package host

import "syscall"

// SignalVerified sends sig to pid after verify approves it (no pidfds here).
func SignalVerified(pid int, sig syscall.Signal, verify func() error) error {
	if err := verify(); err != nil {
		return err
	}
	return syscall.Kill(pid, sig)
}

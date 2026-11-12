// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !darwin

package helper

import (
	"errors"
	"syscall"
)

func signalPID(pid int, sig syscall.Signal, wantStart uint64) error {
	if wantStart != 0 {
		return errors.New("start-time verification is only supported on Linux")
	}
	return syscall.Kill(pid, sig)
}

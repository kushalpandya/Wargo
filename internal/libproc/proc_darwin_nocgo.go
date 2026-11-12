// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin && !cgo

package libproc

import (
	"errors"

	"golang.org/x/sys/unix"
)

// PIDs lists every process from sysctl kern.proc.all.
func PIDs() ([]int, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(ks))
	for _, k := range ks {
		pids = append(pids, int(k.Proc.P_pid))
	}
	return pids, nil
}

// Get returns the kinfo_proc fields only (Task is false).
func Get(pid int) (Info, error) { return kinfoGet(pid) }

func Args(pid int, buf []byte) ([]string, int, error) { return nil, 0, errors.ErrUnsupported }
func Path(pid int) (string, error)                    { return "", errors.ErrUnsupported }
func Cwd(pid int) (string, error)                     { return "", errors.ErrUnsupported }
func FDCount(pid int) (int, error)                    { return 0, errors.ErrUnsupported }
func DiskIO(pid int) (uint64, uint64, error)          { return 0, 0, errors.ErrUnsupported }

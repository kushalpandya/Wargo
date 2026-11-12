// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package libproc

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// StartTime returns the process start time in microseconds since the
// epoch, read with sysctl kern.proc.pid. It needs no cgo and no privilege,
// so the root helper can use it for its pid-reuse check.
func StartTime(pid int) (uint64, error) {
	k, err := kinfo(pid)
	if err != nil {
		return 0, err
	}
	return tvUs(k.Proc.P_starttime), nil
}

// kinfo reads one kinfo_proc. sysctl returns no data (which x/sys reports
// as EIO) for a pid that does not exist: that is ESRCH.
func kinfo(pid int) (*unix.KinfoProc, error) {
	if pid < 0 {
		return nil, syscall.ESRCH
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.EIO) || (err == nil && int(k.Proc.P_pid) != pid) {
		return nil, syscall.ESRCH
	}
	return k, err
}

func tvUs(tv unix.Timeval) uint64 {
	return uint64(tv.Sec)*1e6 + uint64(tv.Usec)
}

// ArgMax returns kern.argmax, the largest buffer Args can need.
func ArgMax() int {
	n, err := unix.SysctlUint32("kern.argmax")
	if err != nil || n == 0 {
		return 1 << 20
	}
	return int(n)
}

// kinfoGet builds Info from kinfo_proc alone (Task false). sysctl answers
// for every process without privilege; Name is p_comm, at most 16 bytes.
func kinfoGet(pid int) (Info, error) {
	k, err := kinfo(pid)
	if err != nil {
		return Info{}, err
	}
	return Info{
		PID:     int(k.Proc.P_pid),
		PPID:    int(k.Eproc.Ppid),
		UID:     int(k.Eproc.Ucred.Uid),
		RUID:    int(k.Eproc.Pcred.P_ruid),
		Nice:    int(k.Proc.P_nice),
		State:   StateLetter(uint32(k.Proc.P_stat)),
		Name:    unix.ByteSliceToString(k.Proc.P_comm[:]),
		StartUs: tvUs(k.Proc.P_starttime),
	}, nil
}

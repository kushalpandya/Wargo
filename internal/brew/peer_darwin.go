// SPDX-License-Identifier: AGPL-3.0-only

package brew

import "golang.org/x/sys/unix"

// peerPID is the process at the other end of a unix socket.
func peerPID(fd int) (int, error) { return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID) }

// parentPID is pid's parent process.
func parentPID(pid int) (int, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return int(k.Eproc.Ppid), nil
}

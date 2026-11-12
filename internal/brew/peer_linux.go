// SPDX-License-Identifier: AGPL-3.0-only

package brew

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// peerPID is the process at the other end of a unix socket.
func peerPID(fd int) (int, error) {
	c, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(c.Pid), nil
}

// parentPID is pid's parent process (field 4 of /proc/<pid>/stat).
func parentPID(pid int) (int, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, errors.New("bad stat")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, errors.New("bad stat")
	}
	return strconv.Atoi(f[1])
}

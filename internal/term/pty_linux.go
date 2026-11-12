// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package term

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"github.com/kushalpandya/wargo/internal/host"
)

// ptyPaths returns the ptmx device and the devpts directory to use. In
// container mode they point into the host's devpts instance, so the shell
// (which runs in the host's mount namespace) sees its /dev/pts/N.
func ptyPaths() (ptmx, ptsDir string) {
	if host.Container() {
		return host.Path("/dev/pts/ptmx"), host.Path("/dev/pts")
	}
	return "/dev/ptmx", "/dev/pts"
}

var warnHostPTY sync.Once

// openPTY returns the master (non-blocking, poller-backed) and slave ends.
// The slave is opened from the same devpts instance as the master.
func openPTY() (master, slave *os.File, err error) {
	ptmx, ptsDir := ptyPaths()
	master, err = os.OpenFile(ptmx, os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil && ptmx != "/dev/ptmx" {
		warnHostPTY.Do(func() {
			slog.Warn("term: cannot open the host's devpts, falling back to the container's", "err", err)
		})
		ptmx, ptsDir = "/dev/ptmx", "/dev/pts"
		master, err = os.OpenFile(ptmx, os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if err = ioctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	var n uint32
	if err = ioctl(master, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}
	slave, err = os.OpenFile(ptsDir+"/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

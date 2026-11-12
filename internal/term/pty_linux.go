// SPDX-License-Identifier: AGPL-3.0-only

package term

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"github.com/kushalpandya/wargo/internal/host"
)

// A tiny Linux PTY implementation (the parts of creack/pty we need) so the
// feature carries no extra module dependency.

type winsize struct {
	Rows, Cols, X, Y uint16
}

func ioctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

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

func setSize(master *os.File, cols, rows int) error {
	ws := winsize{Rows: uint16(rows), Cols: uint16(cols)}
	return ioctl(master, syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

// startPTY starts cmd attached to a fresh PTY as session leader with the
// PTY as controlling terminal.
func startPTY(cmd *exec.Cmd, cols, rows int) (*os.File, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	if err := setSize(master, cols, rows); err != nil {
		master.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, err
	}
	return master, nil
}

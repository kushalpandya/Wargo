// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux || darwin

package term

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// A tiny PTY implementation (the parts of creack/pty we need) so the feature
// carries no extra module dependency. openPTY is per OS.

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

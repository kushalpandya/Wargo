// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// renameatx is renameatx_np(2); tests replace it to exercise the fallback.
var renameatx = unix.RenameatxNp

func renameNoReplace(oldfd int, oldname string, newfd int, newname string) error {
	err := renameatx(oldfd, oldname, newfd, newname, unix.RENAME_EXCL)
	switch err {
	case nil:
		return nil
	case unix.ENOTSUP, unix.EINVAL:
		// A filesystem without RENAME_EXCL: fall back.
	default:
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: err}
	}
	// Best effort (racy) check; newname is a single component looked up
	// inside the open directory itself.
	var st unix.Stat_t
	if unix.Fstatat(newfd, newname, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: unix.EEXIST}
	}
	return renameat(oldfd, oldname, newfd, newname)
}

func renameat(oldfd int, oldname string, newfd int, newname string) error {
	err := unix.Renameat(oldfd, oldname, newfd, newname)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: err}
	}
	return nil
}

// pathOfFD returns the path the kernel has for the open file f
// (fcntl F_GETPATH).
func pathOfFD(f *os.File) (string, error) {
	var buf [unix.PathMax]byte
	sc, err := f.SyscallConn()
	if err != nil {
		return "", err
	}
	var e unix.Errno
	if err := sc.Control(func(fd uintptr) {
		_, _, e = unix.Syscall(unix.SYS_FCNTL, fd, unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	}); err != nil {
		return "", err
	}
	if e != 0 {
		return "", &os.PathError{Op: "fcntl", Path: f.Name(), Err: e}
	}
	n := bytes.IndexByte(buf[:], 0)
	if n < 0 {
		n = len(buf)
	}
	return string(buf[:n]), nil
}

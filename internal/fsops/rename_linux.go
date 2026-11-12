// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

// sysRenameat2 is the renameat2(2) syscall number; package syscall doesn't
// define it on every architecture. 0 means unknown (use the fallback).
var sysRenameat2 = map[string]uintptr{
	"amd64": 316, "386": 353, "arm": 382, "arm64": 276, "riscv64": 276, "loong64": 276,
	"ppc64": 357, "ppc64le": 357, "s390x": 347,
}[runtime.GOARCH]

const flagRenameNoReplace = 1 // RENAME_NOREPLACE

func renameNoReplace(oldfd int, oldname string, newfd int, newname string) error {
	if sysRenameat2 != 0 {
		op, err := syscall.BytePtrFromString(oldname)
		if err != nil {
			return err
		}
		np, err := syscall.BytePtrFromString(newname)
		if err != nil {
			return err
		}
		_, _, e := syscall.Syscall6(sysRenameat2, uintptr(oldfd), uintptr(unsafe.Pointer(op)),
			uintptr(newfd), uintptr(unsafe.Pointer(np)), flagRenameNoReplace, 0)
		switch e {
		case 0:
			return nil
		case syscall.ENOSYS, syscall.EINVAL:
			// Old kernel or a filesystem without RENAME_NOREPLACE: fall back.
		default:
			return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: e}
		}
	}
	// Best effort (racy) check; newname is a single component, so the
	// /proc path names the entry inside the open directory itself.
	if _, err := os.Lstat("/proc/self/fd/" + strconv.Itoa(newfd) + "/" + newname); err == nil {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: syscall.EEXIST}
	}
	return renameat(oldfd, oldname, newfd, newname)
}

func renameat(oldfd int, oldname string, newfd int, newname string) error {
	err := syscall.Renameat(oldfd, oldname, newfd, newname)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: err}
	}
	return nil
}

// pathOfFD returns the path the kernel has for the open file f.
func pathOfFD(f *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
}

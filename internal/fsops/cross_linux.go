// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/kushalpandya/wargo/internal/host"
)

// openParent opens the directory containing rel inside root and returns it
// with the final path component. The final component is never resolved, so
// a symlink there is renamed itself rather than followed.
func openParent(root *os.Root, rel string) (*os.File, string, error) {
	rel = Clean(rel)
	if rel == "." {
		return nil, "", errors.New("invalid path: the root itself")
	}
	dir, base := path.Split(rel)
	d, err := OpenNonblock(root, Clean(dir))
	if err != nil {
		return nil, "", err
	}
	if st, err := d.Stat(); err != nil || !st.IsDir() {
		d.Close()
		if err == nil {
			err = syscall.ENOTDIR
		}
		return nil, "", err
	}
	return d, base, nil
}

// RenameIn atomically moves the absolute path src (outside the root, e.g. a
// finished upload) to rel inside root. It fails with EXDEV across devices.
// An existing file at rel is replaced.
func RenameIn(root *os.Root, src, rel string) error {
	d, base, err := openParent(root, rel)
	if err != nil {
		return err
	}
	defer d.Close()
	sd, err := os.Open(filepath.Dir(src))
	if err != nil {
		return err
	}
	defer sd.Close()
	return renameat(int(sd.Fd()), filepath.Base(src), int(d.Fd()), base)
}

// RenameOut moves rel inside root to the absolute path dst (e.g. a trash
// folder). It fails with EXDEV across devices and never replaces an
// existing dst.
func RenameOut(root *os.Root, rel, dst string) error {
	d, base, err := openParent(root, rel)
	if err != nil {
		return err
	}
	defer d.Close()
	dd, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer dd.Close()
	return renameNoReplace(int(d.Fd()), base, int(dd.Fd()), filepath.Base(dst))
}

// RenameBetween moves src inside srcRoot to dst inside dstRoot without
// following a symlink in the final component. It fails with EXDEV when the
// roots live on different filesystems. An existing file at dst is replaced.
func RenameBetween(srcRoot *os.Root, src string, dstRoot *os.Root, dst string) error {
	sd, sb, err := openParent(srcRoot, src)
	if err != nil {
		return err
	}
	defer sd.Close()
	dd, db, err := openParent(dstRoot, dst)
	if err != nil {
		return err
	}
	defer dd.Close()
	return renameat(int(sd.Fd()), sb, int(dd.Fd()), db)
}

// RenameBetweenNoReplace is RenameBetween that fails with fs.ErrExist
// instead of replacing an existing dst (atomically where the kernel and
// filesystem support RENAME_NOREPLACE).
func RenameBetweenNoReplace(srcRoot *os.Root, src string, dstRoot *os.Root, dst string) error {
	sd, sb, err := openParent(srcRoot, src)
	if err != nil {
		return err
	}
	defer sd.Close()
	dd, db, err := openParent(dstRoot, dst)
	if err != nil {
		return err
	}
	defer dd.Close()
	return renameNoReplace(int(sd.Fd()), sb, int(dd.Fd()), db)
}

// RenameInNoReplace is RenameIn that never replaces an existing rel.
func RenameInNoReplace(root *os.Root, src, rel string) error {
	d, base, err := openParent(root, rel)
	if err != nil {
		return err
	}
	defer d.Close()
	sd, err := os.Open(filepath.Dir(src))
	if err != nil {
		return err
	}
	defer sd.Close()
	return renameNoReplace(int(sd.Fd()), filepath.Base(src), int(d.Fd()), base)
}

// RealPath returns the absolute, symlink-free host path of rel (resolved
// inside root). The final component is not followed if it is a symlink.
func RealPath(root *os.Root, rel string) (string, error) {
	rel = Clean(rel)
	if rel == "." {
		return fdPath(root, ".")
	}
	d, base, err := openParent(root, rel)
	if err != nil {
		return "", err
	}
	defer d.Close()
	dir, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(d.Fd())))
	if err != nil {
		return "", err
	}
	return hostView(filepath.Join(dir, base))
}

func fdPath(root *os.Root, rel string) (string, error) {
	f, err := OpenNonblock(root, rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	p, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Fd())))
	if err != nil {
		return "", err
	}
	return hostView(p)
}

// hostView converts a kernel path (below the host root in container mode)
// to the host's own path.
func hostView(p string) (string, error) {
	hp, ok := host.Unpath(p)
	if !ok {
		return "", fmt.Errorf("%s is outside the host filesystem", p)
	}
	return hp, nil
}

// Dev returns the device id of the filesystem holding rel's parent.
func Dev(root *os.Root, rel string) (uint64, error) {
	d, _, err := openParent(root, rel)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	st, err := d.Stat()
	if err != nil {
		return 0, err
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Dev), nil
	}
	return 0, errors.ErrUnsupported
}

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

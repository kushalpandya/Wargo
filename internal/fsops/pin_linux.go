// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package fsops

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// os.Root follows relative symlinks that stay inside the root. That is
// fine for a user's own folders but not for the root helper on the
// System drive ("/"), where another local user's link like
// /home/other/x -> ../../etc/shadow stays inside "/". The functions below
// refuse every symlink on the way instead.

const walkFlags = syscall.O_PATH | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC

// PinParent opens the folder holding p (a Clean path relative to root)
// without following a symlink in any folder of the path, and returns it as
// its own root together with p's last component. A symlinked folder yields
// an error wrapping ErrSymlinkInPath. The caller closes parent.
func PinParent(root *os.Root, p string) (parent *os.Root, base string, err error) {
	if p == "." || p == "" {
		return nil, "", errors.New("invalid path: the root itself")
	}
	if Clean(p) != p {
		return nil, "", &fs.PathError{Op: "open", Path: p, Err: fs.ErrInvalid}
	}
	dir, base := path.Split(p)
	rf, err := root.Open(".")
	if err != nil {
		return nil, "", err
	}
	cur, err := openat(int(rf.Fd()), ".", walkFlags, 0)
	rf.Close()
	if err != nil {
		return nil, "", &fs.PathError{Op: "open", Path: ".", Err: err}
	}
	walked := ""
	if dir != "" {
		for _, c := range strings.Split(strings.TrimSuffix(dir, "/"), "/") {
			walked = path.Join(walked, c)
			if c == "" || c == "." || c == ".." {
				syscall.Close(cur)
				return nil, "", &fs.PathError{Op: "open", Path: walked, Err: fs.ErrInvalid}
			}
			next, err := openat(cur, c, walkFlags, 0)
			if err != nil {
				if (err == syscall.ENOTDIR || err == syscall.ELOOP) && isSymlinkAt(cur, c) {
					err = ErrSymlinkInPath
				}
				syscall.Close(cur)
				return nil, "", &fs.PathError{Op: "open", Path: walked, Err: err}
			}
			syscall.Close(cur)
			cur = next
		}
	}
	parent, err = os.OpenRoot(procFD(cur))
	syscall.Close(cur)
	if err != nil {
		return nil, "", err
	}
	return parent, base, nil
}

// ChmodNoFollow changes the mode of base, a single name inside dir. A
// symlink there is refused (ErrSymlinkInPath) instead of followed.
func ChmodNoFollow(dir *os.Root, base string, m os.FileMode) error {
	fd, err := openEntry(dir, base, syscall.O_PATH|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := fstat(fd, &st); err != nil {
		return &fs.PathError{Op: "chmod", Path: base, Err: err}
	}
	if st.Mode&syscall.S_IFMT == syscall.S_IFLNK {
		return &fs.PathError{Op: "chmod", Path: base, Err: ErrSymlinkInPath}
	}
	// fchmod does not work on O_PATH descriptors; the /proc link names the
	// opened inode itself, never the path again.
	if err := os.Chmod(procFD(fd), m); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return &fs.PathError{Op: "chmod", Path: base, Err: err}
	}
	return nil
}

// OpenNoFollow opens base, a single name inside dir, with flag plus
// O_NOFOLLOW (os.Root.OpenFile follows a final symlink even with
// O_NOFOLLOW set). A symlink there yields ErrSymlinkInPath.
func OpenNoFollow(dir *os.Root, base string, flag int) (*os.File, error) {
	return openNoFollow(dir, base, flag, 0)
}

// openNoFollow is OpenNoFollow with a mode for O_CREATE.
func openNoFollow(dir *os.Root, base string, flag int, perm os.FileMode) (*os.File, error) {
	fd, err := openEntry(dir, base, flag|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, perm)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), base), nil
}

// rootOf opens the folder f (an open directory) as an os.Root. The /proc
// link names the open folder itself, never a path again.
func rootOf(f *os.File) (*os.Root, error) {
	return os.OpenRoot(procFD(int(f.Fd())))
}

// chtimesFile sets the access and modification times of the open file f.
func chtimesFile(f *os.File, t time.Time) error {
	if err := os.Chtimes(procFD(int(f.Fd())), t, t); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return &fs.PathError{Op: "chtimes", Path: f.Name(), Err: err}
	}
	return nil
}

// openEntry opens base inside dir with flags (which must include
// O_NOFOLLOW) and returns the raw descriptor.
func openEntry(dir *os.Root, base string, flags int, perm os.FileMode) (int, error) {
	if base == "" || base == "." || base == ".." || strings.Contains(base, "/") {
		return -1, &fs.PathError{Op: "open", Path: base, Err: fs.ErrInvalid}
	}
	df, err := dir.Open(".")
	if err != nil {
		return -1, err
	}
	defer df.Close()
	dfd := int(df.Fd())
	fd, err := openat(dfd, base, flags, uint32(perm.Perm()))
	if err != nil {
		if (err == syscall.ELOOP || err == syscall.ENOTDIR) && isSymlinkAt(dfd, base) {
			err = ErrSymlinkInPath
		}
		return -1, &fs.PathError{Op: "open", Path: base, Err: err}
	}
	return fd, nil
}

// isSymlinkAt reports whether name inside the folder dirfd is a symlink.
func isSymlinkAt(dirfd int, name string) bool {
	fd, err := openat(dirfd, name, syscall.O_PATH|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	return fstat(fd, &st) == nil && st.Mode&syscall.S_IFMT == syscall.S_IFLNK
}

func openat(dirfd int, name string, flags int, mode uint32) (int, error) {
	for {
		fd, err := syscall.Openat(dirfd, name, flags, mode)
		if err != syscall.EINTR {
			return fd, err
		}
	}
}

func fstat(fd int, st *syscall.Stat_t) error {
	for {
		if err := syscall.Fstat(fd, st); err != syscall.EINTR {
			return err
		}
	}
}

func procFD(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

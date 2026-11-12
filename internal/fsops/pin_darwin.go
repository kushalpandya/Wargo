// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// os.Root follows relative symlinks that stay inside the root. That is
// fine for a user's own folders but not for the root helper on the
// System drive ("/"), where another local user's link like
// /Users/other/x -> ../../etc/master.passwd stays inside "/". The
// functions below refuse every symlink on the way instead.
//
// macOS has no O_PATH. Folders are opened O_RDONLY with O_NOFOLLOW_ANY
// (macOS 11 and later), which refuses a symlink in any component, and an
// open folder is turned into an os.Root through /dev/fd/N, which names the
// open descriptor itself (a reopen of it), never a path again. A folder
// opened O_SEARCH cannot be reopened that way, so the folder holding an
// entry must be readable (as on Linux, the root helper always can).

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
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "."
	} else {
		for _, c := range strings.Split(dir, "/") {
			if c == "" || c == "." || c == ".." {
				return nil, "", &fs.PathError{Op: "open", Path: dir, Err: fs.ErrInvalid}
			}
		}
	}
	rf, err := root.Open(".")
	if err != nil {
		return nil, "", err
	}
	fd, err := openat(int(rf.Fd()), dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	rf.Close()
	if err != nil {
		if err == unix.ELOOP {
			// Nothing is followed under O_NOFOLLOW_ANY, so ELOOP can only
			// mean a symlink in the path.
			err = ErrSymlinkInPath
		}
		return nil, "", &fs.PathError{Op: "open", Path: dir, Err: err}
	}
	parent, err = os.OpenRoot(devFD(fd))
	unix.Close(fd)
	if err != nil {
		return nil, "", err
	}
	return parent, base, nil
}

// ChmodNoFollow changes the mode of base, a single name inside dir. A
// symlink there is refused (ErrSymlinkInPath) instead of followed.
func ChmodNoFollow(dir *os.Root, base string, m os.FileMode) error {
	if err := checkName(base); err != nil {
		return err
	}
	df, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer df.Close()
	dfd := int(df.Fd())
	var st unix.Stat_t
	if err := fstatat(dfd, base, &st); err != nil {
		return &fs.PathError{Op: "chmod", Path: base, Err: err}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return &fs.PathError{Op: "chmod", Path: base, Err: ErrSymlinkInPath}
	}
	// AT_SYMLINK_NOFOLLOW: a symlink swapped in since the check above gets
	// the new mode itself (macOS has lchmod); its target is never touched.
	for {
		err = unix.Fchmodat(dfd, base, sysMode(m), unix.AT_SYMLINK_NOFOLLOW)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return &fs.PathError{Op: "chmod", Path: base, Err: err}
	}
	return nil
}

// sysMode converts m to chmod(2) bits, special bits included.
func sysMode(m os.FileMode) uint32 {
	o := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		o |= unix.S_ISUID
	}
	if m&os.ModeSetgid != 0 {
		o |= unix.S_ISGID
	}
	if m&os.ModeSticky != 0 {
		o |= unix.S_ISVTX
	}
	return o
}

// OpenNoFollow opens base, a single name inside dir, with flag plus
// O_NOFOLLOW (os.Root.OpenFile follows a final symlink even with
// O_NOFOLLOW set). A symlink there yields ErrSymlinkInPath.
func OpenNoFollow(dir *os.Root, base string, flag int) (*os.File, error) {
	return openNoFollow(dir, base, flag, 0)
}

// openNoFollow is OpenNoFollow with a mode for O_CREATE.
func openNoFollow(dir *os.Root, base string, flag int, perm os.FileMode) (*os.File, error) {
	if err := checkName(base); err != nil {
		return nil, err
	}
	df, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer df.Close()
	dfd := int(df.Fd())
	fd, err := openat(dfd, base, flag|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err != nil {
		if (err == unix.ELOOP || err == unix.ENOTDIR) && isSymlinkAt(dfd, base) {
			err = ErrSymlinkInPath
		}
		return nil, &fs.PathError{Op: "open", Path: base, Err: err}
	}
	return os.NewFile(uintptr(fd), base), nil
}

// rootOf opens the folder f (an open directory) as an os.Root. The
// /dev/fd link names the open folder itself, never a path again.
func rootOf(f *os.File) (*os.Root, error) {
	return os.OpenRoot(devFD(int(f.Fd())))
}

// chtimesFile sets the access and modification times of the open file f.
// The /dev/fd link names the open file itself (and keeps nanoseconds,
// which futimes would drop).
func chtimesFile(f *os.File, t time.Time) error {
	if err := os.Chtimes(devFD(int(f.Fd())), t, t); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return &fs.PathError{Op: "chtimes", Path: f.Name(), Err: err}
	}
	return nil
}

// checkName refuses anything but a single path component.
func checkName(base string) error {
	if base == "" || base == "." || base == ".." || strings.Contains(base, "/") {
		return &fs.PathError{Op: "open", Path: base, Err: fs.ErrInvalid}
	}
	return nil
}

// isSymlinkAt reports whether name inside the folder dirfd is a symlink.
func isSymlinkAt(dirfd int, name string) bool {
	var st unix.Stat_t
	return fstatat(dirfd, name, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK
}

func openat(dirfd int, name string, flags int, mode uint32) (int, error) {
	for {
		fd, err := unix.Openat(dirfd, name, flags, mode)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// fstatat is lstat of name inside dirfd.
func fstatat(dirfd int, name string, st *unix.Stat_t) error {
	for {
		if err := unix.Fstatat(dirfd, name, st, unix.AT_SYMLINK_NOFOLLOW); err != unix.EINTR {
			return err
		}
	}
}

func devFD(fd int) string { return "/dev/fd/" + strconv.Itoa(fd) }

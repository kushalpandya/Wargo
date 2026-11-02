// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"syscall"
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
	d, err := root.Open(Clean(dir))
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
// folder). It fails with EXDEV across devices.
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
	return renameat(int(d.Fd()), base, int(dd.Fd()), filepath.Base(dst))
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

func renameat(oldfd int, oldname string, newfd int, newname string) error {
	err := syscall.Renameat(oldfd, oldname, newfd, newname)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: err}
	}
	return nil
}

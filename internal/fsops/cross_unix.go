// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux || darwin

package fsops

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"syscall"

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
	dir, err := pathOfFD(d)
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
	p, err := pathOfFD(f)
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

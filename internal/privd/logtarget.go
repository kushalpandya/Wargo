// SPDX-License-Identifier: AGPL-3.0-only

package privd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Upgrade units write their output to a log file chosen by Wargo
// (--property=StandardOutput=append:/var/lib/wargo/<id>.log). systemd opens
// that path as root, and the directory belongs to the unprivileged Wargo
// user — who could plant a symlink or hard link there and make root
// append to /etc/sudoers.d/x. So privd opens the file itself, refusing
// links, verifies what it opened, and hands systemd the magic link
// /proc/<privd>/fd/<n>, which always refers to the inode privd verified
// regardless of later renames in the directory. The descriptor stays open
// until systemd-run returns (systemd opens the output when it starts the
// unit, and systemd-run waits for that unless --no-block is used, in
// which case the unit fails to open its log: safe).

// openLogTargets rewrites file-based StandardOutput/StandardError targets
// in a systemd-run argv. The returned files must be closed after the
// command finished.
func (c *Config) openLogTargets(argv []string) ([]string, []*os.File, error) {
	out := slices.Clone(argv)
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	opened := map[string]string{} // path -> rewritten target (stdout and stderr share one fd)
	for i, a := range out {
		if a == "--" {
			break
		}
		kv, ok := strings.CutPrefix(a, "--property=")
		if !ok {
			continue
		}
		k, v, _ := strings.Cut(kv, "=")
		if k != "StandardOutput" && k != "StandardError" {
			continue
		}
		mode, p, ok := cutLogMode(v)
		if !ok {
			continue
		}
		target, seen := opened[p]
		if !seen {
			f, err := c.openLogFile(p, mode == "truncate:")
			if err != nil {
				closeAll()
				return nil, nil, err
			}
			files = append(files, f)
			target = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), f.Fd())
			opened[p] = target
		}
		out[i] = "--property=" + k + "=" + mode + target
	}
	return out, files, nil
}

func cutLogMode(v string) (mode, path string, ok bool) {
	for _, m := range []string{"file:", "append:", "truncate:"} {
		if p, ok := strings.CutPrefix(v, m); ok {
			return m, p, true
		}
	}
	return "", "", false
}

// openLogFile opens (creating if needed) a log file directly inside one of
// logDirs, refusing symlinks, hard links and files of other owners.
func (c *Config) openLogFile(p string, truncate bool) (*os.File, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return nil, fmt.Errorf("log path %q is not clean and absolute", p)
	}
	dir := filepath.Dir(p)
	if !slices.Contains(logDirs, dir+"/") {
		// Only files directly in a log directory: subdirectories belong to
		// the Wargo user and could be symlinks (O_NOFOLLOW only guards the
		// last component).
		return nil, fmt.Errorf("log %q must be directly inside %s", p, strings.Join(logDirs, ", "))
	}
	if err := c.checkLogDir(dir); err != nil {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_APPEND | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NOCTTY
	f, err := os.OpenFile(p, flags|os.O_CREATE|os.O_EXCL, 0o640)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		f, err = os.OpenFile(p, flags, 0)
	}
	if err != nil {
		// ELOOP: the final component is a symlink.
		return nil, fmt.Errorf("open log %s: %w", p, err)
	}
	if created && os.Geteuid() == 0 {
		if err := f.Chown(c.AllowedUID, -1); err != nil {
			f.Close()
			return nil, err
		}
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	sys, _ := st.Sys().(*syscall.Stat_t)
	switch {
	case !st.Mode().IsRegular():
		err = fmt.Errorf("log %s is not a regular file", p)
	case sys == nil:
		err = fmt.Errorf("log %s: no ownership information", p)
	case sys.Nlink != 1:
		err = fmt.Errorf("log %s has %d hard links", p, sys.Nlink)
	case int(sys.Uid) != c.AllowedUID:
		err = fmt.Errorf("log %s is owned by uid %d, not %d", p, sys.Uid, c.AllowedUID)
	}
	if err == nil && truncate {
		err = f.Truncate(0)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// checkLogDir requires dir to be a real directory owned by root or the
// allowed user without group/other write access, below root-owned,
// non-writable, non-symlink ancestors.
func (c *Config) checkLogDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("log directory %s is not a directory (symlink?)", dir)
	}
	if st.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("log directory %s is writable by group or others", dir)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != 0 && int(sys.Uid) != c.AllowedUID {
		return fmt.Errorf("log directory %s is owned by uid %d", dir, sys.Uid)
	}
	if os.Geteuid() != 0 {
		return nil
	}
	for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
		st, err := os.Lstat(d)
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return fmt.Errorf("%s is not a directory (symlink?)", d)
		}
		if err := checkOwner(d, st); err != nil {
			return err
		}
		if d == "/" {
			return nil
		}
	}
}

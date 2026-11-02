// SPDX-License-Identifier: AGPL-3.0-only

// Package fsops implements file operations confined to an os.Root, shared
// by the unprivileged file manager and the privileged helper.
package fsops

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"strconv"
	"strings"
)

// Clean normalises a client path into a root-relative path. It rejects
// nothing on its own; os.Root enforces confinement.
func Clean(p string) string {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}

// Progress is called with bytes copied so far.
type Progress func(done int64, current string)

// Copy copies src to dst (recursively for directories) inside root.
func Copy(root *os.Root, src, dst string, prog Progress) error {
	src, dst = Clean(src), Clean(dst)
	if dst == src || strings.HasPrefix(dst+"/", src+"/") {
		return errors.New("cannot copy a folder into itself")
	}
	if _, err := root.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	var done int64
	return copyTree(root, src, dst, &done, prog)
}

func copyTree(root *os.Root, src, dst string, done *int64, prog Progress) error {
	st, err := root.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		t, err := root.Readlink(src)
		if err != nil {
			return err
		}
		return root.Symlink(t, dst)
	case st.IsDir():
		if err := root.Mkdir(dst, st.Mode().Perm()|0o700); err != nil {
			return err
		}
		d, err := root.Open(src)
		if err != nil {
			return err
		}
		ents, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyTree(root, path.Join(src, e.Name()), path.Join(dst, e.Name()), done, prog); err != nil {
				return err
			}
		}
		return root.Chmod(dst, st.Mode().Perm())
	case st.Mode().IsRegular():
		in, err := root.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
		if err != nil {
			return err
		}
		w := io.Writer(out)
		if prog != nil {
			w = &countWriter{w: out, done: done, prog: prog, name: src}
		}
		if _, err := io.Copy(w, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return root.Chtimes(dst, st.ModTime(), st.ModTime())
	default:
		return nil // skip devices, sockets, fifos
	}
}

type countWriter struct {
	w    io.Writer
	done *int64
	prog Progress
	name string
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	*c.done += int64(n)
	c.prog(*c.done, c.name)
	return n, err
}

// Size returns the total size of regular files under p.
func Size(root *os.Root, p string) (int64, int, error) {
	var total int64
	var count int
	err := fs.WalkDir(root.FS(), Clean(p), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if i, err := d.Info(); err == nil {
				total += i.Size()
				count++
			}
		}
		return nil
	})
	return total, count, err
}

// Move renames src to dst; falls back to copy+delete across devices.
func Move(root *os.Root, src, dst string) error {
	src, dst = Clean(src), Clean(dst)
	if src == "." {
		return errors.New("cannot move the root")
	}
	if strings.HasPrefix(dst+"/", src+"/") {
		return errors.New("cannot move a folder into itself")
	}
	if _, err := root.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	err := root.Rename(src, dst)
	var le *os.LinkError
	if errors.As(err, &le) && strings.Contains(le.Err.Error(), "cross-device") {
		if err := Copy(root, src, dst, nil); err != nil {
			return err
		}
		return root.RemoveAll(src)
	}
	return err
}

// Remove deletes p recursively. The root itself cannot be removed.
func Remove(root *os.Root, p string) error {
	p = Clean(p)
	if p == "." {
		return errors.New("cannot delete the root")
	}
	return root.RemoveAll(p)
}

// ParseMode parses an octal mode like "755" or "0644".
func ParseMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil || v > 0o7777 {
		return 0, fmt.Errorf("invalid mode %q", s)
	}
	return os.FileMode(v), nil
}

// ParseOwner resolves "user[:group]" (names or ids) to uid/gid (-1 = unchanged).
func ParseOwner(s string) (int, int, error) {
	u, g, _ := strings.Cut(s, ":")
	uid, gid := -1, -1
	if u != "" {
		if n, err := strconv.Atoi(u); err == nil {
			uid = n
		} else if x, err := user.Lookup(u); err == nil {
			uid, _ = strconv.Atoi(x.Uid)
		} else {
			return 0, 0, fmt.Errorf("unknown user %q", u)
		}
	}
	if g != "" {
		if n, err := strconv.Atoi(g); err == nil {
			gid = n
		} else if x, err := user.LookupGroup(g); err == nil {
			gid, _ = strconv.Atoi(x.Gid)
		} else {
			return 0, 0, fmt.Errorf("unknown group %q", g)
		}
	}
	return uid, gid, nil
}

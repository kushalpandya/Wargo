// SPDX-License-Identifier: AGPL-3.0-only

// Package fsops implements file operations confined to an os.Root, shared
// by the unprivileged file manager and the privileged helper.
package fsops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"strconv"
	"strings"
	"syscall"
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

// ErrIntoSelf is returned when copying or moving a folder into itself.
var ErrIntoSelf = errors.New("cannot copy or move a folder into itself")

// Inside reports whether p equals dir or lies below it (both cleaned).
func Inside(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}

// Copy copies src to dst (recursively for directories) inside root.
func Copy(root *os.Root, src, dst string, prog Progress) error {
	return CopyContext(context.Background(), root, src, dst, prog)
}

// CopyContext is Copy with cancellation (checked between files and writes).
func CopyContext(ctx context.Context, root *os.Root, src, dst string, prog Progress) error {
	src, dst = Clean(src), Clean(dst)
	if src == "." || Inside(dst, src) {
		return ErrIntoSelf
	}
	return CopyBetween(ctx, root, src, root, dst, prog)
}

// CopyBetween copies src inside srcRoot to dst inside dstRoot (which may be
// the same root). Each side stays confined to its own root. The caller must
// rule out copying a folder into itself; as a safety net, folders created
// by this copy are never descended into again.
func CopyBetween(ctx context.Context, srcRoot *os.Root, src string, dstRoot *os.Root, dst string, prog Progress) error {
	src, dst = Clean(src), Clean(dst)
	if src == "." || dst == "." {
		return ErrIntoSelf
	}
	if _, err := dstRoot.Lstat(dst); err == nil {
		return fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	c := &copier{ctx: ctx, src: srcRoot, dst: dstRoot, prog: prog, made: map[fileID]bool{}}
	return c.tree(src, dst)
}

type fileID struct{ dev, ino uint64 }

func idOf(fi fs.FileInfo) (fileID, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fileID{uint64(st.Dev), uint64(st.Ino)}, true
	}
	return fileID{}, false
}

type copier struct {
	ctx      context.Context
	src, dst *os.Root
	done     int64
	prog     Progress
	made     map[fileID]bool
}

func (c *copier) tree(src, dst string) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	st, err := c.src.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		t, err := c.src.Readlink(src)
		if err != nil {
			return err
		}
		return c.dst.Symlink(t, dst)
	case st.IsDir():
		if id, ok := idOf(st); ok && c.made[id] {
			return ErrIntoSelf
		}
		if err := c.dst.Mkdir(dst, st.Mode().Perm()|0o700); err != nil {
			return err
		}
		if ds, err := c.dst.Lstat(dst); err == nil {
			if id, ok := idOf(ds); ok {
				c.made[id] = true
			}
		}
		d, err := c.src.Open(src)
		if err != nil {
			return err
		}
		ents, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := c.tree(path.Join(src, e.Name()), path.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return c.dst.Chmod(dst, st.Mode().Perm())
	case st.Mode().IsRegular():
		in, err := c.src.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := c.dst.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
		if err != nil {
			return err
		}
		w := &countWriter{ctx: c.ctx, w: out, done: &c.done, prog: c.prog, name: src}
		if _, err := io.Copy(w, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return c.dst.Chtimes(dst, st.ModTime(), st.ModTime())
	default:
		return nil // skip devices, sockets, fifos
	}
}

type countWriter struct {
	ctx  context.Context
	w    io.Writer
	done *int64
	prog Progress
	name string
}

func (c *countWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.w.Write(p)
	*c.done += int64(n)
	if c.prog != nil {
		c.prog(*c.done, c.name)
	}
	return n, err
}

// Size returns the total size of regular files under p.
func Size(root *os.Root, p string) (int64, int, error) {
	return SizeContext(context.Background(), root, p)
}

// SizeContext is Size with cancellation; on cancel it returns the partial
// totals together with ctx.Err().
func SizeContext(ctx context.Context, root *os.Root, p string) (int64, int, error) {
	var total int64
	var count int
	err := fs.WalkDir(root.FS(), Clean(p), func(_ string, d fs.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
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
	if Inside(dst, src) {
		return ErrIntoSelf
	}
	if _, err := root.Lstat(dst); err == nil {
		return fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	err := root.Rename(src, dst)
	if errors.Is(err, syscall.EXDEV) {
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

// ParseMode parses an octal mode like "755" or "4755" into an os.FileMode
// (setuid/setgid/sticky mapped to Go's mode bits, so Chmod applies them).
func ParseMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil || v > 0o7777 || s == "" {
		return 0, fmt.Errorf("invalid mode %q", s)
	}
	m := os.FileMode(v & 0o777)
	if v&0o4000 != 0 {
		m |= os.ModeSetuid
	}
	if v&0o2000 != 0 {
		m |= os.ModeSetgid
	}
	if v&0o1000 != 0 {
		m |= os.ModeSticky
	}
	return m, nil
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

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
	"path"
	"strconv"
	"strings"
	"syscall"

	"github.com/kushalpandya/wargo/internal/host"
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

// ErrSymlinkInPath is returned when a root file operation meets a symbolic
// link in a folder of the path, or as the item itself where that matters.
var ErrSymlinkInPath = errors.New("path contains a symbolic link")

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
//
// Only the folders holding src and dst are found by path (os.Root rules).
// Below them the copy walks by descriptor: every folder is opened
// O_NOFOLLOW relative to its parent and kept open while its entries are
// copied, files are opened O_NOFOLLOW and checked to be the inode that was
// examined, new files are created O_EXCL|O_NOFOLLOW, and modes and times
// are set on descriptors. Nothing swapped in during the copy (a symlink in
// place of a folder or file) is ever followed, on either side.
func CopyBetween(ctx context.Context, srcRoot *os.Root, src string, dstRoot *os.Root, dst string, prog Progress) error {
	src, dst = Clean(src), Clean(dst)
	if src == "." || dst == "." {
		return ErrIntoSelf
	}
	sp, sbase, err := parentRoot(srcRoot, src)
	if err != nil {
		return err
	}
	defer sp.Close()
	dp, dbase, err := parentRoot(dstRoot, dst)
	if err != nil {
		return err
	}
	defer dp.Close()
	if _, err := dp.Lstat(dbase); err == nil {
		return fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	c := &copier{ctx: ctx, prog: prog, made: map[fileID]bool{}}
	return c.tree(sp, sbase, dp, dbase, src)
}

// parentRoot opens the folder holding rel inside root as its own root and
// returns it with rel's last component (not resolved).
func parentRoot(root *os.Root, rel string) (*os.Root, string, error) {
	d, base, err := openParent(root, rel)
	if err != nil {
		return nil, "", err
	}
	defer d.Close()
	r, err := rootOf(d)
	if err != nil {
		return nil, "", err
	}
	return r, base, nil
}

type fileID struct{ dev, ino uint64 }

func idOf(fi fs.FileInfo) (fileID, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fileID{uint64(st.Dev), uint64(st.Ino)}, true
	}
	return fileID{}, false
}

type copier struct {
	ctx  context.Context
	done int64
	prog Progress
	made map[fileID]bool
}

// copyHook, when set by tests, runs between the steps of a copy (step is
// "lstat" after an entry was examined, "mkdir" after a folder was created)
// so that tests can swap entries in at the worst moment.
var copyHook func(step, name string)

func hook(step, name string) {
	if copyHook != nil {
		copyHook(step, name)
	}
}

// tree copies the entry sname inside the pinned folder sdir to dname inside
// the pinned folder ddir. show is the entry's path for progress and errors.
func (c *copier) tree(sdir *os.Root, sname string, ddir *os.Root, dname, show string) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	st, err := sdir.Lstat(sname)
	if err != nil {
		return err
	}
	hook("lstat", show)
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		t, err := sdir.Readlink(sname)
		if err != nil {
			return err
		}
		return ddir.Symlink(t, dname)
	case st.IsDir():
		return c.dir(sdir, sname, st, ddir, dname, show)
	case st.Mode().IsRegular():
		return c.file(sdir, sname, st, ddir, dname, show)
	default:
		return nil // skip devices, sockets, fifos
	}
}

func (c *copier) dir(sdir *os.Root, sname string, st fs.FileInfo, ddir *os.Root, dname, show string) error {
	if id, ok := idOf(st); ok && c.made[id] {
		return ErrIntoSelf
	}
	sf, err := openNoFollow(sdir, sname, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	ents, sub, err := func() ([]fs.DirEntry, *os.Root, error) {
		defer sf.Close()
		if fst, err := sf.Stat(); err != nil || !os.SameFile(fst, st) {
			return nil, nil, errChanged(show, err)
		}
		ents, err := sf.ReadDir(-1)
		if err != nil {
			return nil, nil, err
		}
		sub, err := rootOf(sf)
		return ents, sub, err
	}()
	if err != nil {
		return err
	}
	defer sub.Close()
	if err := ddir.Mkdir(dname, st.Mode().Perm()|0o700); err != nil {
		return err
	}
	hook("mkdir", show)
	df, err := openNoFollow(ddir, dname, os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer df.Close()
	if dst, err := df.Stat(); err == nil {
		if id, ok := idOf(dst); ok {
			c.made[id] = true
		}
	}
	dsub, err := rootOf(df)
	if err != nil {
		return err
	}
	defer dsub.Close()
	for _, e := range ents {
		if err := c.tree(sub, e.Name(), dsub, e.Name(), path.Join(show, e.Name())); err != nil {
			return err
		}
	}
	return df.Chmod(st.Mode().Perm())
}

func (c *copier) file(sdir *os.Root, sname string, st fs.FileInfo, ddir *os.Root, dname, show string) error {
	in, err := openNoFollow(sdir, sname, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	// The entry may have been replaced (by a FIFO, or another file) since
	// the Lstat above: copy only the inode that was examined.
	if ist, err := in.Stat(); err != nil || !ist.Mode().IsRegular() || !os.SameFile(ist, st) {
		return errChanged(show, err)
	}
	out, err := openNoFollow(ddir, dname, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	w := &countWriter{ctx: c.ctx, w: out, done: &c.done, prog: c.prog, name: show}
	if _, err := io.Copy(w, in); err != nil {
		out.Close()
		return err
	}
	if err := chtimesFile(out, st.ModTime()); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// OpenNonblock opens name inside root read-only with O_NONBLOCK (and
// O_NOCTTY), so that a FIFO can't block the caller indefinitely. For
// regular files and directories the flags have no effect.
func OpenNonblock(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}

func errChanged(name string, err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%s changed while being copied", name)
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
	return MoveBetween(root, src, root, dst)
}

// MoveBetween moves src inside srcRoot to dst inside dstRoot without
// replacing anything at dst. Across devices it copies and then deletes
// src. The caller must rule out moving a folder into itself.
func MoveBetween(srcRoot *os.Root, src string, dstRoot *os.Root, dst string) error {
	src, dst = Clean(src), Clean(dst)
	if src == "." {
		return errors.New("cannot move the root")
	}
	if _, err := dstRoot.Lstat(dst); err == nil {
		return fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	// No-replace: something created at dst since the check above is kept.
	err := RenameBetweenNoReplace(srcRoot, src, dstRoot, dst)
	if errors.Is(err, syscall.EXDEV) {
		if err := CopyBetween(context.Background(), srcRoot, src, dstRoot, dst, nil); err != nil {
			return err
		}
		return srcRoot.RemoveAll(src)
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
		} else if x, err := host.LookupUser(u); err == nil {
			uid = x.UID
		} else {
			return 0, 0, fmt.Errorf("unknown user %q", u)
		}
	}
	if g != "" {
		if n, err := strconv.Atoi(g); err == nil {
			gid = n
		} else if x, err := host.LookupGroup(g); err == nil {
			gid = x.GID
		} else {
			return 0, 0, fmt.Errorf("unknown group %q", g)
		}
	}
	return uid, gid, nil
}

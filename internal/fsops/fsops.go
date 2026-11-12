// SPDX-License-Identifier: AGPL-3.0-only

// Package fsops implements file operations confined to an os.Root, shared
// by the unprivileged file manager and the privileged helper.
package fsops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

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
	_, err := CopyBetween(ctx, root, src, root, dst, prog)
	return err
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
//
// Special files (devices, sockets, FIFOs) are not copied; their paths are
// returned in skipped.
func CopyBetween(ctx context.Context, srcRoot *os.Root, src string, dstRoot *os.Root, dst string, prog Progress) (skipped []string, err error) {
	c, err := copyBetween(ctx, srcRoot, src, dstRoot, dst, prog, false)
	return c.Skipped, err
}

// Copied describes what a copy for a move made: the special files it
// skipped and, per source path, what it copied, so that RemoveCopied
// removes nothing else.
type Copied struct {
	Skipped []string
	recs    map[string]copyRec
}

// copyRec is the state of a source entry when it was copied. Size and
// modification time are compared for regular files only.
type copyRec struct {
	id      fileID
	regular bool
	size    int64
	mtime   time.Time
}

// CopyForMove is CopyBetween that also records what it copied, for
// RemoveCopied once the copy is complete.
func CopyForMove(ctx context.Context, srcRoot *os.Root, src string, dstRoot *os.Root, dst string, prog Progress) (*Copied, error) {
	return copyBetween(ctx, srcRoot, src, dstRoot, dst, prog, true)
}

func copyBetween(ctx context.Context, srcRoot *os.Root, src string, dstRoot *os.Root, dst string, prog Progress, record bool) (*Copied, error) {
	c := &copier{ctx: ctx, prog: prog, made: map[fileID]bool{}}
	if record {
		c.recs = map[string]copyRec{}
	}
	src, dst = Clean(src), Clean(dst)
	if src == "." || dst == "." {
		return c.result(), ErrIntoSelf
	}
	sp, sbase, err := parentRoot(srcRoot, src)
	if err != nil {
		return c.result(), err
	}
	defer sp.Close()
	dp, dbase, err := parentRoot(dstRoot, dst)
	if err != nil {
		return c.result(), err
	}
	defer dp.Close()
	if _, err := dp.Lstat(dbase); err == nil {
		return c.result(), fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	err = c.tree(sp, sbase, dp, dbase, src)
	return c.result(), err
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
	// skipped lists the special files (devices, sockets, FIFOs) met.
	skipped []string
	// recs, when set, records every entry copied (see Copied).
	recs map[string]copyRec
}

func (c *copier) result() *Copied { return &Copied{Skipped: c.skipped, recs: c.recs} }

// note records that show (examined as st) was copied.
func (c *copier) note(show string, st fs.FileInfo) {
	if c.recs == nil {
		return
	}
	id, ok := idOf(st)
	if !ok {
		return // nothing to check against later: never removed
	}
	r := copyRec{id: id}
	if st.Mode().IsRegular() {
		r.regular, r.size, r.mtime = true, st.Size(), st.ModTime()
	}
	c.recs[show] = r
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
		if err == nil {
			err = ddir.Symlink(t, dname)
		}
		if err == nil {
			c.note(show, st)
		}
		return err
	case st.IsDir():
		err = c.dir(sdir, sname, st, ddir, dname, show)
		if err == nil {
			c.note(show, st)
		}
		return err
	case st.Mode().IsRegular():
		err = c.file(sdir, sname, st, ddir, dname, show)
		if err == nil {
			c.note(show, st)
		}
		return err
	default:
		// Devices, sockets and FIFOs are not copied; the caller decides
		// what to tell the user (and a move leaves them in place).
		c.skipped = append(c.skipped, show)
		return nil
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
// what was copied: special files (which are never copied) stay in src,
// together with the folders holding them. The caller must rule out moving
// a folder into itself.
func MoveBetween(srcRoot *os.Root, src string, dstRoot *os.Root, dst string) error {
	src, dst = Clean(src), Clean(dst)
	if src == "." {
		return errors.New("cannot move the root")
	}
	if dfi, err := dstRoot.Lstat(dst); err == nil {
		// On a case-insensitive drive "Photo.jpg" and "photo.jpg" name the
		// same entry: renaming one to the other is not a clash.
		if sfi, err := srcRoot.Lstat(src); err == nil && os.SameFile(sfi, dfi) {
			return renameSameFile(srcRoot, src, dstRoot, dst, sfi)
		}
		return fmt.Errorf("%s: %w", dst, fs.ErrExist)
	}
	// No-replace: something created at dst since the check above is kept.
	err := renameBetween(srcRoot, src, dstRoot, dst)
	if errors.Is(err, syscall.EXDEV) {
		c, err := CopyForMove(context.Background(), srcRoot, src, dstRoot, dst, nil)
		if err != nil {
			return err
		}
		_, err = RemoveCopied(srcRoot, src, c)
		return err
	}
	return err
}

// renameSameFile renames src to dst when both already name the inode st:
// either one entry under two spellings (a case-insensitive drive) or two
// hard links. A plain rename between two names of one inode does nothing,
// and a no-replace rename fails because dst "exists", so src is first
// moved to a temporary name next to it. If dst then no longer exists it
// was src under another spelling and the rename completes; if it still
// does it is another link to the same file, so src's name is dropped.
func renameSameFile(srcRoot *os.Root, src string, dstRoot *os.Root, dst string, st fs.FileInfo) error {
	if srcRoot == dstRoot && src == dst {
		return nil
	}
	b := make([]byte, 6)
	rand.Read(b)
	tmp := path.Join(path.Dir(src), sideName(".wargo-rename-"+hex.EncodeToString(b)+"-", path.Base(src)))
	if err := RenameBetweenNoReplace(srcRoot, src, srcRoot, tmp); err != nil {
		return err
	}
	err := RenameBetweenNoReplace(srcRoot, tmp, dstRoot, dst)
	if errors.Is(err, fs.ErrExist) && !st.IsDir() {
		tfi, terr := srcRoot.Lstat(tmp)
		dfi, derr := dstRoot.Lstat(dst)
		if terr == nil && derr == nil && os.SameFile(tfi, st) && os.SameFile(dfi, st) {
			return srcRoot.Remove(tmp)
		}
	}
	if err != nil {
		// Put src back under its own name.
		if rerr := RenameBetweenNoReplace(srcRoot, tmp, srcRoot, src); rerr != nil {
			return fmt.Errorf("%w (the item was left as %s)", err, tmp)
		}
	}
	return err
}

// sideName prefixes base, shortened so the result stays a valid name.
func sideName(prefix, base string) string {
	const maxName = 255
	if room := maxName - len(prefix); len(base) > room {
		base = base[:max(room, 0)]
		for len(base) > 0 && !utf8.ValidString(base) {
			base = base[:len(base)-1]
		}
	}
	return prefix + base
}

// RemoveCopied deletes p inside root after CopyForMove copied it, but only
// the entries that are still what was copied: same inode and, for regular
// files, same size and modification time. Anything else (special files,
// entries created or changed during the copy) stays, with the folders
// holding it; the paths kept for a reason other than being skipped special
// files are returned in changed.
//
// The walk goes by descriptor like the copy: every folder is opened
// O_NOFOLLOW relative to its parent and checked to be the one examined,
// and entries are removed by single names inside it, so a symlink swapped
// in for a folder is never followed.
func RemoveCopied(root *os.Root, p string, c *Copied) (changed []string, err error) {
	p = Clean(p)
	if p == "." {
		return nil, errors.New("cannot delete the root")
	}
	r := &remover{c: c, skipped: make(map[string]bool, len(c.Skipped))}
	for _, k := range c.Skipped {
		r.skipped[k] = true
	}
	parent, base, err := parentRoot(root, p)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	err = r.prune(parent, base, p)
	return r.changed, err
}

type remover struct {
	c       *Copied
	skipped map[string]bool
	changed []string
}

// copied reports whether show, now st, is still the entry that was copied.
func (r *remover) copied(show string, st fs.FileInfo) bool {
	rec, ok := r.c.recs[show]
	if !ok {
		return false
	}
	if id, ok := idOf(st); !ok || id != rec.id {
		return false
	}
	if rec.regular {
		return st.Mode().IsRegular() && st.Size() == rec.size && st.ModTime().Equal(rec.mtime)
	}
	return true
}

// prune removes name inside the pinned folder dir (show is its path) if it
// is what was copied; folders are emptied first and stay when something
// inside them stays.
func (r *remover) prune(dir *os.Root, name, show string) error {
	st, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !r.copied(show, st) {
		if !r.skipped[show] {
			r.changed = append(r.changed, show)
		}
		return nil
	}
	if !st.IsDir() {
		return dir.Remove(name)
	}
	f, err := openNoFollow(dir, name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	ents, sub, err := func() ([]fs.DirEntry, *os.Root, error) {
		defer f.Close()
		if fst, err := f.Stat(); err != nil || !os.SameFile(fst, st) {
			return nil, nil, errChanged(show, err)
		}
		ents, err := f.ReadDir(-1)
		if err != nil {
			return nil, nil, err
		}
		sub, err := rootOf(f)
		return ents, sub, err
	}()
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := r.prune(sub, e.Name(), path.Join(show, e.Name())); err != nil {
			sub.Close()
			return err
		}
	}
	sub.Close()
	err = dir.Remove(name)
	if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
		return nil // something inside stays
	}
	return err
}

// renameBetween is the rename MoveBetween tries first (tests replace it
// to simulate a move across devices).
var renameBetween = RenameBetweenNoReplace

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

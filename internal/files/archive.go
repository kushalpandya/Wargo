// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
)

// ---- archive ---------------------------------------------------------------

func (s *Service) handleArchive(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root   string   `json:"root"`
		Paths  []string `json:"paths"`
		Dest   string   `json:"dest"`
		Format string   `json:"format"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if req.Format == "" {
		req.Format = "zip"
	}
	ext := map[string]string{"zip": ".zip", "tar.gz": ".tar.gz", "tgz": ".tar.gz", "tar": ".tar"}[req.Format]
	if ext == "" {
		return httpx.BadRequest("format must be zip, tar or tar.gz")
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	paths, err := cleanPaths(req.Paths)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if _, err := root.Lstat(p); err != nil {
			return fsErr(err, p)
		}
	}
	dest := fsops.Clean(req.Dest)
	if req.Dest == "" {
		name := "Archive"
		if len(paths) == 1 {
			name = baseName(paths[0])
		}
		dir := parentOf(paths[0])
		dest = join(dir, uniqueName(root, dir, name+ext))
	}
	if dest == "." || validName(baseName(dest)) != nil {
		return httpx.BadRequest("invalid archive name")
	}
	if _, err := root.Lstat(dest); err == nil {
		return fsErr(fs.ErrExist, dest)
	}
	if st, err := root.Stat(parentOf(dest)); err != nil || !st.IsDir() {
		return httpx.Errorf(http.StatusBadRequest, "not_dir", "Destination folder does not exist")
	}
	for _, p := range paths {
		if fsops.Inside(dest, p) {
			return httpx.BadRequest("the archive cannot be created inside a folder being archived")
		}
	}
	format := strings.TrimPrefix(ext, ".")
	title := fmt.Sprintf("Compress %s", baseName(dest))
	id := s.startJob(r, "files.archive", title, ref, dest, map[string]any{"paths": paths, "format": format},
		func(ctx context.Context, root *os.Root, job *jobs.Job) (map[string]any, error) {
			return createArchive(ctx, root, job, paths, dest, format)
		})
	httpx.OK(w, map[string]any{"jobId": id, "path": dest})
	return nil
}

func createArchive(ctx context.Context, root *os.Root, job *jobs.Job, paths []string, dest, format string) (map[string]any, error) {
	job.Progress("Scanning", -1, "")
	total, files, _ := measure(ctx, root, paths, 0, 0)
	tmp := join(parentOf(dest), sideName(".", baseName(dest), ".partial"))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			root.Remove(tmp)
		}
	}()
	prog := func(done int64, cur string) { job.Progress("Compressing", frac(done, total), cur) }
	switch format {
	case "zip":
		zw := zip.NewWriter(f)
		if err := writeZip(ctx, zw, root, paths, prog); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
	default:
		var w io.Writer = f
		var gz *gzip.Writer
		if format == "tar.gz" {
			gz = gzip.NewWriter(f)
			w = gz
		}
		tw := tar.NewWriter(w)
		if err := writeTar(ctx, tw, root, paths, prog); err != nil {
			return nil, err
		}
		if err := tw.Close(); err != nil {
			return nil, err
		}
		if gz != nil {
			if err := gz.Close(); err != nil {
				return nil, err
			}
		}
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// No-replace: a file that appeared at dest meanwhile is kept.
	if err := archiveRename(root, tmp, root, dest); err != nil {
		if errors.Is(err, fs.ErrExist) {
			err = fmt.Errorf("%s: %w", dest, fs.ErrExist)
		}
		return nil, err
	}
	ok = true
	st, _ := root.Stat(dest)
	var size int64
	if st != nil {
		size = st.Size()
	}
	return map[string]any{"path": dest, "size": size, "files": files, "bytes": total}, nil
}

func writeTar(ctx context.Context, tw *tar.Writer, root *os.Root, paths []string, prog fsops.Progress) error {
	var done int64
	for _, item := range paths {
		base := parentOf(item)
		err := fs.WalkDir(root.FS(), item, func(p string, d fs.DirEntry, err error) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if err != nil {
				return err
			}
			rel := p
			if base != "." {
				rel = strings.TrimPrefix(p, base+"/")
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			link := ""
			if info.Mode()&fs.ModeSymlink != 0 {
				link, _ = root.Readlink(p)
			} else if !info.Mode().IsRegular() && !info.IsDir() {
				return nil
			}
			hdr, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return nil
			}
			hdr.Name = rel
			if info.IsDir() {
				hdr.Name += "/"
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			f, _, err := openRegular(root, p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.CopyN(tw, &progressReader{ctx: ctx, r: f, done: &done, prog: prog, name: p}, hdr.Size)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ---- extract ---------------------------------------------------------------

func (s *Service) handleExtract(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root string `json:"root"`
		Path string `json:"path"`
		Dest string `json:"dest"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	p := fsops.Clean(req.Path)
	format, err := sniffArchive(root, p)
	if err != nil {
		return err
	}
	dest := fsops.Clean(req.Dest)
	if req.Dest == "" {
		stem, _ := splitExt(baseName(p))
		dir := parentOf(p)
		dest = join(dir, uniqueName(root, dir, stem))
	}
	if st, err := root.Stat(dest); err == nil && !st.IsDir() {
		return httpx.Errorf(http.StatusBadRequest, "not_dir", "Destination is not a folder")
	}
	if st, err := root.Stat(parentOf(dest)); err != nil || !st.IsDir() {
		return httpx.Errorf(http.StatusBadRequest, "not_dir", "Destination's parent folder does not exist")
	}
	id := s.startJob(r, "files.extract", "Extract "+baseName(p), ref, p, map[string]any{"dest": dest, "format": format},
		func(ctx context.Context, root *os.Root, job *jobs.Job) (map[string]any, error) {
			return extract(ctx, root, job, p, dest, format)
		})
	httpx.OK(w, map[string]any{"jobId": id, "dest": dest})
	return nil
}

// sniffArchive identifies zip / tar / tar.gz by content.
func sniffArchive(root *os.Root, p string) (string, error) {
	f, _, err := openRegular(root, p)
	if err != nil {
		return "", fsErr(err, p)
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return "zip", nil
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "tar.gz", nil
	case n >= 262 && bytes.Equal(head[257:262], []byte("ustar")):
		return "tar", nil
	}
	return "", httpx.Errorf(http.StatusUnsupportedMediaType, "unsupported_archive", "Only zip, tar and tar.gz archives can be extracted")
}

// safeRel validates an archive member name and returns it relative to the
// extraction folder. Absolute names and any ".." component are rejected.
func safeRel(name string) (string, bool) {
	n := strings.ReplaceAll(name, "\\", "/")
	if n == "" || strings.HasPrefix(n, "/") || strings.ContainsRune(n, 0) {
		return "", false
	}
	if len(n) >= 2 && n[1] == ':' { // C:/...
		return "", false
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return "", false
		}
	}
	c := path.Clean(n)
	if c == "." {
		return "", false
	}
	return c, true
}

type extractor struct {
	ctx      context.Context
	root     *os.Root
	job      *jobs.Job
	dest     string
	written  int64
	total    int64 // expected bytes (0 = unknown)
	files    int
	entries  int
	skipped  int
	progress func()
}

var errTooBig = errors.New("archive exceeds the extraction size limit")

// errNoRoom stops an extraction that would leave less than extractReserve
// free on the drive.
var errNoRoom = httpx.Errorf(http.StatusInsufficientStorage, "insufficient_space",
	"Not enough free space to extract this archive (some space must stay free on the drive)")

// entry is called before each archive entry of size bytes.
func (x *extractor) entry(size int64) error {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	x.entries++
	if x.entries > maxExtractFiles {
		return fmt.Errorf("archive has more than %d entries", maxExtractFiles)
	}
	// Folders (and empty files) hold no data: never refuse them for space.
	if size <= 0 {
		return nil
	}
	if free, total, ok := freeSpaceIn(x.root, x.dest); ok && free < uint64(size)+extractReserve(total) {
		return errNoRoom
	}
	return nil
}

func (x *extractor) skip(name, why string) {
	x.skipped++
	x.job.Log(fmt.Sprintf("skipped %s (%s)", name, why))
}

func (x *extractor) dir(name string) error {
	rel, ok := safeRel(name)
	if !ok {
		x.skip(name, "unsafe path")
		return nil
	}
	return x.root.MkdirAll(join(x.dest, rel), 0o755)
}

func (x *extractor) file(name string, mode fs.FileMode, mtime time.Time, r io.Reader) error {
	rel, ok := safeRel(name)
	if !ok {
		x.skip(name, "unsafe path")
		return nil
	}
	target := join(x.dest, rel)
	if err := x.root.MkdirAll(parentOf(target), 0o755); err != nil {
		return err
	}
	f, err := x.root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, (mode.Perm()&0o777)|0o600)
	if errors.Is(err, fs.ErrExist) {
		x.skip(name, "already exists")
		return nil
	}
	if err != nil {
		return err
	}
	remaining := maxExtractBytes - x.written
	n, err := io.Copy(f, &progressReader{ctx: x.ctx, r: io.LimitReader(r, remaining+1), done: &x.written, prog: func(int64, string) { x.progress() }, name: rel})
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > remaining {
		err = errTooBig
	}
	if err != nil {
		x.root.Remove(target)
		return err
	}
	if mode.Perm()&0o600 != 0o600 {
		x.root.Chmod(target, mode.Perm()&0o777)
	}
	if !mtime.IsZero() {
		x.root.Chtimes(target, mtime, mtime)
	}
	x.files++
	return nil
}

func extract(ctx context.Context, root *os.Root, job *jobs.Job, p, dest, format string) (res map[string]any, err error) {
	// The parent folder was checked to exist; a folder made here is the
	// job's own and goes again if the job fails.
	if merr := root.Mkdir(dest, 0o755); merr == nil {
		defer func() {
			if err != nil {
				root.RemoveAll(dest)
			}
		}()
	} else if !errors.Is(merr, fs.ErrExist) {
		return nil, merr
	} else if st, serr := root.Stat(dest); serr != nil || !st.IsDir() {
		return nil, merr
	}
	f, st, err := openRegular(root, p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	x := &extractor{ctx: ctx, root: root, job: job, dest: dest}
	cr := &countingReader{r: f}
	x.progress = func() {
		switch {
		case format == "tar.gz":
			job.Progress("Extracting", frac(cr.n, st.Size()), "")
		case x.total > 0:
			job.Progress("Extracting", frac(x.written, x.total), "")
		}
	}
	job.Progress("Extracting", -1, "")
	switch format {
	case "zip":
		err = extractZip(x, f, st.Size())
	default:
		var rd io.Reader = cr
		if format == "tar.gz" {
			gz, gerr := gzip.NewReader(cr)
			if gerr != nil {
				return nil, gerr
			}
			defer gz.Close()
			rd = gz
		} else {
			x.total = st.Size()
		}
		err = extractTar(x, rd)
	}
	res = map[string]any{"dest": dest, "files": x.files, "bytes": x.written, "skipped": x.skipped}
	return res, err
}

func extractZip(x *extractor, ra io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return err
	}
	if len(zr.File) > maxExtractFiles {
		return fmt.Errorf("archive has more than %d entries", maxExtractFiles)
	}
	for _, zf := range zr.File {
		x.total += int64(zf.UncompressedSize64)
	}
	if x.total > maxExtractBytes {
		return errTooBig
	}
	// The declared sizes are known up front (and enforced by archive/zip
	// while reading), so refuse early instead of filling the disk.
	if free, total, ok := freeSpaceIn(x.root, x.dest); ok && x.total > 0 && free < uint64(x.total)+extractReserve(total) {
		return errNoRoom
	}
	for _, zf := range zr.File {
		if err := x.entry(int64(min(zf.UncompressedSize64, 1<<62))); err != nil {
			return err
		}
		m := zf.Mode()
		switch {
		case m.IsDir() || strings.HasSuffix(zf.Name, "/"):
			err = x.dir(zf.Name)
		case m&fs.ModeSymlink != 0:
			x.skip(zf.Name, "symlink")
		case !m.IsRegular():
			x.skip(zf.Name, "special file")
		default:
			var rc io.ReadCloser
			rc, err = zf.Open()
			if err == nil {
				err = x.file(zf.Name, m, zf.Modified, rc)
				rc.Close()
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTar(x *extractor, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		size := int64(0)
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == tar.TypeRegA {
			size = hdr.Size
		}
		if err := x.entry(size); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = x.dir(hdr.Name)
		case tar.TypeReg, tar.TypeRegA:
			err = x.file(hdr.Name, fs.FileMode(hdr.Mode), hdr.ModTime, tr)
		case tar.TypeSymlink, tar.TypeLink:
			x.skip(hdr.Name, "link")
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			x.skip(hdr.Name, "special file")
		}
		if err != nil {
			return err
		}
	}
}

// maxExtractReserve caps the free space extraction always leaves on a drive.
const maxExtractReserve = 1 << 30

// extractReserve is the free space extraction leaves on a drive of total
// bytes: 1 GiB, or 5% of the drive when that is smaller.
func extractReserve(total uint64) uint64 {
	return min(maxExtractReserve, total/20)
}

// archiveRename moves a finished archive into place without replacing
// anything (tests replace it to create the name at the last moment).
var archiveRename = fsops.RenameBetweenNoReplace

// freeSpaceIn reports the space available to unprivileged users and the
// total size of the filesystem holding dir inside root (tests replace it).
var freeSpaceIn = freeSpaceInDir

func freeSpaceInDir(root *os.Root, dir string) (free, total uint64, ok bool) {
	d, err := openDir(root, dir)
	if err != nil {
		return 0, 0, false
	}
	defer d.Close()
	var st syscall.Statfs_t
	if syscall.Fstatfs(int(d.Fd()), &st) != nil {
		return 0, 0, false
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), true
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

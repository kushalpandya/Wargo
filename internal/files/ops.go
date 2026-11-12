// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
)

// jobFunc does the work of a background job with its own root handle.
type jobFunc func(ctx context.Context, root *os.Root, job *jobs.Job) (map[string]any, error)

// startJob runs fn in the background and audits the outcome.
func (s *Service) startJob(r *http.Request, kind, title string, ref rootRef, target string, details any, fn jobFunc) string {
	ctx, cancel := context.WithCancel(context.Background())
	job := s.jobs.Start(kind, title, cancel)
	// Keep only what the audit record needs; r's context ends with the request.
	ar := r.Clone(context.Background())
	go func() {
		defer cancel()
		var res map[string]any
		// Every filesystem step of the job runs with the admin's identity
		// (container mode); the job bookkeeping below doesn't touch files.
		err := host.AsUser(func() error {
			root, err := openRoot(ref.Path)
			if err != nil {
				return err
			}
			defer root.Close()
			res, err = fn(ctx, root, job)
			return err
		})
		if err != nil && ctx.Err() != nil {
			err = context.Canceled
		}
		if res == nil {
			res = map[string]any{}
		}
		res["root"] = ref.ID
		if err != nil {
			job.Log(err.Error())
		}
		job.Finish(err, res)
		s.record(ar, kind, ref, target, err, details)
	}()
	return job.ID()
}

// measure sums sizes under paths, stopping early once over the inline caps.
func measure(ctx context.Context, root *os.Root, paths []string, capBytes int64, capFiles int) (int64, int, bool) {
	var total int64
	var n int
	errStop := errors.New("stop")
	for _, p := range paths {
		err := fs.WalkDir(root.FS(), p, func(_ string, d fs.DirEntry, err error) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			if err != nil {
				return nil
			}
			n++
			if d.Type().IsRegular() {
				if i, err := d.Info(); err == nil {
					total += i.Size()
				}
			}
			if capFiles > 0 && (total > capBytes || n > capFiles) {
				return errStop
			}
			return nil
		})
		if err != nil {
			return total, n, true
		}
	}
	return total, n, false
}

func frac(done, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return min(1, float64(done)/float64(total))
}

// ---- mkdir / rename --------------------------------------------------------

func (s *Service) handleMkdir(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root string `json:"root"`
		Path string `json:"path"`
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
	if p == "." {
		return httpx.BadRequest("path required")
	}
	if err := validName(baseName(p)); err != nil {
		return err
	}
	err = s.tryElevated(r, ref, "mkdir", p, "", func() error { return root.Mkdir(p, 0o755) })
	s.record(r, "files.mkdir", ref, p, err, nil)
	if err != nil {
		return fsErr(err, p)
	}
	httpx.OK(w, map[string]string{"path": p})
	return nil
}

func (s *Service) handleRename(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root string `json:"root"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := validName(req.Name); err != nil {
		return err
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	p := fsops.Clean(req.Path)
	if p == "." {
		return httpx.BadRequest("cannot rename the root")
	}
	dst := join(parentOf(p), req.Name)
	if dst == p {
		httpx.OK(w, map[string]string{"path": dst})
		return nil
	}
	if _, err := root.Lstat(p); err != nil {
		return fsErr(err, p)
	}
	err = s.tryElevated(r, ref, "move", p, dst, func() error { return fsops.Move(root, p, dst) })
	s.record(r, "files.rename", ref, p, err, map[string]string{"to": dst})
	if err != nil {
		return fsErr(err, dst)
	}
	httpx.OK(w, map[string]string{"path": dst})
	return nil
}

// ---- move / copy -----------------------------------------------------------

type transferReq struct {
	Root string `json:"root"`
	// DestRoot is the destination root; empty means the same as Root.
	DestRoot string   `json:"destRoot"`
	Paths    []string `json:"paths"`
	Dest     string   `json:"dest"`
	// OnConflict: "error" (default), "rename" (keep both) or "skip".
	OnConflict string `json:"onConflict"`
}

type pair struct{ src, dst string }

// transferRoots holds the open source and destination roots of a move/copy.
// For a single-root transfer both handles are the same.
type transferRoots struct {
	src, dst   *os.Root
	sref, dref rootRef
	cross      bool
}

func (t *transferRoots) Close() {
	t.src.Close()
	if t.cross {
		t.dst.Close()
	}
}

func (s *Service) openTransfer(req transferReq) (*transferRoots, error) {
	src, sref, err := s.open(req.Root)
	if err != nil {
		return nil, err
	}
	t := &transferRoots{src: src, dst: src, sref: sref, dref: sref}
	if req.DestRoot == "" {
		return t, nil
	}
	dref, err := s.lookupRoot(req.DestRoot)
	if err != nil {
		src.Close()
		return nil, err
	}
	t.dref = dref
	if dref.Path == sref.Path {
		return t, nil
	}
	dst, err := openRoot(dref.Path)
	if err != nil {
		src.Close()
		return nil, fsErr(err, dref.Path)
	}
	t.dst, t.cross = dst, true
	return t, nil
}

// destAbs is the host path of a destination for audit records.
func (t *transferRoots) destAbs(rel string) string { return filepath.Join(t.dref.Path, rel) }

// plan validates a move/copy and resolves destination names.
func plan(t *transferRoots, req transferReq, isCopy bool) ([]pair, []string, error) {
	paths, err := cleanPaths(req.Paths)
	if err != nil {
		return nil, nil, err
	}
	dest := fsops.Clean(req.Dest)
	st, err := t.dst.Stat(dest)
	if err != nil {
		return nil, nil, fsErr(err, dest)
	}
	if !st.IsDir() {
		return nil, nil, httpx.Errorf(http.StatusBadRequest, "not_dir", "Destination is not a folder")
	}
	// Across roots, compare real host paths: the roots may overlap (a drive
	// mounted at "/" contains the home folder).
	realDest := ""
	if t.cross {
		realDest, _ = fsops.RealPath(t.dst, dest)
	}
	var out []pair
	var conflicts []string
	for _, p := range paths {
		if _, err := t.src.Lstat(p); err != nil {
			return nil, nil, fsErr(err, p)
		}
		name := baseName(p)
		dst := join(dest, name)
		same := false
		if t.cross {
			if realDest != "" {
				if rs, err := fsops.RealPath(t.src, p); err == nil {
					if inside(realDest, rs) {
						return nil, nil, fsErr(fsops.ErrIntoSelf, p)
					}
					same = filepath.Dir(rs) == realDest
				}
			}
		} else {
			if fsops.Inside(dest, p) {
				return nil, nil, fsErr(fsops.ErrIntoSelf, p)
			}
			same = dst == p
		}
		if same {
			if !isCopy {
				continue // already there
			}
			dst = join(dest, uniqueName(t.dst, dest, name)) // duplicate in place
		} else if _, err := t.dst.Lstat(dst); err == nil {
			switch req.OnConflict {
			case "rename":
				dst = join(dest, uniqueName(t.dst, dest, name))
			case "skip":
				continue
			default:
				conflicts = append(conflicts, dst)
				continue
			}
		}
		out = append(out, pair{p, dst})
	}
	if len(conflicts) > 0 {
		return nil, nil, httpx.Errorf(http.StatusConflict, "exists",
			fmt.Sprintf("%d item(s) already exist in the destination", len(conflicts))).With(map[string]any{"conflicts": conflicts})
	}
	srcs := make([]string, len(out))
	for i, x := range out {
		srcs[i] = x.src
	}
	return out, srcs, nil
}

// elevatedTransfer retries a single failed move/copy through the helper.
// Across roots the helper works on the real host paths below "/".
func (s *Service) elevatedTransfer(r *http.Request, t *transferRoots, op string, x pair) error {
	if !t.cross {
		return s.elevated(r, t.sref, op, x.src, x.dst, nil)
	}
	if s.elev == nil {
		return httpx.Errorf(http.StatusForbidden, "permission_denied", "Permission denied: "+x.src)
	}
	from, err := fsops.RealPath(t.src, x.src)
	if err != nil {
		return err
	}
	to, err := fsops.RealPath(t.dst, x.dst)
	if err != nil {
		return err
	}
	_, err = s.elev.Run(r, "files.op", op+" "+from+" → "+to, s.elev.Helper("fs", op, "/", from, to), nil)
	return err
}

func (s *Service) handleMove(w http.ResponseWriter, r *http.Request) error {
	var req transferReq
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	t, err := s.openTransfer(req)
	if err != nil {
		return err
	}
	defer t.Close()
	pairs, _, err := plan(t, req, false)
	if err != nil {
		return err
	}
	var cross []pair
	moved := 0
	for _, x := range pairs {
		// Never replace: plan() checked for conflicts, but something may have
		// appeared at the destination since.
		err := fsops.RenameBetweenNoReplace(t.src, x.src, t.dst, x.dst)
		switch {
		case err == nil:
			moved++
		case errors.Is(err, syscall.EXDEV):
			cross = append(cross, x)
		case isPerm(err) && len(pairs) == 1:
			err = s.elevatedTransfer(r, t, "move", x)
			s.record(r, "files.move", t.sref, x.src, err, map[string]string{"to": t.destAbs(x.dst)})
			if err != nil {
				return fsErr(err, x.src)
			}
			moved++
		default:
			s.record(r, "files.move", t.sref, x.src, err, map[string]string{"to": t.destAbs(x.dst)})
			return fsErr(err, x.src)
		}
	}
	dest := fsops.Clean(req.Dest)
	if moved > 0 {
		s.record(r, "files.move", t.dref, dest, nil, map[string]any{"count": moved})
	}
	out := map[string]any{"moved": moved}
	if len(cross) > 0 {
		out["jobId"] = s.transferJob(r, t, "files.move", fmt.Sprintf("Move %s", describe(cross)), dest, cross, true)
	}
	httpx.OK(w, out)
	return nil
}

// transferJob copies (and for moves removes) pairs in the background.
func (s *Service) transferJob(r *http.Request, t *transferRoots, kind, title, dest string, pairs []pair, move bool) string {
	dref, cross := t.dref, t.cross
	return s.startJob(r, kind, title, t.sref, t.destAbs(dest), map[string]any{"count": len(pairs)},
		func(ctx context.Context, root *os.Root, job *jobs.Job) (map[string]any, error) {
			dst := root
			if cross {
				d, err := openRoot(dref.Path)
				if err != nil {
					return nil, err
				}
				defer d.Close()
				dst = d
			}
			res, err := transfer(ctx, root, dst, job, pairs, move)
			if res != nil {
				res["destRoot"] = dref.ID
			}
			return res, err
		})
}

func describe(p []pair) string {
	if len(p) == 1 {
		return baseName(p[0].src)
	}
	return fmt.Sprintf("%d items", len(p))
}

// transfer copies pairs from src to dst with byte progress; move removes
// the sources. src and dst may be the same root.
func transfer(ctx context.Context, src, dst *os.Root, job *jobs.Job, pairs []pair, move bool) (map[string]any, error) {
	job.Progress("Scanning", -1, "")
	srcs := make([]string, len(pairs))
	for i, x := range pairs {
		srcs[i] = x.src
	}
	total, files, _ := measure(ctx, src, srcs, 0, 0)
	stage := "Copying"
	if move {
		stage = "Moving"
	}
	var base int64
	kept := 0
	for _, x := range pairs {
		prog := func(done int64, cur string) { job.Progress(stage, frac(base+done, total), cur) }
		var skipped []string
		var copied *fsops.Copied
		var err error
		if move {
			copied, err = fsops.CopyForMove(ctx, src, x.src, dst, x.dst, prog)
			skipped = copied.Skipped
		} else {
			skipped, err = fsops.CopyBetween(ctx, src, x.src, dst, x.dst, prog)
		}
		if err != nil {
			if ctx.Err() != nil {
				dst.RemoveAll(x.dst) // don't leave a half copy behind
			}
			return nil, err
		}
		n, _, _ := fsops.Size(dst, x.dst)
		base += n
		for _, p := range skipped {
			if move {
				job.Log("Not moved (special file): " + p)
			} else {
				job.Log("Not copied (special file): " + p)
			}
		}
		kept += len(skipped)
		if move {
			// Only what was copied goes: special files and anything
			// created or changed during the copy stay in the source.
			changed, err := fsops.RemoveCopied(src, x.src, copied)
			for _, p := range changed {
				job.Log("Not moved (changed during the move): " + p)
			}
			kept += len(changed)
			if err != nil {
				return nil, err
			}
		}
		job.Log(x.src + " → " + x.dst)
	}
	return map[string]any{"bytes": total, "files": files, "skipped": kept}, nil
}

func (s *Service) handleCopy(w http.ResponseWriter, r *http.Request) error {
	var req transferReq
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	t, err := s.openTransfer(req)
	if err != nil {
		return err
	}
	defer t.Close()
	pairs, srcs, err := plan(t, req, true)
	if err != nil {
		return err
	}
	if len(pairs) == 0 {
		httpx.OK(w, map[string]any{"copied": 0})
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	_, _, big := measure(ctx, t.src, srcs, inlineCopyBytes, inlineCopyFiles)
	cancel()
	dest := fsops.Clean(req.Dest)
	if big {
		id := s.transferJob(r, t, "files.copy", fmt.Sprintf("Copy %s", describe(pairs)), dest, pairs, false)
		httpx.OK(w, map[string]any{"copied": 0, "jobId": id})
		return nil
	}
	copied := []string{}
	for _, x := range pairs {
		_, err := fsops.CopyBetween(r.Context(), t.src, x.src, t.dst, x.dst, nil)
		if err != nil && isPerm(err) && len(pairs) == 1 {
			t.dst.RemoveAll(x.dst)
			err = s.elevatedTransfer(r, t, "copy", x)
		}
		s.record(r, "files.copy", t.sref, x.src, err, map[string]string{"to": t.destAbs(x.dst)})
		if err != nil {
			return fsErr(err, x.src)
		}
		copied = append(copied, x.dst)
	}
	httpx.OK(w, map[string]any{"copied": len(copied), "paths": copied})
	return nil
}

// ---- delete ----------------------------------------------------------------

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root  string   `json:"root"`
		Paths []string `json:"paths"`
		Trash bool     `json:"trash"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
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
	if req.Trash {
		return s.trash(w, r, root, ref, paths)
	}
	n := 0
	for _, p := range paths {
		err := fsops.Remove(root, p)
		if err != nil && isPerm(err) && len(paths) == 1 {
			err = s.elevated(r, ref, "remove", p, "", nil)
		}
		s.record(r, "files.delete", ref, p, err, nil)
		if err != nil {
			return fsErr(err, p)
		}
		n++
	}
	httpx.OK(w, map[string]any{"deleted": n})
	return nil
}

func errNoTrash(msg string, paths []string) error {
	return httpx.Errorf(http.StatusConflict, "trash_unavailable", msg).With(map[string]any{"paths": paths})
}

func (s *Service) trash(w http.ResponseWriter, r *http.Request, root *os.Root, ref rootRef, paths []string) error {
	if s.trashDir == "" {
		return errNoTrash("No trash folder is configured", paths)
	}
	trashDir, err := host.Resolve(s.trashDir)
	if err != nil {
		return errNoTrash("Trash folder is not usable: "+errText(err), paths)
	}
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return errNoTrash("Trash folder is not usable: "+errText(err), paths)
	}
	tst, err := os.Stat(trashDir)
	if err != nil {
		return errNoTrash("Trash folder is not usable", paths)
	}
	tdev := uint64(tst.Sys().(*syscall.Stat_t).Dev)
	var other []string
	for _, p := range paths {
		if dev, err := fsops.Dev(root, p); err != nil || dev != tdev {
			other = append(other, p)
		}
	}
	if len(other) > 0 {
		return errNoTrash("The trash is on a different filesystem. Delete permanently instead?", other)
	}
	stamp := time.Now().Format("20060102-150405")
	var moved []string
	for _, p := range paths {
		dst, err := trashName(trashDir, stamp, baseName(p))
		if err == nil {
			err = fsops.RenameOut(root, p, dst)
		}
		s.record(r, "files.trash", ref, p, err, map[string]string{"to": filepath.Join(s.trashDir, filepath.Base(dst))})
		if err != nil {
			return fsErr(err, p)
		}
		moved = append(moved, filepath.Base(dst))
	}
	httpx.OK(w, map[string]any{"deleted": len(moved), "trashed": moved})
	return nil
}

// trashName picks a free name in trashDir for an item called name. The
// stamp prefix is kept within the 255-byte name limit (long names used to
// make every candidate fail with ENAMETOOLONG, and the search never ended).
func trashName(trashDir, stamp, name string) (string, error) {
	for i := 1; i <= 1000; i++ {
		prefix := stamp + "-"
		if i > 1 {
			prefix = fmt.Sprintf("%s-%d-", stamp, i)
		}
		dst := filepath.Join(trashDir, sideName(prefix, name, ""))
		_, err := os.Lstat(dst)
		if errors.Is(err, fs.ErrNotExist) {
			return dst, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("trash: no free name for %s: %w", name, fs.ErrExist)
}

// ---- chmod / chown ---------------------------------------------------------

// eachPath applies fn to p and, when recursive, to everything below it
// (symlinks are never followed or changed).
func eachPath(root *os.Root, p string, recursive bool, fn func(string) error) error {
	if !recursive {
		return fn(p)
	}
	return fs.WalkDir(root.FS(), p, func(q string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return fn(q)
	})
}

func (s *Service) handleChmod(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root      string   `json:"root"`
		Paths     []string `json:"paths"`
		Mode      string   `json:"mode"`
		DirMode   string   `json:"dirMode"` // optional separate mode for folders when recursive
		Recursive bool     `json:"recursive"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	mode, err := fsops.ParseMode(strings.TrimSpace(req.Mode))
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	dirMode := mode
	if req.DirMode != "" {
		if dirMode, err = fsops.ParseMode(req.DirMode); err != nil {
			return httpx.BadRequest(err.Error())
		}
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
		err := eachPath(root, p, req.Recursive, func(q string) error {
			m := mode
			if st, err := root.Lstat(q); err == nil && st.IsDir() {
				m = dirMode
			}
			return chmodNoFollow(root, q, m)
		})
		if err != nil && isPerm(err) && len(paths) == 1 && !req.Recursive {
			err = s.elevated(r, ref, "chmod", p, strings.TrimSpace(req.Mode), nil)
		}
		s.record(r, "files.chmod", ref, p, err, map[string]any{"mode": req.Mode, "recursive": req.Recursive})
		if err != nil {
			return fsErr(err, p)
		}
	}
	httpx.OK(w, map[string]any{"changed": len(paths)})
	return nil
}

// chmodNoFollow changes q's mode like the root helper does: no symlink is
// followed, neither in a folder of the path nor as q itself.
func chmodNoFollow(root *os.Root, q string, m os.FileMode) error {
	parent, base, err := fsops.PinParent(root, q)
	if err != nil {
		return err
	}
	defer parent.Close()
	return fsops.ChmodNoFollow(parent, base, m)
}

func (s *Service) handleChown(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root      string   `json:"root"`
		Paths     []string `json:"paths"`
		Owner     string   `json:"owner"`
		Recursive bool     `json:"recursive"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	owner := strings.TrimSpace(req.Owner)
	if owner == "" || strings.ContainsAny(owner, " /\x00") {
		return httpx.BadRequest("owner must be user[:group]")
	}
	uid, gid, err := fsops.ParseOwner(owner)
	if err != nil {
		return httpx.BadRequest(err.Error())
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
		err := eachPath(root, p, req.Recursive, func(q string) error { return root.Lchown(q, uid, gid) })
		if err != nil && isPerm(err) && len(paths) == 1 {
			if req.Recursive {
				err = httpx.Errorf(http.StatusForbidden, "permission_denied",
					"Changing ownership recursively needs root; change one item at a time or use a root terminal")
			} else {
				err = s.elevated(r, ref, "chown", p, owner, nil)
			}
		}
		s.record(r, "files.chown", ref, p, err, map[string]any{"owner": owner, "recursive": req.Recursive})
		if err != nil {
			return fsErr(err, p)
		}
	}
	httpx.OK(w, map[string]any{"changed": len(paths)})
	return nil
}

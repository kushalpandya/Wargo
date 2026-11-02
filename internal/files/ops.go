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
		root, err := os.OpenRoot(ref.Path)
		if err == nil {
			res, err = fn(ctx, root, job)
			root.Close()
		}
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
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
	Dest  string   `json:"dest"`
	// OnConflict: "error" (default), "rename" (keep both) or "skip".
	OnConflict string `json:"onConflict"`
}

type pair struct{ src, dst string }

// plan validates a move/copy and resolves destination names.
func plan(root *os.Root, req transferReq, isCopy bool) ([]pair, []string, error) {
	paths, err := cleanPaths(req.Paths)
	if err != nil {
		return nil, nil, err
	}
	dest := fsops.Clean(req.Dest)
	st, err := root.Stat(dest)
	if err != nil {
		return nil, nil, fsErr(err, dest)
	}
	if !st.IsDir() {
		return nil, nil, httpx.Errorf(http.StatusBadRequest, "not_dir", "Destination is not a folder")
	}
	var out []pair
	var conflicts []string
	for _, p := range paths {
		if _, err := root.Lstat(p); err != nil {
			return nil, nil, fsErr(err, p)
		}
		if fsops.Inside(dest, p) {
			return nil, nil, fsErr(fsops.ErrIntoSelf, p)
		}
		name := baseName(p)
		dst := join(dest, name)
		if dst == p {
			if !isCopy {
				continue // already there
			}
			dst = join(dest, uniqueName(root, dest, name)) // duplicate in place
		} else if _, err := root.Lstat(dst); err == nil {
			switch req.OnConflict {
			case "rename":
				dst = join(dest, uniqueName(root, dest, name))
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

func (s *Service) handleMove(w http.ResponseWriter, r *http.Request) error {
	var req transferReq
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	pairs, _, err := plan(root, req, false)
	if err != nil {
		return err
	}
	var cross []pair
	moved := 0
	for _, x := range pairs {
		err := root.Rename(x.src, x.dst)
		switch {
		case err == nil:
			moved++
		case errors.Is(err, syscall.EXDEV):
			cross = append(cross, x)
		case isPerm(err) && len(pairs) == 1:
			err = s.elevated(r, ref, "move", x.src, x.dst, nil)
			s.record(r, "files.move", ref, x.src, err, map[string]string{"to": x.dst})
			if err != nil {
				return fsErr(err, x.src)
			}
			moved++
		default:
			s.record(r, "files.move", ref, x.src, err, map[string]string{"to": x.dst})
			return fsErr(err, x.src)
		}
	}
	if moved > 0 {
		s.record(r, "files.move", ref, fsops.Clean(req.Dest), nil, map[string]any{"count": moved})
	}
	out := map[string]any{"moved": moved}
	if len(cross) > 0 {
		out["jobId"] = s.startJob(r, "files.move", fmt.Sprintf("Move %s", describe(cross)), ref, fsops.Clean(req.Dest),
			map[string]any{"count": len(cross)}, func(ctx context.Context, root *os.Root, job *jobs.Job) (map[string]any, error) {
				return transfer(ctx, root, job, cross, true)
			})
	}
	httpx.OK(w, out)
	return nil
}

func describe(p []pair) string {
	if len(p) == 1 {
		return baseName(p[0].src)
	}
	return fmt.Sprintf("%d items", len(p))
}

// transfer copies pairs with byte progress; move removes the sources.
func transfer(ctx context.Context, root *os.Root, job *jobs.Job, pairs []pair, move bool) (map[string]any, error) {
	job.Progress("Scanning", -1, "")
	srcs := make([]string, len(pairs))
	for i, x := range pairs {
		srcs[i] = x.src
	}
	total, files, _ := measure(ctx, root, srcs, 0, 0)
	stage := "Copying"
	if move {
		stage = "Moving"
	}
	var base int64
	for _, x := range pairs {
		err := fsops.CopyContext(ctx, root, x.src, x.dst, func(done int64, cur string) {
			job.Progress(stage, frac(base+done, total), cur)
		})
		if err != nil {
			if ctx.Err() != nil {
				root.RemoveAll(x.dst) // don't leave a half copy behind
			}
			return nil, err
		}
		n, _, _ := fsops.Size(root, x.dst)
		base += n
		if move {
			if err := root.RemoveAll(x.src); err != nil {
				return nil, err
			}
		}
		job.Log(x.src + " → " + x.dst)
	}
	return map[string]any{"bytes": total, "files": files}, nil
}

func (s *Service) handleCopy(w http.ResponseWriter, r *http.Request) error {
	var req transferReq
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	pairs, srcs, err := plan(root, req, true)
	if err != nil {
		return err
	}
	if len(pairs) == 0 {
		httpx.OK(w, map[string]any{"copied": 0})
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	_, _, big := measure(ctx, root, srcs, inlineCopyBytes, inlineCopyFiles)
	cancel()
	dest := fsops.Clean(req.Dest)
	if big {
		id := s.startJob(r, "files.copy", fmt.Sprintf("Copy %s", describe(pairs)), ref, dest,
			map[string]any{"count": len(pairs)}, func(ctx context.Context, root *os.Root, job *jobs.Job) (map[string]any, error) {
				return transfer(ctx, root, job, pairs, false)
			})
		httpx.OK(w, map[string]any{"copied": 0, "jobId": id})
		return nil
	}
	copied := []string{}
	for _, x := range pairs {
		err := fsops.CopyContext(r.Context(), root, x.src, x.dst, nil)
		if err != nil && isPerm(err) && len(pairs) == 1 {
			root.RemoveAll(x.dst)
			err = s.elevated(r, ref, "copy", x.src, x.dst, nil)
		}
		s.record(r, "files.copy", ref, x.src, err, map[string]string{"to": x.dst})
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
	if err := os.MkdirAll(s.trashDir, 0o700); err != nil {
		return errNoTrash("Trash folder is not usable: "+errText(err), paths)
	}
	tst, err := os.Stat(s.trashDir)
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
		name := stamp + "-" + baseName(p)
		dst := filepath.Join(s.trashDir, name)
		for i := 2; ; i++ {
			if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
				break
			}
			dst = filepath.Join(s.trashDir, fmt.Sprintf("%s-%d-%s", stamp, i, baseName(p)))
		}
		err := fsops.RenameOut(root, p, dst)
		s.record(r, "files.trash", ref, p, err, map[string]string{"to": dst})
		if err != nil {
			return fsErr(err, p)
		}
		moved = append(moved, filepath.Base(dst))
	}
	httpx.OK(w, map[string]any{"deleted": len(moved), "trashed": moved})
	return nil
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
			return root.Chmod(q, m)
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

// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"archive/zip"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// inlineType returns the Content-Type to use when previewing name inline,
// or "" if it must not be shown inline. Active formats (HTML, SVG, XML) are
// downgraded to plain text so they can never execute.
func inlineType(name string) string {
	m := mimeOf(name)
	switch {
	case m == "image/svg+xml", m == "text/html", m == "text/xml", m == "application/xhtml+xml":
		return "text/plain; charset=utf-8"
	case strings.HasPrefix(m, "image/"), strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return m
	case m == "application/pdf":
		return m
	case strings.HasPrefix(m, "text/"), m == "application/json":
		return "text/plain; charset=utf-8"
	}
	return ""
}

func disposition(kind, name string) string {
	v := mime.FormatMediaType(kind, map[string]string{"filename": name})
	if v == "" {
		v = kind
	}
	return v
}

func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	root, ref, err := s.open(q.Get("root"))
	if err != nil {
		return err
	}
	defer root.Close()
	p := fsops.Clean(q.Get("path"))
	info, err := root.Stat(p)
	if err != nil {
		return ref.readErr(err, p)
	}
	if info.IsDir() {
		name := baseName(p)
		if p == "." {
			name = ref.Name
		}
		return s.streamZip(w, r, root, []string{p}, name+".zip")
	}
	if !info.Mode().IsRegular() {
		return httpx.BadRequest("not a regular file")
	}
	f, info, err := openRegular(root, p)
	if err != nil {
		return ref.readErr(err, p)
	}
	defer f.Close()
	name := baseName(p)
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-cache")
	ct := ""
	if q.Get("inline") == "1" {
		ct = inlineType(name)
	}
	if ct != "" {
		h.Set("Content-Type", ct)
		h.Set("Content-Disposition", disposition("inline", name))
		h.Set("X-Frame-Options", "SAMEORIGIN")
		if ct == "application/pdf" {
			// The browser's PDF viewer refuses to run in a sandboxed document.
			h.Set("Content-Security-Policy", "default-src 'none'; object-src 'self'; frame-ancestors 'self'")
		} else {
			h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'self'")
		}
	} else {
		ct = mimeOf(name)
		if ct == "" || inlineType(name) == "text/plain; charset=utf-8" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		h.Set("Content-Disposition", disposition("attachment", name))
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
	return nil
}

func (s *Service) handleDownloadZip(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root  string   `json:"root"`
		Paths []string `json:"paths"`
		Name  string   `json:"name"`
	}
	switch {
	case r.Method == http.MethodGet:
		q := r.URL.Query()
		req.Root, req.Paths, req.Name = q.Get("root"), q["path"], q.Get("name")
	case strings.HasPrefix(r.Header.Get("Content-Type"), "application/json"):
		if err := httpx.Decode(r, &req); err != nil {
			return err
		}
	default:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			return httpx.BadRequest("invalid form")
		}
		req.Root, req.Paths, req.Name = r.PostForm.Get("root"), r.PostForm["paths"], r.PostForm.Get("name")
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	if len(req.Paths) == 0 {
		return httpx.BadRequest("no paths given")
	}
	if len(req.Paths) > maxZipPaths {
		return httpx.BadRequest("too many paths")
	}
	paths := make([]string, 0, len(req.Paths))
	seen := map[string]bool{}
	for _, p := range req.Paths {
		p = fsops.Clean(p)
		if seen[p] {
			continue // the same entry twice would produce a corrupt zip
		}
		seen[p] = true
		if _, err := root.Lstat(p); err != nil {
			return ref.readErr(err, p)
		}
		paths = append(paths, p)
	}
	name := req.Name
	if name == "" || validName(name) != nil {
		switch {
		case len(paths) == 1 && paths[0] != ".":
			name = baseName(paths[0])
		case parentOf(paths[0]) != ".":
			name = baseName(parentOf(paths[0]))
		default:
			name = ref.Name
		}
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	return s.streamZip(w, r, root, paths, name)
}

// streamZip writes a zip of paths straight to the response (no temp file).
func (s *Service) streamZip(w http.ResponseWriter, r *http.Request, root *os.Root, paths []string, name string) error {
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", disposition("attachment", name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	err := writeZip(r.Context(), zw, root, paths, nil)
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		slog.Warn("files: zip stream aborted", "err", err)
		panic(http.ErrAbortHandler) // signal a truncated download to the client
	}
	return nil
}

// maxZipPaths bounds the explicit selection of a zip download.
const maxZipPaths = 10000

// precompressed formats are stored instead of deflated.
var precompressed = map[string]bool{
	".zip": true, ".gz": true, ".tgz": true, ".xz": true, ".bz2": true, ".zst": true, ".7z": true, ".rar": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true, ".heic": true,
	".mp4": true, ".mkv": true, ".webm": true, ".mov": true, ".m4v": true, ".avi": true,
	".mp3": true, ".flac": true, ".ogg": true, ".opus": true, ".m4a": true, ".aac": true, ".pdf": true,
}

// writeZip adds each path (recursively) to zw, named relative to its
// parent folder. Symlinks and special files are skipped. prog receives the
// number of bytes read so far.
func writeZip(ctx context.Context, zw *zip.Writer, root *os.Root, paths []string, prog fsops.Progress) error {
	var done int64
	for _, item := range paths {
		base := parentOf(item)
		if item == "." {
			base = "."
		}
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
			if rel == "." {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if d.IsDir() {
				fh, _ := zip.FileInfoHeader(info)
				fh.Name = rel + "/"
				fh.Method = zip.Store
				_, err := zw.CreateHeader(fh)
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			fh, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			fh.Name = rel
			fh.Method = zip.Deflate
			if precompressed[strings.ToLower(path.Ext(rel))] {
				fh.Method = zip.Store
			}
			f, _, err := openRegular(root, p)
			if err != nil {
				return err
			}
			defer f.Close()
			out, err := zw.CreateHeader(fh)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, &progressReader{ctx: ctx, r: f, done: &done, prog: prog, name: p})
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type progressReader struct {
	ctx  context.Context
	r    io.Reader
	done *int64
	prog fsops.Progress
	name string
	last time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	*p.done += int64(n)
	if p.prog != nil {
		p.prog(*p.done, p.name)
	}
	return n, err
}

// SPDX-License-Identifier: AGPL-3.0-only

// Package files is Wargo's file manager. Every operation is confined to a
// configured root with os.Root, so neither ".." nor symlinks can reach
// outside it. Permission failures on single-path mutations are retried
// through the privileged helper after the user approves the elevation.
package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/server"
)

// Auditor records mutating actions (satisfied by *audit.Log).
type Auditor interface {
	Record(r *http.Request, action, target string, err error, details any)
}

// Elevator runs privileged helper commands (satisfied by *elevate.Service).
type Elevator interface {
	Run(r *http.Request, action, target string, argv []string, stdin io.Reader) ([]byte, error)
	Helper(args ...string) []string
}

// Options configure a Service; New fills them from server.Deps.
type Options struct {
	Roots       []config.Root
	DataDir     string
	TrashDir    string
	MaxEditSize int64
	// ShowMounts offers mounted filesystems as additional roots.
	ShowMounts bool
	Jobs       *jobs.Manager
	Audit      Auditor
	Elevate    Elevator
}

type Service struct {
	roots    []config.Root
	trashDir string
	maxEdit  int64
	jobs     *jobs.Manager
	audit    Auditor
	elev     Elevator
	uploads  *uploads
	owners   *ownerCache

	showMounts bool
	mounts     mountCache
}

// Limits (variables so tests can shrink them).
var (
	maxListEntries        = 20000
	inlineCopyBytes int64 = 32 << 20
	inlineCopyFiles       = 500
	maxExtractBytes int64 = 20 << 30
	maxExtractFiles       = 200000
	sizeTimeout           = 10 * time.Second
)

// New builds the service from shared deps.
func New(d *server.Deps) *Service {
	return NewWithOptions(Options{
		Roots: d.Cfg.Files.Roots, DataDir: d.Cfg.DataDir, TrashDir: d.Cfg.Files.TrashDir,
		MaxEditSize: d.Cfg.Files.MaxEditSize, ShowMounts: d.Cfg.Files.ShowMounts, Jobs: d.Jobs, Audit: d.Audit, Elevate: d.Elevate,
	})
}

func NewWithOptions(o Options) *Service {
	s := &Service{trashDir: o.TrashDir, maxEdit: o.MaxEditSize, jobs: o.Jobs, audit: o.Audit, elev: o.Elevate, owners: newOwnerCache(), showMounts: o.ShowMounts}
	if s.maxEdit <= 0 {
		s.maxEdit = 4 << 20
	}
	for _, r := range o.Roots {
		p, err := filepath.Abs(r.Path)
		if err != nil || r.Path == "" {
			slog.Warn("files: ignoring root", "path", r.Path, "err", err)
			continue
		}
		name := r.Name
		if name == "" {
			name = filepath.Base(p)
		}
		s.roots = append(s.roots, config.Root{Name: name, Path: p})
	}
	s.uploads = newUploads(filepath.Join(o.DataDir, "uploads"))
	return s
}

func (s *Service) Capabilities(context.Context) (string, any) {
	roots := []map[string]any{}
	for i, r := range s.roots {
		roots = append(roots, map[string]any{"id": strconv.Itoa(i), "kind": "place", "name": r.Name, "path": r.Path})
	}
	for _, d := range s.drives(false) {
		roots = append(roots, map[string]any{"id": d.ID, "kind": "drive", "name": d.Label, "path": d.Path})
	}
	return "files", map[string]any{
		"roots": roots, "trash": s.trashDir != "", "maxEditSize": s.maxEdit, "chunkSize": chunkSize,
		"showMounts": s.showMounts, "crossRoot": true,
	}
}

// Run expires stale uploads.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	s.uploads.expire(uploadTTL)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.uploads.expire(uploadTTL)
		}
	}
}

func (s *Service) Register(mux *http.ServeMux) {
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) error) {
		mux.HandleFunc(pattern, httpx.Handler(fn))
	}
	h("GET /api/files/roots", s.handleRoots)
	h("GET /api/files/list", s.handleList)
	h("GET /api/files/stat", s.handleStat)
	h("GET /api/files/size", s.handleSize)
	h("GET /api/files/download", s.handleDownload)
	h("GET /api/files/download-zip", s.handleDownloadZip)
	h("POST /api/files/download-zip", s.handleDownloadZip)
	h("GET /api/files/content", s.handleGetContent)
	h("PUT /api/files/content", s.handlePutContent)
	h("POST /api/files/mkdir", s.handleMkdir)
	h("POST /api/files/rename", s.handleRename)
	h("POST /api/files/move", s.handleMove)
	h("POST /api/files/copy", s.handleCopy)
	h("POST /api/files/delete", s.handleDelete)
	h("POST /api/files/chmod", s.handleChmod)
	h("POST /api/files/chown", s.handleChown)
	h("POST /api/files/archive", s.handleArchive)
	h("POST /api/files/extract", s.handleExtract)
	h("POST /api/files/upload/init", s.handleUploadInit)
	h("PUT /api/files/upload/{id}", s.handleUploadChunk)
	h("POST /api/files/upload/{id}/complete", s.handleUploadComplete)
	h("DELETE /api/files/upload/{id}", s.handleUploadCancel)
	h("GET /api/files/uploads", s.handleUploadList)
}

// ---- roots -----------------------------------------------------------------

type rootRef struct {
	ID   string
	Name string
	Path string
}

func (s *Service) placeRef(i int) rootRef {
	return rootRef{strconv.Itoa(i), s.roots[i].Name, s.roots[i].Path}
}

func (s *Service) lookupRoot(v string) (rootRef, error) {
	if v == "" {
		if len(s.roots) > 0 {
			return s.placeRef(0), nil
		}
		if ds := s.drives(false); len(ds) > 0 {
			return rootRef{ds[0].ID, ds[0].Label, ds[0].Path}, nil
		}
	}
	if i, err := strconv.Atoi(v); err == nil && i >= 0 && i < len(s.roots) {
		return s.placeRef(i), nil
	}
	// Mount ids are only honoured for filesystems that are mounted right now,
	// so a crafted id can't turn an arbitrary directory into a root.
	if p, ok := parseMountID(v); ok && s.showMounts {
		for _, fresh := range []bool{false, true} {
			for _, d := range s.drives(fresh) {
				if d.Path == p {
					return rootRef{d.ID, d.Label, d.Path}, nil
				}
			}
		}
		return rootRef{}, httpx.Errorf(http.StatusNotFound, "unknown_root", "That drive is no longer mounted")
	}
	for i, r := range s.roots {
		if r.Name == v {
			return s.placeRef(i), nil
		}
	}
	return rootRef{}, httpx.Errorf(http.StatusNotFound, "unknown_root", "Unknown root "+strconv.Quote(v))
}

// open resolves a root reference and opens it. Callers must Close.
func (s *Service) open(v string) (*os.Root, rootRef, error) {
	ref, err := s.lookupRoot(v)
	if err != nil {
		return nil, ref, err
	}
	root, err := os.OpenRoot(ref.Path)
	if err != nil {
		return nil, ref, fsErr(err, ref.Path)
	}
	return root, ref, nil
}

func statInto(m map[string]any, p string) {
	st, err := statfs(p)
	if err != nil {
		m["error"] = errText(err)
		return
	}
	bs := uint64(st.Bsize)
	m["total"], m["free"], m["used"] = st.Blocks*bs, st.Bavail*bs, (st.Blocks-st.Bfree)*bs
	if st.Flags&1 != 0 { // ST_RDONLY
		m["readOnly"] = true
	}
}

func (s *Service) handleRoots(w http.ResponseWriter, r *http.Request) error {
	out := []map[string]any{}
	for i, rt := range s.roots {
		m := map[string]any{"id": strconv.Itoa(i), "kind": "place", "name": rt.Name, "path": rt.Path}
		statInto(m, rt.Path)
		out = append(out, m)
	}
	drives := s.drives(true)
	ms := make([]map[string]any, len(drives))
	var wg sync.WaitGroup
	for i, d := range drives {
		m := map[string]any{
			"id": d.ID, "kind": "drive", "name": d.Label, "path": d.Path, "device": d.Device,
			"fstype": d.FSType, "readOnly": d.ReadOnly, "removable": d.Removable, "network": d.Network,
		}
		ms[i] = m
		wg.Add(1)
		go func() {
			defer wg.Done()
			ro := d.ReadOnly
			statInto(m, d.Path)
			if ro {
				m["readOnly"] = true
			}
		}()
	}
	wg.Wait()
	out = append(out, ms...)
	httpx.OK(w, out)
	return nil
}

// ---- errors ----------------------------------------------------------------

// fsErr converts filesystem errors into API errors.
func fsErr(err error, rel string) error {
	if err == nil {
		return nil
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return err
	}
	msg := func(what string) string {
		if rel == "" || rel == "." {
			return what
		}
		return what + ": " + rel
	}
	switch {
	case errors.Is(err, fsops.ErrIntoSelf):
		return httpx.Errorf(http.StatusBadRequest, "into_self", "Cannot copy or move a folder into itself")
	case strings.Contains(err.Error(), "path escapes from parent"):
		return httpx.Errorf(http.StatusForbidden, "outside_root", msg("Path leads outside the root"))
	case errors.Is(err, fs.ErrNotExist):
		return httpx.Errorf(http.StatusNotFound, "not_found", msg("No such file or folder"))
	case errors.Is(err, fs.ErrExist):
		return httpx.Errorf(http.StatusConflict, "exists", msg("Already exists"))
	case errors.Is(err, fs.ErrPermission):
		return httpx.Errorf(http.StatusForbidden, "permission_denied", msg("Permission denied"))
	case errors.Is(err, syscall.ENOTDIR):
		return httpx.Errorf(http.StatusBadRequest, "not_dir", msg("Not a folder"))
	case errors.Is(err, syscall.EISDIR):
		return httpx.Errorf(http.StatusBadRequest, "is_dir", msg("Is a folder"))
	case errors.Is(err, syscall.ENOTEMPTY):
		return httpx.Errorf(http.StatusConflict, "not_empty", msg("Folder is not empty"))
	case errors.Is(err, syscall.ENOSPC):
		return httpx.Errorf(http.StatusInsufficientStorage, "no_space", "No space left on device")
	case errors.Is(err, syscall.EROFS):
		return httpx.Errorf(http.StatusForbidden, "read_only", "Read-only filesystem")
	case errors.Is(err, context.DeadlineExceeded):
		return httpx.Errorf(http.StatusGatewayTimeout, "timeout", "Operation timed out")
	}
	return httpx.Errorf(http.StatusInternalServerError, "fs_error", msg(errText(err)))
}

func errText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err.Error()
	}
	return err.Error()
}

func isPerm(err error) bool { return errors.Is(err, fs.ErrPermission) }

// ---- helpers ---------------------------------------------------------------

// elevated retries a failed single-path operation through the root helper.
// Without a credential on the request the client receives
// elevation_required, prompts the user and repeats the request.
func (s *Service) elevated(r *http.Request, ref rootRef, op, rel, arg string, stdin io.Reader) error {
	if s.elev == nil {
		return httpx.Errorf(http.StatusForbidden, "permission_denied", "Permission denied: "+rel)
	}
	args := []string{"fs", op, ref.Path, rel}
	if arg != "" {
		args = append(args, arg)
	}
	_, err := s.elev.Run(r, "files.op", op+" "+filepath.Join(ref.Path, rel), s.elev.Helper(args...), stdin)
	return err
}

// tryElevated runs fn, falling back to the helper on permission errors.
func (s *Service) tryElevated(r *http.Request, ref rootRef, op, rel, arg string, fn func() error) error {
	err := fn()
	if err != nil && isPerm(err) {
		return s.elevated(r, ref, op, rel, arg, nil)
	}
	return err
}

func (s *Service) record(r *http.Request, action string, ref rootRef, target string, err error, details any) {
	if s.audit != nil {
		s.audit.Record(r, action, filepath.Join(ref.Path, target), err, details)
	}
}

// decode reads a JSON body with a custom size limit.
func decode(r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	if err := dec.Decode(v); err != nil {
		return httpx.BadRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

// validName checks a single path component supplied by the client.
func validName(name string) error {
	switch {
	case name == "", name == ".", name == "..":
		return httpx.BadRequest("invalid name")
	case strings.ContainsAny(name, "/\x00"):
		return httpx.BadRequest(`name must not contain "/"`)
	case len(name) > 255:
		return httpx.BadRequest("name is too long")
	}
	return nil
}

// cleanPaths normalises a list of client paths and rejects the root itself.
func cleanPaths(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, httpx.BadRequest("no paths given")
	}
	if len(in) > 10000 {
		return nil, httpx.BadRequest("too many paths")
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		c := fsops.Clean(p)
		if c == "." {
			return nil, httpx.BadRequest("the root itself cannot be used here")
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func parentOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

func join(dir, name string) string {
	if dir == "." || dir == "" {
		return fsops.Clean(name)
	}
	return fsops.Clean(dir + "/" + name)
}

// uniqueName returns name, or "name (2).ext", "name (3).ext"... so that it
// doesn't exist in dir.
func uniqueName(root *os.Root, dir, name string) string {
	if _, err := root.Lstat(join(dir, name)); errors.Is(err, fs.ErrNotExist) {
		return name
	}
	stem, ext := splitExt(name)
	for i := 2; i < 10000; i++ {
		n := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := root.Lstat(join(dir, n)); errors.Is(err, fs.ErrNotExist) {
			return n
		}
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext)
}

func splitExt(name string) (string, string) {
	low := strings.ToLower(name)
	for _, e := range []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.zst"} {
		if strings.HasSuffix(low, e) && len(name) > len(e) {
			return name[:len(name)-len(e)], name[len(name)-len(e):]
		}
	}
	ext := filepath.Ext(name)
	if ext == name {
		return name, ""
	}
	return strings.TrimSuffix(name, ext), ext
}

// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
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

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Chunks stay well below Cloudflare's 100 MB request body limit.
const (
	chunkSize = 8 << 20
	uploadTTL = 24 * time.Hour
)

// upload is a resumable upload in progress. The data accumulates in
// <dataDir>/uploads/<id>.part, described by <id>.json.
type upload struct {
	ID        string    `json:"id"`
	RootPath  string    `json:"rootPath"`
	Dir       string    `json:"dir"`
	Name      string    `json:"name"` // may contain "/" for folder uploads
	Size      int64     `json:"size"`
	Overwrite bool      `json:"overwrite"`
	Created   time.Time `json:"created"`

	mu sync.Mutex
}

type uploads struct {
	dir    string
	mu     sync.Mutex
	m      map[string]*upload
	loaded bool
}

func newUploads(dir string) *uploads { return &uploads{dir: dir, m: map[string]*upload{}} }

func (u *uploads) part(id string) string    { return filepath.Join(u.dir, id+".part") }
func (u *uploads) sidecar(id string) string { return filepath.Join(u.dir, id+".json") }

// load reads sidecars from disk once (call with u.mu held).
func (u *uploads) load() {
	if u.loaded {
		return
	}
	u.loaded = true
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		slog.Warn("files: uploads dir", "err", err)
		return
	}
	ents, _ := os.ReadDir(u.dir)
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !validID(id) {
			continue
		}
		b, err := os.ReadFile(u.sidecar(id))
		if err != nil {
			continue
		}
		var up upload
		if json.Unmarshal(b, &up) != nil || up.ID != id {
			continue
		}
		if _, err := os.Stat(u.part(id)); err != nil {
			os.Remove(u.sidecar(id))
			continue
		}
		u.m[id] = &up
	}
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (u *uploads) get(id string) *upload {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.load()
	return u.m[id]
}

func (u *uploads) received(id string) int64 {
	st, err := os.Stat(u.part(id))
	if err != nil {
		return 0
	}
	return st.Size()
}

func (u *uploads) remove(id string) {
	u.mu.Lock()
	delete(u.m, id)
	u.mu.Unlock()
	os.Remove(u.part(id))
	os.Remove(u.sidecar(id))
}

// expire drops uploads whose data hasn't changed for ttl.
func (u *uploads) expire(ttl time.Duration) {
	u.mu.Lock()
	u.load()
	var stale []string
	for id := range u.m {
		st, err := os.Stat(u.part(id))
		if err != nil || time.Since(st.ModTime()) > ttl {
			stale = append(stale, id)
		}
	}
	u.mu.Unlock()
	for _, id := range stale {
		slog.Info("files: expiring stale upload", "id", id)
		u.remove(id)
	}
	// Orphaned files without a sidecar.
	ents, _ := os.ReadDir(u.dir)
	for _, e := range ents {
		id, _, _ := strings.Cut(e.Name(), ".")
		if u.get(id) == nil {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				os.Remove(filepath.Join(u.dir, e.Name()))
			}
		}
	}
}

func freeSpace(p string) (uint64, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(host.Path(p), &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

// freeSpaceLocal is freeSpace for Wargo's own (container) paths.
func freeSpaceLocal(p string) (uint64, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(p, &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

// uploadTarget validates dir/name and returns the root-relative target.
func uploadTarget(dir, name string) (string, error) {
	rel := fsops.Clean(name)
	if rel == "." {
		return "", httpx.BadRequest("name required")
	}
	for _, seg := range strings.Split(rel, "/") {
		if err := validName(seg); err != nil {
			return "", err
		}
	}
	return join(fsops.Clean(dir), rel), nil
}

func (s *Service) handleUploadInit(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root      string `json:"root"`
		Dir       string `json:"dir"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if req.Size < 0 {
		return httpx.BadRequest("invalid size")
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	dir := fsops.Clean(req.Dir)
	target, err := uploadTarget(dir, req.Name)
	if err != nil {
		return err
	}
	if st, err := root.Stat(dir); err != nil {
		return fsErr(err, dir)
	} else if !st.IsDir() {
		return fsErr(syscall.ENOTDIR, dir)
	}
	if st, err := root.Lstat(target); err == nil {
		if st.IsDir() {
			return httpx.Errorf(http.StatusConflict, "is_dir", "A folder with this name exists: "+target)
		}
		if !req.Overwrite {
			return httpx.Errorf(http.StatusConflict, "exists", "Already exists: "+target).With(map[string]string{"path": target})
		}
	}
	name := strings.TrimPrefix(target, dir+"/")
	if dir == "." {
		name = target
	}

	s.uploads.mu.Lock()
	s.uploads.load()
	var up *upload
	for _, x := range s.uploads.m {
		if x.RootPath == ref.Path && x.Dir == dir && x.Name == name && x.Size == req.Size {
			up = x
			break
		}
	}
	if up != nil {
		up.Overwrite = req.Overwrite
		s.uploads.mu.Unlock()
		httpx.OK(w, map[string]any{"uploadId": up.ID, "chunkSize": chunkSize, "received": s.uploads.received(up.ID), "size": up.Size, "resumed": true})
		return nil
	}
	s.uploads.mu.Unlock()

	if free, ok := freeSpace(ref.Path); ok && uint64(req.Size) > free {
		return httpx.Errorf(http.StatusInsufficientStorage, "no_space", "Not enough free space in "+ref.Name)
	}
	if err := os.MkdirAll(s.uploads.dir, 0o700); err != nil {
		return err
	}
	if free, ok := freeSpaceLocal(s.uploads.dir); ok && uint64(req.Size) > free {
		return httpx.Errorf(http.StatusInsufficientStorage, "no_space", "Not enough free space for the upload staging area")
	}
	b := make([]byte, 16)
	rand.Read(b)
	up = &upload{ID: hex.EncodeToString(b), RootPath: ref.Path, Dir: dir, Name: name, Size: req.Size, Overwrite: req.Overwrite, Created: time.Now()}
	f, err := os.OpenFile(s.uploads.part(up.ID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	meta, _ := json.Marshal(up)
	if err := os.WriteFile(s.uploads.sidecar(up.ID), meta, 0o600); err != nil {
		os.Remove(s.uploads.part(up.ID))
		return err
	}
	s.uploads.mu.Lock()
	s.uploads.m[up.ID] = up
	s.uploads.mu.Unlock()
	httpx.OK(w, map[string]any{"uploadId": up.ID, "chunkSize": chunkSize, "received": 0, "size": up.Size, "resumed": false})
	return nil
}

func (s *Service) getUpload(r *http.Request) (*upload, error) {
	id := r.PathValue("id")
	if !validID(id) {
		return nil, httpx.ErrNotFound
	}
	up := s.uploads.get(id)
	if up == nil {
		return nil, httpx.Errorf(http.StatusNotFound, "upload_not_found", "Upload not found or expired")
	}
	return up, nil
}

func errOffset(cur int64) error {
	return httpx.Errorf(http.StatusConflict, "offset_mismatch", "Upload offset mismatch").With(map[string]int64{"offset": cur})
}

func (s *Service) handleUploadChunk(w http.ResponseWriter, r *http.Request) error {
	up, err := s.getUpload(r)
	if err != nil {
		return err
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		return httpx.BadRequest("offset required")
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	cur := s.uploads.received(up.ID)
	if offset != cur {
		return errOffset(cur)
	}
	limit := int64(chunkSize + 1<<20)
	if r.ContentLength > limit {
		return httpx.Errorf(http.StatusRequestEntityTooLarge, "too_large", "Chunk too large")
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	f, err := os.OpenFile(s.uploads.part(up.ID), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	var h hash.Hash
	var dst io.Writer = f
	want := strings.ToLower(r.Header.Get("X-Chunk-Sha256"))
	if want != "" {
		h = sha256.New()
		dst = io.MultiWriter(f, h)
	}
	n, err := io.Copy(dst, io.LimitReader(body, up.Size-offset+1))
	fail := func(e error) error {
		f.Truncate(offset)
		return e
	}
	switch {
	case err != nil:
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return fail(httpx.Errorf(http.StatusRequestEntityTooLarge, "too_large", "Chunk too large"))
		}
		return fail(httpx.Errorf(http.StatusBadRequest, "upload_interrupted", "Chunk upload interrupted"))
	case offset+n > up.Size:
		return fail(httpx.BadRequest("upload exceeds the declared size"))
	case h != nil && hex.EncodeToString(h.Sum(nil)) != want:
		return fail(httpx.Errorf(http.StatusUnprocessableEntity, "checksum_mismatch", "Chunk checksum mismatch"))
	}
	httpx.OK(w, map[string]int64{"received": offset + n})
	return nil
}

func (s *Service) rootByPath(p string) (rootRef, bool) {
	for i, r := range s.roots {
		if r.Path == p {
			return s.placeRef(i), true
		}
	}
	for _, d := range s.drives(false) {
		if d.Path == p {
			return rootRef{d.ID, d.Label, d.Path}, true
		}
	}
	return rootRef{}, false
}

func (s *Service) handleUploadComplete(w http.ResponseWriter, r *http.Request) error {
	up, err := s.getUpload(r)
	if err != nil {
		return err
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if cur := s.uploads.received(up.ID); cur != up.Size {
		return errOffset(cur)
	}
	ref, ok := s.rootByPath(up.RootPath)
	if !ok {
		s.uploads.remove(up.ID)
		return httpx.Errorf(http.StatusNotFound, "unknown_root", "The upload's root is no longer configured")
	}
	root, err := openRoot(ref.Path)
	if err != nil {
		return fsErr(err, "")
	}
	defer root.Close()
	target := join(up.Dir, up.Name)
	err = s.placeUpload(root, up, target)
	s.record(r, "files.upload", ref, target, err, map[string]any{"size": up.Size, "overwrite": up.Overwrite})
	if err != nil {
		return fsErr(err, target)
	}
	s.uploads.remove(up.ID)
	httpx.OK(w, map[string]any{"path": target, "size": up.Size})
	return nil
}

func (s *Service) placeUpload(root *os.Root, up *upload, target string) error {
	if parent := parentOf(target); parent != "." {
		if err := root.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	if st, err := root.Lstat(target); err == nil {
		if st.IsDir() {
			return syscall.EISDIR
		}
		if !up.Overwrite {
			return fs.ErrExist
		}
	}
	part := s.uploads.part(up.ID)
	if err := os.Chmod(part, 0o644); err != nil {
		return err
	}
	err := fsops.RenameIn(root, part, target)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	// Staging area on another filesystem: copy into a temp file next to
	// the target, then rename into place.
	in, err := os.Open(part)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := join(parentOf(target), "."+baseName(target)+".upload-"+up.ID[:8])
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, target)
	}
	if err != nil {
		root.Remove(tmp)
	}
	return err
}

func (s *Service) handleUploadCancel(w http.ResponseWriter, r *http.Request) error {
	up, err := s.getUpload(r)
	if err != nil {
		return err
	}
	up.mu.Lock()
	s.uploads.remove(up.ID)
	up.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) handleUploadList(w http.ResponseWriter, r *http.Request) error {
	s.uploads.mu.Lock()
	s.uploads.load()
	list := make([]*upload, 0, len(s.uploads.m))
	for _, u := range s.uploads.m {
		list = append(list, u)
	}
	s.uploads.mu.Unlock()
	out := []map[string]any{}
	for _, u := range list {
		ref, ok := s.rootByPath(u.RootPath)
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"id": u.ID, "root": ref.ID, "dir": u.Dir, "name": u.Name, "size": u.Size,
			"received": s.uploads.received(u.ID), "created": u.Created,
		})
	}
	httpx.OK(w, out)
	return nil
}

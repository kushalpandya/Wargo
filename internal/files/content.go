// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/httpx"
)

func isBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0
}

func latin1Decode(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func latin1Encode(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xff {
			return nil, httpx.BadRequest("text contains characters that cannot be saved as Latin-1")
		}
		out = append(out, byte(r))
	}
	return out, nil
}

func (s *Service) handleGetContent(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	root, ref, err := s.open(q.Get("root"))
	if err != nil {
		return err
	}
	defer root.Close()
	p := fsops.Clean(q.Get("path"))
	st, err := root.Stat(p)
	if err != nil {
		return fsErr(err, p)
	}
	if !st.Mode().IsRegular() {
		return httpx.BadRequest("not a regular file")
	}
	if st.Size() > s.maxEdit {
		return httpx.Errorf(http.StatusRequestEntityTooLarge, "too_large", "File is too large to edit").
			With(map[string]int64{"size": st.Size(), "max": s.maxEdit})
	}
	data, err := root.ReadFile(p)
	if err != nil && isPerm(err) {
		data, err = s.elevatedOut(r, ref, "read", p)
	}
	if err != nil {
		return fsErr(err, p)
	}
	if isBinary(data) {
		return httpx.Errorf(http.StatusUnsupportedMediaType, "binary", "This looks like a binary file")
	}
	enc, text := "utf-8", ""
	if utf8.Valid(data) {
		text = string(data)
	} else {
		enc, text = "latin1", latin1Decode(data)
	}
	e := s.entry(root, p, mustLstat(root, p, st))
	httpx.OK(w, map[string]any{
		"content": text, "mtime": st.ModTime(), "size": st.Size(), "encoding": enc,
		"writable": e.Writable, "mode": e.Octal,
	})
	return nil
}

func mustLstat(root *os.Root, p string, fallback fs.FileInfo) fs.FileInfo {
	if st, err := root.Lstat(p); err == nil && st.Mode()&fs.ModeSymlink == 0 {
		return st
	}
	return fallback
}

func (s *Service) elevatedOut(r *http.Request, ref rootRef, op, rel string) ([]byte, error) {
	if s.elev == nil {
		return nil, fs.ErrPermission
	}
	return s.elev.Run(r, "files.op", op+" "+ref.Path+"/"+rel, s.elev.Helper("fs", op, ref.Path, rel), nil)
}

func (s *Service) handlePutContent(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root     string     `json:"root"`
		Path     string     `json:"path"`
		Content  string     `json:"content"`
		MTime    *time.Time `json:"mtime"`
		Encoding string     `json:"encoding"`
		Create   bool       `json:"create"`
	}
	if err := decode(r, &req, s.maxEdit*6+(1<<20)); err != nil {
		return err
	}
	data := []byte(req.Content)
	if req.Encoding == "latin1" {
		var err error
		if data, err = latin1Encode(req.Content); err != nil {
			return err
		}
	}
	if int64(len(data)) > s.maxEdit {
		return httpx.Errorf(http.StatusRequestEntityTooLarge, "too_large", "Content exceeds the editor size limit")
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
	st, err := root.Stat(p)
	switch {
	case err == nil && req.Create:
		return fsErr(fs.ErrExist, p)
	case err == nil:
		if !st.Mode().IsRegular() {
			return httpx.BadRequest("not a regular file")
		}
		if req.MTime == nil || !st.ModTime().Equal(*req.MTime) {
			return httpx.Errorf(http.StatusConflict, "conflict", "The file was changed on disk since it was opened").
				With(map[string]any{"mtime": st.ModTime(), "size": st.Size()})
		}
	case errors.Is(err, fs.ErrNotExist) && req.Create:
		st = nil
	default:
		return fsErr(err, p)
	}
	err = writeAtomic(root, p, data, st)
	if err != nil && isPerm(err) {
		err = s.elevated(r, ref, "write", p, "", bytes.NewReader(data))
	}
	action := "files.write"
	if st == nil {
		action = "files.create"
	}
	s.record(r, action, ref, p, err, map[string]any{"size": len(data)})
	if err != nil {
		return fsErr(err, p)
	}
	nst, err := root.Stat(p)
	if err != nil {
		return fsErr(err, p)
	}
	httpx.OK(w, map[string]any{"mtime": nst.ModTime(), "size": nst.Size()})
	return nil
}

// writeAtomic replaces p with data via a temp file in the same folder and
// a rename, preserving mode and (best effort) ownership. Symlinks and
// files in folders we can't write to are rewritten in place instead.
func writeAtomic(root *os.Root, p string, data []byte, st fs.FileInfo) error {
	mode := fs.FileMode(0o644)
	if st != nil {
		mode = st.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
		// Respect the file's own permissions: the rename below would otherwise
		// let us replace a read-only file in a writable folder.
		f, err := root.OpenFile(p, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		f.Close()
	}
	inPlace := false
	if lst, err := root.Lstat(p); err == nil && lst.Mode()&fs.ModeSymlink != 0 {
		inPlace = true
	}
	if !inPlace {
		b := make([]byte, 6)
		rand.Read(b)
		tmp := join(parentOf(p), "."+baseName(p)+".wargo-"+hex.EncodeToString(b))
		f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = f.Write(data)
			if err == nil {
				err = f.Sync()
			}
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err == nil {
				err = root.Chmod(tmp, mode)
			}
			if err == nil && st != nil {
				if sys, ok := st.Sys().(*syscall.Stat_t); ok {
					_ = root.Lchown(tmp, int(sys.Uid), int(sys.Gid)) // only works when unchanged or privileged
				}
			}
			if err == nil {
				err = root.Rename(tmp, p)
			}
			if err != nil {
				root.Remove(tmp)
			}
			return err
		}
		if !isPerm(err) || st == nil {
			return err
		}
		// Folder not writable but the file is: fall back to in-place.
	}
	if st == nil {
		f, err := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}
	f, err := root.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

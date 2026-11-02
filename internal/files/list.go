// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Entry describes one directory entry.
type Entry struct {
	Name  string    `json:"name"`
	Type  string    `json:"type"` // file | dir | symlink | other
	Size  int64     `json:"size"`
	Mode  string    `json:"mode"`  // "-rw-r--r--"
	Octal string    `json:"octal"` // "0644"
	MTime time.Time `json:"mtime"`
	Owner string    `json:"owner"`
	Group string    `json:"group"`
	UID   int       `json:"uid"`
	GID   int       `json:"gid"`
	// Target is the symlink target (display only; resolution stays confined).
	Target string `json:"target,omitempty"`
	// TargetType is what the link resolves to inside the root:
	// file | dir | other | broken | outside.
	TargetType string `json:"targetType,omitempty"`
	MIME       string `json:"mime,omitempty"`
	Readable   bool   `json:"readable"`
	Writable   bool   `json:"writable"`
}

func (e *Entry) isDir() bool { return e.Type == "dir" || e.TargetType == "dir" }

func fileType(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m.IsRegular():
		return "file"
	}
	return "other"
}

// lsMode formats a mode like ls(1): "drwxr-sr-x".
func lsMode(m fs.FileMode) string {
	b := []byte("----------")
	switch {
	case m.IsDir():
		b[0] = 'd'
	case m&fs.ModeSymlink != 0:
		b[0] = 'l'
	case m&fs.ModeNamedPipe != 0:
		b[0] = 'p'
	case m&fs.ModeSocket != 0:
		b[0] = 's'
	case m&fs.ModeCharDevice != 0:
		b[0] = 'c'
	case m&fs.ModeDevice != 0:
		b[0] = 'b'
	}
	const rwx = "rwxrwxrwx"
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			b[i+1] = rwx[i]
		}
	}
	special := func(i int, set bool, lower, upper byte) {
		if !set {
			return
		}
		if b[i] == 'x' {
			b[i] = lower
		} else {
			b[i] = upper
		}
	}
	special(3, m&fs.ModeSetuid != 0, 's', 'S')
	special(6, m&fs.ModeSetgid != 0, 's', 'S')
	special(9, m&fs.ModeSticky != 0, 't', 'T')
	return string(b)
}

// unixPerm returns the classic 12-bit permission value.
func unixPerm(m fs.FileMode) uint32 {
	v := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		v |= 0o1000
	}
	return v
}

func octal(m fs.FileMode) string {
	s := strconv.FormatUint(uint64(unixPerm(m)), 8)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

var extraMIME = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".go": "text/x-go", ".rs": "text/x-rust",
	".py": "text/x-python", ".ts": "text/x-typescript", ".tsx": "text/x-typescript", ".jsx": "text/javascript",
	".toml": "text/x-toml", ".yaml": "text/yaml", ".yml": "text/yaml", ".sh": "text/x-shellscript",
	".bash": "text/x-shellscript", ".zsh": "text/x-shellscript", ".conf": "text/plain", ".ini": "text/plain",
	".cfg": "text/plain", ".log": "text/plain", ".env": "text/plain", ".txt": "text/plain", ".csv": "text/csv",
	".json": "application/json", ".c": "text/x-c", ".h": "text/x-c", ".cpp": "text/x-c++", ".java": "text/x-java",
	".sql": "text/x-sql", ".service": "text/plain", ".mkv": "video/x-matroska", ".webm": "video/webm",
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".mp3": "audio/mpeg", ".flac": "audio/flac",
	".ogg": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav", ".m4a": "audio/mp4", ".webp": "image/webp",
	".avif": "image/avif", ".heic": "image/heic", ".svg": "image/svg+xml", ".pdf": "application/pdf",
	".zip": "application/zip", ".gz": "application/gzip", ".tgz": "application/gzip", ".tar": "application/x-tar",
	".7z": "application/x-7z-compressed", ".rar": "application/vnd.rar", ".xz": "application/x-xz",
	".iso": "application/x-iso9660-image", ".torrent": "application/x-bittorrent",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".ico": "image/x-icon",
	".bmp": "image/bmp", ".html": "text/html", ".htm": "text/html", ".css": "text/css", ".js": "text/javascript",
	".xml": "text/xml", ".srt": "text/plain", ".vtt": "text/vtt", ".nfo": "text/plain",
}

func mimeOf(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return ""
	}
	if m, ok := extraMIME[ext]; ok {
		return m
	}
	m := mime.TypeByExtension(ext)
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	return m
}

// ---- ownership & access ----------------------------------------------------

type ownerCache struct {
	mu     sync.Mutex
	users  map[uint32]string
	groups map[uint32]string
	euid   int
	gids   map[int]bool
}

func newOwnerCache() *ownerCache {
	uid, gid, gs := host.Identity()
	c := &ownerCache{users: map[uint32]string{}, groups: map[uint32]string{}, euid: uid, gids: map[int]bool{gid: true}}
	for _, g := range gs {
		c.gids[g] = true
	}
	return c
}

func (c *ownerCache) user(uid uint32) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.users[uid]; ok {
		return n
	}
	n := strconv.Itoa(int(uid))
	if u, err := host.LookupUID(int(uid)); err == nil {
		n = u.Name
	}
	c.users[uid] = n
	return n
}

func (c *ownerCache) group(gid uint32) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.groups[gid]; ok {
		return n
	}
	n := strconv.Itoa(int(gid))
	if g, err := host.LookupGID(int(gid)); err == nil {
		n = g.Name
	}
	c.groups[gid] = n
	return n
}

// access approximates access(2) from the mode bits.
func (c *ownerCache) access(m fs.FileMode, uid, gid int) (read, write bool) {
	if c.euid == 0 {
		return true, true
	}
	p := m.Perm()
	switch {
	case uid == c.euid:
		return p&0o400 != 0, p&0o200 != 0
	case c.gids[gid]:
		return p&0o040 != 0, p&0o020 != 0
	}
	return p&0o004 != 0, p&0o002 != 0
}

// entry builds an Entry for rel (lstat info supplied).
func (s *Service) entry(root *os.Root, rel string, info fs.FileInfo) Entry {
	m := info.Mode()
	e := Entry{
		Name: info.Name(), Type: fileType(m), Size: info.Size(), Mode: lsMode(m), Octal: octal(m),
		MTime: info.ModTime(), UID: -1, GID: -1,
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		e.UID, e.GID = int(st.Uid), int(st.Gid)
		e.Owner, e.Group = s.owners.user(st.Uid), s.owners.group(st.Gid)
	}
	e.Readable, e.Writable = s.owners.access(m, e.UID, e.GID)
	if e.Type == "dir" {
		e.Size = 0
	}
	if e.Type == "symlink" {
		e.Target, _ = root.Readlink(rel)
		t, err := root.Stat(rel)
		switch {
		case err == nil:
			e.TargetType = fileType(t.Mode())
			if e.TargetType == "file" {
				e.Size = t.Size()
				e.MIME = mimeOf(e.Name)
			}
			if st, ok := t.Sys().(*syscall.Stat_t); ok {
				e.Readable, e.Writable = s.owners.access(t.Mode(), int(st.Uid), int(st.Gid))
			}
		case strings.Contains(err.Error(), "path escapes"):
			e.TargetType = "outside"
			e.Readable, e.Writable = false, false
		default:
			e.TargetType = "broken"
			e.Readable, e.Writable = false, false
		}
	}
	if e.Type == "file" {
		e.MIME = mimeOf(e.Name)
	}
	return e
}

// ---- handlers --------------------------------------------------------------

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	root, _, err := s.open(q.Get("root"))
	if err != nil {
		return err
	}
	defer root.Close()
	p := fsops.Clean(q.Get("path"))
	hidden := q.Get("hidden") == "1" || q.Get("hidden") == "true"
	dirsOnly := q.Get("dirsOnly") == "1"
	entries, truncated, err := s.list(root, p, hidden, dirsOnly)
	if err != nil {
		return fsErr(err, p)
	}
	var parent any
	if p != "." {
		parent = parentOf(p)
	}
	httpx.OK(w, map[string]any{"path": p, "parent": parent, "entries": entries, "truncated": truncated})
	return nil
}

func (s *Service) list(root *os.Root, p string, hidden, dirsOnly bool) ([]Entry, bool, error) {
	f, err := openDir(root, p)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	out := []Entry{}
	truncated := false
	for !truncated {
		batch, err := f.ReadDir(1024)
		for _, de := range batch {
			if !hidden && strings.HasPrefix(de.Name(), ".") {
				continue
			}
			info, ierr := de.Info()
			if ierr != nil {
				continue // vanished
			}
			e := s.entry(root, join(p, de.Name()), info)
			if dirsOnly && !e.isDir() {
				continue
			}
			out = append(out, e)
			if len(out) >= maxListEntries {
				truncated = true
				break
			}
		}
		if errors.Is(err, io.EOF) || len(batch) == 0 {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	sortEntries(out)
	return out, truncated, nil
}

// sortEntries orders folders first, then case-insensitively by name. The
// lower-cased keys are computed once (not per comparison), which matters
// for directories with tens of thousands of entries.
func sortEntries(out []Entry) {
	type keyed struct {
		dir   bool
		lower string
		e     Entry
	}
	ks := make([]keyed, len(out))
	for i, e := range out {
		ks[i] = keyed{e.isDir(), strings.ToLower(e.Name), e}
	}
	sort.Slice(ks, func(i, j int) bool {
		a, b := &ks[i], &ks[j]
		if a.dir != b.dir {
			return a.dir
		}
		if a.lower != b.lower {
			return a.lower < b.lower
		}
		return a.e.Name < b.e.Name
	})
	for i := range ks {
		out[i] = ks[i].e
	}
}

func (s *Service) handleStat(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	root, ref, err := s.open(q.Get("root"))
	if err != nil {
		return err
	}
	defer root.Close()
	p := fsops.Clean(q.Get("path"))
	info, err := root.Lstat(p)
	if err != nil {
		return fsErr(err, p)
	}
	e := s.entry(root, p, info)
	if p == "." {
		e.Name = ref.Name
	}
	httpx.OK(w, map[string]any{"path": p, "abs": filepath.Join(ref.Path, p), "entry": e})
	return nil
}

func (s *Service) handleSize(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	root, _, err := s.open(q.Get("root"))
	if err != nil {
		return err
	}
	defer root.Close()
	p := fsops.Clean(q.Get("path"))
	if _, err := root.Lstat(p); err != nil {
		return fsErr(err, p)
	}
	ctx, cancel := context.WithTimeout(r.Context(), sizeTimeout)
	defer cancel()
	size, n, err := fsops.SizeContext(ctx, root, p)
	partial := err != nil
	if err != nil && ctx.Err() == nil {
		return fsErr(err, p)
	}
	httpx.OK(w, map[string]any{"size": size, "files": n, "partial": partial})
	return nil
}

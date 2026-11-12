// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/kushalpandya/wargo/internal/fsops"
	"github.com/kushalpandya/wargo/internal/host"
	"github.com/kushalpandya/wargo/internal/httpx"
)

// Bookmarks are named shortcuts to any folder inside a root (a configured
// place or a drive). They live in the settings store so they follow the
// user across devices. Configured roots are seeded as bookmarks once; from
// then on the stored list is authoritative. Hidden drives are kept out of
// the sidebar but stay valid roots.

// Settings is the persistent key/value store (satisfied by *store.Store).
type Settings interface {
	Get(ctx context.Context, key string, v any) (bool, error)
	Set(ctx context.Context, key string, v any) error
}

const (
	keyBookmarks    = "files.bookmarks"
	keySeededRoots  = "files.seededRoots"
	keyHiddenDrives = "files.hiddenDrives"
)

// Limits (variables so tests can shrink them).
var (
	maxBookmarks      = 100
	maxBookmarkName   = 64
	maxHiddenDrives   = 500
	bookmarkBodyLimit = int64(16 << 10)
)

// Bookmark is a stored bookmark. Root is a root id ("0"… or "m-…");
// RootPath remembers the configured root's path so bookmarks survive the
// config's roots being reordered.
type Bookmark struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Root     string `json:"root"`
	Path     string `json:"path"`
	RootPath string `json:"rootPath,omitempty"`
}

// bookmarkView is a bookmark as returned to clients.
type bookmarkView struct {
	Bookmark
	// Abs is the absolute host path (for matching the current folder).
	Abs       string `json:"abs"`
	Kind      string `json:"kind"` // kind of the root: "place" or "drive"
	Home      bool   `json:"home,omitempty"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// memSettings is used when no store is configured (tests, tools).
type memSettings struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (m *memSettings) Get(_ context.Context, key string, v any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.m[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}

func (m *memSettings) Set(_ context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string][]byte{}
	}
	m.m[key] = b
	return nil
}

// rootSettings accesses the store with full privileges: in container mode
// handlers run inside host.AsUser and the database is root-owned.
type rootSettings struct{ s Settings }

func (r rootSettings) Get(ctx context.Context, key string, v any) (ok bool, err error) {
	host.AsRoot(func() error {
		ok, err = r.s.Get(ctx, key, v)
		return nil
	})
	return
}

func (r rootSettings) Set(ctx context.Context, key string, v any) (err error) {
	host.AsRoot(func() error {
		err = r.s.Set(ctx, key, v)
		return nil
	})
	return
}

func newBookmarkID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func isMountRoot(id string) bool { return strings.HasPrefix(id, mountPrefix) }

// ---- persistence -------------------------------------------------------------

// loadBookmarks returns the stored list, seeding configured roots that were
// never offered before and re-matching configured roots by path. Callers
// hold s.bmMu.
func (s *Service) loadBookmarks(ctx context.Context) ([]Bookmark, error) {
	var list []Bookmark
	if _, err := s.settings.Get(ctx, keyBookmarks, &list); err != nil {
		return nil, err
	}
	var seeded []string
	if _, err := s.settings.Get(ctx, keySeededRoots, &seeded); err != nil {
		return nil, err
	}
	changed := false
	for i := range list {
		b := &list[i]
		if isMountRoot(b.Root) || b.RootPath == "" {
			continue
		}
		if n, err := strconv.Atoi(b.Root); err == nil && n >= 0 && n < len(s.roots) && s.roots[n].Path == b.RootPath {
			continue
		}
		for j, rt := range s.roots {
			if rt.Path == b.RootPath {
				b.Root, changed = strconv.Itoa(j), true
				break
			}
		}
	}
	for i, rt := range s.roots {
		if slices.Contains(seeded, rt.Path) {
			continue
		}
		seeded = append(seeded, rt.Path)
		changed = true
		if slices.ContainsFunc(list, func(b Bookmark) bool { return s.bookmarkAbs(b) == rt.Path }) {
			continue
		}
		list = append(list, Bookmark{ID: newBookmarkID(), Name: truncName(rt.Name), Root: strconv.Itoa(i), Path: ".", RootPath: rt.Path})
	}
	if changed {
		if err := s.saveBookmarks(ctx, list); err != nil {
			return nil, err
		}
		if err := s.settings.Set(ctx, keySeededRoots, seeded); err != nil {
			return nil, err
		}
	}
	if list == nil {
		list = []Bookmark{}
	}
	return list, nil
}

func (s *Service) saveBookmarks(ctx context.Context, list []Bookmark) error {
	if list == nil {
		list = []Bookmark{}
	}
	return s.settings.Set(ctx, keyBookmarks, list)
}

func (s *Service) hiddenDrives(ctx context.Context) ([]string, error) {
	var hidden []string
	if _, err := s.settings.Get(ctx, keyHiddenDrives, &hidden); err != nil {
		return nil, err
	}
	return hidden, nil
}

// ---- resolution --------------------------------------------------------------

// bookmarkAbs is the absolute host path a bookmark points at ("" if its
// root can't be derived without detection).
func (s *Service) bookmarkAbs(b Bookmark) string {
	base := ""
	if p, ok := parseMountID(b.Root); ok {
		base = p
	} else if n, err := strconv.Atoi(b.Root); err == nil && n >= 0 && n < len(s.roots) {
		base = s.roots[n].Path
	} else {
		base = b.RootPath
	}
	if base == "" {
		return ""
	}
	if b.Path == "" || b.Path == "." {
		return base
	}
	return filepath.Join(base, b.Path)
}

// view resolves a bookmark's availability against the current roots.
func (s *Service) view(b Bookmark, drives []Drive, home string) bookmarkView {
	v := bookmarkView{Bookmark: b, Abs: s.bookmarkAbs(b), Kind: "place", Available: true}
	v.RootPath = ""
	if p, ok := parseMountID(b.Root); ok {
		v.Kind = "drive"
		if !s.showMounts || !slices.ContainsFunc(drives, func(d Drive) bool { return d.Path == p }) {
			v.Available, v.Reason = false, "Drive not mounted"
		}
	} else if n, err := strconv.Atoi(b.Root); err != nil || n < 0 || n >= len(s.roots) || (b.RootPath != "" && s.roots[n].Path != b.RootPath) {
		v.Available, v.Reason = false, "Location is no longer configured"
	}
	v.Home = home != "" && v.Abs == home
	return v
}

func adminHome() string {
	if a := host.Admin(); a != nil && a.Home != "" && a.Home != "/" {
		return filepath.Clean(a.Home)
	}
	return ""
}

func (s *Service) bookmarkViews(list []Bookmark, drives []Drive) []bookmarkView {
	home := adminHome()
	out := make([]bookmarkView, 0, len(list))
	for _, b := range list {
		out = append(out, s.view(b, drives, home))
	}
	return out
}

// preferPlace re-expresses a folder inside a drive as one inside the most
// specific configured root containing it, when that root can reach it.
func (s *Service) preferPlace(ref rootRef, rel string) (rootRef, string) {
	if !isMountRoot(ref.ID) {
		return ref, rel
	}
	abs := ref.Path
	if rel != "." {
		abs = filepath.Join(ref.Path, rel)
	}
	best := -1
	for i, rt := range s.roots {
		if inside(abs, rt.Path) && (best < 0 || len(rt.Path) > len(s.roots[best].Path)) {
			best = i
		}
	}
	if best < 0 {
		return ref, rel
	}
	r, err := filepath.Rel(s.roots[best].Path, abs)
	if err != nil {
		return ref, rel
	}
	r = fsops.Clean(r)
	pref := s.placeRef(best)
	root, err := openRoot(pref.Path)
	if err != nil {
		return ref, rel
	}
	defer root.Close()
	if st, err := root.Stat(r); err != nil || !st.IsDir() {
		return ref, rel
	}
	return pref, r
}

// cleanBookmarkName trims and validates a user-supplied name.
func cleanBookmarkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", httpx.BadRequest("Bookmark name must not be empty")
	case !utf8.ValidString(name):
		return "", httpx.BadRequest("Bookmark name is not valid UTF-8")
	case utf8.RuneCountInString(name) > maxBookmarkName:
		return "", httpx.BadRequest("Bookmark name must be at most " + strconv.Itoa(maxBookmarkName) + " characters")
	case strings.IndexFunc(name, unicode.IsControl) >= 0:
		return "", httpx.BadRequest("Bookmark name must not contain control characters")
	}
	return name, nil
}

func truncName(name string) string {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxBookmarkName {
		name = string([]rune(name)[:maxBookmarkName])
	}
	if name == "" {
		name = "Folder"
	}
	return name
}

// ---- handlers ----------------------------------------------------------------

func (s *Service) handleLocations(w http.ResponseWriter, r *http.Request) error {
	roots := s.rootStats(r.Context())
	s.bmMu.Lock()
	list, err := s.loadBookmarks(r.Context())
	s.bmMu.Unlock()
	if err != nil {
		return err
	}
	hidden := 0
	for _, m := range roots {
		if m["hidden"] == true {
			hidden++
		}
	}
	httpx.OK(w, map[string]any{
		"roots":        roots,
		"bookmarks":    s.bookmarkViews(list, s.drives(false)),
		"hiddenDrives": hidden,
		"maxBookmarks": maxBookmarks,
		"home":         adminHome(),
	})
	return nil
}

func (s *Service) handleBookmarkList(w http.ResponseWriter, r *http.Request) error {
	s.bmMu.Lock()
	list, err := s.loadBookmarks(r.Context())
	s.bmMu.Unlock()
	if err != nil {
		return err
	}
	httpx.OK(w, s.bookmarkViews(list, s.drives(false)))
	return nil
}

func (s *Service) handleBookmarkAdd(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Root string `json:"root"`
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := decode(r, &req, bookmarkBodyLimit); err != nil {
		return err
	}
	if req.Root == "" {
		return httpx.BadRequest("root is required")
	}
	root, ref, err := s.open(req.Root)
	if err != nil {
		return err
	}
	rel := fsops.Clean(req.Path)
	st, err := root.Stat(rel)
	root.Close()
	if err != nil {
		return ref.readErr(err, rel)
	}
	if !st.IsDir() {
		return httpx.Errorf(http.StatusBadRequest, "not_dir", "Only folders can be bookmarked")
	}
	ref, rel = s.preferPlace(ref, rel)
	name := req.Name
	if strings.TrimSpace(name) == "" {
		name = baseName(rel)
		if rel == "." {
			name = ref.Name
		}
		name = truncName(name)
	}
	if name, err = cleanBookmarkName(name); err != nil {
		return err
	}
	b := Bookmark{ID: newBookmarkID(), Name: name, Root: ref.ID, Path: rel}
	if !isMountRoot(ref.ID) {
		b.RootPath = ref.Path
	}
	abs := s.bookmarkAbs(b)

	s.bmMu.Lock()
	defer s.bmMu.Unlock()
	list, err := s.loadBookmarks(r.Context())
	if err != nil {
		return err
	}
	for _, x := range list {
		if s.bookmarkAbs(x) == abs {
			return httpx.Errorf(http.StatusConflict, "exists", "This folder is already bookmarked as "+strconv.Quote(x.Name)).
				With(map[string]any{"id": x.ID})
		}
	}
	if len(list) >= maxBookmarks {
		return httpx.Errorf(http.StatusConflict, "limit", "You can have at most "+strconv.Itoa(maxBookmarks)+" bookmarks")
	}
	list = append(list, b)
	err = s.saveBookmarks(r.Context(), list)
	s.auditBookmark(r, "files.bookmark.add", abs, err, map[string]any{"name": b.Name, "root": b.Root, "path": b.Path})
	if err != nil {
		return err
	}
	httpx.OK(w, s.view(b, s.drives(false), adminHome()))
	return nil
}

func (s *Service) handleBookmarkUpdate(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req, bookmarkBodyLimit); err != nil {
		return err
	}
	name, err := cleanBookmarkName(req.Name)
	if err != nil {
		return err
	}
	s.bmMu.Lock()
	defer s.bmMu.Unlock()
	list, err := s.loadBookmarks(r.Context())
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(b Bookmark) bool { return b.ID == r.PathValue("id") })
	if i < 0 {
		return httpx.Errorf(http.StatusNotFound, "not_found", "No such bookmark")
	}
	old := list[i].Name
	list[i].Name = name
	err = s.saveBookmarks(r.Context(), list)
	s.auditBookmark(r, "files.bookmark.rename", s.bookmarkAbs(list[i]), err, map[string]any{"from": old, "to": name})
	if err != nil {
		return err
	}
	httpx.OK(w, s.view(list[i], s.drives(false), adminHome()))
	return nil
}

func (s *Service) handleBookmarkDelete(w http.ResponseWriter, r *http.Request) error {
	s.bmMu.Lock()
	defer s.bmMu.Unlock()
	list, err := s.loadBookmarks(r.Context())
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(b Bookmark) bool { return b.ID == r.PathValue("id") })
	if i < 0 {
		return httpx.Errorf(http.StatusNotFound, "not_found", "No such bookmark")
	}
	b := list[i]
	list = slices.Delete(list, i, i+1)
	err = s.saveBookmarks(r.Context(), list)
	s.auditBookmark(r, "files.bookmark.remove", s.bookmarkAbs(b), err, map[string]any{"name": b.Name})
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"deleted": b.ID})
	return nil
}

func (s *Service) handleBookmarkReorder(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decode(r, &req, bookmarkBodyLimit); err != nil {
		return err
	}
	s.bmMu.Lock()
	defer s.bmMu.Unlock()
	list, err := s.loadBookmarks(r.Context())
	if err != nil {
		return err
	}
	if len(req.IDs) != len(list) {
		return httpx.Errorf(http.StatusConflict, "stale", "The bookmark list changed; reload and try again")
	}
	byID := map[string]Bookmark{}
	for _, b := range list {
		byID[b.ID] = b
	}
	out := make([]Bookmark, 0, len(list))
	for _, id := range req.IDs {
		b, ok := byID[id]
		if !ok {
			return httpx.Errorf(http.StatusConflict, "stale", "The bookmark list changed; reload and try again")
		}
		delete(byID, id)
		out = append(out, b)
	}
	err = s.saveBookmarks(r.Context(), out)
	s.auditBookmark(r, "files.bookmark.reorder", "", err, map[string]any{"ids": req.IDs})
	if err != nil {
		return err
	}
	httpx.OK(w, s.bookmarkViews(out, s.drives(false)))
	return nil
}

// handleDriveHidden shows or hides one drive in the sidebar:
// PUT /api/files/drives/{id}/hidden {"hidden": true}.
func (s *Service) handleDriveHidden(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Hidden *bool `json:"hidden"`
	}
	if err := decode(r, &req, bookmarkBodyLimit); err != nil {
		return err
	}
	if req.Hidden == nil {
		return httpx.BadRequest("hidden is required")
	}
	p, ok := parseMountID(r.PathValue("id"))
	if !ok {
		return httpx.Errorf(http.StatusNotFound, "unknown_root", "Not a drive id")
	}
	if *req.Hidden && !slices.ContainsFunc(s.drives(true), func(d Drive) bool { return d.Path == p }) {
		return httpx.Errorf(http.StatusNotFound, "unknown_root", "That drive is not mounted")
	}
	s.bmMu.Lock()
	defer s.bmMu.Unlock()
	hidden, err := s.hiddenDrives(r.Context())
	if err != nil {
		return err
	}
	has := slices.Contains(hidden, p)
	switch {
	case *req.Hidden && !has:
		if len(hidden) >= maxHiddenDrives {
			return httpx.Errorf(http.StatusConflict, "limit", "Too many hidden drives")
		}
		hidden = append(hidden, p)
	case !*req.Hidden && has:
		hidden = slices.DeleteFunc(hidden, func(x string) bool { return x == p })
	default:
		httpx.OK(w, map[string]any{"path": p, "hidden": *req.Hidden})
		return nil
	}
	if hidden == nil {
		hidden = []string{}
	}
	err = s.settings.Set(r.Context(), keyHiddenDrives, hidden)
	action := "files.drive.show"
	if *req.Hidden {
		action = "files.drive.hide"
	}
	if s.audit != nil {
		s.audit.Record(r, action, p, err, nil)
	}
	if err != nil {
		return err
	}
	httpx.OK(w, map[string]any{"path": p, "hidden": *req.Hidden})
	return nil
}

func (s *Service) auditBookmark(r *http.Request, action, target string, err error, details any) {
	if s.audit != nil {
		s.audit.Record(r, action, target, err, details)
	}
}

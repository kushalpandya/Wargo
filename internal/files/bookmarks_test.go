// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/store"
)

type bmView struct {
	ID, Name, Root, Path, Abs, Kind, Reason string
	Available, Home                         bool
}

type locations struct {
	Roots []struct {
		ID, Kind, Name, Path string
		Hidden               bool
	}
	Bookmarks    []bmView
	HiddenDrives int
}

type recAudit struct{ actions []string }

func (a *recAudit) Record(_ *http.Request, action, target string, err error, _ any) {
	a.actions = append(a.actions, action+" "+target)
}

// bmEnv builds a service with two configured roots backed by a real store.
func bmEnv(t *testing.T, st Settings, roots ...config.Root) (*env, *recAudit) {
	t.Helper()
	base := t.TempDir()
	if len(roots) == 0 {
		a, b := filepath.Join(base, "home"), filepath.Join(base, "data")
		must(t, os.MkdirAll(filepath.Join(a, "Downloads/sub"), 0o755))
		must(t, os.WriteFile(filepath.Join(a, "file.txt"), []byte("x"), 0o644))
		must(t, os.MkdirAll(b, 0o755))
		roots = []config.Root{{Name: "Home", Path: a}, {Name: "DATA", Path: b}}
	}
	au := &recAudit{}
	svc := NewWithOptions(Options{Roots: roots, DataDir: filepath.Join(base, "d"), Jobs: jobs.NewManager(events.NewHub()), Audit: au, Settings: st})
	mux := http.NewServeMux()
	svc.Register(mux)
	return &env{t: t, dir: roots[0].Path, svc: svc, mux: mux}, au
}

func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	must(t, err)
	t.Cleanup(func() { st.Close() })
	return st
}

func TestBookmarkSeedingAndCRUD(t *testing.T) {
	st := openStore(t, t.TempDir())
	e, au := bmEnv(t, st)
	locs := decodeJSON[locations](t, e.do("GET", "/api/files/locations", nil))
	if len(locs.Bookmarks) != 2 || locs.Bookmarks[0].Name != "Home" || locs.Bookmarks[0].Root != "0" || locs.Bookmarks[0].Path != "." || !locs.Bookmarks[0].Available || locs.Bookmarks[1].Name != "DATA" {
		t.Fatalf("seeded: %+v", locs.Bookmarks)
	}
	if locs.Bookmarks[0].Abs != e.dir || locs.Bookmarks[0].Kind != "place" {
		t.Fatalf("abs: %+v", locs.Bookmarks[0])
	}

	// Add a folder; default name is its base name.
	w := e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "Downloads/../Downloads/sub/.."})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	dl := decodeJSON[bmView](t, w)
	if dl.Name != "Downloads" || dl.Path != "Downloads" || dl.Abs != filepath.Join(e.dir, "Downloads") {
		t.Fatalf("added: %+v", dl)
	}
	// Duplicates, files, missing folders and escapes are refused.
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "Downloads"}), 409, "exists")
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "file.txt"}), 400, "not_dir")
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "nope"}), 404, "not_found")
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "7", "path": "."}), 404, "unknown_root")
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "", "path": "."}), 400, "")
	// ".." is clamped to the root, which is already bookmarked.
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "../../.."}), 409, "exists")
	// Names.
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "Downloads/sub", "name": strings.Repeat("x", 65)}), 400, "")
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "Downloads/sub", "name": "a\nb"}), 400, "")
	w = e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "Downloads/sub", "name": "  " + strings.Repeat("é", 64) + " "})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	sub := decodeJSON[bmView](t, w)

	// Rename.
	expectErr(t, e.do("PUT", "/api/files/bookmarks/"+dl.ID, map[string]any{"name": " "}), 400, "")
	expectErr(t, e.do("PUT", "/api/files/bookmarks/missing", map[string]any{"name": "x"}), 404, "not_found")
	if w := e.do("PUT", "/api/files/bookmarks/"+dl.ID, map[string]any{"name": "DL"}); w.Code != 200 || decodeJSON[bmView](t, w).Name != "DL" {
		t.Fatal(w.Body.String())
	}

	// Reorder needs the full set.
	all := decodeJSON[[]bmView](t, e.do("GET", "/api/files/bookmarks", nil))
	if len(all) != 4 {
		t.Fatalf("list: %+v", all)
	}
	expectErr(t, e.do("POST", "/api/files/bookmarks/reorder", map[string]any{"ids": []string{all[0].ID}}), 409, "stale")
	expectErr(t, e.do("POST", "/api/files/bookmarks/reorder", map[string]any{"ids": []string{all[0].ID, all[0].ID, all[1].ID, all[2].ID}}), 409, "stale")
	ids := []string{sub.ID, all[1].ID, dl.ID, all[0].ID}
	if w := e.do("POST", "/api/files/bookmarks/reorder", map[string]any{"ids": ids}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}

	// Delete.
	if w := e.do("DELETE", "/api/files/bookmarks/"+all[0].ID, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	expectErr(t, e.do("DELETE", "/api/files/bookmarks/"+all[0].ID, nil), 404, "not_found")

	// Persisted: a fresh service on the same store sees the same list and
	// does not re-seed the removed "Home" root.
	e2, _ := bmEnv(t, st, e.svc.roots...)
	got := decodeJSON[[]bmView](t, e2.do("GET", "/api/files/bookmarks", nil))
	var names []string
	for _, b := range got {
		names = append(names, b.Name)
	}
	if strings.Join(names, ",") != strings.Repeat("é", 64)+",DATA,DL" {
		t.Fatalf("persisted order: %q", names)
	}
	// Home still works as a root even though it isn't bookmarked.
	if w := e2.do("GET", "/api/files/list?root=0", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if len(au.actions) == 0 || !strings.HasPrefix(au.actions[0], "files.bookmark.add ") {
		t.Fatalf("audit: %q", au.actions)
	}
}

func TestBookmarkSeedNewRootsAndReorderedConfig(t *testing.T) {
	st := &memSettings{}
	base := t.TempDir()
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")
	for _, d := range []string{a, b, c} {
		must(t, os.MkdirAll(filepath.Join(d, "x"), 0o755))
	}
	e, _ := bmEnv(t, st, config.Root{Name: "A", Path: a}, config.Root{Name: "B", Path: b})
	if w := e.do("POST", "/api/files/bookmarks", map[string]any{"root": "1", "path": "x"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Config changes: roots reordered and a new one added.
	e2, _ := bmEnv(t, st, config.Root{Name: "C", Path: c}, config.Root{Name: "B", Path: b}, config.Root{Name: "A", Path: a})
	got := decodeJSON[[]bmView](t, e2.do("GET", "/api/files/bookmarks", nil))
	if len(got) != 4 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Name != "A" || got[0].Root != "2" || got[1].Root != "1" || got[2].Name != "x" || got[2].Root != "1" || got[2].Abs != filepath.Join(b, "x") || got[3].Name != "C" || got[3].Root != "0" {
		t.Fatalf("rematched: %+v", got)
	}
	// A root that vanished from the config is reported unavailable.
	e3, _ := bmEnv(t, st, config.Root{Name: "C", Path: c})
	got = decodeJSON[[]bmView](t, e3.do("GET", "/api/files/bookmarks", nil))
	if got[0].Available || got[0].Reason == "" || !got[3].Available {
		t.Fatalf("availability: %+v", got)
	}
}

func TestBookmarkCap(t *testing.T) {
	old := maxBookmarks
	maxBookmarks = 3
	defer func() { maxBookmarks = old }()
	e, _ := bmEnv(t, nil)
	must(t, os.MkdirAll(filepath.Join(e.dir, "one"), 0o755))
	must(t, os.MkdirAll(filepath.Join(e.dir, "two"), 0o755))
	if w := e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "one"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	expectErr(t, e.do("POST", "/api/files/bookmarks", map[string]any{"root": "0", "path": "two"}), 409, "limit")
}

func TestDriveBookmarksAndHiddenDrives(t *testing.T) {
	st := &memSettings{}
	e, au := bmEnv(t, st)
	drive := filepath.Join(t.TempDir(), "USB")
	must(t, os.MkdirAll(filepath.Join(drive, "photos"), 0o755))
	// The configured Home root lives "on" a drive too.
	withMounts(t, drive, filepath.Dir(e.dir))
	e.svc.showMounts = true
	id, homeDrive := mountID(drive), mountID(filepath.Dir(e.dir))

	w := e.do("POST", "/api/files/bookmarks", map[string]any{"root": id, "path": "photos"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	ph := decodeJSON[bmView](t, w)
	if ph.Root != id || ph.Kind != "drive" || !ph.Available || ph.Abs != filepath.Join(drive, "photos") {
		t.Fatalf("drive bookmark: %+v", ph)
	}
	// A folder reached through a drive but inside a configured root is
	// stored against that root.
	w = e.do("POST", "/api/files/bookmarks", map[string]any{"root": homeDrive, "path": filepath.Base(e.dir) + "/Downloads"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if b := decodeJSON[bmView](t, w); b.Root != "0" || b.Path != "Downloads" || b.Kind != "place" {
		t.Fatalf("preferred place: %+v", b)
	}

	// Hide the USB drive: flagged, counted, still a valid root.
	expectErr(t, e.do("PUT", "/api/files/drives/"+id+"/hidden", map[string]any{}), 400, "")
	expectErr(t, e.do("PUT", "/api/files/drives/nope/hidden", map[string]any{"hidden": true}), 404, "unknown_root")
	expectErr(t, e.do("PUT", "/api/files/drives/"+mountID("/not/mounted")+"/hidden", map[string]any{"hidden": true}), 404, "unknown_root")
	if w := e.do("PUT", "/api/files/drives/"+id+"/hidden", map[string]any{"hidden": true}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	e.do("PUT", "/api/files/drives/"+id+"/hidden", map[string]any{"hidden": true}) // idempotent
	var hidden []string
	if ok, _ := st.Get(context.Background(), keyHiddenDrives, &hidden); !ok || len(hidden) != 1 || hidden[0] != drive {
		t.Fatalf("stored hidden: %q", hidden)
	}
	locs := decodeJSON[locations](t, e.do("GET", "/api/files/locations", nil))
	if locs.HiddenDrives != 1 {
		t.Fatalf("hidden count: %+v", locs)
	}
	for _, r := range locs.Roots {
		if (r.ID == id) != r.Hidden {
			t.Fatalf("hidden flag on %+v", r)
		}
	}
	if w := e.do("GET", "/api/files/list?root="+id+"&path=photos", nil); w.Code != 200 {
		t.Fatal("hidden drive must stay browsable:", w.Body.String())
	}

	// Unmount: the bookmark stays but is unavailable; the hidden entry is kept.
	withMounts(t, filepath.Dir(e.dir))
	e.svc.mounts.at = e.svc.mounts.at.AddDate(-1, 0, 0)
	locs = decodeJSON[locations](t, e.do("GET", "/api/files/locations", nil))
	var found bool
	for _, b := range locs.Bookmarks {
		if b.ID == ph.ID {
			found = true
			if b.Available || b.Reason != "Drive not mounted" {
				t.Fatalf("unmounted: %+v", b)
			}
		}
	}
	if !found || locs.HiddenDrives != 0 {
		t.Fatalf("locations: %+v", locs)
	}
	// Unknown/garbage drive ids in stored bookmarks are just unavailable.
	v := e.svc.view(Bookmark{ID: "x", Name: "x", Root: "m-***", Path: "."}, nil, "")
	if v.Available {
		t.Fatalf("garbage id available: %+v", v)
	}
	// Showing a drive that's no longer mounted is allowed (cleans up).
	if w := e.do("PUT", "/api/files/drives/"+id+"/hidden", map[string]any{"hidden": false}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	st.Get(context.Background(), keyHiddenDrives, &hidden)
	if len(hidden) != 0 {
		t.Fatalf("still hidden: %q", hidden)
	}
	want := []string{"files.drive.hide " + drive, "files.drive.show " + drive}
	got := au.actions[len(au.actions)-2:]
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("audit %q", au.actions)
	}
}

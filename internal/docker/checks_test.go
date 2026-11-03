// SPDX-License-Identifier: AGPL-3.0-only

package docker

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/jobs"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestSplitRef(t *testing.T) {
	for _, c := range []struct{ in, repo, tag string }{
		{"lscr.io/linuxserver/plex:latest", "lscr.io/linuxserver/plex", "latest"},
		{"nextcloud", "nextcloud", "latest"},
		{"mariadb:11.8.2", "mariadb", "11.8.2"},
		{"registry:5000/app", "registry:5000/app", "latest"},
		{"registry:5000/app:v2", "registry:5000/app", "v2"},
		{"app@sha256:abc", "app", ""},
		{"app:1.0@sha256:abc", "app", "1.0"},
		{"sha256:0123456789abcdef", "sha256:0123456789abcdef", ""},
	} {
		repo, tag := splitRef(c.in)
		if repo != c.repo || tag != c.tag {
			t.Errorf("splitRef(%q) = %q, %q; want %q, %q", c.in, repo, tag, c.repo, c.tag)
		}
	}
}

func TestImageVersion(t *testing.T) {
	const id = "sha256:71b649b916810000000000"
	for _, c := range []struct {
		name   string
		labels map[string]string
		tag    string
		want   string
	}{
		{"oci", map[string]string{"org.opencontainers.image.version": "5.4.1", "build_version": "x"}, "latest", "5.4.1"},
		{"lsio build_version", map[string]string{"build_version": "Linuxserver.io version:- 1.32.0.4567 Build-date:- 2024-01-02T03:04:05+00:00"}, "latest", "1.32.0.4567"},
		{"lsio full", map[string]string{"build_version": "Linuxserver.io version:- 4.1.4-r0-ls367 Build-date:- 2026-10-30T01:38:25+00:00"}, "latest", "4.1.4-r0-ls367"},
		{"oci latest falls through", map[string]string{"org.opencontainers.image.version": "latest", "build_version": "Linuxserver.io version:- 2.6.5 Build-date:- x"}, "latest", "2.6.5"},
		{"version label", map[string]string{"version": "3.2"}, "latest", "3.2"},
		{"version label with spaces ignored", map[string]string{"version": "see website"}, "v9", "v9"},
		{"tag", nil, "11.8.2", "11.8.2"},
		{"latest -> id", map[string]string{"org.opencontainers.image.version": "latest"}, "latest", "71b649b91681"},
		{"no tag -> id", nil, "", "71b649b91681"},
	} {
		if got := imageVersion(c.labels, c.tag, id); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIconCandidates(t *testing.T) {
	a := &apiContainer{
		Image: "lscr.io/linuxserver/transmission:latest", Names: []string{"/big-bear-transmission"},
		Labels: map[string]string{labelService: "transmission", "icon": "https://cdn.example.com/t.png"},
	}
	got := iconCandidates(a)
	want := []iconSource{{Slug: "transmission"}, {Slug: "big-bear-transmission"}, {URL: "https://cdn.example.com/t.png"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// Generic repo names fall back to the namespace; http label URLs are ignored.
	a = &apiContainer{Image: "vaultwarden/server:1.30", Names: []string{"/vw"}, Labels: map[string]string{"icon": "http://x/y.png"}}
	got = iconCandidates(a)
	want = []iconSource{{Slug: "vaultwarden"}, {Slug: "vw"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// Image ids yield no slug.
	a = &apiContainer{Image: "sha256:abc", Names: []string{"/"}}
	if got := iconCandidates(a); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func pngBytes(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestIconStoreCacheAndNegative(t *testing.T) {
	pngData := pngBytes(t)
	var hits atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/plex.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngData)
		case "/html.png":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cdn.Close()
	st := newIconStore(t.TempDir())
	st.base = cdn.URL + "/"
	ctx := context.Background()

	// First source misses, second hits.
	p, err := st.resolve(ctx, []iconSource{{Slug: "nosuch"}, {Slug: "plex"}})
	if err != nil || filepath.Ext(p) != ".png" {
		t.Fatalf("resolve: %q %v", p, err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d", hits.Load())
	}
	// Both outcomes are cached: no further CDN requests.
	if _, err := st.resolve(ctx, []iconSource{{Slug: "nosuch"}, {Slug: "plex"}}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("cached resolve hit the CDN: %d", hits.Load())
	}
	if _, err := st.resolve(ctx, []iconSource{{Slug: "nosuch"}}); err != errNoIcon {
		t.Fatalf("want errNoIcon, got %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("negative cache ignored: %d", hits.Load())
	}
	if !st.allMissed([]iconSource{{Slug: "nosuch"}}) || st.allMissed([]iconSource{{Slug: "nosuch"}, {Slug: "plex"}}) {
		t.Fatal("allMissed")
	}
	// Negative entries expire after missTTL.
	old := time.Now().Add(-missTTL - time.Minute)
	if err := os.Chtimes(st.missPath(iconSource{Slug: "nosuch"}), old, old); err != nil {
		t.Fatal(err)
	}
	st.resolve(ctx, []iconSource{{Slug: "nosuch"}})
	if hits.Load() != 3 {
		t.Fatalf("expired miss not refetched: %d", hits.Load())
	}
	// Non-image answers are rejected (and cached as misses).
	if _, err := st.resolve(ctx, []iconSource{{Slug: "html"}}); err != errNoIcon {
		t.Fatalf("html accepted: %v", err)
	}
	// Network failures are not cached as misses and stop the search.
	st2 := newIconStore(t.TempDir())
	st2.base = "http://127.0.0.1:1/"
	if _, err := st2.resolve(ctx, []iconSource{{Slug: "plex"}, {Slug: "other"}}); err == nil || err == errNoIcon {
		t.Fatalf("want network error, got %v", err)
	}
	if _, miss := st2.cached(iconSource{Slug: "plex"}); miss {
		t.Fatal("network failure cached as a miss")
	}
}

func TestMergeKeepsPreviousOnError(t *testing.T) {
	yes := true
	prev := UpdateCheck{Image: "a", UpdateAvailable: &yes, RemoteDigest: "sha256:r", CheckedAt: 1}
	got := merge(prev, true, UpdateCheck{Image: "a", Error: "Registry rate limit reached", CheckedAt: 2})
	if got.UpdateAvailable == nil || !*got.UpdateAvailable || got.Error == "" || got.CheckedAt != 1 {
		t.Fatalf("got %+v", got)
	}
	no := false
	got = merge(prev, true, UpdateCheck{Image: "a", UpdateAvailable: &no, CheckedAt: 3})
	if *got.UpdateAvailable || got.Error != "" {
		t.Fatalf("got %+v", got)
	}
	got = merge(UpdateCheck{}, false, UpdateCheck{Image: "a", Error: "x"})
	if got.UpdateAvailable != nil || got.Error != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestRegistryErr(t *testing.T) {
	err := &APIError{Status: 500, Message: "toomanyrequests: You have reached your pull rate limit"}
	if got := registryErr(err); got != "Registry rate limit reached; will retry on the next check" {
		t.Fatal(got)
	}
}

func TestUpdatesPersisted(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hub := events.NewHub()
	s := New("", hub, jobs.NewManager(hub), nil, nil).Configure(Options{Store: st})
	s.markUpToDate("app:latest", "sha256:img")
	s.mu.Lock()
	s.checkedAt = 1234
	p := s.snapshotLocked()
	s.mu.Unlock()
	s.save(p)

	s2 := New("", hub, jobs.NewManager(hub), nil, nil).Configure(Options{Store: st})
	l := []Container{{ID: "c1", Image: "app:latest", ImageID: "sha256:img"}, {ID: "c2", Image: "other", ImageID: "x"}}
	got := s2.updatesStatus(l)
	if got.CheckedAt == nil || *got.CheckedAt != 1234 {
		t.Fatalf("checkedAt = %v", got.CheckedAt)
	}
	r, ok := got.Results["c1"]
	if !ok || r.UpdateAvailable == nil || *r.UpdateAvailable {
		t.Fatalf("results = %+v", got.Results)
	}
	if _, ok := got.Results["c2"]; ok {
		t.Fatal("unexpected result for c2")
	}
	if got.IntervalMs != 0 || got.NextCheckAt != 0 {
		t.Fatalf("interval: %+v", got)
	}
}

func TestCheckUpdatesFake(t *testing.T) {
	f := &fakeDocker{containers: map[string]*fakeContainer{}, newImageID: "sha256:old"}
	id := "1111111111111111111111111111111111111111111111111111111111111111"
	f.containers[id] = appContainer(id, "myapp", "bridge")
	s := startFake(t, f)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s.Configure(Options{Store: st, DataDir: t.TempDir()})
	if err := s.CheckUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	l, err := s.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 1 || l[0].Version == "" || l[0].Tag != "latest" || l[0].ImageRef != "app" || l[0].Icon != "/api/containers/"+id[:12]+"/icon" {
		t.Fatalf("list = %+v", l)
	}
	// The fake image has no RepoDigests: per-image error, check still completes.
	us := s.updatesStatus(l)
	if us.CheckedAt == nil || us.Checking || us.Results[id[:12]].Error == "" {
		t.Fatalf("status = %+v", us)
	}
	if l[0].UpdateCheckedAt == 0 {
		t.Fatal("updateCheckedAt missing")
	}
	var p persistedUpdates
	if ok, err := st.Get(context.Background(), updatesKey, &p); !ok || err != nil || p.CheckedAt == 0 || len(p.Results) != 1 {
		t.Fatalf("persisted %+v %v %v", p, ok, err)
	}
}

func TestAutoCheckRuns(t *testing.T) {
	f := &fakeDocker{containers: map[string]*fakeContainer{}, newImageID: "sha256:old"}
	id := "2222222222222222222222222222222222222222222222222222222222222222"
	f.containers[id] = appContainer(id, "myapp", "bridge")
	s := startFake(t, f)
	s.Configure(Options{UpdateInterval: time.Hour})
	s.checkDelay = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.autoCheck(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		at, next := s.checkedAt, s.nextCheckAt
		s.mu.Unlock()
		if at > 0 {
			if next < at+time.Hour.Milliseconds()-1000 {
				t.Fatalf("next check %d not ~1h after %d", next, at)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background check did not run")
}

func TestUpdateCheckSettings(t *testing.T) {
	s := startFake(t, &fakeDocker{})
	s.checkInterval = 6 * time.Hour
	mux := http.NewServeMux()
	s.Register(mux)
	put := func(body string) int {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("PUT", "/api/containers/settings", strings.NewReader(body)))
		return w.Code
	}
	if c := put(`{"checkHours":5}`); c != 400 {
		t.Fatalf("odd interval: %d", c)
	}
	if c := put(`{"checkHours":0}`); c != 200 || s.checkInterval != 0 {
		t.Fatalf("off: %d %v", c, s.checkInterval)
	}
	select {
	case <-s.sched.Wake():
	default:
		t.Fatal("background loop was not woken")
	}
	if c := put(`{"checkHours":24}`); c != 200 || s.checkInterval != 24*time.Hour {
		t.Fatalf("daily: %d %v", c, s.checkInterval)
	}
	// A configured interval that isn't selectable reads as the next one up.
	for d, want := range map[time.Duration]string{30 * time.Minute: "1", 2 * time.Hour: "3", 400 * time.Hour: "168", 0: "0"} {
		s.mu.Lock()
		s.checkInterval = d
		s.mu.Unlock()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/containers/settings", nil))
		if got := strings.TrimSpace(w.Body.String()); got != `{"checkHours":`+want+`}` {
			t.Errorf("%v reads as %s", d, got)
		}
	}
}

func TestDueAfterFailure(t *testing.T) {
	s := startFake(t, &fakeDocker{})
	s.checkInterval = 24 * time.Hour
	if !s.dueLocked().IsZero() {
		t.Fatal("never checked should be due now")
	}
	retry := time.Now().Add(30 * time.Minute)
	s.retryAt = retry
	s.checkedAt = time.Now().Add(-48 * time.Hour).UnixMilli()
	if !s.dueLocked().Equal(retry) {
		t.Fatal("overdue check after a failure should wait for retryAt")
	}
}

func TestIsWargo(t *testing.T) {
	s := &Service{selfID: "self123"}
	for _, c := range []struct {
		a    apiContainer
		want bool
	}{
		{apiContainer{ID: "self123", Image: "sha256:abc"}, true},
		{apiContainer{ID: "x", Image: "ghcr.io/kushalpandya/wargo:latest"}, true},
		{apiContainer{ID: "x", Image: "ghcr.io/kushalpandya/wargo@sha256:abc"}, true},
		{apiContainer{ID: "x", Image: "wargo:dev"}, true},
		{apiContainer{ID: "x", Image: "someone/wargo-fork:1"}, false},
		{apiContainer{ID: "x", Image: "lscr.io/linuxserver/transmission"}, false},
	} {
		if got := s.isWargo(&c.a); got != c.want {
			t.Errorf("%+v: got %v", c.a, got)
		}
	}
}

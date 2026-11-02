// SPDX-License-Identifier: AGPL-3.0-only

package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func TestGuessSlug(t *testing.T) {
	cases := map[string]string{
		"lscr.io/linuxserver/prowlarr:latest":      "prowlarr",
		"plexinc/pms-docker":                       "plex",
		"nextcloud":                                "nextcloud",
		"nextcloud:28-apache":                      "nextcloud",
		"jellyfin/jellyfin@sha256:abc":             "jellyfin",
		"vaultwarden/server:latest":                "vaultwarden",
		"ghcr.io/home-assistant/home-assistant":    "home-assistant",
		"portainer/portainer-ce:2.19":              "portainer",
		"adguard/adguardhome":                      "adguard-home",
		"localhost:5000/my_app":                    "my-app",
		"qmcgaw/gluetun":                           "gluetun",
		"registry.example.com:443/team/docker-foo": "foo",
	}
	for in, want := range cases {
		if got := guessSlug(in); got != want {
			t.Errorf("guessSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrettify(t *testing.T) {
	for in, want := range map[string]string{"/prowlarr": "Prowlarr", "big-bear_jellyfin": "Big Bear Jellyfin", "a..b": "A B"} {
		if got := prettify(in); got != want {
			t.Errorf("prettify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildSuggestions(t *testing.T) {
	cs := []dockerContainer{
		{Names: []string{"/prowlarr"}, Image: "linuxserver/prowlarr", State: "running",
			Ports: []dockerPort{{PrivatePort: 9696, PublicPort: 9696, Type: "tcp"}, {PrivatePort: 9696, PublicPort: 9696, Type: "tcp", IP: "::"}}},
		{Names: []string{"/transmission"}, Image: "linuxserver/transmission", State: "running",
			Ports: []dockerPort{{PrivatePort: 51413, PublicPort: 51413, Type: "tcp"}, {PrivatePort: 9091, PublicPort: 9091, Type: "tcp"}, {PrivatePort: 51413, PublicPort: 51413, Type: "udp"}}},
		{Names: []string{"/wargo"}, Image: "wargo:dev", State: "running", Ports: []dockerPort{{PrivatePort: 2207, PublicPort: 2207, Type: "tcp"}}},
		{Names: []string{"/internal"}, Image: "redis", State: "running", Ports: []dockerPort{{PrivatePort: 6379, Type: "tcp"}}},
		{ID: "f9a0ad28e7b3", Names: []string{"/gluetun"}, Image: "qmcgaw/gluetun", State: "running",
			Ports: []dockerPort{{PrivatePort: 9091, PublicPort: 9091, Type: "tcp"}, {PrivatePort: 51413, PublicPort: 51413, Type: "tcp"}, {PrivatePort: 8000, Type: "tcp"}}},
		{Names: []string{"/transmission-vpn"}, Image: "linuxserver/transmission", State: "running", HostConfig: struct {
			NetworkMode string `json:"NetworkMode"`
		}{"container:f9a0ad28e7b3a0e578027039c4d6b6ed40d48c7eac6831fad29a3e98f067bb55"}},
		{Names: []string{"/plex"}, Image: "lscr.io/linuxserver/plex", State: "running", HostConfig: struct {
			NetworkMode string `json:"NetworkMode"`
		}{"host"}},
		{Names: []string{"/portainer"}, Image: "portainer/portainer-ce", State: "running",
			Ports: []dockerPort{{PrivatePort: 8000, PublicPort: 8000, Type: "tcp"}, {PrivatePort: 9000, PublicPort: 9000, Type: "tcp"}, {PrivatePort: 9443, PublicPort: 9443, Type: "tcp"}}},
		{Names: []string{"/stopped"}, Image: "nginx", State: "exited", Ports: []dockerPort{{PrivatePort: 80, PublicPort: 8080, Type: "tcp"}}},
	}
	got := buildSuggestions(cs, "nas.lan", map[string]bool{"http://nas.lan:9696": true})
	byName := map[string]Suggestion{}
	for _, g := range got {
		byName[g.Container] = g
	}
	want := map[string]string{
		"prowlarr": "http://nas.lan:9696", "transmission": "http://nas.lan:9091", "transmission-vpn": "http://nas.lan:9091",
		"plex": "http://nas.lan:32400", "portainer": "http://nas.lan:9000",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions: %+v", len(got), got)
	}
	for n, u := range want {
		if byName[n].URL != u {
			t.Errorf("%s: %+v", n, byName[n])
		}
	}
	if p := byName["prowlarr"]; p.Name != "Prowlarr" || !p.Exists || p.IconSlug != "prowlarr" || len(p.Ports) != 1 {
		t.Errorf("prowlarr: %+v", p)
	}
	if p := byName["transmission"]; len(p.Ports) != 2 || p.Exists {
		t.Errorf("transmission: %+v", p)
	}
}

func TestRequestHostname(t *testing.T) {
	for host, want := range map[string]string{"nas.lan:2207": "nas.lan", "192.168.1.5": "192.168.1.5", "[::1]:80": "[::1]", "": "localhost"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		if got := requestHostname(r); got != want {
			t.Errorf("%q: got %q want %q", host, got, want)
		}
	}
}

func TestValidURL(t *testing.T) {
	ok := []string{"http://a", "https://x.example.com:8443/path?q=1"}
	bad := []string{"javascript:alert(1)", "ftp://x", "file:///etc/passwd", "http://", "//x", "x.com"}
	for _, u := range ok {
		if _, err := validURL(u, true); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	for _, u := range bad {
		if _, err := validURL(u, true); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	if v, err := validURL("", false); err != nil || v != "" {
		t.Error("optional empty url")
	}
}

func pngBytes(t *testing.T) []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	return b.Bytes()
}

func TestSniffIcon(t *testing.T) {
	if sniffIcon(pngBytes(t)) != "png" {
		t.Error("png")
	}
	if sniffIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`)) != "svg" {
		t.Error("svg")
	}
	if sniffIcon([]byte(`<html><script>alert(1)</script></html>`)) != "" {
		t.Error("html accepted")
	}
	if sniffIcon([]byte("GIF89a....")) != "" {
		t.Error("gif accepted")
	}
}

func TestIconNames(t *testing.T) {
	good := strings.Repeat("a", 64) + ".png"
	for _, n := range []string{"../x.png", strings.Repeat("a", 64) + ".html", strings.Repeat("A", 64) + ".png", good + "/", ""} {
		if validIconName(n) {
			t.Errorf("%q accepted", n)
		}
	}
	if !validIconName(good) {
		t.Error("good rejected")
	}
}

func newTestService(t *testing.T) (*Service, *http.ServeMux) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, audit.New(st, httpx.NewProxyTrust(nil)), dir, filepath.Join(dir, "nope.sock"))
	mux := http.NewServeMux()
	s.Register(mux)
	return s, mux
}

func do(t *testing.T, mux *http.ServeMux, method, path string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestTileCRUD(t *testing.T) {
	_, mux := newTestService(t)
	if w := do(t, mux, "POST", "/api/launcher/tiles", map[string]any{"name": "X", "url": "javascript:alert(1)"}); w.Code != 400 {
		t.Fatalf("bad url accepted: %d", w.Code)
	}
	var ids []string
	for _, n := range []string{"A", "B", "C"} {
		w := do(t, mux, "POST", "/api/launcher/tiles", map[string]any{"name": n, "url": "http://" + strings.ToLower(n) + ".lan", "group": "G"})
		if w.Code != 200 {
			t.Fatalf("create: %d %s", w.Code, w.Body)
		}
		var tile Tile
		json.Unmarshal(w.Body.Bytes(), &tile)
		ids = append(ids, tile.ID)
	}
	if w := do(t, mux, "POST", "/api/launcher/tiles/reorder", map[string]any{"ids": []string{ids[2], ids[0], ids[1]}}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := do(t, mux, "PUT", "/api/launcher/tiles/"+ids[0], map[string]any{"name": "A2", "url": "https://a.lan", "newTab": false}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := do(t, mux, "DELETE", "/api/launcher/tiles/"+ids[1], nil); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := do(t, mux, "DELETE", "/api/launcher/tiles/"+ids[1], nil); w.Code != 404 {
		t.Fatal("double delete", w.Code)
	}
	w := do(t, mux, "GET", "/api/launcher/tiles", nil)
	var tiles []Tile
	json.Unmarshal(w.Body.Bytes(), &tiles)
	if len(tiles) != 2 || tiles[0].Name != "C" || tiles[1].Name != "A2" || tiles[1].NewTab || tiles[1].Group != "" {
		t.Fatalf("tiles: %+v", tiles)
	}
}

func TestSettings(t *testing.T) {
	s, mux := newTestService(t)
	if _, v := s.Capabilities(context.Background()); v.(Settings).Enabled {
		t.Fatal("enabled by default")
	}
	if w := do(t, mux, "PUT", "/api/launcher", map[string]any{"enabled": true, "placement": "elsewhere"}); w.Code != 400 {
		t.Fatal("bad placement")
	}
	do(t, mux, "PUT", "/api/launcher", map[string]any{"enabled": true, "placement": "page"})
	if _, v := s.Capabilities(context.Background()); !v.(Settings).Enabled || v.(Settings).Placement != "page" {
		t.Fatal("not saved", v)
	}
}

func TestIconUploadAndServe(t *testing.T) {
	_, mux := newTestService(t)
	upload := func(name string, data []byte) *httptest.ResponseRecorder {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
		mw.Close()
		r := httptest.NewRequest("POST", "/api/launcher/icons", &b)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := upload("x.html", []byte("<html></html>")); w.Code != 400 {
		t.Fatal("html accepted")
	}
	if w := upload("big.png", append(pngBytes(t), make([]byte, maxIconSize)...)); w.Code != 400 {
		t.Fatal("oversize accepted", w.Code)
	}
	w := upload("a.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var res struct{ Icon string }
	json.Unmarshal(w.Body.Bytes(), &res)
	g := do(t, mux, "GET", "/api/launcher/icons/"+res.Icon, nil)
	if g.Code != 200 || g.Header().Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(g.Header().Get("Content-Security-Policy"), "sandbox") || g.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("serve: %d %v", g.Code, g.Header())
	}
	if g := do(t, mux, "GET", "/api/launcher/icons/..%2Fwargo.db", nil); g.Code != 404 {
		t.Fatal("traversal", g.Code)
	}
}

func TestIconFetch(t *testing.T) {
	s, mux := newTestService(t)
	p := pngBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prowlarr.png":
			w.Write(p)
		case "/html.png":
			w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s.iconBase = srv.URL + "/"
	if w := do(t, mux, "POST", "/api/launcher/icons/fetch", map[string]string{"slug": "../etc"}); w.Code != 400 {
		t.Fatal("bad slug", w.Code)
	}
	if w := do(t, mux, "POST", "/api/launcher/icons/fetch", map[string]string{"slug": "missing"}); w.Code != 404 {
		t.Fatal("missing", w.Code)
	}
	if w := do(t, mux, "POST", "/api/launcher/icons/fetch", map[string]string{"slug": "html"}); w.Code != 502 {
		t.Fatal("html", w.Code)
	}
	if w := do(t, mux, "POST", "/api/launcher/icons/fetch", map[string]string{"slug": "prowlarr"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
}

func TestHealthProbe(t *testing.T) {
	h := newHealthChecker()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(405)
			return
		}
		w.WriteHeader(401)
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer redir.Close()
	ctx := context.Background()
	if !h.probe(ctx, ok.URL) {
		t.Error("401 after 405 should be up")
	}
	if h.probe(ctx, bad.URL) {
		t.Error("503 should be down")
	}
	if h.probe(ctx, redir.URL) {
		t.Error("redirect to file: should be down")
	}
	if h.probe(ctx, "file:///etc/passwd") {
		t.Error("file scheme")
	}
}

func TestSuggestFakeDocker(t *testing.T) {
	s, mux := newTestService(t)
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.Write([]byte(`{"ApiVersion":"1.47","Version":"test"}`))
			return
		}
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/containers/json") {
			t.Errorf("unexpected docker call %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode([]dockerContainer{{Names: []string{"/sonarr"}, Image: "linuxserver/sonarr", State: "running",
			Ports: []dockerPort{{PrivatePort: 8989, PublicPort: 8989, Type: "tcp"}}}})
	})}
	go srv.Serve(ln)
	defer srv.Close()
	s.dockerSock = sock
	r := httptest.NewRequest("GET", "/api/launcher/suggest", nil)
	r.Host = "nas:2207"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	var out []Suggestion
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || len(out) != 1 || out[0].URL != "http://nas:8989" || out[0].IconSlug != "sonarr" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

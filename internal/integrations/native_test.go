// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fixtureLoc copies a fixture into a temp dir laid out like a native
// install and returns its location.
func fixtureLoc(t *testing.T, app, format, fixture, rel string) nativeLoc {
	t.Helper()
	root := t.TempDir()
	b, err := os.ReadFile(filepath.Join("testdata", "native", fixture))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return nativeLoc{App: app, Root: root, Rel: rel, Format: format}
}

// The location table itself is data; what must hold for it is checked here.
func TestNativeLocationsInvariants(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		locs := nativeLocations("/home/alice", goos)
		for _, app := range nativeApps {
			if !slices.ContainsFunc(locs, func(l nativeLoc) bool { return l.App == app }) {
				t.Errorf("%s: no location for %s", goos, app)
			}
		}
		for _, l := range locs {
			// Folders below /var/lib belong to service users, who could swap
			// one for a symlink: they must be in Rel, opened without
			// following links, never part of Root.
			if strings.HasPrefix(l.Root, "/var/lib/") {
				t.Errorf("%s: %+v has a service-owned folder in Root", goos, l)
			}
			if goos == "darwin" && l.Root == "/var/lib" {
				t.Errorf("darwin has a Linux package path: %+v", l)
			}
		}
		for _, l := range nativeLocations("", goos) {
			if !filepath.IsAbs(l.Root) || l.Root == "/.config/Prowlarr" {
				t.Errorf("%s without a home folder lists %+v", goos, l)
			}
		}
	}
}

func TestReadNativeConf(t *testing.T) {
	defaultsOnly := func() nativeLoc {
		root := t.TempDir()
		os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"rpc-enabled": true}`), 0o600)
		return nativeLoc{App: "transmission", Root: root, Rel: "settings.json", Format: "transmission-json"}
	}
	for _, c := range []struct {
		name string
		loc  nativeLoc
		want nativeConf
	}{
		{"prowlarr", fixtureLoc(t, "prowlarr", "prowlarr-xml", "prowlarr-config.xml", "config.xml"), nativeConf{Port: 9797, PortSet: true, Base: "/prowlarr"}},
		{"transmission", fixtureLoc(t, "transmission", "transmission-json", "transmission-settings.json", "settings.json"), nativeConf{Port: 9092, PortSet: true, RPCPath: "/tr/rpc"}},
		{"transmission defaults", defaultsOnly(), nativeConf{Port: 9091, RPCPath: transmissionRPC}},
		{"plex", fixtureLoc(t, "plex", "plex-xml", "plex-Preferences.xml", "Preferences.xml"), nativeConf{Port: 32400, ID: "abc"}},
	} {
		if got, ok := readNativeConf(c.loc); !ok || got != c.want {
			t.Errorf("%s: conf = %+v ok %v, want %+v", c.name, got, ok, c.want)
		}
	}
	if _, ok := readNativeConf(nativeLoc{App: "prowlarr", Root: t.TempDir(), Rel: "config.xml", Format: "prowlarr-xml"}); ok {
		t.Fatal("a missing file must not read")
	}
}

// No symlink below a location's Root is followed.
func TestReadNativeConfRefusesSymlinkedServiceFolder(t *testing.T) {
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "config.xml"), []byte("<Config><Port>9797</Port></Config>"), 0o600)
	root := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, "prowlarr")); err != nil {
		t.Fatal(err)
	}
	if _, ok := readNativeConf(nativeLoc{App: "prowlarr", Root: root, Rel: "prowlarr/config.xml", Format: "prowlarr-xml"}); ok {
		t.Fatal("followed a symlinked service folder")
	}
}

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func TestProbe(t *testing.T) {
	ctx := context.Background()
	client := newProbeClient()
	plex := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			w.Write([]byte(`<?xml version="1.0"?><MediaContainer size="0" claimed="1" machineIdentifier="abc" version="1.41"/>`))
			return
		}
		http.NotFound(w, r)
	})
	trans409 := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Transmission-Session-Id", "x")
		w.WriteHeader(http.StatusConflict)
	})
	trans401 := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Transmission"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	prowlarr := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!doctype html><html><head><title>Prowlarr</title></head></html>`))
	})
	// Sonarr answers /ping like Prowlarr does.
	// Sonarr answers /ping like Prowlarr does, so only the page counts.
	sonarr := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!doctype html><html><head><title>Sonarr</title></head></html>`))
	})
	other := serve(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello")) })
	// Prowlarr and Sonarr with Forms login send / to their login page.
	loginApp := func(name string) string {
		return serve(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/login" {
				w.Write([]byte("<!doctype html><title>" + name + "</title>"))
				return
			}
			http.Redirect(w, r, "/login?returnUrl=%2F", http.StatusFound)
		})
	}
	prowlarrLogin, sonarrLogin := loginApp("Prowlarr"), loginApp("Sonarr")
	prowlarrBasic := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="Prowlarr"`)
		w.WriteHeader(http.StatusUnauthorized)
	})

	cases := []struct {
		app, url string
		want     bool
	}{
		{"plex", plex, true},
		{"plex", other, false},
		{"transmission", trans409 + "/transmission/rpc", true},
		{"transmission", trans401 + "/transmission/rpc", true},
		{"transmission", other + "/transmission/rpc", false},
		{"prowlarr", prowlarr, true},
		{"prowlarr", sonarr, false},
		{"prowlarr", prowlarrLogin, true},
		{"prowlarr", sonarrLogin, false},
		{"prowlarr", prowlarrBasic, true},
		{"prowlarr", other, false},
	}
	for _, c := range cases {
		if got, _ := probeID(ctx, client, c.app, c.url); got != c.want {
			t.Errorf("probe(%s, %s) = %v, want %v", c.app, c.url, got, c.want)
		}
	}
}

func portOf(t *testing.T, u string) (string, int) {
	t.Helper()
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(u, "http://"))
	port, _ := strconv.Atoi(p)
	return u, port
}

// prowlarrServer is a fake Prowlarr page; it returns the server and its port.
func prowlarrServer(t *testing.T) (string, int) {
	t.Helper()
	return portOf(t, serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!doctype html><title>Prowlarr</title>`))
	}))
}

// prowlarrConf writes a native Prowlarr config.xml listening on port.
func prowlarrConf(t *testing.T, port int) nativeLoc {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "config.xml"), []byte(fmt.Sprintf("<Config><Port>%d</Port><ApiKey>nativekey0123456789</ApiKey></Config>", port)), 0o600)
	return nativeLoc{App: "prowlarr", Root: root, Rel: "config.xml", Format: "prowlarr-xml"}
}

func TestNativeCandidatesFromConfigPort(t *testing.T) {
	u, port := prowlarrServer(t)
	loc := prowlarrConf(t, port)
	p, tr, px := nativeCandidates(context.Background(), []nativeLoc{loc}, "127.0.0.1", nil, nil)
	if len(p) != 1 || len(tr) != 0 || len(px) != 0 {
		t.Fatalf("candidates = %+v %+v %+v", p, tr, px)
	}
	c := p[0]
	if c.Source != "native" || c.URL != u || c.ConfigPath != filepath.Join(loc.Root, "config.xml") || !c.ConfigReadable || c.Note != "" {
		t.Fatalf("candidate = %+v", c)
	}
}

func TestNativeCandidatesSkipsContainerURL(t *testing.T) {
	u, port := prowlarrServer(t)
	have := []Candidate{{Source: "container", Container: "prowlarr", URL: u}}
	p, _, _ := nativeCandidates(context.Background(), []nativeLoc{prowlarrConf(t, port)}, "127.0.0.1", nil, have)
	if len(p) != 0 {
		t.Fatalf("a container already at %s must not repeat as native: %+v", u, p)
	}
}

func TestNativeCandidatesNotAnswering(t *testing.T) {
	// Something answers on the port, but not Prowlarr.
	_, port := portOf(t, serve(t, http.NotFound))
	p, _, _ := nativeCandidates(context.Background(), []nativeLoc{prowlarrConf(t, port)}, "127.0.0.1", nil, nil)
	if len(p) != 1 || p[0].URL != "" || p[0].Note != fmt.Sprintf("Not answering on port %d", port) || p[0].ConfigPath == "" {
		t.Fatalf("candidates = %+v", p)
	}
}

func TestNativeCandidatesDefaultPortWithoutConfig(t *testing.T) {
	u, port := prowlarrServer(t)
	p, _, _ := nativeCandidates(context.Background(), nil, "127.0.0.1", map[string]int{"prowlarr": port}, nil)
	if len(p) != 1 || p[0].URL != u || p[0].ConfigPath != "" {
		t.Fatalf("candidates = %+v", p)
	}
}

// plexServer is a fake Plex whose /identity reports id; it returns its port.
func plexServer(t *testing.T, id string) int {
	t.Helper()
	_, port := portOf(t, serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			fmt.Fprintf(w, `<?xml version="1.0"?><MediaContainer size="0" machineIdentifier="%s"/>`, id)
			return
		}
		http.NotFound(w, r)
	}))
	return port
}

func plexConf(t *testing.T, id string) nativeLoc {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "Preferences.xml"), []byte(`<Preferences ProcessedMachineIdentifier="`+id+`" PlexOnlineToken="AbCdEfGhIjKlMnOpQrSt"/>`), 0o600)
	return nativeLoc{App: "plex", Root: root, Rel: "Preferences.xml", Format: "plex-xml"}
}

func TestNativeCandidatesPlexMatchesItsConfig(t *testing.T) {
	port := plexServer(t, "server-a")
	_, _, px := nativeCandidates(context.Background(), []nativeLoc{plexConf(t, "server-a")}, "127.0.0.1", map[string]int{"plex": port}, nil)
	if len(px) != 1 || px[0].URL == "" || px[0].ConfigPath == "" || !px[0].ConfigReadable {
		t.Fatalf("candidates = %+v", px)
	}
}

// Something else on Plex's port (a squatter while Plex is stopped) must not
// be handed the real server's config, or import would send it the token.
func TestNativeCandidatesPlexImpostorGetsNoConfig(t *testing.T) {
	port := plexServer(t, "someone-else")
	_, _, px := nativeCandidates(context.Background(), []nativeLoc{plexConf(t, "server-a")}, "127.0.0.1", map[string]int{"plex": port}, nil)
	for _, c := range px {
		if c.URL != "" && c.ConfigPath != "" {
			t.Fatalf("impostor at %s got the config: %+v", c.URL, px)
		}
	}
	if len(px) != 2 || px[1].Note != fmt.Sprintf("Another Plex server answers on port %d", port) {
		t.Fatalf("candidates = %+v, want the answering server and the config with a note", px)
	}
}

func TestConfigExistsDoesNotFollowSymlinks(t *testing.T) {
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "config.xml"), []byte("x"), 0o600)
	root := t.TempDir()
	os.Symlink(elsewhere, filepath.Join(root, "prowlarr"))
	if configExists(nativeLoc{App: "prowlarr", Root: root, Rel: "prowlarr/config.xml", Format: "prowlarr-xml"}) {
		t.Fatal("configExists followed a symlinked folder")
	}
}

func TestNativeCandidatesConfigOnDefaultPortIsOneRow(t *testing.T) {
	u, port := prowlarrServer(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "config.xml"), []byte("<Config><ApiKey>nativekey0123456789</ApiKey></Config>"), 0o600)
	loc := nativeLoc{App: "prowlarr", Root: root, Rel: "config.xml", Format: "prowlarr-xml"}
	p, _, _ := nativeCandidates(context.Background(), []nativeLoc{loc}, "127.0.0.1", map[string]int{"prowlarr": port}, nil)
	if len(p) != 1 || p[0].URL != u || !p[0].ConfigReadable {
		t.Fatalf("candidates = %+v, want one row carrying the config", p)
	}
}

func TestNativeCandidatesUnreadableConfig(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads any file")
	}
	unreadable := func() nativeLoc {
		root := t.TempDir()
		f := filepath.Join(root, "config.xml")
		os.WriteFile(f, []byte("<Config/>"), 0o600)
		os.Chmod(f, 0)
		return nativeLoc{App: "prowlarr", Root: root, Rel: "config.xml", Format: "prowlarr-xml"}
	}
	// Answering on the default port: the row points at the file and says it can't be read.
	u, port := prowlarrServer(t)
	p, _, _ := nativeCandidates(context.Background(), []nativeLoc{unreadable()}, "127.0.0.1", map[string]int{"prowlarr": port}, nil)
	if len(p) != 1 || p[0].URL != u || p[0].ConfigPath == "" || p[0].ConfigReadable {
		t.Fatalf("answering: %+v", p)
	}
	// Nothing answering: listed with a note.
	p, _, _ = nativeCandidates(context.Background(), []nativeLoc{unreadable()}, "127.0.0.1", nil, nil)
	if len(p) != 1 || p[0].URL != "" || p[0].Note != "Installed, but not answering on its default port" {
		t.Fatalf("not answering: %+v", p)
	}
}

func TestNativeCandidatesTransmissionHasNoConfig(t *testing.T) {
	_, port := portOf(t, serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Transmission-Session-Id", "x")
		w.WriteHeader(http.StatusConflict)
	}))
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "settings.json"), []byte(fmt.Sprintf(`{"rpc-port": %d}`, port)), 0o600)
	_, tr, _ := nativeCandidates(context.Background(), []nativeLoc{{App: "transmission", Root: root, Rel: "settings.json", Format: "transmission-json"}}, "127.0.0.1", nil, nil)
	if len(tr) != 1 || tr[0].URL == "" || tr[0].ConfigPath != "" {
		t.Fatalf("candidates = %+v", tr)
	}
}

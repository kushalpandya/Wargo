// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kushalpandya/wargo/internal/config"
)

const plexTok = "plexTOKEN_0123456789"

type fakePlexServer struct {
	*httptest.Server
	sessions atomic.Int32
	photos   atomic.Int32
	lastW    atomic.Value
}

func newFakePlex(t *testing.T) *fakePlexServer {
	f := &fakePlexServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/identity" {
			w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"abc","version":"1.43.5.11079-b3b13e506"}}`))
			return
		}
		if r.Header.Get("X-Plex-Token") != plexTok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/status/sessions":
			f.sessions.Add(1)
			w.Write([]byte(`{"MediaContainer":{"size":2,"Metadata":[
			 {"type":"movie","title":"Heat","year":1995,"viewOffset":1000,"duration":6000,"thumb":"/library/metadata/202/thumb/1",
			  "User":{"title":"guest"},"Player":{"title":"Chrome","product":"Plex Web","state":"paused","local":false},
			  "TranscodeSession":{"videoDecision":"transcode"}},
			 {"type":"episode","title":"Who Is Alive?","grandparentTitle":"Severance","parentIndex":2,"index":3,"viewOffset":600000,"duration":3000000,
			  "grandparentThumb":"/library/metadata/99/thumb/1700000001","User":{"title":"kushal"},
			  "Player":{"title":"Living Room TV","product":"Plex for LG","state":"playing","local":true},"Media":[{"Part":[{"decision":"directplay"}]}]}
			]}}`))
		case "/status/sessions/history/all":
			w.Write([]byte(`{"MediaContainer":{"Metadata":[
			 {"historyKey":"/status/sessions/history/2","type":"episode","title":"Pilot","grandparentTitle":"Carrie","parentIndex":1,"index":1,"viewedAt":1793499294,"accountID":1,"grandparentThumb":"../../etc/passwd"}
			]}}`))
		case "/library/recentlyAdded":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/accounts":
			w.Write([]byte(`{"MediaContainer":{"Account":[{"id":1,"name":"kushal"}]}}`))
		case "/photo/:/transcode":
			f.photos.Add(1)
			f.lastW.Store(r.URL.Query().Get("width"))
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("\xff\xd8\xffjpeg"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestPlexSettingsActivityThumb(t *testing.T) {
	fp := newFakePlex(t)
	h := newHarness(t, config.Config{})

	// not configured
	w, out := h.do(t, "GET", "/api/plex/activity", nil)
	if w.Code != 409 || out["code"] != "not_configured" {
		t.Fatalf("unconfigured: %d %v", w.Code, out)
	}
	if w, _ := h.do(t, "PUT", "/api/integrations/plex", map[string]any{"url": fp.URL, "token": "bad token!"}); w.Code != 400 {
		t.Fatal("malformed token accepted")
	}
	w, out = h.do(t, "PUT", "/api/integrations/plex", map[string]any{"url": fp.URL, "token": plexTok})
	if w.Code != 200 || out["available"] != true || out["hasToken"] != true || out["version"] != "1.43.5.11079-b3b13e506" {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), plexTok) {
		t.Fatal("token returned")
	}
	var raw plexSettings
	h.st.Get(t.Context(), keyPlex, &raw)
	if raw.TokenSealed == "" || strings.Contains(raw.TokenSealed, plexTok) {
		t.Fatalf("token not sealed: %+v", raw)
	}
	// PUT without token keeps it
	if _, out := h.do(t, "PUT", "/api/integrations/plex", map[string]any{"url": fp.URL}); out["hasToken"] != true || out["available"] != true {
		t.Fatalf("keep token: %v", out)
	}
	w, out = h.do(t, "GET", "/api/integrations", nil)
	if p, ok := out["plex"].(map[string]any); !ok || p["available"] != true || strings.Contains(w.Body.String(), plexTok) {
		t.Fatalf("status: %s", w.Body)
	}
	// test endpoint: saved token, then a wrong one
	if _, out := h.do(t, "POST", "/api/integrations/plex/test", nil); out["ok"] != true || out["version"] != "1.43.5.11079-b3b13e506" {
		t.Fatalf("test saved: %v", out)
	}
	w, out = h.do(t, "POST", "/api/integrations/plex/test", map[string]any{"url": fp.URL, "token": "wrongtoken0123456789"})
	if w.Code != 200 || out["ok"] != false || out["error"] != "Plex rejected the token" {
		t.Fatalf("test bad token: %d %v", w.Code, out)
	}

	// activity: partial result, playing first, unsafe thumbs dropped, cached
	w, out = h.do(t, "GET", "/api/plex/activity", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), plexTok) {
		t.Fatalf("activity: %d %s", w.Code, w.Body)
	}
	ss := out["sessions"].([]any)
	first, second := ss[0].(map[string]any), ss[1].(map[string]any)
	if len(ss) != 2 || first["grandparentTitle"] != "Severance" || first["decision"] != "directplay" || first["thumb"] != "/library/metadata/99/thumb/1700000001" || first["user"] != "kushal" {
		t.Fatalf("sessions: %v", ss)
	}
	if second["state"] != "paused" || second["decision"] != "transcode" {
		t.Fatalf("second session: %v", second)
	}
	hist := out["history"].([]any)[0].(map[string]any)
	if hist["user"] != "kushal" || hist["at"] != float64(1793499294) || hist["thumb"] != nil {
		t.Fatalf("history: %v", hist)
	}
	if len(out["recent"].([]any)) != 0 || out["errors"].(map[string]any)["recent"] == nil {
		t.Fatalf("recent error: %v", out)
	}
	if out["webUrl"] != nil {
		t.Fatalf("loopback web link offered: %v", out["webUrl"])
	}
	h.do(t, "GET", "/api/plex/activity", nil)
	if n := fp.sessions.Load(); n != 4 { // health checks (3) + one activity fetch
		t.Fatalf("activity not cached: %d session calls", n)
	}

	// thumb proxy
	for _, bad := range []string{"/photo/:/transcode", "/library/metadata/1/thumb/1?x=1", "/library/metadata/../../x", "http://evil/x", "/library/metadata/1/file", "//evil/library/metadata/1/thumb"} {
		if w, _ := h.do(t, "GET", "/api/plex/thumb?path="+strings.ReplaceAll(bad, "?", "%3F"), nil); w.Code != 400 {
			t.Errorf("bad path %q: %d", bad, w.Code)
		}
	}
	if fp.photos.Load() != 0 {
		t.Fatal("bad path reached Plex")
	}
	w, _ = h.do(t, "GET", "/api/plex/thumb?path=/library/metadata/99/thumb/1700000001&w=9999", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(w.Header().Get("Cache-Control"), "max-age=86400") || fp.lastW.Load() != "240" {
		t.Fatalf("thumb: %d %v %v", w.Code, w.Header(), fp.lastW.Load())
	}
	for in, want := range map[string]string{"1": "80", "100": "120", "121": "160", "200": "240"} {
		if w, _ := h.do(t, "GET", "/api/plex/thumb?path=/library/metadata/99/thumb&w="+in, nil); w.Code != 200 || fp.lastW.Load() != want {
			t.Fatalf("thumb width %s: %d %v", in, w.Code, fp.lastW.Load())
		}
	}
	if w, _ := h.do(t, "GET", "/api/plex/thumb?path=/library/metadata/99/art", nil); w.Code != 400 {
		t.Fatalf("art path: %d", w.Code)
	}

	// clearing the URL unconfigures; audit never holds the token
	h.do(t, "PUT", "/api/integrations/plex", map[string]any{"url": ""})
	if _, out := h.do(t, "GET", "/api/integrations", nil); out["plex"].(map[string]any)["configured"] != false {
		t.Fatal("plex should be unconfigured")
	}
	assertAuditClean(t, h, plexTok)
}

func assertAuditClean(t *testing.T, h *harness, secret string) {
	t.Helper()
	var n int
	h.st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'integrations.plex.%'`).Scan(&n)
	if n == 0 {
		t.Fatal("no plex audit entries")
	}
	rows, err := h.st.DB.Query(`SELECT * FROM audit_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		for _, v := range vals {
			if s, ok := v.(string); ok && strings.Contains(s, secret) {
				t.Fatalf("token in audit log: %s", s)
			}
			if b, ok := v.([]byte); ok && strings.Contains(string(b), secret) {
				t.Fatalf("token in audit log: %s", b)
			}
		}
	}
}

func TestPlexTokenFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
		return name
	}
	readPlexTokenFile := func(name string) (string, error) { return readConfigSecret(dir, name, parsePlexToken) }
	good := write("good.xml", `<?xml version="1.0" encoding="utf-8"?>
<Preferences OldestPreviousVersion="1.0" MachineIdentifier="x" PlexOnlineToken="`+plexTok+`" PlexOnlineUsername="kushal"/>`)
	if tok, err := readPlexTokenFile(good); err != nil || tok != plexTok {
		t.Fatal(tok, err)
	}
	for name, body := range map[string]string{
		"missing.xml": `<Preferences MachineIdentifier="x"/>`,
		"bad.xml":     `<Preferences PlexOnlineToken="short"/>`,
		"evil.xml":    `<Preferences PlexOnlineToken="abc def&quot;0123456789xyz"/>`,
		"other.xml":   `<Config PlexOnlineToken="` + plexTok + `"/>`,
		"junk.xml":    `not xml at all`,
	} {
		tok, err := readPlexTokenFile(write(name, body))
		if err == nil {
			t.Errorf("%s accepted: %q", name, tok)
		} else if strings.Contains(err.Error(), "config.xml") {
			t.Errorf("%s: error mentions config.xml: %v", name, err)
		}
	}
	// a symlinked Preferences.xml is refused (the container owns the folder)
	os.Symlink(filepath.Join(dir, good), filepath.Join(dir, "Preferences.xml"))
	if tok, err := readPlexTokenFile("Preferences.xml"); err == nil || !strings.Contains(err.Error(), "Preferences.xml is a symbolic link") {
		t.Fatalf("followed symlink: %q %v", tok, err)
	}
	if readable(dir, "Preferences.xml") {
		t.Fatal("symlink reported readable")
	}
}

func TestDiscoverPlexAndImportToken(t *testing.T) {
	fp := newFakePlex(t)
	// linuxserver layout
	lsio := t.TempDir()
	lib := filepath.Join(lsio, "Library", "Application Support", "Plex Media Server")
	os.MkdirAll(lib, 0o700)
	os.WriteFile(filepath.Join(lib, "Preferences.xml"), []byte(`<Preferences PlexOnlineToken="`+plexTok+`"/>`), 0o600)
	// flat layout
	flat := t.TempDir()
	os.WriteFile(filepath.Join(flat, "Preferences.xml"), []byte(`<Preferences PlexOnlineToken="`+plexTok+`"/>`), 0o600)
	// nothing readable
	empty := t.TempDir()
	containers := `[
	 {"Id":"p1","Names":["/plex"],"Image":"lscr.io/linuxserver/plex:latest","State":"running","Ports":[],
	  "HostConfig":{"NetworkMode":"host"},"Mounts":[{"Type":"bind","Source":"` + lsio + `","Destination":"/config"}]},
	 {"Id":"p2","Names":["/pms"],"Image":"plexinc/pms-docker","State":"running",
	  "Ports":[{"IP":"0.0.0.0","PrivatePort":32400,"PublicPort":32401,"Type":"tcp"}],
	  "HostConfig":{"NetworkMode":"bridge"},"Mounts":[{"Type":"bind","Source":"` + flat + `","Destination":"/config"}]},
	 {"Id":"p3","Names":["/hotio"],"Image":"ghcr.io/hotio/plex","State":"running","Ports":[],
	  "HostConfig":{"NetworkMode":"bridge"},"Mounts":[{"Type":"bind","Source":"` + empty + `","Destination":"/config"}]},
	 {"Id":"x1","Names":["/tautulli"],"Image":"linuxserver/tautulli","State":"running","HostConfig":{"NetworkMode":"host"}},
	 {"Id":"x2","Names":["/kometa"],"Image":"kometateam/kometa","State":"running","HostConfig":{"NetworkMode":"host"}},
	 {"Id":"x3","Names":["/pmm"],"Image":"meisnate12/plex-meta-manager","State":"running","HostConfig":{"NetworkMode":"host"}},
	 {"Id":"t1","Names":["/transmission"],"Image":"linuxserver/transmission","State":"running","Ports":[],"HostConfig":{"NetworkMode":"host"}}
	]`
	cfg := config.Config{}
	cfg.Docker.Socket = fakeDocker(t, containers)
	h := newHarness(t, cfg)

	w, out := h.do(t, "GET", "/api/integrations/discover", nil)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	px := out["plex"].([]any)
	if len(px) != 3 {
		t.Fatalf("plex candidates: %v", px)
	}
	a, b, c := px[0].(map[string]any), px[1].(map[string]any), px[2].(map[string]any)
	if a["url"] != "http://127.0.0.1:32400" || a["note"] != "Assumes the default port 32400" || a["configPath"] != filepath.Join(lib, "Preferences.xml") || a["configReadable"] != true {
		t.Fatalf("host network candidate: %v", a)
	}
	if b["url"] != "http://127.0.0.1:32401" || b["configPath"] != filepath.Join(flat, "Preferences.xml") || b["configReadable"] != true {
		t.Fatalf("flat candidate: %v", b)
	}
	if c["url"] != nil || c["configPath"] != filepath.Join(empty, "Library", "Application Support", "Plex Media Server", "Preferences.xml") || c["configReadable"] != nil {
		t.Fatalf("unreadable candidate: %v", c)
	}
	// host networking applies to every integration
	if tr := out["transmission"].([]any)[0].(map[string]any); tr["url"] != "http://127.0.0.1:9091/transmission/rpc" {
		t.Fatalf("transmission host network: %v", tr)
	}

	// arbitrary or Prowlarr-style paths are refused
	for _, p := range []string{"/etc/passwd", filepath.Join(lsio, "Preferences.xml"), filepath.Join(empty, "x.xml")} {
		if w, out := h.do(t, "POST", "/api/integrations/plex/import-token", map[string]any{"configPath": p}); w.Code != 403 || out["code"] != "not_detected" {
			t.Fatalf("path %s: %d %v", p, w.Code, out)
		}
	}
	// the detected file works; the token is never returned
	w, out = h.do(t, "POST", "/api/integrations/plex/import-token", map[string]any{"configPath": filepath.Join(lib, "Preferences.xml"), "url": fp.URL})
	if w.Code != 200 || out["hasToken"] != true || out["available"] != true || strings.Contains(w.Body.String(), plexTok) {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	var raw plexSettings
	h.st.Get(t.Context(), keyPlex, &raw)
	if raw.URL != fp.URL || raw.TokenSealed == "" {
		t.Fatalf("saved: %+v", raw)
	}
	assertAuditClean(t, h, plexTok)
}

func TestIsPlexServer(t *testing.T) {
	for img, want := range map[string]bool{
		"plexinc/pms-docker:latest": true, "lscr.io/linuxserver/plex": true, "ghcr.io/hotio/plex:release": true,
		"tautulli/tautulli": false, "kometateam/kometa": false, "meisnate12/plex-meta-manager": false,
		"linuxserver/overseerr": false, "granra/plex-exporter": false, "nginx": false,
	} {
		if isPlexServer(img) != want {
			t.Errorf("%s: want %v", img, want)
		}
	}
}

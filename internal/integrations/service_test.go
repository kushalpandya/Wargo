// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/events"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/integrations/transmission/transmissiontest"
	"github.com/kushalpandya/wargo/internal/store"
)

const testTorrent = "d8:announce25:udp://tr.example.org:69694:infod6:lengthi1048576e4:name15:debian-12.6.iso12:piece lengthi262144e6:pieces20:ABCDEFGHIJKLMNOPQRSTee"

type fakeProwlarr struct {
	*httptest.Server
	mu        sync.Mutex
	withTr    bool
	grabs     []map[string]any
	searches  int
	lastQuery string
}

func newFakeProwlarr(t *testing.T, withTr bool) *fakeProwlarr {
	f := &fakeProwlarr{withTr: withTr}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secretkey0123456789" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		base := f.URL
		switch {
		case r.URL.Path == "/api/v1/system/status":
			w.Write([]byte(`{"appName":"Prowlarr","version":"2.6.5.5623"}`))
		case r.URL.Path == "/api/v1/indexer":
			w.Write([]byte(`[{"id":1,"name":"TPB","enable":true,"protocol":"torrent"},{"id":3,"name":"1337x","enable":true,"protocol":"torrent"},{"id":9,"name":"Off","enable":false,"protocol":"torrent"}]`))
		case r.URL.Path == "/api/v1/indexerstatus":
			w.Write([]byte(`[{"indexerId":3,"disabledTill":"2999-01-01T00:00:00Z"}]`))
		case r.URL.Path == "/api/v1/downloadclient":
			if f.withTr {
				w.Write([]byte(`[{"id":1,"name":"Transmission","enable":true,"implementation":"Transmission","protocol":"torrent","fields":[{"name":"host","value":"10.0.0.100"},{"name":"port","value":9091},{"name":"urlBase","value":"/transmission/"},{"name":"username","value":"kushal"}]}]`))
			} else {
				w.Write([]byte(`[]`))
			}
		case r.URL.Path == "/api/v1/search" && r.Method == http.MethodGet:
			f.searches++
			f.lastQuery = r.URL.RawQuery
			json.NewEncoder(w).Encode([]map[string]any{
				{"guid": "https://tpb.example/1", "title": "Magnet Only", "indexerId": 1, "indexer": "TPB", "size": 10, "seeders": 5, "leechers": 1, "protocol": "torrent",
					"magnetUrl": base + "/1/download?apikey=secretkey0123456789&link=a", "infoUrl": "https://tpb.example/1"},
				{"guid": "https://1337x.example/2", "title": "Debian: 12/6 ISO", "indexerId": 3, "indexer": "1337x", "size": 20, "seeders": 2, "leechers": 0, "protocol": "torrent",
					"downloadUrl": base + "/3/download?apikey=secretkey0123456789&link=b", "infoUrl": base + "/3/x?apikey=secretkey0123456789"},
			})
		case r.URL.Path == "/api/v1/search" && r.Method == http.MethodPost:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.grabs = append(f.grabs, body)
			w.Write([]byte(`{}`))
		case r.URL.Path == "/1/download":
			w.Header().Set("Location", "magnet:?xt=urn:btih:2c6b6858d61da9543d4231a71db4b1c9264b0685&dn=Magnet+Only")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/3/download":
			w.Write([]byte(testTorrent))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

type harness struct {
	s   *Service
	mux *http.ServeMux
	st  *store.Store
}

func newHarness(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(cfg, st, events.NewHub(), audit.New(st, httpx.NewProxyTrust(nil)))
	mux := http.NewServeMux()
	s.Prowlarr().Register(mux)
	s.Transmission().Register(mux)
	return &harness{s: s, mux: mux, st: st}
}

func (h *harness) do(t *testing.T, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	if body == nil {
		req.ContentLength = 0
	}
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func assertNoSecrets(t *testing.T, body string) {
	t.Helper()
	l := strings.ToLower(strings.ReplaceAll(body, `"hasApiKey"`, ""))
	if strings.Contains(l, "apikey") || strings.Contains(body, "secretkey0123456789") || strings.Contains(body, "s3cret") || strings.Contains(l, "sealed") {
		t.Fatalf("secret leaked in response: %s", body)
	}
}

func TestSettingsSeedAndSecrets(t *testing.T) {
	fp := newFakeProwlarr(t, true)
	cfg := config.Config{}
	cfg.Integrations.Prowlarr = config.Prowlarr{URL: fp.URL + "/", APIKey: "secretkey0123456789"}
	cfg.Integrations.Transmission = config.Transmission{URL: "127.0.0.1:1", Username: "u", Password: "s3cret"}
	h := newHarness(t, cfg)

	w, out := h.do(t, "GET", "/api/integrations", nil)
	assertNoSecrets(t, w.Body.String())
	p := out["prowlarr"].(map[string]any)
	if p["hasApiKey"] != true || p["available"] != true || p["hasTransmissionClient"] != true || p["version"] != "2.6.5.5623" {
		t.Fatalf("prowlarr status %v", p)
	}
	tr := out["transmission"].(map[string]any)
	if tr["url"] != "http://127.0.0.1:1/transmission/rpc" || tr["hasPassword"] != true || tr["available"] != false {
		t.Fatalf("transmission status %v", tr)
	}

	// PUT without apiKey keeps it
	w, out = h.do(t, "PUT", "/api/integrations/prowlarr", map[string]any{"url": fp.URL})
	if w.Code != 200 || out["hasApiKey"] != true {
		t.Fatalf("put keep: %d %v", w.Code, out)
	}
	// bad URL rejected
	if w, _ := h.do(t, "PUT", "/api/integrations/prowlarr", map[string]any{"url": "ftp://x"}); w.Code != 400 {
		t.Fatalf("bad url accepted: %d", w.Code)
	}
	// stored sealed, not plain
	var raw prowlarrSettings
	h.st.Get(t.Context(), keyProwlarr, &raw)
	if raw.APIKeySealed == "" || strings.Contains(raw.APIKeySealed, "secretkey") {
		t.Fatalf("key not sealed: %+v", raw)
	}
	// test with wrong key → error, not 401
	w, out = h.do(t, "POST", "/api/integrations/prowlarr/test", map[string]any{"url": fp.URL, "apiKey": "wrongwrongwrong123"})
	if w.Code != 200 || out["ok"] != false {
		t.Fatalf("test bad key: %d %v", w.Code, out)
	}
	w, out = h.do(t, "POST", "/api/integrations/prowlarr/test", nil)
	if out["ok"] != true || out["hasTransmissionClient"] != true {
		t.Fatalf("test saved: %v", out)
	}
	tc := out["transmissionClient"].(map[string]any)
	if tc["username"] != "kushal" || tc["host"] != "10.0.0.100" {
		t.Fatalf("download client hint %v", tc)
	}
	// clearing the URL unconfigures
	h.do(t, "PUT", "/api/integrations/transmission", map[string]any{"url": ""})
	_, out = h.do(t, "GET", "/api/integrations", nil)
	if out["transmission"].(map[string]any)["configured"] != false {
		t.Fatal("transmission should be unconfigured")
	}
}

func TestSearchStripsKeysAndResolves(t *testing.T) {
	fp := newFakeProwlarr(t, false)
	cfg := config.Config{}
	cfg.Integrations.Prowlarr = config.Prowlarr{URL: fp.URL, APIKey: "secretkey0123456789"}
	h := newHarness(t, cfg)

	w, out := h.do(t, "GET", "/api/prowlarr/search?q=debian&categories=2000,5000&indexers=1,3", nil)
	if w.Code != 200 {
		t.Fatalf("search %d %s", w.Code, w.Body)
	}
	assertNoSecrets(t, w.Body.String())
	if !strings.Contains(fp.lastQuery, "categories=2000&categories=5000") || !strings.Contains(fp.lastQuery, "indexerIds=1&indexerIds=3") {
		t.Fatalf("query forwarded wrong: %s", fp.lastQuery)
	}
	results := out["results"].([]any)
	if len(results) != 2 || len(out["indexers"].([]any)) != 2 {
		t.Fatalf("results/indexers: %v", out)
	}
	errs := out["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["indexer"] != "1337x" {
		t.Fatalf("errors: %v", errs)
	}
	if w, _ := h.do(t, "GET", "/api/prowlarr/search?q=Debian&categories=2000,5000&indexers=1,3", nil); w.Code != 200 || fp.searches != 1 {
		t.Fatalf("repeated search should be served from cache: %d searches", fp.searches)
	}
	magnetOnly := results[0].(map[string]any)["id"].(string)
	withTorrent := results[1].(map[string]any)["id"].(string)

	// magnet via redirect
	w, out = h.do(t, "GET", "/api/prowlarr/results/"+magnetOnly+"/magnet", nil)
	if w.Code != 200 || !strings.HasPrefix(out["magnet"].(string), "magnet:?xt=urn:btih:2c6b") {
		t.Fatalf("magnet redirect: %d %s", w.Code, w.Body)
	}
	// magnet from torrent body
	w, out = h.do(t, "GET", "/api/prowlarr/results/"+withTorrent+"/magnet", nil)
	want := "magnet:?xt=urn:btih:fe3f0564c4c5015a479ab7470319c3b866116932&dn=debian-12.6.iso&xl=1048576&tr=udp%3A%2F%2Ftr.example.org%3A6969"
	if w.Code != 200 || out["magnet"] != want {
		t.Fatalf("magnet from torrent: %d %s", w.Code, w.Body)
	}
	// .torrent proxy
	w, _ = h.do(t, "GET", "/api/prowlarr/results/"+withTorrent+"/torrent", nil)
	if w.Code != 200 || w.Body.String() != testTorrent || !strings.Contains(w.Header().Get("Content-Disposition"), `filename="Debian_ 12_6 ISO.torrent"`) {
		t.Fatalf("torrent proxy: %d %q %s", w.Code, w.Header().Get("Content-Disposition"), w.Body)
	}
	w, out = h.do(t, "GET", "/api/prowlarr/results/"+magnetOnly+"/torrent", nil)
	if w.Code != 404 || out["code"] != "magnet_only" {
		t.Fatalf("magnet only: %d %v", w.Code, out)
	}
	w, out = h.do(t, "GET", "/api/prowlarr/results/nope/magnet", nil)
	if w.Code != 404 || out["code"] != "result_expired" {
		t.Fatalf("unknown id: %d %v", w.Code, out)
	}
	// queue without any download client
	w, out = h.do(t, "POST", "/api/prowlarr/results/"+magnetOnly+"/queue", map[string]any{})
	if w.Code != 409 || out["code"] != "no_download_client" {
		t.Fatalf("queue none: %d %v", w.Code, out)
	}
}

func TestQueueViaProwlarr(t *testing.T) {
	fp := newFakeProwlarr(t, true)
	ft := transmissiontest.New("kushal", "s3cret")
	defer ft.Close()
	cfg := config.Config{}
	cfg.Integrations.Prowlarr = config.Prowlarr{URL: fp.URL, APIKey: "secretkey0123456789"}
	cfg.Integrations.Transmission = config.Transmission{URL: ft.RPCURL(), Username: "kushal", Password: "s3cret"}
	h := newHarness(t, cfg)
	_, out := h.do(t, "GET", "/api/prowlarr/search?q=debian", nil)
	id := out["results"].([]any)[1].(map[string]any)["id"].(string)
	w, out := h.do(t, "POST", "/api/prowlarr/results/"+id+"/queue", map[string]any{})
	if w.Code != 200 || out["via"] != "prowlarr" {
		t.Fatalf("queue: %d %v", w.Code, out)
	}
	if len(fp.grabs) != 1 || fp.grabs[0]["guid"] != "https://1337x.example/2" || fp.grabs[0]["indexerId"] != float64(3) {
		t.Fatalf("grab body: %v", fp.grabs)
	}
	for _, m := range ft.Methods() {
		if m == "torrent-add" {
			t.Fatal("must not add directly when Prowlarr has a client")
		}
	}
}

func TestQueueFallbackToTransmission(t *testing.T) {
	fp := newFakeProwlarr(t, false)
	ft := transmissiontest.New("kushal", "s3cret")
	defer ft.Close()
	cfg := config.Config{}
	cfg.Integrations.Prowlarr = config.Prowlarr{URL: fp.URL, APIKey: "secretkey0123456789"}
	cfg.Integrations.Transmission = config.Transmission{URL: ft.RPCURL(), Username: "kushal", Password: "s3cret"}
	h := newHarness(t, cfg)
	_, out := h.do(t, "GET", "/api/prowlarr/search?q=debian", nil)
	res := out["results"].([]any)
	for i, wantSrc := range []string{"magnet:?xt=urn:btih:2c6b6858d61da9543d4231a71db4b1c9264b0685&dn=Magnet+Only", "metainfo"} {
		id := res[i].(map[string]any)["id"].(string)
		w, out := h.do(t, "POST", "/api/prowlarr/results/"+id+"/queue", map[string]any{})
		if w.Code != 200 || out["via"] != "transmission" {
			t.Fatalf("queue %d: %d %v", i, w.Code, out)
		}
		got, ok := ft.Get(int64(i + 1))
		if !ok || got.Source != wantSrc {
			t.Fatalf("queued source %d: %+v", i, got)
		}
	}
	if len(fp.grabs) != 0 {
		t.Fatal("grab must not be used without a Prowlarr client")
	}
}

func TestTransmissionEndpoints(t *testing.T) {
	ft := transmissiontest.New("kushal", "s3cret")
	defer ft.Close()
	a := ft.AddTorrent("debian.iso", 4, 0.5)
	b := ft.AddTorrent("ubuntu.iso", 6, 1)
	cfg := config.Config{}
	cfg.Integrations.Transmission = config.Transmission{URL: ft.RPCURL(), Username: "kushal", Password: "s3cret"}
	h := newHarness(t, cfg)

	w, out := h.do(t, "GET", "/api/transmission/torrents", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	ts := out["torrents"].([]any)
	if len(ts) != 2 || ts[0].(map[string]any)["status"] != "downloading" || ts[1].(map[string]any)["status"] != "seeding" {
		t.Fatalf("torrents: %v", ts)
	}
	stats := out["stats"].(map[string]any)
	if stats["freeSpace"] == nil || stats["downloadSpeed"] != float64(1234567) {
		t.Fatalf("stats: %v", stats)
	}
	if w, _ := h.do(t, "POST", "/api/transmission/torrents/1/stop", map[string]any{}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if g, _ := ft.Get(a.ID); g.Status != 0 {
		t.Fatal("not stopped")
	}
	if w, _ := h.do(t, "POST", "/api/transmission/torrents/"+a.HashString+"/start", map[string]any{}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if g, _ := ft.Get(a.ID); g.Status != 4 {
		t.Fatal("not started")
	}
	if w, _ := h.do(t, "DELETE", "/api/transmission/torrents/1?deleteData=false", nil); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if g, _ := ft.Get(a.ID); !g.Removed || g.DataDeleted {
		t.Fatalf("remove keep data: %+v", g)
	}
	if w, _ := h.do(t, "DELETE", "/api/transmission/torrents/2?deleteData=true", nil); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if g, _ := ft.Get(b.ID); !g.Removed || !g.DataDeleted {
		t.Fatalf("remove with data: %+v", g)
	}
	if w, _ := h.do(t, "DELETE", "/api/transmission/torrents/2?deleteData=maybe", nil); w.Code != 400 {
		t.Fatal("bad deleteData accepted")
	}
	if w, _ := h.do(t, "POST", "/api/transmission/torrents/abc/stop", map[string]any{}); w.Code != 400 {
		t.Fatal("bad id accepted")
	}
	// add magnet + torrent
	w, out = h.do(t, "POST", "/api/transmission/torrents", map[string]any{"magnet": "magnet:?xt=urn:btih:fe3f0564c4c5015a479ab7470319c3b866116932"})
	if w.Code != 200 || out["id"] == nil {
		t.Fatalf("add magnet: %d %s", w.Code, w.Body)
	}
	w, _ = h.do(t, "POST", "/api/transmission/torrents", map[string]any{"torrentBase64": base64.StdEncoding.EncodeToString([]byte(testTorrent))})
	if w.Code != 200 {
		t.Fatalf("add torrent: %d %s", w.Code, w.Body)
	}
	for _, bad := range []map[string]any{{"magnet": "http://x"}, {"torrentBase64": base64.StdEncoding.EncodeToString([]byte("i1e"))}, {}} {
		if w, _ := h.do(t, "POST", "/api/transmission/torrents", bad); w.Code != 400 {
			t.Fatalf("bad add accepted: %v", bad)
		}
	}
	// wrong password → 502 upstream_unauthorized (never 401)
	h.do(t, "PUT", "/api/integrations/transmission", map[string]any{"url": ft.RPCURL(), "username": "kushal", "password": "nope"})
	w, out = h.do(t, "GET", "/api/transmission/torrents", nil)
	if w.Code != 502 || out["code"] != "upstream_unauthorized" {
		t.Fatalf("bad password: %d %v", w.Code, out)
	}
	// audit entries written
	var n int
	h.st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'transmission.%'`).Scan(&n)
	if n < 6 {
		t.Fatalf("audit entries: %d", n)
	}
}

func fakeDocker(t *testing.T, containers string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wargo-dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/json" {
			t.Errorf("unexpected docker call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(containers))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestDiscoverAndImportKey(t *testing.T) {
	fp := newFakeProwlarr(t, true)
	cfgDir := t.TempDir()
	os.WriteFile(filepath.Join(cfgDir, "config.xml"), []byte("<Config>\n  <Port>9696</Port>\n  <ApiKey>secretkey0123456789</ApiKey>\n</Config>\n"), 0o600)
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "config.xml"), []byte("<Config><ApiKey>otherkey0123456789</ApiKey></Config>"), 0o600)
	containers := `[
	 {"Id":"aaa111","Names":["/prowlarr"],"Image":"linuxserver/prowlarr:latest","State":"running",
	  "Ports":[{"IP":"0.0.0.0","PrivatePort":9696,"PublicPort":19696,"Type":"tcp"},{"IP":"::","PrivatePort":9696,"PublicPort":19696,"Type":"tcp"}],
	  "HostConfig":{"NetworkMode":"bridge"},"Mounts":[{"Type":"bind","Source":"` + cfgDir + `","Destination":"/config"}]},
	 {"Id":"bbb222","Names":["/transmission"],"Image":"linuxserver/transmission:latest","State":"running","Ports":[],
	  "HostConfig":{"NetworkMode":"container:ccc333ccc333ccc333"}},
	 {"Id":"ccc333ccc333ccc333","Names":["/gluetun"],"Image":"qmcgaw/gluetun","State":"running",
	  "Ports":[{"IP":"0.0.0.0","PrivatePort":9091,"PublicPort":9091,"Type":"tcp"},{"IP":"0.0.0.0","PrivatePort":51413,"PublicPort":51413,"Type":"udp"}],
	  "HostConfig":{"NetworkMode":"default"}}
	]`
	cfg := config.Config{}
	cfg.Docker.Socket = fakeDocker(t, containers)
	h := newHarness(t, cfg)

	w, out := h.do(t, "GET", "/api/integrations/discover", nil)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	p := out["prowlarr"].([]any)[0].(map[string]any)
	if p["url"] != "http://127.0.0.1:19696" || p["configPath"] != filepath.Join(cfgDir, "config.xml") || p["configReadable"] != true {
		t.Fatalf("prowlarr candidate: %v", p)
	}
	tr := out["transmission"].([]any)[0].(map[string]any)
	if tr["url"] != "http://127.0.0.1:9091/transmission/rpc" || tr["networkVia"] != "gluetun" {
		t.Fatalf("transmission candidate: %v", tr)
	}

	// arbitrary path refused
	w, out = h.do(t, "POST", "/api/integrations/prowlarr/import-key", map[string]any{"configPath": filepath.Join(other, "config.xml")})
	if w.Code != 403 || out["code"] != "not_detected" {
		t.Fatalf("arbitrary path: %d %v", w.Code, out)
	}
	w, _ = h.do(t, "POST", "/api/integrations/prowlarr/import-key", map[string]any{"configPath": "/etc/passwd"})
	if w.Code != 403 {
		t.Fatal("must refuse /etc/passwd")
	}
	// detected path accepted; key never returned
	w, out = h.do(t, "POST", "/api/integrations/prowlarr/import-key", map[string]any{"configPath": filepath.Join(cfgDir, "config.xml") + "/", "url": fp.URL})
	if w.Code != 200 || out["hasApiKey"] != true || out["available"] != true {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	assertNoSecrets(t, w.Body.String())

	// with Prowlarr configured, discovery suggests its Transmission client
	_, out = h.do(t, "GET", "/api/integrations/discover", nil)
	hint := out["transmissionFromProwlarr"].(map[string]any)
	if hint["url"] != "http://10.0.0.100:9091/transmission/rpc" || hint["username"] != "kushal" {
		t.Fatalf("hint: %v", hint)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"127.0.0.1:9091", "/transmission/rpc"}:                 "http://127.0.0.1:9091/transmission/rpc",
		{"http://h:9091/transmission/", "/transmission/rpc"}:    "http://h:9091/transmission/rpc",
		{"http://h:9091/transmission/rpc", "/transmission/rpc"}: "http://h:9091/transmission/rpc",
		{"https://p.example/prowlarr/", ""}:                     "https://p.example/prowlarr",
		{"http://127.0.0.1:9696?apikey=x", ""}:                  "http://127.0.0.1:9696",
	} {
		got, err := normalizeURL(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("%v: got %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"ftp://x", "http://", "http://u:p@h"} {
		if _, err := normalizeURL(bad, ""); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(2, time.Minute)
	ctx := context.Background()
	if err := l.acquire(ctx, time.Now); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := l.acquire(short, time.Now); err == nil {
		t.Fatal("concurrent search must wait for the slot")
	}
	done := make(chan error)
	go func() { done <- l.acquire(ctx, time.Now) }()
	time.Sleep(10 * time.Millisecond)
	l.release()
	if err := <-done; err != nil {
		t.Fatalf("waiter should get the slot: %v", err)
	}
	l.release()
	if err := l.acquire(ctx, time.Now); err == nil {
		t.Fatal("rate limit not enforced")
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

package prowlarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/integrations/torrent"
)

const testTorrent = "d8:announce25:udp://tr.example.org:69694:infod6:lengthi1048576e4:name15:debian-12.6.iso12:piece lengthi262144e6:pieces20:ABCDEFGHIJKLMNOPQRSTee"

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k123" && r.URL.Query().Get("apikey") != "k123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/search":
			if r.URL.Query().Get("query") == "" {
				http.Error(w, "q", 400)
				return
			}
			base := srv.URL
			json.NewEncoder(w).Encode([]map[string]any{
				{"guid": "magnet:?xt=urn:btih:2C6B6858D61DA9543D4231A71DB4B1C9264B0685&dn=Ubuntu", "title": "Ubuntu", "indexerId": 1, "indexer": "TPB", "size": 100, "seeders": 5, "leechers": 1,
					"magnetUrl": base + "/1/download?apikey=k123&link=abc", "infoUrl": "https://tpb.example/description.php?id=1", "protocol": "torrent", "publishDate": "2022-05-18T12:33:51Z",
					"categories": []map[string]any{{"id": 4000, "name": "PC"}, {"id": 100303}}},
				{"guid": "https://1337x.example/torrent/1", "title": "Debian", "indexerId": 3, "indexer": "1337x", "size": 200, "seeders": 9, "leechers": 10,
					"downloadUrl": base + "/3/download?apikey=k123&link=def", "infoUrl": base + "/3/info?apikey=k123", "protocol": "torrent"},
				{"guid": "x", "title": "Usenet", "indexerId": 4, "protocol": "usenet"},
			})
		case "/3/download":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			w.Write([]byte(testTorrent))
		case "/1/download":
			w.Header().Set("Location", "magnet:?xt=urn:btih:2c6b6858d61da9543d4231a71db4b1c9264b0685&dn=Ubuntu&tr=udp%3A%2F%2Fx")
			w.WriteHeader(http.StatusMovedPermanently)
		case "/5/download":
			http.Redirect(w, r, "https://elsewhere.example/file.torrent", http.StatusFound)
		case "/api/v1/downloadclient":
			w.Write([]byte(`[{"id":1,"name":"Transmission","enable":true,"implementation":"Transmission","protocol":"torrent","fields":[{"name":"host","value":"10.0.0.100"},{"name":"port","value":9091},{"name":"useSsl","value":false},{"name":"urlBase","value":"/transmission/"},{"name":"username","value":"kushal"},{"name":"password","value":"********"}]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchMappingStripsSecrets(t *testing.T) {
	srv := fakeServer(t)
	c := New(srv.URL, "k123")
	list, err := c.Search(context.Background(), SearchParams{Query: "ubuntu", Categories: []int{2000}, IndexerIDs: []int{1, 3}})
	if err != nil {
		t.Fatal(err)
	}
	cache := NewCache(100, time.Hour)
	res := cache.Map(list, strings.TrimPrefix(srv.URL, "http://"))
	if len(res) != 2 {
		t.Fatalf("want 2 torrent results, got %d", len(res))
	}
	b, _ := json.Marshal(res)
	if strings.Contains(strings.ToLower(string(b)), "apikey") || strings.Contains(string(b), "k123") || strings.Contains(string(b), "/download") {
		t.Fatalf("secret leaked: %s", b)
	}
	if res[0].InfoHash != "2c6b6858d61da9543d4231a71db4b1c9264b0685" || !res[0].HasMagnet || res[0].HasTorrent {
		t.Errorf("result 0: %+v", res[0])
	}
	if len(res[0].Categories) != 1 || res[0].Categories[0].Name != "PC" || res[0].Age == nil {
		t.Errorf("categories/age: %+v", res[0])
	}
	if res[0].InfoURL != "https://tpb.example/description.php?id=1" || res[1].InfoURL != "" {
		t.Errorf("info urls: %q %q", res[0].InfoURL, res[1].InfoURL)
	}
	if !res[1].HasTorrent || !res[1].HasMagnet {
		t.Errorf("result 1: %+v", res[1])
	}
	e, ok := cache.Get(res[1].ID)
	if !ok || !strings.Contains(e.DownloadURL, "apikey=k123") {
		t.Fatalf("entry must keep the secret server-side: %+v", e)
	}
	// ids are stable for the same release
	if again := cache.Map(list, ""); again[0].ID != res[0].ID {
		t.Fatal("ids not stable")
	}
}

func TestFetch(t *testing.T) {
	srv := fakeServer(t)
	c := New(srv.URL, "k123")
	ctx := context.Background()

	f, err := c.Fetch(ctx, srv.URL+"/1/download?apikey=k123&link=abc")
	if err != nil || !strings.HasPrefix(f.Magnet, "magnet:?xt=urn:btih:2c6b") {
		t.Fatalf("redirect to magnet: %+v %v", f, err)
	}
	f, err = c.Fetch(ctx, srv.URL+"/3/download?apikey=k123&link=def")
	if err != nil || string(f.Torrent) != testTorrent {
		t.Fatalf("torrent body: %+v %v", f, err)
	}
	m, err := torrent.Parse(f.Torrent)
	if err != nil || m.InfoHash != "fe3f0564c4c5015a479ab7470319c3b866116932" {
		t.Fatalf("parse: %+v %v", m, err)
	}
	if _, err := c.Fetch(ctx, srv.URL+"/5/download"); err == nil {
		t.Fatal("non-magnet redirect must not be followed")
	}
	if _, err := c.Fetch(ctx, "http://169.254.169.254/latest"); err == nil {
		t.Fatal("foreign host must be refused")
	}
	if _, err := c.Fetch(ctx, srv.URL+"/nope"); err == nil || strings.Contains(err.Error(), "k123") {
		t.Fatalf("404 error: %v", err)
	}
}

func TestDownloadClients(t *testing.T) {
	srv := fakeServer(t)
	l, err := New(srv.URL, "k123").DownloadClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tc := FindTransmission(l)
	if tc == nil || tc.URL() != "http://10.0.0.100:9091/transmission/rpc" || tc.Username != "kushal" {
		t.Fatalf("transmission client: %+v", tc)
	}
	if _, err := New(srv.URL, "bad").Status(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestCacheLRUAndTTL(t *testing.T) {
	c := NewCache(2, time.Minute)
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	for i, g := range []string{"a", "b", "c"} {
		c.Put(&Entry{ID: c.ID(i, g), GUID: g})
	}
	if c.Len() != 2 {
		t.Fatalf("len %d", c.Len())
	}
	if _, ok := c.Get(c.ID(0, "a")); ok {
		t.Fatal("oldest should be evicted")
	}
	if _, ok := c.Get(c.ID(2, "c")); !ok {
		t.Fatal("newest missing")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.Get(c.ID(2, "c")); ok {
		t.Fatal("expired entry returned")
	}
}

func TestRedactAndSafeInfoURL(t *testing.T) {
	s := Redact(`Get "http://127.0.0.1:9696/1/download?apikey=abc&link=x": timeout`)
	if strings.Contains(strings.ToLower(s), "apikey") || strings.Contains(s, "abc") {
		t.Fatalf("redact: %s", s)
	}
	for in, want := range map[string]string{
		"https://nyaa.si/view/1":          "https://nyaa.si/view/1",
		"https://x.example/t?passkey=zzz": "",
		"javascript:alert(1)":             "",
		"http://127.0.0.1:9696/1/info":    "",
		"https://user:pw@x.example/t":     "",
	} {
		if got := SafeInfoURL(in, "127.0.0.1:9696"); got != want {
			t.Errorf("%s: got %q", in, got)
		}
	}
}

func TestProwlarrIgnoresProxyEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	c := New("http://prowlarr.invalid", "key")
	for name, hc := range map[string]*http.Client{"api": c.HTTP, "download": c.Download} {
		tr, ok := hc.Transport.(*http.Transport)
		if !ok || tr.Proxy != nil {
			t.Errorf("%s client must use a transport without a proxy function, got %#v", name, hc.Transport)
			continue
		}
		if tr.ResponseHeaderTimeout != 0 && tr.ResponseHeaderTimeout < c.SearchTimeout {
			t.Errorf("%s client header timeout %v would cut off slow searches", name, tr.ResponseHeaderTimeout)
		}
	}
}

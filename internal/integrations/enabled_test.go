// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/config"
	"github.com/kushalpandya/wargo/internal/integrations/transmission/transmissiontest"
)

func setEnabled(t *testing.T, h *harness, name string, on bool) map[string]any {
	t.Helper()
	w, out := h.do(t, "PUT", "/api/integrations/"+name+"/enabled", map[string]any{"enabled": on})
	if w.Code != 200 {
		t.Fatalf("%s enabled=%v: %d %s", name, on, w.Code, w.Body)
	}
	return out
}

func capability(t *testing.T, c interface {
	Capabilities(context.Context) (string, any)
}) map[string]any {
	t.Helper()
	_, v := c.Capabilities(context.Background())
	return v.(map[string]any)
}

func TestProwlarrDisableKeepsSettings(t *testing.T) {
	fp := newFakeProwlarr(t, true)
	cfg := config.Config{}
	cfg.Integrations.Prowlarr = config.Prowlarr{URL: fp.URL, APIKey: "secretkey0123456789"}
	h := newHarness(t, cfg)

	out := setEnabled(t, h, "prowlarr", false)
	if out["enabled"] != false || out["configured"] != true || out["available"] != false || out["hasApiKey"] != true || out["url"] != fp.URL {
		t.Fatalf("disable answer %v", out)
	}
	var raw prowlarrSettings
	h.st.Get(t.Context(), keyProwlarr, &raw)
	if !raw.Disabled || raw.URL != fp.URL || raw.APIKeySealed == "" {
		t.Fatalf("stored %+v; want URL and key kept, disabled", raw)
	}
	if c := capability(t, h.s.Prowlarr()); c["available"] != false || c["configured"] != false || c["hasTransmissionClient"] != false {
		t.Fatalf("capabilities while off %v", c)
	}
	_, all := h.do(t, "GET", "/api/integrations", nil)
	if p := all["prowlarr"].(map[string]any); p["enabled"] != false || p["configured"] != true || p["url"] != fp.URL {
		t.Fatalf("status while off %v", p)
	}
	if w, out := h.do(t, "GET", "/api/prowlarr/search?q=debian", nil); w.Code != 409 || out["code"] != "not_configured" {
		t.Fatalf("search while off: %d %v", w.Code, out)
	}
	if w, _ := h.do(t, "GET", "/api/prowlarr/indexers", nil); w.Code != 409 {
		t.Fatalf("indexers while off: %d", w.Code)
	}
	// Testing the saved settings still works while off.
	if _, out := h.do(t, "POST", "/api/integrations/prowlarr/test", nil); out["ok"] != true {
		t.Fatalf("test while off: %v", out)
	}
	// Saving keeps it off.
	if w, out := h.do(t, "PUT", "/api/integrations/prowlarr", map[string]any{"url": fp.URL}); w.Code != 200 || out["enabled"] != false || out["hasApiKey"] != true {
		t.Fatalf("save while off: %d %v", w.Code, out)
	}
	if n := auditCount(h, `action = 'integrations.prowlarr.disable'`); n != 1 {
		t.Fatalf("disable audit rows = %d", n)
	}

	out = setEnabled(t, h, "prowlarr", true)
	if out["enabled"] != true || out["available"] != true {
		t.Fatalf("enable answer %v", out)
	}
	if c := capability(t, h.s.Prowlarr()); c["available"] != true || c["configured"] != true {
		t.Fatalf("capabilities after enable %v", c)
	}
	if w, _ := h.do(t, "GET", "/api/prowlarr/search?q=debian", nil); w.Code != 200 {
		t.Fatalf("search after enable: %d %s", w.Code, w.Body)
	}
	if n := auditCount(h, `action = 'integrations.prowlarr.enable'`); n != 1 {
		t.Fatalf("enable audit rows = %d", n)
	}
}

func TestTransmissionDisablePausesPoller(t *testing.T) {
	h, ft := newWatchHarness(t)
	h.s.downloads = &countingSink{}
	clock(h)

	out := setEnabled(t, h, "transmission", false)
	if out["enabled"] != false || out["configured"] != true || out["hasPassword"] != true || out["username"] != "kushal" {
		t.Fatalf("disable answer %v", out)
	}
	select {
	case <-h.s.wake:
	default:
		t.Fatal("disable did not wake the poller")
	}
	if c := capability(t, h.s.Transmission()); c["available"] != false || c["configured"] != false {
		t.Fatalf("capabilities while off %v", c)
	}
	takeCalls(ft)
	h.s.pollerStep(context.Background(), pollerT0, true)
	stepEvery(h, 12)
	if got := takeCalls(ft); len(got) != 0 {
		t.Fatalf("calls while off = %v; want none", got)
	}
	if w, out := h.do(t, "GET", "/api/transmission/torrents", nil); w.Code != 409 || out["code"] != "not_configured" {
		t.Fatalf("torrents while off: %d %v", w.Code, out)
	}
	if w, _ := h.do(t, "POST", "/api/transmission/torrents", map[string]any{"magnet": "magnet:?xt=urn:btih:2c6b6858d61da9543d4231a71db4b1c9264b0685"}); w.Code != 409 {
		t.Fatalf("add while off: %d %s", w.Code, w.Body)
	}
	if _, out := h.do(t, "POST", "/api/integrations/transmission/test", nil); out["ok"] != true {
		t.Fatalf("test while off: %v", out)
	}

	out = setEnabled(t, h, "transmission", true)
	if out["enabled"] != true || out["available"] != true {
		t.Fatalf("enable answer %v", out)
	}
	takeCalls(ft)
	h.s.pollerStep(context.Background(), pollerT0.Add(time.Minute), true)
	if got := takeCalls(ft); count(got, "torrent-get") != 1 {
		t.Fatalf("calls after enable = %v; want a poll", got)
	}
}

func TestAutoRemoveOffWhileDisabled(t *testing.T) {
	h, ft := removeHarness(t, true)
	setEnabled(t, h, "transmission", false)
	done := ft.AddTorrent("done.iso", 6, 1)
	h.s.pollerStep(context.Background(), pollerT0, true)
	if got := ft.Methods(); count(got, "torrent-remove") != 0 {
		t.Fatalf("calls while off = %v; want no removal", got)
	}
	setEnabled(t, h, "transmission", true)
	h.s.pollerStep(context.Background(), pollerT0.Add(time.Minute), true)
	if got, _ := ft.Get(done.ID); !got.Removed {
		t.Fatal("finished torrent not removed after enable")
	}
}

func TestPlexDisable(t *testing.T) {
	fp := newFakePlex(t)
	h := newHarness(t, config.Config{})
	if w, _ := h.do(t, "PUT", "/api/integrations/plex", map[string]any{"url": fp.URL, "token": plexTok}); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	out := setEnabled(t, h, "plex", false)
	if out["enabled"] != false || out["configured"] != true || out["hasToken"] != true || out["available"] != false {
		t.Fatalf("disable answer %v", out)
	}
	if h.s.plexConfigured() {
		t.Fatal("plex counts as configured while off")
	}
	if c := capability(t, h.s.Plex()); c["available"] != false || c["configured"] != false {
		t.Fatalf("capabilities while off %v", c)
	}
	if w, out := h.do(t, "GET", "/api/plex/activity", nil); w.Code != 409 || out["code"] != "not_configured" {
		t.Fatalf("activity while off: %d %v", w.Code, out)
	}
	if _, out := h.do(t, "POST", "/api/integrations/plex/test", nil); out["ok"] != true {
		t.Fatalf("test while off: %v", out)
	}
	out = setEnabled(t, h, "plex", true)
	if out["enabled"] != true || out["available"] != true {
		t.Fatalf("enable answer %v", out)
	}
	if !h.s.plexConfigured() {
		t.Fatal("plex not configured after enable")
	}
	if w, _ := h.do(t, "GET", "/api/plex/activity", nil); w.Code != 200 {
		t.Fatalf("activity after enable: %d %s", w.Code, w.Body)
	}
}

func TestEnabledRequests(t *testing.T) {
	h := newHarness(t, config.Config{})
	if w, _ := h.do(t, "PUT", "/api/integrations/sonarr/enabled", map[string]any{"enabled": false}); w.Code != 404 {
		t.Fatalf("unknown integration: %d", w.Code)
	}
	if w, _ := h.do(t, "PUT", "/api/integrations/plex/enabled", map[string]any{}); w.Code != 400 {
		t.Fatalf("missing enabled: %d", w.Code)
	}
	if w, out := h.do(t, "PUT", "/api/integrations/plex/enabled", map[string]any{"enabled": false}); w.Code != 409 || out["code"] != "not_configured" {
		t.Fatalf("disable unconfigured: %d %v", w.Code, out)
	}
	if out := setEnabled(t, h, "plex", true); out["enabled"] != true || out["configured"] != false {
		t.Fatalf("enable unconfigured %v", out)
	}
	_, all := h.do(t, "GET", "/api/integrations", nil)
	for _, k := range []string{"prowlarr", "transmission", "plex"} {
		if e := all[k].(map[string]any); e["enabled"] != true || e["configured"] != false {
			t.Fatalf("%s never configured should read enabled, not configured: %v", k, all[k])
		}
	}
}

func TestClearingResetsDisabled(t *testing.T) {
	h, _ := newWatchHarness(t)
	setEnabled(t, h, "transmission", false)
	if w, out := h.do(t, "PUT", "/api/integrations/transmission", map[string]any{"url": ""}); w.Code != 200 || out["configured"] != false || out["enabled"] != true {
		t.Fatalf("clear: %d %v", w.Code, out)
	}
}

// A poll already running when Transmission is switched off is dropped:
// nothing is published, notified or removed from it.
func TestPollInFlightWhenDisabled(t *testing.T) {
	ft := transmissiontest.New("kushal", "s3cret")
	t.Cleanup(ft.Close)
	done := ft.AddTorrent("done.iso", 6, 1)
	target, _ := url.Parse(ft.Server.URL)
	var h *harness
	var armed atomic.Bool
	rp := httputil.NewSingleHostReverseProxy(target)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if armed.CompareAndSwap(true, false) {
			h.s.mu.Lock()
			h.s.tr.Disabled = true
			h.s.mu.Unlock()
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	cfg := config.Config{}
	cfg.Integrations.Transmission = config.Transmission{URL: proxy.URL + "/transmission/rpc", Username: "kushal", Password: "s3cret"}
	h = newHarness(t, cfg)
	sink := &countingSink{}
	h.s.downloads = sink
	h.s.now = func() time.Time { return pollerT0 }
	if w, _ := h.do(t, "PUT", "/api/transmission/settings", map[string]any{"removeFinished": true}); w.Code != 200 {
		t.Fatalf("turn on auto-remove: %d", w.Code)
	}
	msgs := subscribe(t, h)
	armed.Store(true)
	h.s.pollerStep(context.Background(), pollerT0, true)
	if got := ft.Methods(); count(got, "torrent-get") == 0 || count(got, "torrent-remove") != 0 {
		t.Fatalf("calls = %v; want a poll and no removal", got)
	}
	if got, _ := ft.Get(done.ID); got.Removed {
		t.Fatal("removed after switch-off")
	}
	if sink.setDls != 0 {
		t.Fatalf("SetDownloads = %d after switch-off; want 0", sink.setDls)
	}
	select {
	case m := <-msgs:
		t.Fatalf("published after switch-off: %s", m)
	case <-time.After(100 * time.Millisecond):
	}
}

// Switching Transmission off replaces the sticky torrent list, so open
// and new Torrents pages stop showing it.
func TestDisablePublishesNotConfigured(t *testing.T) {
	h, _ := newWatchHarness(t)
	h.s.hub.Publish(Topic, &Payload{Torrents: []TorrentView{{Name: "debian.iso"}}, At: pollerT0})
	setEnabled(t, h, "transmission", false)
	msgs := subscribe(t, h)
	select {
	case m := <-msgs:
		if strings.Contains(m, "debian.iso") || !strings.Contains(m, "not configured") {
			t.Fatalf("sticky after switch-off: %s", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no sticky payload")
	}
}

func TestEnabledNoOp(t *testing.T) {
	h, _ := newWatchHarness(t)
	setEnabled(t, h, "transmission", true)
	if n := auditCount(h, `action LIKE 'integrations.transmission.%able'`); n != 0 {
		t.Fatalf("audit rows after a no-op enable = %d; want 0", n)
	}
	setEnabled(t, h, "transmission", false)
	setEnabled(t, h, "transmission", false)
	if n := auditCount(h, `action = 'integrations.transmission.disable'`); n != 1 {
		t.Fatalf("disable audit rows = %d; want 1", n)
	}
}

func TestStreamsDroppedWhenDisabledMidFetch(t *testing.T) {
	src := &fakeWatchSource{pxOK: true}
	src.during = func() { src.pxOK = false }
	dst := &fakeWatchSink{}
	pollStreams(context.Background(), src, dst)
	if dst.setStream != 0 || dst.resetStream != 1 {
		t.Fatalf("Set %d, Reset %d; want 0, 1", dst.setStream, dst.resetStream)
	}
}

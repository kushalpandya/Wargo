// SPDX-License-Identifier: AGPL-3.0-only

package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/store"
)

func fakeOpenMeteo(t *testing.T, calls *map[string]int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*calls)[r.URL.Path]++
		switch r.URL.Path {
		case "/search":
			if r.URL.Query().Get("name") != "Toronto" {
				w.Write([]byte(`{}`))
				return
			}
			w.Write([]byte(`{"results":[{"name":"Toronto","admin1":"Ontario","country":"Canada","latitude":43.7,"longitude":-79.42}]}`))
		case "/forecast":
			if r.URL.Query().Get("latitude") != "43.7000" {
				t.Errorf("latitude %s", r.URL.Query().Get("latitude"))
			}
			w.Write([]byte(`{"current":{"temperature_2m":4.2,"apparent_temperature":1.1,"weather_code":3,"is_day":1,"relative_humidity_2m":71,"wind_speed_10m":14},"daily":{"temperature_2m_max":[6.5],"temperature_2m_min":[-1.2]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	oldF, oldG := forecastURL, geocodingURL
	forecastURL, geocodingURL = srv.URL+"/forecast", srv.URL+"/search"
	t.Cleanup(func() { forecastURL, geocodingURL = oldF, oldG })
}

func newService(t *testing.T, tz string) *Service {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil)
	s.tz = func() string { return tz }
	return s
}

func TestReportFromTimezoneAndCache(t *testing.T) {
	calls := map[string]int{}
	fakeOpenMeteo(t, &calls)
	s := newService(t, "America/Toronto")
	r := s.Report(context.Background())
	if r.Error != "" || !r.Auto || r.Location == nil || r.Location.Name != "Toronto" || r.Current == nil || r.Current.Temp != 4.2 || r.Today.Min != -1.2 {
		t.Fatalf("report %+v", r)
	}
	s.Report(context.Background())
	if calls["/forecast"] != 1 || calls["/search"] != 1 {
		t.Fatalf("expected cached lookups, got %v", calls)
	}
}

func TestNoGuessForUTCAndDisabled(t *testing.T) {
	calls := map[string]int{}
	fakeOpenMeteo(t, &calls)
	s := newService(t, "Etc/UTC")
	if r := s.Report(context.Background()); r.Error == "" || !strings.Contains(r.Error, "Settings") || calls["/forecast"] != 0 {
		t.Fatalf("report %+v calls %v", r, calls)
	}
	s.st.Set(context.Background(), settingsKey, Settings{Enabled: false})
	if r := s.Report(context.Background()); r.Enabled || r.Error != "" || calls["/search"] != 0 {
		t.Fatalf("disabled report %+v calls %v", r, calls)
	}
}

func TestOfflineGeocodingIsCached(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	oldG := geocodingURL
	geocodingURL = srv.URL + "/search"
	t.Cleanup(func() { geocodingURL = oldG })
	s := newService(t, "America/Toronto")
	for range 3 {
		if r := s.Report(context.Background()); r.Error != unavailable {
			t.Fatalf("report %+v", r)
		}
	}
	if calls != 1 {
		t.Fatalf("failed lookup retried %d times", calls)
	}
}

func TestConcurrentMissesShareLookup(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		w.Write([]byte(`{"current":{"temperature_2m":4.2}}`))
	}))
	t.Cleanup(srv.Close)
	oldF := forecastURL
	forecastURL = srv.URL + "/forecast"
	t.Cleanup(func() { forecastURL = oldF })
	s := newService(t, "UTC")
	s.st.Set(context.Background(), settingsKey, Settings{Enabled: true, Location: &Place{Name: "X", Lat: 1, Lon: 2}})
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := s.Report(context.Background()); r.Current == nil || r.Current.Temp != 4.2 {
				t.Errorf("report %+v", r)
			}
		}()
	}
	wg.Wait()
	if calls["/forecast"] != 1 {
		t.Fatalf("forecast fetched %d times", calls["/forecast"])
	}
}

func TestExpiredCacheDropped(t *testing.T) {
	calls := map[string]int{}
	fakeOpenMeteo(t, &calls)
	s := newService(t, "America/Toronto")
	s.cache["old"] = cached{at: time.Now().Add(-time.Hour), ttl: time.Minute}
	s.Report(context.Background())
	if _, ok := s.cache["old"]; ok || len(s.cache) != 1 {
		t.Fatalf("cache %v", s.cache)
	}
}

func TestPutSettingsAudited(t *testing.T) {
	s := newService(t, "UTC")
	s.audit = audit.New(s.st, httpx.NewProxyTrust(nil))
	mux := http.NewServeMux()
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("PUT", "/api/weather/settings", strings.NewReader(`{"enabled":true,"units":"imperial"}`)))
	if w.Code != 200 {
		t.Fatalf("put %d", w.Code)
	}
	var target string
	if err := s.st.DB.QueryRow(`SELECT target FROM audit_log WHERE action = 'weather.settings'`).Scan(&target); err != nil || target != "imperial, auto" {
		t.Fatalf("audit %q %v", target, err)
	}
}

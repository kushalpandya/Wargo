// SPDX-License-Identifier: AGPL-3.0-only

// Package weather shows the current weather on Home, from Open-Meteo
// (open-meteo.com: free, no account or API key). The location is the one
// set in Settings, or guessed from the host's timezone ("Europe/Berlin" →
// Berlin) via Open-Meteo's geocoding; no IP geolocation service is used.
// Lookups happen only when Home asks, and are cached for 15 minutes.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/metrics"
	"github.com/kushalpandya/wargo/internal/store"
)

const (
	settingsKey = "weather"
	cacheTTL    = 15 * time.Minute
	failTTL     = 3 * time.Minute
)

// Endpoints (variables so tests can point them at a fake server).
var (
	forecastURL  = "https://api.open-meteo.com/v1/forecast"
	geocodingURL = "https://geocoding-api.open-meteo.com/v1/search"
)

// Settings are saved per install.
type Settings struct {
	// Enabled shows weather on Home (default on).
	Enabled bool `json:"enabled"`
	// Location set by hand; empty Name means "from the host's timezone".
	Location *Place `json:"location,omitempty"`
	// Units: "metric" (°C, km/h) or "imperial" (°F, mph).
	Units string `json:"units"`
}

// Place is a named location.
type Place struct {
	Name    string  `json:"name"`
	Region  string  `json:"region,omitempty"`
	Country string  `json:"country,omitempty"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

// Report is what Home shows.
type Report struct {
	Enabled  bool   `json:"enabled"`
	Units    string `json:"units"`
	Location *Place `json:"location,omitempty"`
	// Auto: the location was guessed from the timezone.
	Auto    bool     `json:"auto,omitempty"`
	Current *Current `json:"current,omitempty"`
	Today   *Today   `json:"today,omitempty"`
	// UpdatedAt is when Open-Meteo was asked (unix ms).
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Current struct {
	Temp     float64 `json:"temp"`
	Feels    float64 `json:"feels"`
	Code     int     `json:"code"` // WMO weather code
	IsDay    bool    `json:"isDay"`
	Humidity float64 `json:"humidity"`
	Wind     float64 `json:"wind"`
}

type Today struct {
	Max float64 `json:"max"`
	Min float64 `json:"min"`
}

type Service struct {
	st     *store.Store
	audit  *audit.Log // nil in tests
	client *http.Client
	tz     func() string

	mu     sync.Mutex
	cache  map[string]cached    // by lat,lon,units
	auto   map[string]autoPlace // by timezone
	flight map[string]*call     // lookups in progress
}

type cached struct {
	at  time.Time
	ttl time.Duration
	r   Report
}

// autoPlace is a timezone lookup: p is nil when the city wasn't found.
// Failed lookups (failed set) are retried after failTTL.
type autoPlace struct {
	p      *Place
	failed bool
	at     time.Time
}

type call struct {
	done chan struct{}
	v    any
}

func New(st *store.Store, al *audit.Log) *Service {
	return &Service{
		st: st, audit: al, client: &http.Client{Timeout: 8 * time.Second}, tz: metrics.Timezone,
		cache: map[string]cached{}, auto: map[string]autoPlace{}, flight: map[string]*call{},
	}
}

// shared runs fn once per key at a time; concurrent callers wait for its
// result. fn runs even if the first caller goes away (the client timeout
// bounds it). ok is false when ctx ended first.
func (s *Service) shared(ctx context.Context, key string, fn func(context.Context) any) (v any, ok bool) {
	s.mu.Lock()
	c, busy := s.flight[key]
	if !busy {
		c = &call{done: make(chan struct{})}
		s.flight[key] = c
	}
	s.mu.Unlock()
	if !busy {
		c.v = fn(context.WithoutCancel(ctx))
		s.mu.Lock()
		delete(s.flight, key)
		s.mu.Unlock()
		close(c.done)
		return c.v, true
	}
	select {
	case <-c.done:
		return c.v, true
	case <-ctx.Done():
		return nil, false
	}
}

func (s *Service) settings(ctx context.Context) Settings {
	v := Settings{Enabled: true, Units: "metric"}
	if ok, err := s.st.Get(ctx, settingsKey, &v); !ok || err != nil {
		return Settings{Enabled: true, Units: "metric"}
	}
	if v.Units != "imperial" {
		v.Units = "metric"
	}
	return v
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/weather", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, s.Report(r.Context()))
		return nil
	}))
	mux.HandleFunc("GET /api/weather/settings", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		httpx.OK(w, s.settings(r.Context()))
		return nil
	}))
	mux.HandleFunc("PUT /api/weather/settings", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		var v Settings
		if err := httpx.Decode(r, &v); err != nil {
			return err
		}
		if v.Units != "imperial" {
			v.Units = "metric"
		}
		if v.Location != nil {
			p := v.Location
			p.Name = strings.TrimSpace(p.Name)
			if p.Name == "" {
				v.Location = nil
			} else if p.Lat < -90 || p.Lat > 90 || p.Lon < -180 || p.Lon > 180 || len(p.Name) > 120 || len(p.Region) > 120 || len(p.Country) > 120 {
				return httpx.BadRequest("invalid location")
			}
		}
		if err := s.st.Set(r.Context(), settingsKey, v); err != nil {
			return err
		}
		if s.audit != nil {
			s.audit.Record(r, "weather.settings", describe(v), nil, nil)
		}
		httpx.OK(w, v)
		return nil
	}))
	mux.HandleFunc("GET /api/weather/search", httpx.Handler(func(w http.ResponseWriter, r *http.Request) error {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) < 2 || len(q) > 80 {
			httpx.OK(w, []Place{})
			return nil
		}
		places, err := s.geocode(r.Context(), q, 8)
		if err != nil {
			return httpx.Errorf(http.StatusBadGateway, "weather_unavailable", "Couldn't search places right now")
		}
		httpx.OK(w, places)
		return nil
	}))
}

// describe summarizes settings for the audit log.
func describe(v Settings) string {
	if !v.Enabled {
		return "off"
	}
	place := "auto"
	if v.Location != nil {
		place = v.Location.Name
	}
	return v.Units + ", " + place
}

const unavailable = "Weather is unavailable right now."

// Report returns the current weather for the configured or guessed place.
func (s *Service) Report(ctx context.Context) Report {
	set := s.settings(ctx)
	rep := Report{Enabled: set.Enabled, Units: set.Units}
	if !set.Enabled {
		return rep
	}
	place := set.Location
	if place == nil {
		var err error
		place, err = s.autoPlace(ctx)
		rep.Auto = true
		if err != nil {
			rep.Error = unavailable
			return rep
		}
		if place == nil {
			rep.Error = "Set your location in Settings → Appearance to see the weather."
			return rep
		}
	}
	rep.Location = place
	key := fmt.Sprintf("%.3f,%.3f,%s", place.Lat, place.Lon, set.Units)
	v, ok := s.shared(ctx, "forecast:"+key, func(ctx context.Context) any {
		if r, ok := s.cached(key); ok {
			return r
		}
		out := rep
		cur, today, err := s.forecast(ctx, place, set.Units)
		ttl := cacheTTL
		if err != nil {
			out.Error = unavailable
			ttl = failTTL
		} else {
			out.Current, out.Today, out.UpdatedAt = cur, today, time.Now().UnixMilli()
		}
		s.mu.Lock()
		now := time.Now()
		for k, c := range s.cache { // drop expired entries
			if now.Sub(c.at) >= c.ttl {
				delete(s.cache, k)
			}
		}
		s.cache[key] = cached{at: now, ttl: ttl, r: out}
		s.mu.Unlock()
		return out
	})
	if !ok {
		rep.Error = unavailable
		return rep
	}
	r := v.(Report)
	r.Location, r.Auto, r.Enabled, r.Units = rep.Location, rep.Auto, true, set.Units
	return r
}

func (s *Service) cached(key string) (Report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cache[key]
	return c.r, ok && time.Since(c.at) < c.ttl
}

// autoPlace guesses a place from the host's timezone (the city part of
// "Region/City"), resolving it with Open-Meteo's geocoding once. It
// returns nil without an error when the timezone names no city.
func (s *Service) autoPlace(ctx context.Context) (*Place, error) {
	tz := s.tz()
	region, city, ok := strings.Cut(tz, "/")
	if !ok || region == "Etc" || city == "" {
		return nil, nil
	}
	if i := strings.LastIndex(city, "/"); i >= 0 { // America/Argentina/Buenos_Aires
		city = city[i+1:]
	}
	city = strings.ReplaceAll(city, "_", " ")
	v, ok := s.shared(ctx, "tz:"+tz, func(ctx context.Context) any {
		s.mu.Lock()
		a, done := s.auto[tz]
		s.mu.Unlock()
		if done && (!a.failed || time.Since(a.at) < failTTL) {
			return a
		}
		places, err := s.geocode(ctx, city, 1)
		a = autoPlace{failed: err != nil, at: time.Now()}
		if len(places) > 0 {
			a.p = &places[0]
		}
		s.mu.Lock()
		s.auto[tz] = a
		s.mu.Unlock()
		return a
	})
	if !ok {
		return nil, ctx.Err()
	}
	a := v.(autoPlace)
	if a.failed {
		return nil, errors.New("weather: could not look up the timezone's city")
	}
	return a.p, nil
}

func (s *Service) get(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "wargo")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("open-meteo: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

func (s *Service) geocode(ctx context.Context, name string, count int) ([]Place, error) {
	q := url.Values{"name": {name}, "count": {fmt.Sprint(count)}, "language": {"en"}, "format": {"json"}}
	var res struct {
		Results []struct {
			Name      string  `json:"name"`
			Admin1    string  `json:"admin1"`
			Country   string  `json:"country"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := s.get(ctx, geocodingURL+"?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	out := make([]Place, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, Place{Name: r.Name, Region: r.Admin1, Country: r.Country, Lat: r.Latitude, Lon: r.Longitude})
	}
	return out, nil
}

func (s *Service) forecast(ctx context.Context, p *Place, units string) (*Current, *Today, error) {
	q := url.Values{
		"latitude":      {fmt.Sprintf("%.4f", p.Lat)},
		"longitude":     {fmt.Sprintf("%.4f", p.Lon)},
		"current":       {"temperature_2m,apparent_temperature,weather_code,is_day,relative_humidity_2m,wind_speed_10m"},
		"daily":         {"temperature_2m_max,temperature_2m_min"},
		"timezone":      {"auto"},
		"forecast_days": {"1"},
	}
	if units == "imperial" {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
	}
	var res struct {
		Current struct {
			Temp     float64 `json:"temperature_2m"`
			Feels    float64 `json:"apparent_temperature"`
			Code     int     `json:"weather_code"`
			IsDay    int     `json:"is_day"`
			Humidity float64 `json:"relative_humidity_2m"`
			Wind     float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Daily struct {
			Max []float64 `json:"temperature_2m_max"`
			Min []float64 `json:"temperature_2m_min"`
		} `json:"daily"`
	}
	if err := s.get(ctx, forecastURL+"?"+q.Encode(), &res); err != nil {
		return nil, nil, err
	}
	c := res.Current
	cur := &Current{Temp: c.Temp, Feels: c.Feels, Code: c.Code, IsDay: c.IsDay == 1, Humidity: c.Humidity, Wind: c.Wind}
	var today *Today
	if len(res.Daily.Max) > 0 && len(res.Daily.Min) > 0 {
		today = &Today{Max: res.Daily.Max[0], Min: res.Daily.Min[0]}
	}
	return cur, today, nil
}

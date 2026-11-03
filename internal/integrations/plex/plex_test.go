// SPDX-License-Identifier: AGPL-3.0-only

package plex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "plexTOKEN_0123456789"

const sessionsJSON = `{"MediaContainer":{"size":2,"Metadata":[
 {"ratingKey":"101","type":"episode","title":"Who Is Alive?","grandparentTitle":"Severance","parentIndex":2,"index":3,
  "viewOffset":600000,"duration":3000000,"thumb":"/library/metadata/101/thumb/1700000000","grandparentThumb":"/library/metadata/99/thumb/1700000001",
  "User":{"id":"1","title":"kushal"},"Player":{"title":"Living Room TV","product":"Plex for LG","state":"paused","local":true},
  "Media":[{"Part":[{"decision":"directplay"}]}]},
 {"ratingKey":"202","type":"movie","title":"Heat","year":1995,"viewOffset":"1000","duration":"6000",
  "thumb":"/library/metadata/202/thumb/1","User":{"title":"guest"},"Player":{"title":"Chrome","product":"Plex Web","state":"playing","local":"0"},
  "TranscodeSession":{"videoDecision":"transcode","audioDecision":"copy"},"Media":[{"Part":[{"decision":"transcode"}]}]}
]}}`

const historyJSON = `{"MediaContainer":{"size":2,"Metadata":[
 {"historyKey":"/status/sessions/history/2","type":"episode","title":"Pilot","grandparentTitle":"Carrie","parentIndex":1,"index":1,"viewedAt":1793499294,"accountID":1},
 {"historyKey":"/status/sessions/history/1","type":"movie","title":"Heat","viewedAt":1793000000,"accountID":7}
]}}`

const recentJSON = `{"MediaContainer":{"size":2,"Metadata":[
 {"ratingKey":"11260","type":"movie","title":"Resident Evil","year":2002,"addedAt":1793639066,"thumb":"/library/metadata/11260/thumb/1793639070"},
 {"ratingKey":"11246","type":"season","title":"Season 1","parentTitle":"Carrie","index":1,"leafCount":8,"addedAt":1793107437,"thumb":"/library/metadata/11246/thumb/1793107441"}
]}}`

func fakePlex(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("missing Accept header on %s", r.URL.Path)
		}
		if strings.Contains(r.URL.RawQuery, testToken) {
			t.Errorf("token in URL: %s", r.URL)
		}
		if r.URL.Path == "/identity" {
			w.Write([]byte(`{"MediaContainer":{"size":0,"claimed":true,"machineIdentifier":"abc","version":"1.43.5.11079-b3b13e506"}}`))
			return
		}
		if r.Header.Get("X-Plex-Token") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/status/sessions":
			w.Write([]byte(sessionsJSON))
		case "/status/sessions/history/all":
			if r.URL.Query().Get("sort") != "viewedAt:desc" || r.Header.Get("X-Plex-Container-Size") != "5" || r.Header.Get("X-Plex-Container-Start") != "0" {
				t.Errorf("history paging: %s %v", r.URL.RawQuery, r.Header)
			}
			w.Write([]byte(historyJSON))
		case "/library/recentlyAdded":
			w.Write([]byte(recentJSON))
		case "/accounts":
			w.Write([]byte(`{"MediaContainer":{"size":3,"Account":[{"id":0,"name":""},{"id":1,"name":"kushal"},{"id":7,"name":"guest"}]}}`))
		case "/photo/:/transcode":
			q := r.URL.Query()
			if q.Get("url") != "/library/metadata/1/thumb/2" || q.Get("width") != "80" || q.Get("height") != "120" {
				t.Errorf("photo query: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("\xff\xd8\xffjpeg"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient(t *testing.T) {
	srv := fakePlex(t)
	ctx := context.Background()
	c := New(srv.URL+"/", testToken)

	v, err := c.Check(ctx)
	if err != nil || v != "1.43.5.11079-b3b13e506" {
		t.Fatal(v, err)
	}
	if _, err := New(srv.URL, "wrongtoken0123456789").Check(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}

	ss, err := c.Sessions(ctx)
	if err != nil || len(ss) != 2 {
		t.Fatal(ss, err)
	}
	a, b := ss[0], ss[1]
	if a.User.Title != "kushal" || a.Player.State != "paused" || !bool(a.Player.Local) || a.ParentIndex != 2 || a.Index != 3 || a.ViewOffset != 600000 {
		t.Fatalf("session a: %+v", a)
	}
	if a.Decision() != "directplay" || a.Poster() != "/library/metadata/99/thumb/1700000001" {
		t.Fatalf("session a decision/poster: %s %s", a.Decision(), a.Poster())
	}
	if b.Decision() != "transcode" || bool(b.Player.Local) || b.Year != 1995 || b.Duration != 6000 {
		t.Fatalf("session b: %+v", b)
	}

	h, err := c.History(ctx, 5)
	if err != nil || len(h) != 2 || h[0].ViewedAt != 1793499294 || h[1].AccountID != 7 {
		t.Fatal(h, err)
	}
	r, err := c.RecentlyAdded(ctx, 1)
	if err != nil || len(r) != 1 || r[0].Title != "Resident Evil" || r[0].AddedAt != 1793639066 {
		t.Fatal(r, err)
	}
	acc, err := c.Accounts(ctx)
	if err != nil || acc[1] != "kushal" || acc[7] != "guest" || len(acc) != 2 {
		t.Fatal(acc, err)
	}

	img, ct, err := c.Photo(ctx, "/library/metadata/1/thumb/2", 80, 120)
	if err != nil || ct != "image/jpeg" || len(img) == 0 {
		t.Fatal(ct, err)
	}
}

func TestDecision(t *testing.T) {
	m := Metadata{Type: "movie"}
	m.Media = append(m.Media, struct {
		Part []struct {
			Decision string `json:"decision"`
		} `json:"Part"`
	}{Part: []struct {
		Decision string `json:"decision"`
	}{{Decision: "copy"}}})
	if m.Decision() != "directstream" {
		t.Fatal(m.Decision())
	}
	if (Metadata{}).Decision() != "directplay" {
		t.Fatal("default")
	}
}

func TestRedact(t *testing.T) {
	s := Redact("GET /x?X-Plex-Token=abcdef123456&y=1 failed")
	if strings.Contains(s, "abcdef123456") || !strings.Contains(s, "y=1") {
		t.Fatal(s)
	}
}

func TestNotPlex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/photo/:/transcode" {
			w.Header().Set("Content-Type", "image/svg+xml")
		}
		w.Write([]byte(`<html>hello</html>`))
	}))
	defer srv.Close()
	c := New(srv.URL, testToken)
	if _, err := c.Identity(context.Background()); err == nil {
		t.Fatal("non-Plex server accepted")
	}
	if _, _, err := c.Photo(context.Background(), "/library/metadata/1/thumb/2", 80, 120); !errors.Is(err, ErrNotImage) {
		t.Fatalf("svg or html passed as an image: %v", err)
	}
}

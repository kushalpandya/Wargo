// SPDX-License-Identifier: AGPL-3.0-only

// Package transmissiontest provides an in-memory fake Transmission RPC server
// for tests (session-id handshake, basic auth, torrent-get/start/stop/remove/add).
package transmissiontest

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Torrent is a fake torrent's state.
type Torrent struct {
	ID           int64   `json:"id"`
	HashString   string  `json:"hashString"`
	Name         string  `json:"name"`
	Status       int     `json:"status"`
	PercentDone  float64 `json:"percentDone"`
	SizeWhenDone int64   `json:"sizeWhenDone"`
	Removed      bool    `json:"-"`
	DataDeleted  bool    `json:"-"`
	Source       string  `json:"-"` // magnet or "metainfo"
}

// Fake is a fake Transmission RPC endpoint.
type Fake struct {
	*httptest.Server
	User, Pass string
	SessionID  string

	mu       sync.Mutex
	torrents map[int64]*Torrent
	nextID   int64
	// Calls records RPC method names in order.
	Calls []string
	// Handshakes counts 409 responses sent.
	Handshakes int
}

// New starts a fake with basic auth user/pass ("" disables auth).
func New(user, pass string) *Fake {
	f := &Fake{User: user, Pass: pass, SessionID: "sid-1", torrents: map[int64]*Torrent{}, nextID: 1}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL of the RPC endpoint.
func (f *Fake) RPCURL() string { return f.Server.URL + "/transmission/rpc" }

// AddTorrent seeds a torrent.
func (f *Fake) AddTorrent(name string, status int, pct float64) *Torrent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.add(name, status, pct, "")
}

func (f *Fake) add(name string, status int, pct float64, src string) *Torrent {
	h := sha1.Sum([]byte(name))
	t := &Torrent{ID: f.nextID, HashString: hex.EncodeToString(h[:]), Name: name, Status: status, PercentDone: pct, SizeWhenDone: 1 << 30, Source: src}
	f.torrents[t.ID] = t
	f.nextID++
	return t
}

// Get returns a copy of torrent id's state.
func (f *Fake) Get(id int64) (Torrent, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.torrents[id]
	if !ok {
		return Torrent{}, false
	}
	return *t, true
}

// Methods returns recorded calls.
func (f *Fake) Methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Calls...)
}

// RotateSession forces the next request to perform the 409 handshake again.
func (f *Fake) RotateSession(id string) {
	f.mu.Lock()
	f.SessionID = id
	f.mu.Unlock()
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/transmission/rpc" {
		http.NotFound(w, r)
		return
	}
	if f.User != "" || f.Pass != "" {
		u, p, ok := r.BasicAuth()
		if !ok || u != f.User || p != f.Pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="Transmission"`)
			http.Error(w, "401: Unauthorized", http.StatusUnauthorized)
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Transmission-Session-Id") != f.SessionID {
		f.Handshakes++
		w.Header().Set("X-Transmission-Session-Id", f.SessionID)
		http.Error(w, "409: Conflict", http.StatusConflict)
		return
	}
	var req struct {
		Method    string         `json:"method"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f.Calls = append(f.Calls, req.Method)
	result, args := "success", map[string]any{}
	ids := f.ids(req.Arguments["ids"])
	switch req.Method {
	case "session-get":
		args = map[string]any{"version": "4.0.6 (38c164933e)", "rpc-version": 17, "download-dir": "/downloads/complete"}
	case "session-stats":
		var active, paused int
		for _, t := range f.torrents {
			if t.Removed {
				continue
			}
			if t.Status == 0 {
				paused++
			} else {
				active++
			}
		}
		args = map[string]any{"downloadSpeed": 1234567, "uploadSpeed": 45678, "activeTorrentCount": active, "pausedTorrentCount": paused}
	case "free-space":
		args = map[string]any{"path": req.Arguments["path"], "size-bytes": int64(500) << 30}
	case "torrent-get":
		list := []map[string]any{}
		for id := int64(1); id < f.nextID; id++ {
			t, ok := f.torrents[id]
			if !ok || t.Removed {
				continue
			}
			list = append(list, map[string]any{
				"id": t.ID, "hashString": t.HashString, "name": t.Name, "status": t.Status,
				"percentDone": t.PercentDone, "rateDownload": 0, "rateUpload": 0, "eta": -1,
				"sizeWhenDone": t.SizeWhenDone, "leftUntilDone": int64(float64(t.SizeWhenDone) * (1 - t.PercentDone)),
				"uploadRatio": 0.5, "error": 0, "errorString": "", "addedDate": 1700000000,
				"peersConnected": 3, "downloadDir": "/downloads",
			})
		}
		args = map[string]any{"torrents": list}
	case "torrent-start":
		for _, t := range ids {
			if t.PercentDone >= 1 {
				t.Status = 6
			} else {
				t.Status = 4
			}
		}
	case "torrent-stop":
		for _, t := range ids {
			t.Status = 0
		}
	case "torrent-remove":
		del, _ := req.Arguments["delete-local-data"].(bool)
		for _, t := range ids {
			t.Removed = true
			t.DataDeleted = del
		}
	case "torrent-add":
		src, _ := req.Arguments["filename"].(string)
		if src == "" {
			if _, ok := req.Arguments["metainfo"].(string); ok {
				src = "metainfo"
			}
		}
		if src == "" {
			result = "no filename or metainfo"
			break
		}
		for _, t := range f.torrents {
			if t.Source == src && !t.Removed {
				args = map[string]any{"torrent-duplicate": map[string]any{"id": t.ID, "name": t.Name, "hashString": t.HashString}}
				goto done
			}
		}
		{
			t := f.add("added-"+src[:min(len(src), 24)], 4, 0, src)
			args = map[string]any{"torrent-added": map[string]any{"id": t.ID, "name": t.Name, "hashString": t.HashString}}
		}
	default:
		result = "method name not recognized"
	}
done:
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"result": result, "arguments": args})
}

func (f *Fake) ids(v any) []*Torrent {
	list, _ := v.([]any)
	var out []*Torrent
	for _, x := range list {
		switch id := x.(type) {
		case float64:
			if t, ok := f.torrents[int64(id)]; ok {
				out = append(out, t)
			}
		case string:
			for _, t := range f.torrents {
				if t.HashString == id {
					out = append(out, t)
				}
			}
		}
	}
	return out
}

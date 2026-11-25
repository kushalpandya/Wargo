// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"net/http"
	"time"

	"github.com/kushalpandya/wargo/internal/audit"
	"github.com/kushalpandya/wargo/internal/httpx"
	"github.com/kushalpandya/wargo/internal/integrations/transmission"
)

// transmissionPrefs is what the owner chose about Transmission.
type transmissionPrefs struct {
	// RemoveFinished removes torrents from Transmission, keeping their
	// files, once they finish. Off when absent.
	RemoveFinished bool `json:"removeFinished"`
}

// removeFinished says whether finished torrents are removed automatically.
func (s *Service) removeFinished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prefs.RemoveFinished
}

const (
	// removedMemo is how long a removed hash is skipped while Transmission
	// keeps listing it. Once it passes, a torrent still listed (or added
	// again with the same hash) is removed again, at most once per period.
	removedMemo = time.Minute
	// removeLimit bounds a removal and its audit rows, which run detached
	// from the poller's context so that a shutdown mid-removal still
	// records what was removed.
	removeLimit = 10 * time.Second
)

// finished says whether t is complete and safe to remove: all of it is
// there, it is stopped or seeding (so not being checked, which can lower
// percentDone, and not a status Wargo does not know) and Transmission
// reports no local error (3), such as missing data.
func finished(t TorrentView) bool {
	switch t.Status {
	case "stopped", "seed-wait", "seeding":
	default:
		return false
	}
	return t.PercentDone >= 1 && t.Error != 3
}

// pruneRemoved forgets the removed hashes that snap no longer lists (the
// removal took effect), those removed removedMemo ago or more, and all of
// them when snap is from another Transmission.
func (s *Service) pruneRemoved(snap *snapshot) {
	p := &s.poller
	if p.removedFrom != snap.c {
		p.removed, p.removedFrom = nil, snap.c
	}
	if len(p.removed) == 0 {
		return
	}
	listed := make(map[string]bool, len(snap.torrents))
	for _, t := range snap.torrents {
		listed[t.Hash] = true
	}
	for h, at := range p.removed {
		if !listed[h] || snap.at.Sub(at) >= removedMemo {
			delete(p.removed, h)
		}
	}
}

// autoRemove removes the finished torrents of snap from Transmission, never
// their data, when the setting is on. A failure is retried with the next
// snapshot. Only torrents the snapshot lists as finished are touched, so a
// partial list can't remove anything else. A torrent removed less than
// removedMemo ago is skipped while it is still listed, so a Transmission
// that answers a removal with success but keeps the torrent is not asked
// again (and audited again) on every poll.
func (s *Service) autoRemove(ctx context.Context, snap *snapshot) {
	s.pruneRemoved(snap)
	if !s.removeFinished() {
		return
	}
	p := &s.poller
	var ids []transmission.ID
	names := map[string]string{}
	var hashes []string
	for _, t := range snap.torrents {
		if t.Hash == "" || !finished(t) {
			continue
		}
		if _, done := p.removed[t.Hash]; done {
			continue
		}
		ids = append(ids, transmission.ID{Hash: t.Hash})
		hashes = append(hashes, t.Hash)
		names[t.Hash] = t.Name
	}
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeLimit)
	defer cancel()
	err := snap.c.RemoveMany(ctx, ids, false)
	s.audit.Outcome("transmission.autoremove", "background", err, nil)
	if err != nil {
		return
	}
	if p.removed == nil {
		p.removed = map[string]time.Time{}
	}
	for _, h := range hashes {
		p.removed[h] = snap.at
		s.audit.Write(nil, audit.Event{Action: "transmission.remove", Target: h, Details: map[string]any{"name": names[h], "automatic": true, "deleteData": false}})
	}
}

func (s *Service) handleGetTransmissionPrefs(w http.ResponseWriter, r *http.Request) error {
	s.mu.Lock()
	p := s.prefs
	s.mu.Unlock()
	httpx.OK(w, p)
	return nil
}

func (s *Service) handlePutTransmissionPrefs(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		RemoveFinished *bool `json:"removeFinished"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if in.RemoveFinished == nil {
		return httpx.BadRequest("removeFinished is required")
	}
	next := transmissionPrefs{RemoveFinished: *in.RemoveFinished}
	s.mu.Lock()
	err := s.st.Set(r.Context(), keyTransmissionPrefs, next)
	if err == nil {
		s.prefs = next
	}
	s.mu.Unlock()
	s.audit.Record(r, "transmission.settings", "", err, map[string]any{"removeFinished": next.RemoveFinished})
	if err != nil {
		return err
	}
	if next.RemoveFinished {
		s.wakePoller()
	}
	httpx.OK(w, next)
	return nil
}

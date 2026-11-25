// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/integrations/transmission"
)

const (
	// slowEvery is how often the download dir and free space are refreshed.
	slowEvery = time.Minute
	// pageReuse is how old a full snapshot may be and still serve a page load.
	pageReuse = 2 * time.Second
	pollLimit = 10 * time.Second
)

// snapshot is one fetch from Transmission. A full one has every torrent
// field and the session stats; a light one has only downloadFields.
type snapshot struct {
	full     bool
	at       time.Time
	c        *transmission.Client
	torrents []TorrentView
	stats    StatsView // only set when full
}

type snapState struct {
	mu   sync.Mutex
	last *snapshot
}

// flightState lets concurrent page loads share one poll.
type flightState struct {
	mu  sync.Mutex
	cur *flight
}

// flight is one shared poll; p and err are set before done closes.
type flight struct {
	done chan struct{}
	p    *Payload
	err  error
}

// slowState is the data that changes rarely: the free space in
// Transmission's download dir.
type slowState struct {
	mu   sync.Mutex
	c    *transmission.Client
	at   time.Time
	free *int64
}

// latest is the most recent successful snapshot, or nil.
func (s *Service) latest() *snapshot {
	s.snap.mu.Lock()
	defer s.snap.mu.Unlock()
	return s.snap.last
}

// poll takes one snapshot from the current client and stores it as the
// latest. A full one also refreshes the slow data when it is over a minute
// old or from another client; a failure there does not fail the poll.
func (s *Service) poll(ctx context.Context, full bool) (*snapshot, error) {
	c := s.transmissionClient()
	if c == nil {
		return nil, errTransmissionNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, pollLimit)
	defer cancel()
	snap := &snapshot{full: full, c: c}
	fields := downloadFields
	if full {
		fields = transmission.TorrentFields
	}
	list, err := c.TorrentsWith(ctx, fields)
	if err != nil {
		return nil, err
	}
	snap.torrents = make([]TorrentView, 0, len(list))
	for _, t := range list {
		snap.torrents = append(snap.torrents, TorrentView{
			ID: t.ID, Hash: t.HashString, Name: t.Name, Status: transmission.StatusName(t.Status),
			PercentDone: t.PercentDone, RateDownload: t.RateDownload, RateUpload: t.RateUpload, ETA: t.ETA,
			SizeWhenDone: t.SizeWhenDone, LeftUntilDone: t.LeftUntilDone, UploadRatio: t.UploadRatio,
			Error: t.Error, ErrorString: t.ErrorString, AddedDate: t.AddedDate, PeersConnected: t.PeersConnected,
			DownloadDir: t.DownloadDir,
		})
	}
	if full {
		st, err := c.Stats(ctx)
		if err != nil {
			return nil, err
		}
		snap.stats = StatsView{
			DownloadSpeed: st.DownloadSpeed, UploadSpeed: st.UploadSpeed,
			ActiveTorrentCount: st.ActiveTorrentCount, PausedTorrentCount: st.PausedTorrentCount,
			FreeSpace: s.freeSpace(ctx, c),
		}
	}
	snap.at = s.now()
	s.snap.mu.Lock()
	s.snap.last = snap
	s.snap.mu.Unlock()
	return snap, nil
}

// freeSpace returns the free space in Transmission's download dir, asking
// again only when the cached value is over slowEvery old or from another
// client.
func (s *Service) freeSpace(ctx context.Context, c *transmission.Client) *int64 {
	s.slow.mu.Lock()
	defer s.slow.mu.Unlock()
	now := s.now()
	if s.slow.c == c && now.Sub(s.slow.at) < slowEvery {
		return s.slow.free
	}
	s.slow.c, s.slow.at, s.slow.free = c, now, nil
	sess, err := c.Session(ctx)
	if err != nil || sess.DownloadDir == "" {
		return nil
	}
	if n, err := c.FreeSpace(ctx, sess.DownloadDir); err == nil && n >= 0 {
		s.slow.free = &n
	}
	return s.slow.free
}

// pagePayload serves a page load from the latest full snapshot when it came
// from the current client at most pageReuse ago, and otherwise polls.
// Concurrent callers that miss share one poll.
func (s *Service) pagePayload(ctx context.Context) (*Payload, error) {
	c := s.transmissionClient()
	if c == nil {
		return nil, errTransmissionNotConfigured
	}
	f := &s.flight
	f.mu.Lock()
	if snap := s.latest(); snap != nil && snap.full && snap.c == c && s.now().Sub(snap.at) <= pageReuse {
		f.mu.Unlock()
		return snap.payload(), nil
	}
	fl := f.cur
	if fl == nil {
		fl = &flight{done: make(chan struct{})}
		f.cur = fl
		// Detached from this caller, so that its disconnecting does not
		// fail the callers sharing the poll; poll bounds it with pollLimit.
		go func(ctx context.Context) {
			snap, err := s.poll(ctx, true)
			if err == nil {
				fl.p = snap.payload()
			}
			fl.err = err
			f.mu.Lock()
			f.cur = nil
			f.mu.Unlock()
			close(fl.done)
		}(context.WithoutCancel(ctx))
	}
	f.mu.Unlock()
	select {
	case <-fl.done:
		return fl.p, fl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (snap *snapshot) payload() *Payload {
	return &Payload{Torrents: snap.torrents, Stats: snap.stats, At: snap.at}
}

// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kushalpandya/wargo/internal/notify"
)

const (
	watchDownloadsEvery = 10 * time.Second
	watchStreamsEvery   = 15 * time.Second
	// maxETA is the longest ETA reported; longer is as good as unknown.
	maxETA = 30 * 24 * time.Hour
)

// SetNotify gives the watcher somewhere to report downloads and streams.
// Call it before Run; without it nothing is watched.
func (s *Service) SetNotify(c *notify.Center) { s.notify = c }

// watchSource is what the watcher polls (the Service, or a fake in tests).
type watchSource interface {
	transmissionConfigured() bool
	plexConfigured() bool
	fetchDownloads(ctx context.Context) ([]notify.Download, error)
	fetchStreams(ctx context.Context) ([]notify.Stream, error)
}

// watchSink is what the watcher reports to (*notify.Center).
type watchSink interface {
	Mode(cat notify.Category) notify.Mode
	SetDownloads(ctx context.Context, list []notify.Download)
	SetStreams(ctx context.Context, list []notify.Stream)
	ResetDownloads()
	ResetStreams()
}

// watch polls Transmission and Plex for notifications, with or without a
// browser open. One goroutine, so the Set calls are serialized.
func (s *Service) watch(ctx context.Context) {
	if s.notify == nil {
		return
	}
	runWatch(ctx, s, s.notify)
}

func runWatch(ctx context.Context, src watchSource, dst watchSink) {
	dl := time.NewTicker(watchDownloadsEvery)
	defer dl.Stop()
	st := time.NewTicker(watchStreamsEvery)
	defer st.Stop()
	pollDownloads(ctx, src, dst)
	pollStreams(ctx, src, dst)
	for {
		select {
		case <-ctx.Done():
			return
		case <-dl.C:
			pollDownloads(ctx, src, dst)
		case <-st.C:
			pollStreams(ctx, src, dst)
		}
	}
}

// pollDownloads reports torrents when Transmission is set up and a
// download category is on. A failed fetch reports nothing: an empty list
// would end every download. While it does not poll, the next report is a
// baseline, so what started meanwhile is not pushed when polling resumes.
func pollDownloads(ctx context.Context, src watchSource, dst watchSink) {
	if !src.transmissionConfigured() || !anyOn(dst, notify.DownloadStarted, notify.DownloadFinished, notify.DownloadError) {
		dst.ResetDownloads()
		return
	}
	list, err := src.fetchDownloads(ctx)
	if err != nil || ctx.Err() != nil {
		return
	}
	dst.SetDownloads(ctx, list)
}

// pollStreams is pollDownloads for Plex sessions.
func pollStreams(ctx context.Context, src watchSource, dst watchSink) {
	if !src.plexConfigured() || !anyOn(dst, notify.StreamStarted) {
		dst.ResetStreams()
		return
	}
	list, err := src.fetchStreams(ctx)
	if err != nil || ctx.Err() != nil {
		return
	}
	dst.SetStreams(ctx, list)
}

func anyOn(dst watchSink, cats ...notify.Category) bool {
	for _, c := range cats {
		if dst.Mode(c) != notify.ModeOff {
			return true
		}
	}
	return false
}

func (s *Service) transmissionConfigured() bool { return s.transmissionSettings().URL != "" }

func (s *Service) plexConfigured() bool {
	p := s.plexSettings()
	return p.URL != "" && p.TokenSealed != ""
}

func (s *Service) fetchDownloads(ctx context.Context) ([]notify.Download, error) {
	p, err := s.payload(ctx)
	if err != nil {
		return nil, err
	}
	return toDownloads(p.Torrents), nil
}

func (s *Service) fetchStreams(ctx context.Context) ([]notify.Stream, error) {
	a, err := s.plexActivity(ctx)
	if err != nil {
		return nil, err
	}
	// Activity succeeds when only history or recent loaded; without
	// sessions it says nothing about streams.
	if msg := a.Errors["sessions"]; msg != "" {
		return nil, errors.New(msg)
	}
	return toStreams(a.Sessions), nil
}

// toDownloads maps Transmission's statuses to the notify ones.
func toDownloads(ts []TorrentView) []notify.Download {
	out := make([]notify.Download, 0, len(ts))
	for _, t := range ts {
		d := notify.Download{
			Hash: t.Hash, Name: t.Name, Percent: t.PercentDone, RateDown: float64(t.RateDownload),
		}
		switch t.Status {
		case "downloading":
			d.Status = "downloading"
		case "check-wait", "checking":
			d.Status = "checking"
		case "download-wait":
			d.Status = "queued"
		case "seed-wait", "seeding":
			d.Status = "seeding"
		default:
			d.Status = "stopped"
			if t.PercentDone >= 1 {
				d.Status = "done"
			}
		}
		// Transmission sends -1 or -2 when it can't tell.
		if t.ETA > 0 && t.ETA <= int64(maxETA/time.Second) {
			d.ETA = time.Duration(t.ETA) * time.Second
		}
		// 2 is a tracker error, 3 a local error; 1 (tracker warning) is
		// common and clears itself, so it isn't one.
		if t.Error == 2 || t.Error == 3 {
			d.Error = t.ErrorString
			if d.Error == "" {
				d.Error = fmt.Sprintf("Transmission error %d", t.Error)
			}
		}
		out = append(out, d)
	}
	return out
}

// toStreams titles each session like "Alex is watching Severance S2 E4"
// ("Alex is listening to" for music).
func toStreams(ss []PlexSession) []notify.Stream {
	out := make([]notify.Stream, 0, len(ss))
	for _, p := range ss {
		what := p.Title
		switch {
		case p.Type == "episode" && p.GrandparentTitle != "":
			what = fmt.Sprintf("%s S%d E%d", p.GrandparentTitle, p.ParentIndex, p.Index)
		case p.Type == "movie" && p.Year > 0:
			what = fmt.Sprintf("%s (%d)", p.Title, p.Year)
		}
		title := what
		if p.User != "" {
			verb := " is watching "
			if p.Type == "track" {
				verb = " is listening to "
			}
			title = p.User + verb + what
		}
		player := p.Player
		if player == "" {
			player = p.Product
		}
		out = append(out, notify.Stream{
			SessionKey: p.SessionKey, User: p.User, RatingKey: p.RatingKey, ShowKey: p.ShowKey,
			Title: title, Player: player, State: p.State,
			ViewOffset: time.Duration(p.ViewOffset) * time.Millisecond,
			Duration:   time.Duration(p.Duration) * time.Millisecond,
		})
	}
	return out
}

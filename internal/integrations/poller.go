// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import (
	"context"
	"time"

	"github.com/kushalpandya/wargo/internal/integrations/transmission"
	"github.com/kushalpandya/wargo/internal/notify"
)

const (
	watchedEvery    = 2 * time.Second
	backgroundEvery = 10 * time.Second
	backoffStart    = 2 * time.Second
	backoffMax      = time.Minute
	// tickSlack is how early a tick may be taken as due: the loop ticks
	// every second, and a tick that arrives a hair early must not wait
	// for the next one.
	tickSlack = 500 * time.Millisecond
)

// pace says how often Transmission is polled, and with which snapshot.
type pace struct {
	every time.Duration
	full  bool
}

// pollerState is what pollerStep remembers between steps. Only the poller
// goroutine (or a test driving pollerStep) touches it.
type pollerState struct {
	last     time.Time
	failures int
	retryAt  time.Time
	// reported is the client of the last snapshot given to the
	// notifications, to tell when it is another Transmission.
	reported *transmission.Client
	// removed is when autoRemove last removed each hash from removedFrom,
	// so a torrent Transmission keeps listing after a successful removal
	// is not removed and audited again on every poll. See pruneRemoved.
	removed     map[string]time.Time
	removedFrom *transmission.Client
}

func (s *Service) transmissionConfigured() bool { return s.transmissionSettings().URL != "" }

// downloadsOn says whether a download notification category is on.
func (s *Service) downloadsOn() bool {
	return s.downloads != nil && anyOn(s.downloads, notify.DownloadStarted, notify.DownloadFinished, notify.DownloadError)
}

// demand is how often Transmission needs polling right now: every 2 s with
// the full snapshot while the Torrents page is open, every 10 s with the
// light one for notifications or auto-remove, and not at all otherwise.
func (s *Service) demand() pace {
	if !s.transmissionConfigured() {
		return pace{}
	}
	if s.hub != nil && s.hub.Subscribers(Topic) > 0 {
		return pace{every: watchedEvery, full: true}
	}
	if s.downloadsOn() || s.removeFinished() {
		return pace{every: backgroundEvery}
	}
	return pace{}
}

// pollerStep polls Transmission when it is due: when woken, or when now
// reaches the last poll plus the pace and the backoff after a failure has
// passed. Nothing is polled while nothing needs it, woken or not. After a good snapshot it publishes it (page open and full),
// reports the downloads and runs auto-remove, in that order.
func (s *Service) pollerStep(ctx context.Context, now time.Time, woken bool) {
	p := &s.poller
	d := s.demand()
	if !s.transmissionConfigured() || d.every == 0 {
		// Not polling, so the next report is a baseline.
		if s.downloads != nil {
			s.downloads.ResetDownloads()
		}
		return
	}
	if woken {
		p.failures, p.retryAt = 0, time.Time{}
	} else if p.last.Add(d.every).Sub(now) > tickSlack || p.retryAt.Sub(now) > tickSlack {
		return
	}
	p.last = now
	snap, err := s.poll(ctx, d.full)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		p.failures++
		backoff := backoffStart << (p.failures - 1)
		if backoff > backoffMax || backoff <= 0 {
			backoff = backoffMax
		}
		p.retryAt = now.Add(backoff)
		if s.hub != nil && s.hub.Subscribers(Topic) > 0 {
			s.hub.Publish(Topic, &Payload{Torrents: []TorrentView{}, Error: errString(err), At: s.now()})
		}
		return
	}
	p.failures, p.retryAt = 0, time.Time{}
	if snap.full && s.hub != nil && s.hub.Subscribers(Topic) > 0 {
		s.hub.Publish(Topic, snap.payload())
	}
	s.reportDownloads(ctx, snap)
	s.autoRemove(ctx, snap)
}

// reportDownloads gives the notifications the torrents of snap when a
// download category is on. While none is, the next report is a baseline,
// so what started meanwhile is not pushed when reporting resumes. So is
// the first report from another Transmission: the torrents it already has
// are not news.
func (s *Service) reportDownloads(ctx context.Context, snap *snapshot) {
	if s.downloads == nil {
		return
	}
	if !s.downloadsOn() {
		s.downloads.ResetDownloads()
		return
	}
	p := &s.poller
	newSource := p.reported != nil && p.reported != snap.c
	p.reported = snap.c
	if newSource {
		s.downloads.ResetDownloads()
	}
	s.downloads.SetDownloads(ctx, toDownloads(snap.torrents))
}

// wakePoller asks the poller to poll now, for after a change.
func (s *Service) wakePoller() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// runPoller is the one loop that talks to Transmission for the page and
// the notifications.
func (s *Service) runPoller(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	s.pollerStep(ctx, s.now(), false)
	for {
		woken := false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
			woken = true
		}
		s.pollerStep(ctx, s.now(), woken)
	}
}

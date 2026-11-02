// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"sync"
	"time"
)

// Throttle limits authentication attempts per IP with exponential backoff
// and a global budget, so brute force never reaches PAM at scale.
type Throttle struct {
	mu     sync.Mutex
	ips    map[string]*ipState
	global []time.Time
	// GlobalPerMinute caps failed attempts across all IPs.
	GlobalPerMinute int
}

type ipState struct {
	fails int
	until time.Time
	seen  time.Time
}

func NewThrottle() *Throttle {
	return &Throttle{ips: map[string]*ipState{}, GlobalPerMinute: 30}
}

// Allow reports whether ip may attempt now, and if not, how long to wait.
func (t *Throttle) Allow(ip string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.gc(now)
	if s := t.ips[ip]; s != nil && now.Before(s.until) {
		return false, s.until.Sub(now)
	}
	if len(t.global) >= t.GlobalPerMinute {
		return false, t.global[0].Add(time.Minute).Sub(now)
	}
	return true, 0
}

// Fail records a failed attempt.
func (t *Throttle) Fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	s := t.ips[ip]
	if s == nil {
		s = &ipState{}
		t.ips[ip] = s
	}
	s.fails++
	s.seen = now
	// 3 free attempts, then 2s, 4s, 8s ... capped at 15 minutes.
	if s.fails > 3 {
		d := time.Second << min(s.fails-3, 10)
		s.until = now.Add(min(d, 15*time.Minute))
	}
	t.global = append(t.global, now)
}

// Success clears the IP's failure history.
func (t *Throttle) Success(ip string) {
	t.mu.Lock()
	delete(t.ips, ip)
	t.mu.Unlock()
}

func (t *Throttle) gc(now time.Time) {
	i := 0
	for i < len(t.global) && now.Sub(t.global[i]) > time.Minute {
		i++
	}
	t.global = t.global[i:]
	for ip, s := range t.ips {
		if now.Sub(s.seen) > time.Hour {
			delete(t.ips, ip)
		}
	}
}

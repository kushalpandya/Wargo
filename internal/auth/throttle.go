// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"net"
	"sync"
	"time"
)

// Throttle limits authentication attempts per client with exponential
// backoff and a global budget, so brute force never reaches PAM at scale.
//
// Clients are bucketed by IPv4 address or IPv6 /64 (one end-user network
// can trivially rotate through a /64). Every Allow reserves an in-flight
// slot that counts towards the limits until Fail, Success or Release is
// called (or it expires), so a burst of parallel requests cannot all slip
// past the check before the first failure is recorded.
//
// The global budget protects PAM against distributed guessing, but must not
// let an attacker lock the real user out: clients that authenticated
// successfully recently ("known good") are exempt from it (they still have
// their own per-client backoff), and AllowLocal skips it for methods that
// cannot be brute-forced (passkeys).
type Throttle struct {
	mu       sync.Mutex
	ips      map[string]*ipState
	global   []time.Time
	inflight map[string][]time.Time
	good     map[string]time.Time
	// GlobalPerMinute caps failed attempts across all clients.
	GlobalPerMinute int
	// MaxInflight caps concurrent attempts per client.
	MaxInflight int
	now         func() time.Time
	lastGC      time.Time
}

type ipState struct {
	fails int
	until time.Time
	seen  time.Time
}

const (
	inflightTTL = 30 * time.Second
	goodTTL     = 30 * 24 * time.Hour
	maxTracked  = 100_000
)

func NewThrottle() *Throttle {
	return &Throttle{
		ips: map[string]*ipState{}, inflight: map[string][]time.Time{}, good: map[string]time.Time{},
		GlobalPerMinute: 30, MaxInflight: 4, now: time.Now,
	}
}

// throttleKey buckets an address: IPv4 as is, IPv6 by /64.
func throttleKey(ip string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return ip
	}
	if v4 := p.To4(); v4 != nil {
		return v4.String()
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// Allow reports whether ip may attempt now, and if not, how long to wait.
// A successful Allow must be followed by Fail, Success or Release.
func (t *Throttle) Allow(ip string) (bool, time.Duration) { return t.allow(ip, true) }

// AllowLocal is Allow without the global budget (for passkey sign-in,
// which cannot be brute-forced, so the real user is never locked out).
func (t *Throttle) AllowLocal(ip string) (bool, time.Duration) { return t.allow(ip, false) }

func (t *Throttle) allow(ip string, global bool) (bool, time.Duration) {
	k := throttleKey(ip)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.gc(now)
	if s := t.ips[k]; s != nil && now.Before(s.until) {
		return false, s.until.Sub(now)
	}
	if len(t.inflight[k]) >= t.MaxInflight {
		return false, time.Second
	}
	if _, ok := t.good[k]; global && !ok {
		pending := 0
		for _, v := range t.inflight {
			pending += len(v)
		}
		if len(t.global)+pending >= t.GlobalPerMinute {
			wait := time.Second
			if len(t.global) > 0 {
				wait = max(t.global[0].Add(time.Minute).Sub(now), time.Second)
			}
			return false, wait
		}
	}
	t.inflight[k] = append(t.inflight[k], now)
	return true, 0
}

func (t *Throttle) release(k string) {
	if v := t.inflight[k]; len(v) > 1 {
		t.inflight[k] = v[1:]
	} else {
		delete(t.inflight, k)
	}
}

// Release ends an attempt that was neither a failure nor a success.
func (t *Throttle) Release(ip string) {
	t.mu.Lock()
	t.release(throttleKey(ip))
	t.mu.Unlock()
}

// Fail records a failed attempt (counted towards the global budget).
func (t *Throttle) Fail(ip string) { t.fail(ip, true) }

// FailLocal records a failure for ip only (see AllowLocal).
func (t *Throttle) FailLocal(ip string) { t.fail(ip, false) }

func (t *Throttle) fail(ip string, global bool) {
	k := throttleKey(ip)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.release(k)
	s := t.ips[k]
	if s == nil {
		if len(t.ips) >= maxTracked {
			t.gc(now)
		}
		s = &ipState{}
		t.ips[k] = s
	}
	s.fails++
	s.seen = now
	// 3 free attempts, then 2s, 4s, 8s ... capped at 15 minutes.
	if s.fails > 3 {
		d := time.Second << min(s.fails-3, 10)
		s.until = now.Add(min(d, 15*time.Minute))
	}
	if global {
		t.global = append(t.global, now)
	}
}

// Success clears the client's failure history and marks it known good.
func (t *Throttle) Success(ip string) {
	k := throttleKey(ip)
	t.mu.Lock()
	t.release(k)
	delete(t.ips, k)
	if len(t.good) < maxTracked {
		t.good[k] = t.now()
	}
	t.mu.Unlock()
}

func (t *Throttle) gc(now time.Time) {
	i := 0
	for i < len(t.global) && now.Sub(t.global[i]) > time.Minute {
		i++
	}
	t.global = t.global[i:]
	// The map sweeps are O(clients); do them at most once a second.
	if now.Sub(t.lastGC) < time.Second && len(t.ips) < maxTracked {
		return
	}
	t.lastGC = now
	for ip, s := range t.ips {
		if now.Sub(s.seen) > time.Hour && now.After(s.until) {
			delete(t.ips, ip)
		}
	}
	for k, v := range t.inflight {
		j := 0
		for j < len(v) && now.Sub(v[j]) > inflightTTL {
			j++
		}
		if j == len(v) {
			delete(t.inflight, k)
		} else if j > 0 {
			t.inflight[k] = v[j:]
		}
	}
	for k, at := range t.good {
		if now.Sub(at) > goodTTL {
			delete(t.good, k)
		}
	}
}

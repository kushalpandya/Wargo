// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package metrics

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

var (
	linksMu sync.Mutex
	linksAt time.Time
	links   map[string]*Link

	portsOnce sync.Once
	ports     map[string]string

	primMu sync.Mutex
	primAt time.Time
	prim   string
)

// hardwarePorts reads the device to port-type map once.
func hardwarePorts() map[string]string {
	portsOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ports = map[string]string{}
		if out, err := host.Command(ctx, "/usr/sbin/networksetup", "-listallhardwareports").Output(); err == nil {
			ports = parseHardwarePorts(out)
		}
	})
	return ports
}

// GetLinks returns link details per interface, cached for 30 s.
func GetLinks(names []string) map[string]*Link {
	linksMu.Lock()
	defer linksMu.Unlock()
	if time.Since(linksAt) < 30*time.Second && links != nil {
		return links
	}
	hw := hardwarePorts()
	out := map[string]*Link{}
	for _, n := range names {
		l := &Link{Kind: "other"}
		switch {
		case strings.HasPrefix(n, "utun"):
			l.Kind = "vpn"
		case hw[n] != "":
			l.Kind = hw[n]
		case strings.HasPrefix(n, "bridge"):
			l.Kind = "bridge"
		}
		if i, err := net.InterfaceByName(n); err == nil {
			l.Up = i.Flags&net.FlagRunning != 0
		}
		out[n] = l
	}
	links, linksAt = out, time.Now()
	return links
}

// primaryIf returns the interface carrying the default route, cached 30 s.
func primaryIf() string {
	primMu.Lock()
	defer primMu.Unlock()
	if time.Since(primAt) < 30*time.Second {
		return prim
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prim = ""
	if out, err := host.Command(ctx, "/sbin/route", "-n", "get", "default").Output(); err == nil {
		prim = parseRouteGet(out)
	}
	primAt = time.Now()
	return prim
}

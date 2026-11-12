// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package metrics

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kushalpandya/wargo/internal/host"
)

var (
	linksMu sync.Mutex
	linksAt time.Time
	links   map[string]*Link
)

// GetLinks returns link details per interface, cached for 30 s.
func GetLinks(names []string) map[string]*Link {
	linksMu.Lock()
	defer linksMu.Unlock()
	if time.Since(linksAt) < 30*time.Second && links != nil {
		return links
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := map[string]*Link{}
	for _, n := range names {
		out[n] = linkInfo(ctx, n)
	}
	links, linksAt = out, time.Now()
	return links
}

func sysNet(name, file string) string {
	return host.Path(filepath.Join("/sys/class/net", name, file))
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func linkKind(name string) string {
	switch {
	case exists(sysNet(name, "wireless")) || exists(sysNet(name, "phy80211")):
		return "wifi"
	case exists(sysNet(name, "tun_flags")) || strings.HasPrefix(name, "wg") || strings.HasPrefix(name, "tun") ||
		strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "ppp") || strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "zt"):
		return "vpn"
	case exists(sysNet(name, "bridge")):
		return "bridge"
	case exists(sysNet(name, "device")):
		return "ethernet"
	}
	return "other"
}

func linkInfo(ctx context.Context, name string) *Link {
	l := &Link{Kind: linkKind(name)}
	state := readStr(sysNet(name, "operstate"))
	l.Up = state == "up" || (state == "unknown" && readStr(sysNet(name, "carrier")) == "1")
	switch l.Kind {
	case "ethernet":
		if v, ok := readInt(sysNet(name, "speed")); ok && v > 0 && l.Up {
			l.RateMbps = float64(v)
		}
	case "wifi":
		if l.Up {
			wifiDetails(ctx, name, l)
		}
	}
	if l.Up && hasHostBin("nmcli") {
		if out, err := host.Command(ctx, "nmcli", "-t", "-f", "GENERAL.CONNECTION", "device", "show", name).Output(); err == nil {
			_, v, _ := strings.Cut(strings.TrimSpace(string(out)), ":")
			if v != "" && v != "--" {
				l.Connection = v
				if l.Kind == "wifi" && l.SSID == "" {
					l.SSID = v // connection names default to the SSID
				}
			}
		}
	}
	return l
}

// wifiDetails fills SSID, signal, band and bitrate from `iw dev X link`,
// falling back to /proc/net/wireless for the signal.
func wifiDetails(ctx context.Context, name string, l *Link) {
	if hasHostBin("iw") {
		if out, err := host.Command(ctx, "iw", "dev", name, "link").Output(); err == nil {
			parseIWLink(string(out), l)
		}
	}
	if l.SignalDBm == 0 {
		l.SignalDBm = procWirelessLevel(name)
	}
}

func parseIWLink(out string, l *Link) {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "SSID":
			l.SSID = v
		case "signal":
			if f := strings.Fields(v); len(f) > 0 {
				if n, err := strconv.Atoi(f[0]); err == nil {
					l.SignalDBm = n
				}
			}
		case "freq":
			if f, err := strconv.ParseFloat(strings.Fields(v + " ")[0], 64); err == nil {
				l.Band = bandOf(f)
			}
		case "tx bitrate":
			if f := strings.Fields(v); len(f) > 0 {
				if n, err := strconv.ParseFloat(f[0], 64); err == nil {
					l.RateMbps = n
				}
			}
		}
	}
}

func bandOf(mhz float64) string {
	switch {
	case mhz >= 5925:
		return "6 GHz"
	case mhz >= 4900:
		return "5 GHz"
	case mhz >= 2400:
		return "2.4 GHz"
	}
	return ""
}

// procWirelessLevel reads the signal level (dBm) from /proc/net/wireless.
func procWirelessLevel(name string) int {
	b, err := os.ReadFile(host.Path("/proc/net/wireless"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || k != name {
			continue
		}
		f := strings.Fields(rest)
		if len(f) >= 3 {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(f[2], "."), 64); err == nil && v < 0 {
				return int(v)
			}
		}
	}
	return 0
}

// primaryIf returns the interface carrying the default IPv4 route.
func primaryIf() string {
	f, err := os.Open(host.Path("/proc/net/route"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 2 && fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

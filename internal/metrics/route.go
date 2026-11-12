// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import (
	"bufio"
	"bytes"
	"strings"
)

// Link describes what an interface is connected to. On Linux Wi-Fi details
// come from the host's own tools (iw, then NetworkManager), so they work the
// same natively and in container mode (which shares the host network). On
// macOS only the kind and carrier state are reported.
type Link struct {
	Kind       string  `json:"kind"`                 // wifi | ethernet | vpn | bridge | other
	Up         bool    `json:"up"`                   // carrier / operstate up
	SSID       string  `json:"ssid,omitempty"`       // Wi-Fi network name
	SignalDBm  int     `json:"signalDbm,omitempty"`  // Wi-Fi signal, e.g. -45
	Band       string  `json:"band,omitempty"`       // "2.4 GHz" | "5 GHz" | "6 GHz"
	RateMbps   float64 `json:"rateMbps,omitempty"`   // Wi-Fi tx bitrate or Ethernet speed
	Connection string  `json:"connection,omitempty"` // NetworkManager connection name
}

// parseRouteGet returns the interface from `route -n get default` output.
func parseRouteGet(out []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":"); ok && k == "interface" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// parseHardwarePorts maps device names to "wifi" or "ethernet" from
// `networksetup -listallhardwareports` output.
func parseHardwarePorts(out []byte) map[string]string {
	m := map[string]string{}
	port := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "Hardware Port":
			port = v
		case "Device":
			if v == "" {
				continue
			}
			if port == "Wi-Fi" {
				m[v] = "wifi"
			} else {
				m[v] = "ethernet"
			}
		}
	}
	return m
}

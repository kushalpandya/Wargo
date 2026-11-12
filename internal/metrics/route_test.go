// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import "testing"

func TestParseRouteGet(t *testing.T) {
	if parseRouteGet([]byte("   route to: default\n  interface: en0\n")) != "en0" {
		t.Fatal()
	}
	if parseRouteGet([]byte("route: writing to routing socket: not in table\n")) != "" {
		t.Fatal()
	}
}

func TestParseHardwarePorts(t *testing.T) {
	m := parseHardwarePorts([]byte("Hardware Port: Wi-Fi\nDevice: en0\n\nHardware Port: Thunderbolt Ethernet Slot 0\nDevice: en5\n"))
	if m["en0"] != "wifi" || m["en5"] != "ethernet" {
		t.Fatal(m)
	}
}

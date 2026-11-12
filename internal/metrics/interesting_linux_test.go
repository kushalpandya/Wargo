// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package metrics

import "testing"

func TestInterestingIf(t *testing.T) {
	for name, want := range map[string]bool{"lo": false, "veth12ab": false, "br-1234": false, "docker0": false, "enp3s0": true, "wlan0": true, "tun0": true} {
		if interestingIf(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
}

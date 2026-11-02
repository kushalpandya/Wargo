// SPDX-License-Identifier: AGPL-3.0-only

package metrics

import "testing"

func TestParseIWLink(t *testing.T) {
	out := `Connected to 3e:2d:9e:a7:2d:12 (on wlan0)
	SSID: Home Net: 5G
	freq: 5785.0
	signal: -40 dBm
	rx bitrate: 648.5 MBit/s 80MHz HE-MCS 6
	tx bitrate: 1080.6 MBit/s 80MHz HE-MCS 10 HE-NSS 2
	bss flags: short-slot-time
`
	var l Link
	parseIWLink(out, &l)
	if l.SSID != "Home Net: 5G" || l.SignalDBm != -40 || l.Band != "5 GHz" || l.RateMbps != 1080.6 {
		t.Fatalf("%+v", l)
	}
	for mhz, want := range map[float64]string{2412: "2.4 GHz", 5180: "5 GHz", 5955: "6 GHz", 900: ""} {
		if got := bandOf(mhz); got != want {
			t.Errorf("%v: %q", mhz, got)
		}
	}
}

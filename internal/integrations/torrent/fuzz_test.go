// SPDX-License-Identifier: AGPL-3.0-only

package torrent

import (
	"strings"
	"testing"
)

func FuzzDecode(f *testing.F) {
	for _, s := range []string{
		"i42e", "i-1e", "0:", "4:spam", "le", "de", "l4:spami42ee", "d3:cow3:moo4:spam4:eggse",
		"d8:announce25:udp://tr.example.org:69694:infod6:lengthi1048576e4:name15:debian-12.6.iso12:piece lengthi262144e6:pieces20:ABCDEFGHIJKLMNOPQRSTee",
		"d4:infod9:file treed1:ad0:d6:lengthi5eeee12:meta versioni2eee",
		strings.Repeat("l", 70) + strings.Repeat("e", 70), "i00e", "i-0e", "01:a", "d1:a0:1:a0:e", "999999999:x",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := Decode(b)
		if err == nil && v == nil {
			t.Fatal("nil value without error")
		}
		m, err := Parse(b)
		if err != nil {
			return
		}
		if m.InfoHash == "" && m.InfoHashV2 == "" {
			t.Fatal("parsed torrent without info hash")
		}
		mag := m.Magnet()
		if !strings.HasPrefix(mag, "magnet:?") || strings.ContainsAny(mag, " \r\n\x00") {
			t.Fatalf("bad magnet %q", mag)
		}
		if m.InfoHash != "" && MagnetInfoHash(mag) != m.InfoHash {
			t.Fatalf("magnet hash mismatch %q", mag)
		}
	})
}

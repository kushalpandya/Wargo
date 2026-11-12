// SPDX-License-Identifier: AGPL-3.0-only

package about

import "testing"

func TestParseVmRSS(t *testing.T) {
	s := "Name:\twargo\nVmPeak:\t  999 kB\nVmRSS:\t   15360 kB\nThreads:\t8\n"
	if got := parseVmRSS(s); got != 15360*1024 {
		t.Fatalf("got %d", got)
	}
	if parseVmRSS("Name: x\n") != 0 {
		t.Fatal("missing")
	}
}

func TestSelfRSS(t *testing.T) {
	if selfRSS() == 0 {
		t.Fatal("rss 0")
	}
}

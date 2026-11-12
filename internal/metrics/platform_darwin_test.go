// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package metrics

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kushalpandya/wargo/internal/events"
)

func TestSampleDarwin(t *testing.T) {
	c := NewCollector(events.NewHub(), time.Second, time.Minute)
	c.sample(context.Background())
	time.Sleep(time.Second)
	c.sample(context.Background())
	s := c.Latest()
	if len(s.CPU.PerCore) != runtime.NumCPU() || s.Memory.Total == 0 || s.Procs < 50 || s.PrimaryIf == "" {
		t.Fatalf("%+v", s)
	}
	if !slices.ContainsFunc(s.Disks, func(d DiskIO) bool { return d.Name == "disk0" }) {
		t.Fatal("no disk0")
	}
	for _, n := range s.Net {
		if strings.HasPrefix(n.Name, "utun") || n.Name == "lo0" {
			t.Fatal(n.Name)
		}
	}
}

func TestInterestingIfDarwin(t *testing.T) {
	for name, want := range map[string]bool{"lo0": false, "utun3": false, "awdl0": false, "bridge100": false, "en0": true, "en5": true} {
		if interestingIf(name) != want {
			t.Errorf("%s: want %v", name, want)
		}
	}
}

// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package metrics

import (
	"context"
	"regexp"

	"github.com/kushalpandya/wargo/internal/libproc"
)

var wholeDisk = regexp.MustCompile(`^disk[0-9]+$`)

// physicalDisk matches whole disks (disk0), not slices (disk0s1) or
// synthesized volumes' partitions.
func physicalDisk(name string) bool { return wholeDisk.MatchString(name) }

// Loopback, tunnels, AirDrop/Continuity and VM bridges are not traffic
// the user cares about.
var ifSkipPrefixes = []string{"lo", "utun", "awdl", "llw", "anpi", "ap", "gif", "stf", "bridge", "vmenet"}

// procCount counts processes through libproc. gopsutil's version execs
// ps every second on macOS.
func procCount(context.Context) uint64 {
	pids, err := libproc.PIDs()
	if err != nil {
		return 0
	}
	return uint64(len(pids))
}

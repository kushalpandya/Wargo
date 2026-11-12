// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package about

import "os"

// selfRSS returns Wargo's resident set size in bytes.
func selfRSS() uint64 {
	b, _ := os.ReadFile("/proc/self/status")
	return parseVmRSS(string(b))
}

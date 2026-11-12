// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package files

import "github.com/kushalpandya/wargo/internal/host"

// detectDrives lists the system volume and the volumes under /Volumes
// from getfsstat. It is a variable so tests can fake the mount table.
var detectDrives = func() []Drive {
	ms, _ := host.Mounts()
	return drivesFromMounts(ms)
}

// sealedRoot reports whether p is the macOS system volume: its sealed
// snapshot is mounted read-only, but writes to user paths go through
// firmlinks into the Data volume, so it isn't shown as read-only.
func sealedRoot(p string) bool { return p == "/" }

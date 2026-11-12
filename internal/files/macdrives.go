// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"path"

	"github.com/kushalpandya/wargo/internal/host"
)

// drivesFromMounts turns the macOS mount table into drives: the system
// volume and the volumes under /Volumes. It is untagged so Linux tests
// cover it too.
func drivesFromMounts(ms []host.Mount) []Drive {
	out := []Drive{}
	for _, m := range ms {
		if !host.UserVolume(m) {
			continue
		}
		d := Drive{ID: mountID(m.Path), Label: path.Base(m.Path), Path: m.Path, Device: m.Device, FSType: m.FSType}
		if m.Path == "/" {
			// The sealed system snapshot is read-only, but user data is
			// reached through firmlinks into the writable Data volume.
			d.Label = "System"
		} else {
			d.ReadOnly, d.Removable, d.Network = m.ReadOnly, m.Removable, !m.Local
		}
		out = append(out, d)
	}
	return out
}

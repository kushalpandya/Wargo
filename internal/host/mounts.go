// SPDX-License-Identifier: AGPL-3.0-only

package host

import "strings"

// Mount is one mounted filesystem as the kernel reports it (getfsstat on
// macOS). Sizes are in bytes.
type Mount struct {
	Device, Path, FSType                           string
	ReadOnly, Local, Removable, DontBrowse, RootFS bool
	Total, Free, Avail, Files, FilesFree           uint64
}

// UserVolume reports whether a mount is one a user browses: the system
// volume at / or a volume under /Volumes (external disks, disk images,
// network shares). Hidden system volumes are marked nobrowse.
func UserVolume(m Mount) bool {
	if m.DontBrowse || m.FSType == "devfs" || m.FSType == "autofs" {
		return false
	}
	return m.Path == "/" || strings.HasPrefix(m.Path, "/Volumes/") && len(m.Path) > len("/Volumes/")
}

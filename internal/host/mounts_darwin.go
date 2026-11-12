// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package host

import (
	"golang.org/x/sys/unix"
)

// Mounts lists mounted filesystems with getfsstat. MNT_NOWAIT returns the
// kernel's cached statistics, so a stale network mount never blocks it.
func Mounts() ([]Mount, error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	var buf []unix.Statfs_t
	for range 4 {
		// Room for a few mounts appearing between the two calls.
		buf = make([]unix.Statfs_t, n+8)
		if n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT); err != nil {
			return nil, err
		}
		if n < len(buf) {
			break
		}
	}
	n = min(n, len(buf))
	out := make([]Mount, 0, n)
	for _, s := range buf[:n] {
		bs := uint64(s.Bsize)
		out = append(out, Mount{
			Device:     unix.ByteSliceToString(s.Mntfromname[:]),
			Path:       unix.ByteSliceToString(s.Mntonname[:]),
			FSType:     unix.ByteSliceToString(s.Fstypename[:]),
			ReadOnly:   s.Flags&unix.MNT_RDONLY != 0,
			Local:      s.Flags&unix.MNT_LOCAL != 0,
			Removable:  s.Flags&unix.MNT_REMOVABLE != 0,
			DontBrowse: s.Flags&unix.MNT_DONTBROWSE != 0,
			RootFS:     s.Flags&unix.MNT_ROOTFS != 0,
			Total:      s.Blocks * bs,
			Free:       s.Bfree * bs,
			Avail:      s.Bavail * bs,
			Files:      s.Files,
			FilesFree:  s.Ffree,
		})
	}
	return out, nil
}

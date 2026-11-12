// SPDX-License-Identifier: AGPL-3.0-only

package pkg

import (
	"syscall"

	"github.com/kushalpandya/wargo/internal/host"
)

func kernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	b := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func diskSpace() []DiskInfo {
	var out []DiskInfo
	seen := map[uint64]bool{}
	for _, p := range []string{"/", "/var/cache"} {
		var s syscall.Statfs_t
		if err := syscall.Statfs(host.Path(p), &s); err != nil {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat(host.Path(p), &st) == nil {
			if seen[st.Dev] {
				continue
			}
			seen[st.Dev] = true
		}
		free := s.Bavail * uint64(s.Bsize)
		out = append(out, DiskInfo{Path: p, Free: free, Low: free < lowSpace})
	}
	return out
}

// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package about

import (
	"os"

	"github.com/kushalpandya/wargo/internal/libproc"
)

// selfRSS returns Wargo's resident set size in bytes.
func selfRSS() uint64 {
	i, err := libproc.Get(os.Getpid())
	if err != nil {
		return 0
	}
	return i.RSS
}

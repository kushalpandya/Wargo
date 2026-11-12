// SPDX-License-Identifier: AGPL-3.0-only

package term

// Terminal environment defaults on Linux.
const (
	defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	defaultLang = "C.UTF-8"

	// resolvePath: Linux terminals keep the PATH Wargo runs with.
	resolvePath = false
)

// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import "golang.org/x/sys/unix"

func openat(dirfd int, name string, flags int) (int, error) {
	return unix.Openat(dirfd, name, flags, 0)
}

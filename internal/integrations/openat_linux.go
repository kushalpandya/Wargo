// SPDX-License-Identifier: AGPL-3.0-only

package integrations

import "syscall"

func openat(dirfd int, name string, flags int) (int, error) {
	return syscall.Openat(dirfd, name, flags, 0)
}

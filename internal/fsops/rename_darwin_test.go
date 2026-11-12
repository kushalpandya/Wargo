// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import "golang.org/x/sys/unix"

// forceNoReplaceFallback makes renameNoReplace act as on a filesystem
// without RENAME_EXCL until the returned function runs.
func forceNoReplaceFallback() func() {
	old := renameatx
	renameatx = func(int, string, int, string, uint32) error { return unix.ENOTSUP }
	return func() { renameatx = old }
}

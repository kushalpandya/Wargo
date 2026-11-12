// SPDX-License-Identifier: AGPL-3.0-only

package fsops

// forceNoReplaceFallback makes renameNoReplace skip renameat2 until the
// returned function runs.
func forceNoReplaceFallback() func() {
	old := sysRenameat2
	sysRenameat2 = 0
	return func() { sysRenameat2 = old }
}

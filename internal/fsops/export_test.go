// SPDX-License-Identifier: AGPL-3.0-only

package fsops

import (
	"os"
	"syscall"
)

// SetCopyHook and ForceCrossDevice expose the copy seams to tests in the
// external fsops_test package (which can drive the root helper). Each
// returns a function that restores the previous state.

func SetCopyHook(fn func(step, name string)) func() {
	old := copyHook
	copyHook = fn
	return func() { copyHook = old }
}

func ForceCrossDevice() func() {
	old := renameBetween
	renameBetween = func(*os.Root, string, *os.Root, string) error {
		return &os.LinkError{Op: "rename", Err: syscall.EXDEV}
	}
	return func() { renameBetween = old }
}

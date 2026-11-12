// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package files

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lstat is a variable so tests can fake what macOS answers.
var lstat = os.Lstat

// platformPrivacyReadBlocked is the read-only rule: EPERM in a protected place.
func platformPrivacyReadBlocked(err error, home, abs string) bool {
	return errors.Is(err, syscall.EPERM) && privacyProtected(home, abs)
}

// platformPrivacyBlocked reports a TCC denial. EPERM alone is not enough:
// chown by a non-root user, or chmod/unlink/rename by a non-owner, also fail
// with EPERM. So the path the user asked for (or its parent folder when it
// does not exist yet) must itself fail a plain Lstat with EPERM. Ordinary
// permission problems are EACCES and keep the elevation path.
func platformPrivacyBlocked(err error, home, abs string) bool {
	if !errors.Is(err, syscall.EPERM) || !privacyProtected(home, abs) {
		return false
	}
	p := filepath.Clean(abs)
	for range 2 {
		_, e := lstat(p)
		switch {
		case e == nil:
			return false
		case errors.Is(e, syscall.EPERM):
			return true
		case errors.Is(e, os.ErrNotExist):
			p = filepath.Dir(p)
		default:
			return false
		}
	}
	return false
}

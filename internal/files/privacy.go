// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kushalpandya/wargo/internal/httpx"
)

// privacyBlocked reports whether err is a macOS privacy (TCC) denial for abs.
// It only looks at the error of an operation the user asked for; Wargo never
// probes protected files to find out. A variable so tests can replace it.
var privacyBlocked = platformPrivacyBlocked

// privacyReadBlocked is the rule for read-only operations (list, stat, read,
// download): EPERM under a protected folder is enough, because TCC guards a
// folder's contents while its own metadata stays visible.
var privacyReadBlocked = platformPrivacyReadBlocked

// privacyProtected reports whether abs is in a place macOS guards with a
// privacy prompt: the user's Desktop, Documents, Downloads and cloud folders,
// or a removable or network volume.
func privacyProtected(home, abs string) bool {
	abs = filepath.Clean(abs)
	if abs == "/Volumes" || strings.HasPrefix(abs, "/Volumes/") {
		return true
	}
	if home == "" || home == "/" {
		return false
	}
	for _, d := range []string{"Desktop", "Documents", "Downloads", "Library/Mobile Documents", "Library/CloudStorage"} {
		p := filepath.Join(home, d)
		if abs == p || strings.HasPrefix(abs, p+"/") {
			return true
		}
	}
	return false
}

// privacyDenied turns the failure cause of an unprivileged attempt on abs
// into the full_disk_access error when macOS privacy protection caused it.
// Root is blocked by it too, so callers return this instead of elevating.
func privacyDenied(cause error, abs string) error {
	if cause != nil && privacyBlocked(cause, userHome(), abs) {
		return fullDiskAccessErr(abs)
	}
	return nil
}

// privacyDeniedRead is privacyDenied for read-only operations.
func privacyDeniedRead(cause error, abs string) error {
	if cause != nil && privacyReadBlocked(cause, userHome(), abs) {
		return fullDiskAccessErr(abs)
	}
	return nil
}

var (
	homeOnce sync.Once
	homeDir  string
)

// userHome is the home folder of the user Wargo runs as. HOME may be unset
// under launchd, so it falls back to the account database.
func userHome() string {
	homeOnce.Do(func() {
		if h, err := os.UserHomeDir(); err == nil && h != "" {
			homeDir = h
		} else if u, err := user.Current(); err == nil {
			homeDir = u.HomeDir
		}
	})
	return homeDir
}

func fullDiskAccessErr(abs string) *httpx.Error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "the wargo program"
	}
	return httpx.Errorf(http.StatusForbidden, "full_disk_access",
		"macOS privacy protection blocks Wargo from "+abs+". To allow it, open System Settings > Privacy & Security > Full Disk Access and add "+exe+", then restart Wargo. Your organisation may not allow this.")
}

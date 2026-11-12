// SPDX-License-Identifier: AGPL-3.0-only

package host

import (
	"path/filepath"
	"strings"
)

// parseDsclShell extracts the shell from `dscl . -read /Users/<name>
// UserShell` output ("UserShell: /bin/zsh"). It returns "" when the key is
// missing or the value is not an absolute path.
func parseDsclShell(out []byte) string {
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "UserShell:"); ok {
			if v = strings.TrimSpace(v); filepath.IsAbs(v) {
				return v
			}
			return ""
		}
	}
	return ""
}
